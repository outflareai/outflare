package api

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
)

// Contact rules say what may go out to whom, by each person's contact
// basis (customers.Basis). The one rule people set: whether customers and
// people of unknown origin who never agreed to marketing email may get it.
// Third-party leads never do without agreeing.

type contactRules struct {
	CustomerMarketing string `json:"customer_marketing"`
}

func (s *Server) getContactRules(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	rule, err := customers.MarketingRule(r.Context(), s.pool, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, contactRules{CustomerMarketing: rule})
}

func (s *Server) updateContactRules(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req contactRules
	if !decode(w, r, &req) {
		return
	}
	if req.CustomerMarketing != customers.MarketingOptIn && req.CustomerMarketing != customers.MarketingInferred {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "customer_marketing must be opt_in (only people who agreed get marketing email) or inferred (everyone but third-party leads does, unless they said no)")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var old string
		if err := tx.QueryRow(ctx, `SELECT customer_marketing FROM workspaces WHERE id = $1 FOR UPDATE`, a.WorkspaceID).Scan(&old); err != nil {
			return err
		}
		if old == req.CustomerMarketing {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE workspaces SET customer_marketing = $2 WHERE id = $1`, a.WorkspaceID, req.CustomerMarketing); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "contact_rules.updated", "workspace", a.WorkspaceID,
			map[string]any{"customer_marketing": req.CustomerMarketing, "previous_customer_marketing": old})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "workspace not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}
