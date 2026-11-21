package api

import (
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// Who else has a conversation open, so two teammates don't answer the
// same customer. The Inbox asks every few seconds while a conversation is
// open, and asking is what says "I am here" (and, with typing=true, "I am
// writing"). It is a read on purpose: a heartbeat is not an action worth
// an audit entry or an event each time.

const (
	viewingWindow = `interval '40 seconds'`
	typingWindow  = `interval '10 seconds'`
)

func (s *Server) routesInboxViewing(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/inbox-viewing", s.onBrand(auth.ScopeInboxRead, s.inboxViewing))
}

type inboxViewerView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Typing bool   `json:"typing"`
}

func (s *Server) inboxViewing(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.inboxBrand(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	kind, id := q.Get("kind"), q.Get("id")
	if (kind != loopInReply && kind != loopInSupport) || !validUUID(id) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is reply or support, and id is the conversation's id")
		return
	}
	ctx := r.Context()
	exists := `SELECT EXISTS (SELECT 1 FROM inbound_messages i WHERE i.id = $1 AND i.workspace_id = $2 AND ` + replyInBrand("i.", "$3") + `)`
	if kind == loopInSupport {
		exists = `SELECT EXISTS (SELECT 1 FROM support_conversations c WHERE c.id = $1 AND c.workspace_id = $2 AND ` + replyInBrand("c.", "$3") + `)`
	}
	var found bool
	if err := s.pool.QueryRow(ctx, exists, id, a.WorkspaceID, brand).Scan(&found); err != nil {
		s.internal(w, r, err)
		return
	}
	if !found {
		writeError(w, r, http.StatusNotFound, "not_found", "conversation not found")
		return
	}
	// An API token is nobody at a screen: it can look, and is never listed.
	if a.UserID != "" {
		if _, err := s.pool.Exec(ctx, `INSERT INTO inbox_viewing (workspace_id, brand_id, kind, item_id, user_id, typing_at)
			VALUES ($1, $2, $3, $4, $5, CASE WHEN $6 THEN now() END)
			ON CONFLICT (item_id, user_id) DO UPDATE SET seen_at = now(), typing_at = CASE WHEN $6 THEN now() ELSE inbox_viewing.typing_at END`,
			a.WorkspaceID, brand, kind, id, a.UserID, q.Get("typing") == "true"); err != nil {
			s.internal(w, r, err)
			return
		}
		// Sweep what the workspace left behind; cheap, and keeps the table tiny.
		if _, err := s.pool.Exec(ctx, `DELETE FROM inbox_viewing WHERE workspace_id = $1 AND seen_at < now() - interval '10 minutes'`, a.WorkspaceID); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT u.id::text, COALESCE(NULLIF(u.name, ''), u.email), COALESCE(v.typing_at > now() - `+typingWindow+`, false)
		FROM inbox_viewing v JOIN users u ON u.id = v.user_id
		WHERE v.item_id = $1 AND v.workspace_id = $2 AND v.kind = $3 AND v.seen_at > now() - `+viewingWindow+` AND v.user_id::text <> $4
		ORDER BY 2, 1`, id, a.WorkspaceID, kind, a.UserID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[inboxViewerView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []inboxViewerView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"viewers": list})
}
