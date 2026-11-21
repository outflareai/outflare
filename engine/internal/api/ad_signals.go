package api

import (
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Where a campaign is held back and where its money goes: the share of
// searches it missed for want of budget or rank, and its numbers by
// device, network, country, placement and hour. Read live from the
// platform, as the recommendations read it.

func (s *Server) routesAdSignals(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/ads/campaigns/{id}/signals", s.onBrand(auth.ScopeAnalyticsRead, s.getAdCampaignSignals))
}

func (s *Server) getAdCampaignSignals(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !isUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "campaign not found")
		return
	}
	q := r.URL.Query()
	to := time.Now().UTC().Truncate(24 * time.Hour)
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.DateOnly, v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "to should be a date like 2026-10-24")
			return
		}
		to = t
	}
	from := to.AddDate(0, 0, -27)
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.DateOnly, v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "from should be a date like 2026-09-27")
			return
		}
		from = t
	}
	if to.Before(from) || to.Sub(from) > 366*24*time.Hour {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "from must be on or before to, and at most a year before it")
		return
	}
	var (
		external, integrationID string
		brand                   *string
		acct                    connectors.AdAccount
	)
	err := s.pool.QueryRow(r.Context(), `SELECT e.external_id, COALESCE(e.brand_id, a.brand_id), a.integration_id, a.external_id, a.name, a.currency, a.time_zone, a.meta
		FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id
		WHERE e.workspace_id = $1 AND e.id = $2 AND e.level = 'campaign' AND a.status = 'active'`, a.WorkspaceID, id).
		Scan(&external, &brand, &integrationID, &acct.ID, &acct.Name, &acct.Currency, &acct.TimeZone, &acct.Meta)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.sees(brand)) {
		writeError(w, r, http.StatusNotFound, "not_found", "campaign not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	o, err := s.integrations.Open(r.Context(), s.pool, a.WorkspaceID, integrationID)
	if err != nil {
		s.connectorFailed(w, r, err)
		return
	}
	out := map[string]any{"supported": false, "from": from.Format(time.DateOnly), "to": to.Format(time.DateOnly), "currency": acct.Currency,
		"budget_lost_impression_share": 0.0, "rank_lost_impression_share": 0.0, "search_partners": false, "display_expansion": false, "segments": []connectors.AdSegment{}}
	rd, ok := o.Connector.(connectors.AdSignalsReader)
	if !ok {
		writeJSON(w, http.StatusOK, out)
		return
	}
	sig, err := rd.AdCampaignSignals(r.Context(), o.Client, o.Creds, acct, external, from.Format(time.DateOnly), to.Format(time.DateOnly))
	if err != nil {
		s.connectorFailed(w, r, err)
		return
	}
	segs := sig.Segments
	if segs == nil {
		segs = []connectors.AdSegment{}
	}
	sort.SliceStable(segs, func(i, j int) bool {
		if segs[i].Kind != segs[j].Kind {
			return segs[i].Kind < segs[j].Kind
		}
		return segs[i].SpendMicros > segs[j].SpendMicros
	})
	out["supported"] = true
	out["budget_lost_impression_share"], out["rank_lost_impression_share"] = sig.BudgetLostImpressionShare, sig.RankLostImpressionShare
	out["search_partners"], out["display_expansion"], out["segments"] = sig.SearchPartners, sig.DisplayExpansion, segs
	writeJSON(w, http.StatusOK, out)
}
