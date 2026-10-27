package api

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// Brand rules are the workspace's rules between its brands. The one rule
// so far: with cross_brand_days set, no person gets a first email from one
// brand within that many days of an email from another (the send policy
// checks it; see policy.OtherBrands).

type brandRules struct {
	CrossBrandDays *int `json:"cross_brand_days"`
}

func (s *Server) getBrandRules(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var out brandRules
	if err := s.pool.QueryRow(r.Context(), `SELECT cross_brand_days FROM workspaces WHERE id = $1`, a.WorkspaceID).Scan(&out.CrossBrandDays); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) updateBrandRules(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req map[string]json.RawMessage
	if !decode(w, r, &req) {
		return
	}
	raw, ok := req["cross_brand_days"]
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "cross_brand_days is required: a number of days from 1 to 365, or null to turn the rule off")
		return
	}
	var days *int
	if err := json.Unmarshal(raw, &days); err != nil || (days != nil && (*days < 1 || *days > 365)) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "cross_brand_days must be a number of days from 1 to 365, or null to turn the rule off")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var old *int
		if err := tx.QueryRow(ctx, `SELECT cross_brand_days FROM workspaces WHERE id = $1 FOR UPDATE`, a.WorkspaceID).Scan(&old); err != nil {
			return err
		}
		if (old == nil) == (days == nil) && (old == nil || *old == *days) {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE workspaces SET cross_brand_days = $2 WHERE id = $1`, a.WorkspaceID, days); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "brand.rules_updated", "workspace", a.WorkspaceID,
			map[string]any{"cross_brand_days": days, "previous_cross_brand_days": old})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, brandRules{CrossBrandDays: days})
}
