package api

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// Targets are the brand's own: the cost per conversion it wants from a
// campaign, or from every campaign of an account without one of its own.
// Outflare only reads them to say whether a campaign is over or under; it
// never sends them to the platform, which keeps its own bid targets
// (bidding_strategy, platform_target_cpa_micros).

func (s *Server) routesAdTargets(mux *http.ServeMux) {
	mux.HandleFunc("PUT /v1/workspaces/{ws}/ads/campaigns/{id}/target", s.inBrand(auth.ScopeAnalyticsManage, s.setAdCampaignTarget))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/ads/accounts/{id}/target", s.inWorkspace(auth.ScopeAnalyticsManage, s.setAdAccountTarget))
}

type adTargetIn struct {
	CostPerConversionMicros *int64 `json:"cost_per_conversion_micros"`
}

func (t adTargetIn) valid(w http.ResponseWriter, r *http.Request) bool {
	if t.CostPerConversionMicros != nil && (*t.CostPerConversionMicros <= 0 || *t.CostPerConversionMicros > 1e15) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "cost_per_conversion_micros is a positive amount in micros of the account's currency (25 USD is 25000000), or null for none")
		return false
	}
	return true
}

func (s *Server) setAdCampaignTarget(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req adTargetIn
	if !decode(w, r, &req) || !req.valid(w, r) {
		return
	}
	id := r.PathValue("id")
	if !isUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "campaign not found")
		return
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var brand *string
		if err := tx.QueryRow(r.Context(), `SELECT COALESCE(e.brand_id, a.brand_id) FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id
			WHERE e.workspace_id = $1 AND e.id = $2 AND e.level = 'campaign' FOR UPDATE OF e`, a.WorkspaceID, id).Scan(&brand); err != nil {
			return err
		}
		if !a.sees(brand) {
			return pgx.ErrNoRows
		}
		if !a.writes(brand) {
			return errForbiddenBrand
		}
		if _, err := tx.Exec(r.Context(), `UPDATE ad_entities SET target_cost_per_conversion_micros = $3, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, id, req.CostPerConversionMicros); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "ad_campaign.target_set", "ad_entity", id,
			map[string]any{"cost_per_conversion_micros": req.CostPerConversionMicros})
	})
	s.targetSet(w, r, err, "campaign not found", req)
}

func (s *Server) setAdAccountTarget(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req adTargetIn
	if !decode(w, r, &req) || !req.valid(w, r) {
		return
	}
	id := r.PathValue("id")
	if !isUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "ad account not found")
		return
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var brand *string
		if err := tx.QueryRow(r.Context(), `SELECT brand_id FROM ad_accounts WHERE workspace_id = $1 AND id = $2 FOR UPDATE`, a.WorkspaceID, id).Scan(&brand); err != nil {
			return err
		}
		if !a.sees(brand) {
			return pgx.ErrNoRows
		}
		if !a.writes(brand) {
			return errForbiddenBrand
		}
		if _, err := tx.Exec(r.Context(), `UPDATE ad_accounts SET target_cost_per_conversion_micros = $3, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, id, req.CostPerConversionMicros); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "ad_account.target_set", "ad_account", id,
			map[string]any{"cost_per_conversion_micros": req.CostPerConversionMicros})
	})
	s.targetSet(w, r, err, "ad account not found", req)
}

var errForbiddenBrand = errors.New("forbidden brand")

func (s *Server) targetSet(w http.ResponseWriter, r *http.Request, err error, notFound string, req adTargetIn) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, r, http.StatusNotFound, "not_found", notFound)
	case errors.Is(err, errForbiddenBrand):
		writeError(w, r, http.StatusForbidden, "forbidden", "you can only set targets for your own brands")
	case err != nil:
		s.internal(w, r, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"cost_per_conversion_micros": req.CostPerConversionMicros})
	}
}
