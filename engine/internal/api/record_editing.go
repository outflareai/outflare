package api

import (
	"context"
	"net/http"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// The workspace switch "Only owners and admins can edit records"
// (workspaces.records_edit_owners_only, docs/customers.md): when it is on,
// a person on any other role, or an API token one of them made, can read
// customer records but not change them. Automatic writes (imports, syncs,
// ingest, jobs) don't pass through these routes and are not affected.

// ownerOrAdmin reports whether the caller is an owner or admin: a person
// on that starting role, or a token made by one (a token whose maker left
// is neither).
func (s *Server) ownerOrAdmin(ctx context.Context, a *access) (bool, error) {
	if a.TokenID == "" {
		return a.RoleID == nil && (a.Role == auth.RoleOwner || a.Role == auth.RoleAdmin), nil
	}
	if a.TokenOwnerID == "" {
		return false, nil
	}
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM memberships WHERE workspace_id = $1 AND user_id = $2
		AND role_id IS NULL AND role IN ('owner', 'admin')`, a.WorkspaceID, a.TokenOwnerID).Scan(&n)
	return n > 0, err
}

// recordEditsAllowed answers whether the caller may change records now,
// writing the 403 when not.
func (s *Server) recordEditsAllowed(w http.ResponseWriter, r *http.Request) bool {
	a := accessFrom(r.Context())
	ownersOnly, err := s.recordsEditOwnersOnly(r.Context(), a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return false
	}
	if !ownersOnly {
		return true
	}
	ok, err := s.ownerOrAdmin(r.Context(), a)
	if err != nil {
		s.internal(w, r, err)
		return false
	}
	if ok {
		return true
	}
	writeError(w, r, http.StatusForbidden, "records_locked",
		"in this workspace only owners and admins change customer records (Settings, Workspace); ask one of them")
	return false
}

func (s *Server) recordsEditOwnersOnly(ctx context.Context, ws string) (bool, error) {
	var on bool
	err := s.pool.QueryRow(ctx, `SELECT records_edit_owners_only FROM workspaces WHERE id = $1`, ws).Scan(&on)
	return on, err
}

// editsRecords wraps a handler that changes customer records.
func (s *Server) editsRecords(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.recordEditsAllowed(w, r) {
			next(w, r)
		}
	}
}
