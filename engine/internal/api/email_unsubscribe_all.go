package api

import (
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
)

// unsubscribeAllBrands turns off marketing email to an address in every
// brand at once (plans/brands/README.md, decision 5). An unsubscribe
// applies to the brand it came from; a workspace admin can apply one to
// every brand. In each brand the address's person gets a withdrawal of all
// marketing email, and a brand that has no one with the address gets a
// person holding it, so its later email to them stops too. Transactional
// email still goes, as for any unsubscribe.
func (s *Server) unsubscribeAllBrands(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Email string `json:"email"`
	}
	if !decode(w, r, &req) {
		return
	}
	addr, ok := validEmail(strings.ToLower(req.Email))
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "email must be an email address")
		return
	}
	ctx := r.Context()
	n := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM brands WHERE workspace_id = $1 ORDER BY created_at, id`, a.WorkspaceID)
		if err != nil {
			return err
		}
		brands, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, b := range brands {
			// The address is the person's in each brand: the admin applies
			// their request for it everywhere.
			rec, err := customers.EnsurePersonIn(ctx, tx, a.WorkspaceID, b, "email", addr, true, RequestID(ctx))
			if err != nil {
				return err
			}
			st, _, err := customers.Consent(ctx, tx, a.WorkspaceID, rec, "email", "", "marketing")
			if err != nil {
				return err
			}
			if st == customers.Withdrawn {
				continue
			}
			if err := customers.RecordConsent(ctx, tx, a.WorkspaceID, customers.ConsentChange{RecordID: rec, Channel: "email",
				Status: customers.Withdrawn, Source: "all_brands", Evidence: map[string]any{"by": "admin"}, RequestID: RequestID(ctx)}); err != nil {
				return err
			}
			n++
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "email.unsubscribed_all_brands", "workspace", a.WorkspaceID,
			map[string]any{"brands": len(brands), "changed": n})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"changed": n})
}
