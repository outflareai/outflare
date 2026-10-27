package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/allocation"
	"github.com/outflareai/outflare/engine/internal/auth"
)

// Teams and senders: who owns a sequence, and who each mailbox sends as.

func (s *Server) routesTeams(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/workspaces/{ws}/teams", s.inBrand(auth.ScopeMembersRead, s.listTeams))
	mux.HandleFunc("POST /v1/workspaces/{ws}/teams", s.inWorkspace(auth.ScopeMembersManage, s.createTeam))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/teams/{id}", s.inWorkspace(auth.ScopeMembersManage, s.updateTeam))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/teams/{id}", s.inWorkspace(auth.ScopeMembersManage, s.deleteTeam))

	read, manage := auth.ScopeMailboxesRead, auth.ScopeMailboxesManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/senders", s.inBrand(read, s.listSenders))
	mux.HandleFunc("POST /v1/workspaces/{ws}/senders", s.inWorkspace(manage, s.createSender))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/senders/{id}", s.inWorkspace(manage, s.updateSender))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/senders/{id}", s.inWorkspace(manage, s.deleteSender))
	mux.HandleFunc("GET /v1/workspaces/{ws}/senders/matches", s.inWorkspace(read, s.senderMatches))
	mux.HandleFunc("POST /v1/workspaces/{ws}/mailboxes/sender", s.inWorkspace(manage, s.setMailboxSender))
}

type teamView struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	AllocationMode *string   `json:"allocation_mode"`
	MemberIDs      []string  `json:"member_ids"`
	Senders        int       `json:"senders"`
	Sequences      int       `json:"sequences"`
	CreatedAt      time.Time `json:"created_at"`
}

func (s *Server) loadTeams(ctx context.Context, ws string, id *string) ([]teamView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.id, t.name, t.allocation_mode,
		       COALESCE((SELECT array_agg(user_id::text ORDER BY user_id) FROM team_members WHERE team_id = t.id), '{}'),
		       (SELECT count(*) FROM sender_teams WHERE team_id = t.id)::int,
		       (SELECT count(*) FROM sequences WHERE team_id = t.id AND status <> 'archived')::int,
		       t.created_at
		FROM teams t WHERE t.workspace_id = $1 AND ($2::uuid IS NULL OR t.id = $2) ORDER BY lower(t.name)`, ws, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[teamView])
}

func (s *Server) listTeams(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	list, err := s.loadTeams(r.Context(), a.WorkspaceID, nil)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []teamView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

type teamInput struct {
	Name           *string          `json:"name"`
	AllocationMode optional[string] `json:"allocation_mode"`
	MemberIDs      []string         `json:"member_ids"`
}

func (in *teamInput) validate() error {
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if *in.Name == "" || len(*in.Name) > 100 {
			return errInvalid("name must be 1 to 100 characters")
		}
	}
	if v := in.AllocationMode.Value; v != nil && *v != allocation.ModeSuggest && *v != allocation.ModeAutomatic {
		return errInvalid("allocation_mode is suggest, automatic, or null to follow the workspace")
	}
	for _, id := range in.MemberIDs {
		if !validUUID(id) {
			return errInvalid("member_ids must be user ids")
		}
	}
	return nil
}

// setMembers replaces a team's members with workspace members.
func setMembers(ctx context.Context, tx pgx.Tx, ws, team string, ids []string) error {
	if ids == nil {
		return nil
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM memberships WHERE workspace_id = $1 AND user_id = ANY($2::uuid[])`, ws, ids).Scan(&n); err != nil {
		return err
	}
	if n != len(slices.Compact(slices.Sorted(slices.Values(ids)))) {
		return errInvalid("member_ids must be members of this workspace")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM team_members WHERE team_id = $1`, team); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO team_members (team_id, user_id) SELECT $1, unnest($2::uuid[]) ON CONFLICT DO NOTHING`, team, ids)
	return err
}

func (s *Server) createTeam(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req teamInput
	if !decode(w, r, &req) {
		return
	}
	if req.Name == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	if err := req.validate(); err != nil {
		s.allocError(w, r, err)
		return
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO teams (workspace_id, name, allocation_mode) VALUES ($1, $2, $3) RETURNING id`,
			a.WorkspaceID, *req.Name, req.AllocationMode.Value).Scan(&id)
		if isUnique(err) {
			return errInvalid("a team with that name already exists")
		}
		if err != nil {
			return err
		}
		if err := setMembers(ctx, tx, a.WorkspaceID, id, req.MemberIDs); err != nil {
			return err
		}
		if err := allocation.Request(ctx, tx, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "team.created", "team", id, map[string]any{"name": *req.Name})
	})
	if s.allocError(w, r, err) {
		return
	}
	list, err := s.loadTeams(ctx, a.WorkspaceID, &id)
	if err != nil || len(list) == 0 {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, list[0])
}

func (s *Server) updateTeam(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var req teamInput
	if !decode(w, r, &req) {
		return
	}
	if err := req.validate(); err != nil || !validUUID(id) {
		if err == nil {
			err = errNotFound
		}
		s.allocError(w, r, err)
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE teams SET name = COALESCE($3, name), allocation_mode = CASE WHEN $4 THEN $5 ELSE allocation_mode END
			WHERE id = $1 AND workspace_id = $2`, id, a.WorkspaceID, req.Name, req.AllocationMode.Set, req.AllocationMode.Value)
		if isUnique(err) {
			return errInvalid("a team with that name already exists")
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		if err := setMembers(ctx, tx, a.WorkspaceID, id, req.MemberIDs); err != nil {
			return err
		}
		if err := allocation.Request(ctx, tx, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "team.updated", "team", id, nil)
	})
	if s.allocError(w, r, err) {
		return
	}
	list, err := s.loadTeams(ctx, a.WorkspaceID, &id)
	if err != nil || len(list) == 0 {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list[0])
}

