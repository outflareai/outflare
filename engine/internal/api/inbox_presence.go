package api

import (
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/support"
)

// Online and Away in the Inbox: whether a teammate is taking new chats
// for a brand (package support, presence.go). The Inbox page sends the
// person's status again every minute while it is open; that check-in is
// what keeps them online.

func (s *Server) routesInboxPresence(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/inbox-presence", s.onBrand(auth.ScopeInboxRead, s.getInboxPresence))
	mux.HandleFunc("PUT /v1/brands/{brand}/inbox-presence", s.onBrand(auth.ScopeInboxManage, s.putInboxPresence))
}

type inboxPresenceView struct {
	// Status is the caller's own: online, away, or null for an API token
	// or someone who has never set it.
	Status *string         `json:"status"`
	Online []supportPerson `json:"online"`
}

func (s *Server) inboxPresence(r *http.Request, ws, brand, user string) (inboxPresenceView, error) {
	v := inboxPresenceView{Online: []supportPerson{}}
	ctx := r.Context()
	rows, err := s.pool.Query(ctx, `SELECT u.id::text, COALESCE(NULLIF(u.name, ''), u.email) FROM inbox_presence p JOIN users u ON u.id = p.user_id
		JOIN memberships m ON m.workspace_id = p.workspace_id AND m.user_id = p.user_id
		WHERE p.workspace_id = $1 AND p.brand_id = $2 AND p.status = 'online' AND p.seen_at > now() - `+support.PresenceWindow+`
		ORDER BY 2, 1`, ws, brand)
	if err != nil {
		return v, err
	}
	if v.Online, err = pgx.CollectRows(rows, pgx.RowToStructByPos[supportPerson]); err != nil {
		return v, err
	}
	if v.Online == nil {
		v.Online = []supportPerson{}
	}
	if user == "" {
		return v, nil
	}
	// Online with a stale check-in reads as away.
	var st string
	err = s.pool.QueryRow(ctx, `SELECT CASE WHEN status = 'online' AND seen_at > now() - `+support.PresenceWindow+` THEN 'online' ELSE 'away' END
		FROM inbox_presence WHERE brand_id = $1 AND user_id = $2 AND workspace_id = $3`, brand, user, ws).Scan(&st)
	if err == pgx.ErrNoRows {
		return v, nil
	}
	v.Status = &st
	return v, err
}

func (s *Server) getInboxPresence(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.inboxBrand(w, r)
	if !ok {
		return
	}
	v, err := s.inboxPresence(r, a.WorkspaceID, brand, a.UserID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) putInboxPresence(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Status string `json:"status"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Status != "online" && req.Status != "away" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is online or away")
		return
	}
	if a.UserID == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "only a signed-in person can be online or away")
		return
	}
	brand, ok := s.inboxBrand(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Sending the same status again is the page checking in: only a
		// change is worth recording.
		var before *string
		if err := tx.QueryRow(ctx, `SELECT (SELECT status FROM inbox_presence WHERE brand_id = $1 AND user_id = $2)`, brand, a.UserID).Scan(&before); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO inbox_presence (workspace_id, brand_id, user_id, status) VALUES ($1, $2, $3, $4)
			ON CONFLICT (brand_id, user_id) DO UPDATE SET status = EXCLUDED.status, seen_at = now(),
				updated_at = CASE WHEN inbox_presence.status = EXCLUDED.status THEN inbox_presence.updated_at ELSE now() END`,
			a.WorkspaceID, brand, a.UserID, req.Status); err != nil {
			return err
		}
		if before != nil && *before == req.Status {
			return nil
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "inbox.presence_changed", "user", a.UserID, map[string]any{"status": req.Status, "brand_id": brand})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v, err := s.inboxPresence(r, a.WorkspaceID, brand, a.UserID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
