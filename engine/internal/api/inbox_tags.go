package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/inbox"
)

// Inbox tags and saved views. Tags are the names in a reply's labels, kept
// in a list with colours; a view is a saved set of inbox filters, for one
// person or shared with the workspace.
func (s *Server) routesInboxTags(mux *http.ServeMux) {
	read, manage := auth.ScopeInboxRead, auth.ScopeInboxManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/inbox-tags", s.inWorkspace(read, s.listInboxTags))
	mux.HandleFunc("POST /v1/workspaces/{ws}/inbox-tags", s.inWorkspace(manage, s.createInboxTag))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/inbox-tags/{id}", s.inWorkspace(manage, s.updateInboxTag))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/inbox-tags/{id}", s.inWorkspace(manage, s.deleteInboxTag))
	mux.HandleFunc("GET /v1/workspaces/{ws}/inbox-views", s.inWorkspace(read, s.listInboxViews))
	mux.HandleFunc("POST /v1/workspaces/{ws}/inbox-views", s.inWorkspace(read, s.createInboxView))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/inbox-views/{id}", s.inWorkspace(read, s.updateInboxView))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/inbox-views/{id}", s.inWorkspace(read, s.deleteInboxView))
}

// tagColors are the colours a tag can have; the web app maps each to a
// shade that reads in light and dark mode.
var tagColors = []string{"gray", "red", "orange", "amber", "green", "teal", "blue", "violet", "pink"}

type inboxTag struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Color     string    `json:"color"`
	Replies   int       `json:"replies"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) listInboxTags(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), `
		SELECT t.id, t.name, t.color,
		       (SELECT count(*) FROM inbound_messages i WHERE i.workspace_id = t.workspace_id AND t.name = ANY(i.labels))::int,
		       t.created_at
		FROM inbox_tags t WHERE t.workspace_id = $1 ORDER BY t.name`, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[inboxTag])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func tagName(raw string) (string, error) {
	n := strings.ToLower(strings.TrimSpace(raw))
	if n == "" || len(n) > 40 {
		return "", errInvalid("name must be 1 to 40 characters")
	}
	return n, nil
}

func tagColor(c *string) (string, error) {
	if c == nil {
		return "gray", nil
	}
	if !slices.Contains(tagColors, *c) {
		return "", errInvalid("color must be one of " + strings.Join(tagColors, ", "))
	}
	return *c, nil
}

func (s *Server) oneInboxTag(w http.ResponseWriter, r *http.Request) (*inboxTag, bool) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "tag not found")
		return nil, false
	}
	var t inboxTag
	err := s.pool.QueryRow(r.Context(), `SELECT id, name, color, 0, created_at FROM inbox_tags WHERE id = $1 AND workspace_id = $2`,
		id, a.WorkspaceID).Scan(&t.ID, &t.Name, &t.Color, &t.Replies, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "tag not found")
		return nil, false
	}
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	return &t, true
}

func (s *Server) createInboxTag(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Name  string  `json:"name"`
		Color *string `json:"color"`
	}
	if !decode(w, r, &req) {
		return
	}
	name, err := tagName(req.Name)
	color := ""
	if err == nil {
		color, err = tagColor(req.Color)
	}
	var inv errInvalid
	if errors.As(err, &inv) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
		return
	}
	ctx := r.Context()
	var t inboxTag
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM inbox_tags WHERE workspace_id = $1`, a.WorkspaceID).Scan(&n); err != nil {
			return err
		}
		if n >= 200 {
			return errInvalid("a workspace can have at most 200 tags")
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO inbox_tags (workspace_id, name, color) VALUES ($1, $2, $3)
			ON CONFLICT (workspace_id, name) DO NOTHING
			RETURNING id, name, color,
			          (SELECT count(*) FROM inbound_messages i WHERE i.workspace_id = $1 AND $2 = ANY(i.labels))::int, created_at`, a.WorkspaceID, name, color).Scan(&t.ID, &t.Name, &t.Color, &t.Replies, &t.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return errConflict
		}
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "inbox_tag.created", "inbox_tag", t.ID, map[string]any{"name": name, "color": color})
	})
	switch {
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
	case errors.Is(err, errConflict):
		writeError(w, r, http.StatusConflict, "conflict", "a tag with that name exists")
	case err != nil:
		s.internal(w, r, err)
	default:
		writeJSON(w, http.StatusCreated, t)
	}
}

// updateInboxTag renames or recolours a tag. A rename changes the name on
// every reply that has it.
func (s *Server) updateInboxTag(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Name  *string `json:"name"`
		Color *string `json:"color"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Name == nil && req.Color == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "nothing to change: send name or color")
		return
	}
	cur, ok := s.oneInboxTag(w, r)
	if !ok {
		return
	}
	name, color := cur.Name, cur.Color
	var err error
	if req.Name != nil {
		name, err = tagName(*req.Name)
	}
	if err == nil && req.Color != nil {
		color, err = tagColor(req.Color)
	}
	var inv errInvalid
	if errors.As(err, &inv) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
		return
	}
	ctx := r.Context()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if name != cur.Name {
			var taken bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM inbox_tags WHERE workspace_id = $1 AND name = $2)`, a.WorkspaceID, name).Scan(&taken); err != nil {
				return err
			}
			if taken {
				return errConflict
			}
			if _, err := tx.Exec(ctx, `
				UPDATE inbound_messages SET labels = array_replace(labels, $2, $3)
				WHERE workspace_id = $1 AND $2 = ANY(labels)`, a.WorkspaceID, cur.Name, name); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE inbox_tags SET name = $3, color = $4 WHERE id = $1 AND workspace_id = $2`,
			cur.ID, a.WorkspaceID, name, color); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "inbox_tag.updated", "inbox_tag", cur.ID,
			map[string]any{"name": name, "previous_name": cur.Name, "color": color})
	})
	switch {
	case errors.Is(err, errConflict):
		writeError(w, r, http.StatusConflict, "conflict", "a tag with that name exists")
	case err != nil:
		s.internal(w, r, err)
	default:
		cur.Name, cur.Color = name, color
		writeJSON(w, http.StatusOK, cur)
	}
}