func (s *Server) deleteTeam(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		s.allocError(w, r, errNotFound)
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var name string
		err := tx.QueryRow(ctx, `DELETE FROM teams WHERE id = $1 AND workspace_id = $2 RETURNING name`, id, a.WorkspaceID).Scan(&name)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if err := allocation.Request(ctx, tx, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "team.deleted", "team", id, map[string]any{"name": name})
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Senders ----

type senderView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	UserID    *string   `json:"user_id"`
	Status    string    `json:"status"`
	TeamIDs   []string  `json:"team_ids"`
	Mailboxes int       `json:"mailboxes"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) loadSenders(ctx context.Context, ws string, id *string) ([]senderView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT sn.id, sn.name, sn.kind, sn.user_id, sn.status,
		       COALESCE((SELECT array_agg(team_id::text ORDER BY team_id) FROM sender_teams WHERE sender_id = sn.id), '{}'),
		       (SELECT count(*) FROM mailboxes WHERE sender_id = sn.id AND sender_confirmed_at IS NOT NULL)::int,
		       sn.created_at
		FROM senders sn WHERE sn.workspace_id = $1 AND ($2::uuid IS NULL OR sn.id = $2) ORDER BY lower(sn.name)`, ws, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[senderView])
}

func (s *Server) listSenders(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	list, err := s.loadSenders(r.Context(), a.WorkspaceID, nil)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []senderView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

type senderInput struct {
	Name    *string          `json:"name"`
	Kind    *string          `json:"kind"`
	UserID  optional[string] `json:"user_id"`
	Status  *string          `json:"status"`
	TeamIDs []string         `json:"team_ids"`
}

func (in *senderInput) validate() error {
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if *in.Name == "" || len(*in.Name) > 200 {
			return errInvalid("name must be 1 to 200 characters")
		}
	}
	if in.Kind != nil && !slices.Contains([]string{"person", "persona", "shared"}, *in.Kind) {
		return errInvalid("kind is person, persona or shared")
	}
	if in.Status != nil && *in.Status != "active" && *in.Status != "retired" {
		return errInvalid("status is active or retired")
	}
	if in.UserID.Value != nil && !validUUID(*in.UserID.Value) {
		return errInvalid("user_id must be a member of this workspace")
	}
	for _, id := range in.TeamIDs {
		if !validUUID(id) {
			return errInvalid("team_ids must be team ids")
		}
	}
	return nil
}

func (in *senderInput) save(ctx context.Context, tx pgx.Tx, ws, id string) error {
	if in.UserID.Value != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM memberships WHERE workspace_id = $1 AND user_id = $2)`, ws, *in.UserID.Value).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errInvalid("user_id must be a member of this workspace")
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE senders SET name = COALESCE($3, name), kind = COALESCE($4, kind),
			user_id = CASE WHEN $5 THEN $6::uuid ELSE user_id END, status = COALESCE($7, status), updated_at = now()
		WHERE id = $1 AND workspace_id = $2`, id, ws, in.Name, in.Kind, in.UserID.Set, in.UserID.Value, in.Status)
	if isUnique(err) {
		return errInvalid("a sender with that name already exists")
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound
	}
	if in.TeamIDs == nil {
		return nil
	}
	var n int
	ids := slices.Compact(slices.Sorted(slices.Values(in.TeamIDs)))
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM teams WHERE workspace_id = $1 AND id = ANY($2::uuid[])`, ws, ids).Scan(&n); err != nil {
		return err
	}
	if n != len(ids) {
		return errInvalid("team not found")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sender_teams WHERE sender_id = $1`, id); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO sender_teams (sender_id, team_id) SELECT $1, unnest($2::uuid[])`, id, ids)
	return err
}

func (s *Server) createSender(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req senderInput
	if !decode(w, r, &req) {
		return
	}
	if req.Name == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	if err := req.validate(); err != nil {
		s.allocError(w, r, err)
		return
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO senders (workspace_id, name) VALUES ($1, $2) RETURNING id`, a.WorkspaceID, *req.Name).Scan(&id)
		if isUnique(err) {
			return errInvalid("a sender with that name already exists")
		}
		if err != nil {
			return err
		}
		if err := req.save(ctx, tx, a.WorkspaceID, id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "sender.created", "sender", id, map[string]any{"name": *req.Name})
	})
	if s.allocError(w, r, err) {
		return
	}
	list, err := s.loadSenders(ctx, a.WorkspaceID, &id)
	if err != nil || len(list) == 0 {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, list[0])
}

