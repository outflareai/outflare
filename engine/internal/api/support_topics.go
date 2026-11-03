package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// Hand-off topics: what a brand's support conversations are about. The
// assistant tags each conversation it looks at with one, and when it hands
// a conversation to people, assigns it to the topic's person and team. A
// topic with always_handoff is never answered by the assistant (billing
// questions always go to Finance, say).

const maxHandoffTopics = 30

type handoffTopicView struct {
	ID            string         `json:"id"`
	BrandID       *string        `json:"brand_id"`
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	Assignee      *supportPerson `json:"assignee"`
	Team          *supportPerson `json:"team"`
	AlwaysHandoff bool           `json:"always_handoff"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

const handoffTopicSelect = `SELECT t.id::text, t.brand_id::text, t.name, t.description, t.assignee_id::text, COALESCE(NULLIF(u.name, ''), u.email),
	t.team_id::text, tm.name, t.always_handoff, t.updated_at
	FROM support_handoff_topics t LEFT JOIN users u ON u.id = t.assignee_id LEFT JOIN teams tm ON tm.id = t.team_id`

func scanHandoffTopic(row pgx.Row) (handoffTopicView, error) {
	var v handoffTopicView
	var uid, uname, tid, tname *string
	err := row.Scan(&v.ID, &v.BrandID, &v.Name, &v.Description, &uid, &uname, &tid, &tname, &v.AlwaysHandoff, &v.UpdatedAt)
	if uid != nil {
		v.Assignee = &supportPerson{ID: *uid, Name: deref(uname)}
	}
	if tid != nil {
		v.Team = &supportPerson{ID: *tid, Name: deref(tname)}
	}
	return v, err
}

func (s *Server) routesSupportTopics(mux *http.ServeMux) {
	read, manage := auth.ScopeInboxRead, auth.ScopeInboxManage
	base := "/v1/workspaces/{ws}/support/handoff-topics"
	mux.HandleFunc("GET "+base, s.inBrand(read, s.listHandoffTopics))
	mux.HandleFunc("POST "+base, s.inBrand(manage, s.createHandoffTopic))
	mux.HandleFunc("PATCH "+base+"/{id}", s.inBrand(manage, s.updateHandoffTopic))
	mux.HandleFunc("DELETE "+base+"/{id}", s.inBrand(manage, s.deleteHandoffTopic))
}

func (s *Server) listHandoffTopics(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID}
	rows, err := s.pool.Query(r.Context(), handoffTopicSelect+` WHERE t.workspace_id = $1`+sharedBrandSQL(brand, "t.brand_id", &args)+
		` ORDER BY t.brand_id IS NOT NULL, lower(t.name)`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list := []handoffTopicView{}
	for rows.Next() {
		v, err := scanHandoffTopic(rows)
		if err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		list = append(list, v)
	}
	if err := rows.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

type handoffTopicReq struct {
	Brand         *string          `json:"brand"`
	Name          *string          `json:"name"`
	Description   *string          `json:"description"`
	AssigneeID    optional[string] `json:"assignee_id"`
	TeamID        optional[string] `json:"team_id"`
	AlwaysHandoff *bool            `json:"always_handoff"`
}

// check validates a topic's fields and who takes it.
func (req *handoffTopicReq) check(ctx context.Context, q jobs.Querier, ws string) error {
	if req.Name != nil {
		*req.Name = strings.TrimSpace(*req.Name)
		if *req.Name == "" || utf8.RuneCountInString(*req.Name) > 60 {
			return errInvalid("name is 1 to 60 characters")
		}
	}
	if req.Description != nil {
		*req.Description = strings.TrimSpace(*req.Description)
		if utf8.RuneCountInString(*req.Description) > 300 {
			return errInvalid("description is at most 300 characters")
		}
	}
	if req.AssigneeID.Value != nil && *req.AssigneeID.Value == "" {
		req.AssigneeID.Value = nil
	}
	if req.TeamID.Value != nil && *req.TeamID.Value == "" {
		req.TeamID.Value = nil
	}
	if v := req.AssigneeID.Value; v != nil {
		var ok bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM memberships WHERE workspace_id = $1 AND user_id::text = $2)`, ws, *v).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errInvalid("assignee_id is not a member of this workspace")
		}
	}
	if v := req.TeamID.Value; v != nil {
		var ok bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM teams WHERE workspace_id = $1 AND id::text = $2)`, ws, *v).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errInvalid("team_id is not a team in this workspace")
		}
	}
	return nil
}

func (s *Server) createHandoffTopic(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var req handoffTopicReq
	if !decode(w, r, &req) {
		return
	}
	if req.Name == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	var brand *string
	if req.Brand != nil && *req.Brand != "" {
		id, err := s.brandIDFor(ctx, a.WorkspaceID, *req.Brand)
		if errors.Is(err, errNotFound) || err == nil && !a.writes(&id) {
			writeError(w, r, http.StatusNotFound, "not_found", "no such brand")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		brand = &id
	} else if !a.writes(nil) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "say which of your brands the topic is for (brand)")
		return
	}
	desc := ""
	if req.Description != nil {
		desc = *req.Description
	}
	always := req.AlwaysHandoff != nil && *req.AlwaysHandoff
	var v handoffTopicView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := req.check(ctx, tx, a.WorkspaceID); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM support_handoff_topics WHERE workspace_id = $1 AND brand_id IS NOT DISTINCT FROM $2::uuid`,
			a.WorkspaceID, brand).Scan(&n); err != nil {
			return err
		}
		if n >= maxHandoffTopics {
			return errInvalid("at most 30 topics per brand")
		}
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO support_handoff_topics (workspace_id, brand_id, name, description, assignee_id, team_id, always_handoff)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id::text`,
			a.WorkspaceID, brand, *req.Name, desc, req.AssigneeID.Value, req.TeamID.Value, always).Scan(&id)
		if isUniqueViolation(err) {
			return errInvalid("there is already a topic with that name")
		}
		if err != nil {
			return err
		}
		if v, err = scanHandoffTopic(tx.QueryRow(ctx, handoffTopicSelect+` WHERE t.id = $1`, id)); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "support.topic_created", "support_handoff_topic", id, map[string]any{
			"brand": brand != nil, "assignee": req.AssigneeID.Value != nil, "team": req.TeamID.Value != nil, "always_handoff": always})
	})
	if s.supportError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// handoffTopicFor finds a topic the caller may change.
func (s *Server) handoffTopicFor(w http.ResponseWriter, r *http.Request, a *access) (string, bool) {
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "topic not found")
		return "", false
	}
	var brand *string
	err := s.pool.QueryRow(r.Context(), `SELECT brand_id::text FROM support_handoff_topics WHERE id = $1 AND workspace_id = $2`, id, a.WorkspaceID).Scan(&brand)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !a.writes(brand) {
		writeError(w, r, http.StatusNotFound, "not_found", "topic not found")
		return "", false
	}
	if err != nil {
		s.internal(w, r, err)
		return "", false
	}
	return id, true
}

func (s *Server) updateHandoffTopic(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var req handoffTopicReq
	if !decode(w, r, &req) {
		return
	}
	id, ok := s.handoffTopicFor(w, r, a)
	if !ok {
		return
	}
	var v handoffTopicView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := req.check(ctx, tx, a.WorkspaceID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE support_handoff_topics SET name = COALESCE($2, name), description = COALESCE($3, description),
			assignee_id = CASE WHEN $4 THEN $5::uuid ELSE assignee_id END, team_id = CASE WHEN $6 THEN $7::uuid ELSE team_id END,
			always_handoff = COALESCE($8, always_handoff), updated_at = now() WHERE id = $1`,
			id, req.Name, req.Description, req.AssigneeID.Set, req.AssigneeID.Value, req.TeamID.Set, req.TeamID.Value, req.AlwaysHandoff)
		if isUniqueViolation(err) {
			return errInvalid("there is already a topic with that name")
		}
		if err != nil {
			return err
		}
		if v, err = scanHandoffTopic(tx.QueryRow(ctx, handoffTopicSelect+` WHERE t.id = $1`, id)); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "support.topic_updated", "support_handoff_topic", id, map[string]any{
			"assignee": v.Assignee != nil, "team": v.Team != nil, "always_handoff": v.AlwaysHandoff})
	})
	if s.supportError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) deleteHandoffTopic(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	id, ok := s.handoffTopicFor(w, r, a)
	if !ok {
		return
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM support_handoff_topics WHERE id = $1`, id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "support.topic_deleted", "support_handoff_topic", id, map[string]any{})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- The assistant's side ----

type handoffTopic struct {
	ID, Name, Description string
	Assignee, Team        *string
	Always                bool
}

// handoffTopics are the topics for a conversation's brand: its own and the
// workspace's.
func handoffTopics(ctx context.Context, q partnerQuerier, ws string, brand *string) ([]handoffTopic, error) {
	rows, err := q.Query(ctx, `SELECT id::text, name, description, assignee_id::text, team_id::text, always_handoff FROM support_handoff_topics
		WHERE workspace_id = $1 AND (brand_id IS NULL OR brand_id = $2::uuid) ORDER BY brand_id IS NULL, lower(name) LIMIT 60`, ws, brand)
	if err != nil {
		return nil, err
	}
	var out []handoffTopic
	var t handoffTopic
	_, err = pgx.ForEachRow(rows, []any{&t.ID, &t.Name, &t.Description, &t.Assignee, &t.Team, &t.Always}, func() error {
		for _, x := range out {
			if strings.EqualFold(x.Name, t.Name) {
				return nil // the brand's own wins
			}
		}
		out = append(out, t)
		return nil
	})
	return out, err
}

// topicNamed finds the topic the model picked.
func topicNamed(topics []handoffTopic, name string) *handoffTopic {
	for i := range topics {
		if strings.EqualFold(topics[i].Name, strings.TrimSpace(name)) {
			return &topics[i]
		}
	}
	return nil
}

// applyHandoffTopic tags the conversation with the topic and, on a
// hand-off, gives it to the topic's person and team when nobody has it yet.
func applyHandoffTopic(ctx context.Context, tx pgx.Tx, ws, conv string, t *handoffTopic, handoff bool) error {
	if t == nil {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE support_conversations SET topic_id = $2 WHERE id = $1`, conv, t.ID); err != nil {
		return err
	}
	if !handoff || (t.Assignee == nil && t.Team == nil) {
		return nil
	}
	tag, err := tx.Exec(ctx, `UPDATE support_conversations SET assignee_id = COALESCE(assignee_id, $2::uuid), team_id = COALESCE(team_id, $3::uuid),
		updated_at = now() WHERE id = $1 AND assignee_id IS NULL AND team_id IS NULL`, conv, t.Assignee, t.Team)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "support.conversation_assigned",
		TargetType: "support_conversation", TargetID: conv,
		Data:  map[string]any{"assignee_id": t.Assignee, "team_id": t.Team, "topic_id": t.ID, "via": "handoff_topic"},
		Props: map[string]any{"via": "handoff_topic", "assignee": t.Assignee != nil, "team": t.Team != nil}})
}