// deleteInboxTag removes a tag from the list and from every reply.
func (s *Server) deleteInboxTag(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.oneInboxTag(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE inbound_messages SET labels = array_remove(labels, $2) WHERE workspace_id = $1 AND $2 = ANY(labels)`,
			a.WorkspaceID, cur.Name)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM inbox_tags WHERE id = $1 AND workspace_id = $2`, cur.ID, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "inbox_tag.deleted", "inbox_tag", cur.ID,
			map[string]any{"name": cur.Name, "replies": tag.RowsAffected()})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Saved views.

type inboxView struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Shared    bool           `json:"shared"`
	Filters   map[string]any `json:"filters"`
	Position  int            `json:"position"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// viewFilters are the inbox list's query parameters a view can save.
var viewFilters = []string{"status", "kind", "category", "group", "source", "tag", "assigned_to", "mailbox_id",
	"sequence_id", "brand", "unread", "needs_review", "min_confidence", "q"}

// cleanFilters checks a view's filters: known keys, short string values.
// The list endpoint checks the values themselves when the view is used.
func cleanFilters(in map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for k, v := range in {
		if !slices.Contains(viewFilters, k) {
			return nil, errInvalid("unknown filter " + k + "; a view can save " + strings.Join(viewFilters, ", "))
		}
		var str string
		switch x := v.(type) {
		case string:
			str = x
		case bool:
			if x {
				str = "true"
			} else {
				str = "false"
			}
		case float64:
			b, _ := json.Marshal(x)
			str = string(b)
		case nil:
			continue
		default:
			return nil, errInvalid("filter " + k + " must be a string")
		}
		if len(str) > 500 {
			return nil, errInvalid("filter " + k + " is too long")
		}
		if k == "group" {
			for _, g := range strings.Split(str, ",") {
				if _, ok := inbox.Groups[strings.TrimSpace(g)]; !ok {
					return nil, errInvalid("group must be one or more of " + strings.Join(inbox.GroupNames, ", "))
				}
			}
		}
		if str != "" {
			out[k] = str
		}
	}
	return out, nil
}

const inboxViewColumns = `id, name, user_id IS NULL, filters, position, created_at, updated_at`

// visibleViews is the condition for the views a caller sees: shared ones
// and their own.
func visibleViews(userID string, args *[]any) string {
	if userID == "" {
		return ` AND user_id IS NULL`
	}
	*args = append(*args, userID)
	return ` AND (user_id IS NULL OR user_id = $` + itoa(len(*args)) + `)`
}

func (s *Server) listInboxViews(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	args := []any{a.WorkspaceID}
	rows, err := s.pool.Query(r.Context(), `SELECT `+inboxViewColumns+` FROM inbox_views WHERE workspace_id = $1`+
		visibleViews(a.UserID, &args)+` ORDER BY position, created_at`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[inboxView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) oneInboxView(w http.ResponseWriter, r *http.Request) (*inboxView, bool) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "view not found")
		return nil, false
	}
	args := []any{a.WorkspaceID, id}
	rows, err := s.pool.Query(r.Context(), `SELECT `+inboxViewColumns+` FROM inbox_views WHERE workspace_id = $1 AND id = $2`+
		visibleViews(a.UserID, &args), args...)
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	v, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[inboxView])
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "view not found")
		return nil, false
	}
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	return &v, true
}

func viewName(raw string) (string, error) {
	n := strings.TrimSpace(raw)
	if n == "" || len(n) > 60 {
		return "", errInvalid("name must be 1 to 60 characters")
	}
	return n, nil
}

func (s *Server) createInboxView(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Name     string         `json:"name"`
		Filters  map[string]any `json:"filters"`
		Shared   *bool          `json:"shared"`
		Position *int           `json:"position"`
	}
	if !decode(w, r, &req) {
		return
	}
	name, err := viewName(req.Name)
	var filters map[string]any
	if err == nil {
		filters, err = cleanFilters(req.Filters)
	}
	var inv errInvalid
	if errors.As(err, &inv) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
		return
	}
	// Views are personal by default; API tokens act for nobody in
	// particular, so theirs are shared.
	var owner *string
	if a.UserID != "" && (req.Shared == nil || !*req.Shared) {
		owner = &a.UserID
	}
	ctx := r.Context()
	var v inboxView
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM inbox_views WHERE workspace_id = $1`, a.WorkspaceID).Scan(&n); err != nil {
			return err
		}
		if n >= 100 {
			return errInvalid("a workspace can have at most 100 saved views")
		}
		pos := n
		if req.Position != nil {
			pos = *req.Position
		}
		rows, err := tx.Query(ctx, `
			INSERT INTO inbox_views (workspace_id, user_id, name, filters, position, created_by)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+inboxViewColumns,
			a.WorkspaceID, owner, name, filters, pos, nilIfEmpty(a.ActorID))
		if err != nil {
			return err
		}
		if v, err = pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[inboxView]); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "inbox_view.created", "inbox_view", v.ID,
			map[string]any{"name": name, "shared": v.Shared, "filters": filterKeys(filters)})
	})
	switch {
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
	case err != nil:
		s.internal(w, r, err)
	default:
		writeJSON(w, http.StatusCreated, v)
	}
}

func (s *Server) updateInboxView(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Name     *string        `json:"name"`
		Filters  map[string]any `json:"filters"`
		Position *int           `json:"position"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Name == nil && req.Filters == nil && req.Position == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "nothing to change: send name, filters or position")
		return
	}
	cur, ok := s.oneInboxView(w, r)
	if !ok {
		return
	}
	name, filters, pos := cur.Name, cur.Filters, cur.Position
	var err error
	if req.Name != nil {
		name, err = viewName(*req.Name)
	}
	if err == nil && req.Filters != nil {
		filters, err = cleanFilters(req.Filters)
	}
	if req.Position != nil {
		pos = *req.Position
	}
	var inv errInvalid
	if errors.As(err, &inv) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
		return
	}
	ctx := r.Context()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE inbox_views SET name = $3, filters = $4, position = $5, updated_at = now() WHERE id = $1 AND workspace_id = $2`,
			cur.ID, a.WorkspaceID, name, filters, pos); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "inbox_view.updated", "inbox_view", cur.ID,
			map[string]any{"name": name, "filters": filterKeys(filters)})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	cur.Name, cur.Filters, cur.Position = name, filters, pos
	writeJSON(w, http.StatusOK, cur)
}

func (s *Server) deleteInboxView(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.oneInboxView(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM inbox_views WHERE id = $1 AND workspace_id = $2`, cur.ID, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "inbox_view.deleted", "inbox_view", cur.ID, map[string]any{"name": cur.Name})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func filterKeys(f map[string]any) []string {
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