func (s *Server) updateSender(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var req senderInput
	if !decode(w, r, &req) {
		return
	}
	if err := req.validate(); err != nil || !validUUID(id) {
		if err == nil {
			err = errNotFound
		}
		s.allocError(w, r, err)
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := req.save(ctx, tx, a.WorkspaceID, id); err != nil {
			return err
		}
		if err := allocation.Request(ctx, tx, a.WorkspaceID); err != nil {
			return err
		}
		action := "sender.updated"
		if req.Status != nil && *req.Status == "retired" {
			action = "sender.retired"
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "sender", id, nil)
	})
	if s.allocError(w, r, err) {
		return
	}
	list, err := s.loadSenders(ctx, a.WorkspaceID, &id)
	if err != nil || len(list) == 0 {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list[0])
}

func (s *Server) deleteSender(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		s.allocError(w, r, errNotFound)
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var name string
		err := tx.QueryRow(ctx, `DELETE FROM senders WHERE id = $1 AND workspace_id = $2 RETURNING name`, id, a.WorkspaceID).Scan(&name)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE mailboxes SET sender_confirmed_at = NULL, sender_confirmed_by = NULL WHERE workspace_id = $1 AND sender_id IS NULL AND sender_confirmed_at IS NOT NULL`, a.WorkspaceID); err != nil {
			return err
		}
		if err := allocation.Request(ctx, tx, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "sender.deleted", "sender", id, map[string]any{"name": name})
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type senderMatch struct {
	MailboxID   string `json:"mailbox_id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	SenderID    string `json:"sender_id"`
	SenderName  string `json:"sender_name"`
}

// senderMatches suggests a sender for mailboxes without a confirmed one,
// where the mailbox's display name is exactly a sender's name. Nothing is
// guessed from the address; a person or agent confirms each match.
func (s *Server) senderMatches(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), `
		SELECT m.id::text, m.email, m.display_name, sn.id::text, sn.name
		FROM mailboxes m JOIN senders sn ON sn.workspace_id = m.workspace_id AND lower(sn.name) = lower(trim(m.display_name))
		WHERE m.workspace_id = $1 AND m.sender_confirmed_at IS NULL AND sn.status = 'active'
		ORDER BY sn.name, m.email`, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[senderMatch])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []senderMatch{}
	}
	var unconfirmed int
	if err := s.pool.QueryRow(r.Context(), `SELECT count(*) FROM mailboxes WHERE workspace_id = $1 AND sender_confirmed_at IS NULL`, a.WorkspaceID).Scan(&unconfirmed); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "unconfirmed": unconfirmed})
}

// setMailboxSender confirms who mailboxes send as, in bulk. A null
// sender_id clears it.
func (s *Server) setMailboxSender(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		MailboxIDs []string         `json:"mailbox_ids"`
		SenderID   optional[string] `json:"sender_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.MailboxIDs) == 0 || !req.SenderID.Set || req.SenderID.Value != nil && !validUUID(*req.SenderID.Value) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "give mailbox_ids and sender_id (null to clear)")
		return
	}
	if !allUUIDs(req.MailboxIDs) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "mailbox_ids must be UUIDs")
		return
	}
	ctx := r.Context()
	var n int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if v := req.SenderID.Value; v != nil {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM senders WHERE id = $1 AND workspace_id = $2)`, *v, a.WorkspaceID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return errInvalid("sender not found")
			}
		}
		tag, err := tx.Exec(ctx, `
			UPDATE mailboxes SET sender_id = $3,
				sender_confirmed_at = CASE WHEN $3::uuid IS NULL THEN NULL ELSE now() END,
				sender_confirmed_by = CASE WHEN $3::uuid IS NULL THEN NULL ELSE $4::uuid END, updated_at = now()
			WHERE workspace_id = $1 AND id = ANY($2::uuid[])`, a.WorkspaceID, req.MailboxIDs, req.SenderID.Value, a.ActorID)
		if err != nil {
			return err
		}
		n = tag.RowsAffected()
		if err := allocation.Request(ctx, tx, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "mailbox.sender_set", "workspace", a.WorkspaceID,
			map[string]any{"mailboxes": n, "sender_id": req.SenderID.Value})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": n})
}

// errDenied is a refusal with the reason a person can act on.
type errDenied string

func (e errDenied) Error() string { return string(e) }

func allUUIDs(ids []string) bool {
	for _, id := range ids {
		if !validUUID(id) {
			return false
		}
	}
	return true
}

// allocError writes err as a response and reports whether there was one.
func (s *Server) allocError(w http.ResponseWriter, r *http.Request, err error) bool {
	var inv errInvalid
	switch {
	case err == nil:
		return false
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "not found")
	case errors.As(err, new(errDenied)):
		writeError(w, r, http.StatusForbidden, "forbidden", err.Error())
	case errors.Is(err, allocation.ErrConflict):
		writeError(w, r, http.StatusConflict, "conflict", "the mailbox changed since this was planned; refresh and try again")
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
	default:
		s.internal(w, r, err)
	}
	return true
}
