package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adsync"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Targeting (step 5 of the paid ads plan): how a campaign bids, which
// customer list audiences it targets and which countries it shows in. The
// campaign's targeting and the account's audiences are read live from the
// platform; a change is an ad_changes row queued like a pause or a budget
// (ad_changes.go), and an agent's always waits for a person (adsHold).
// Audiences are the account's customer lists (GET .../ads/audiences),
// which journeys fill from smart lists (sync_audience).

func (s *Server) routesAdTargeting(mux *http.ServeMux) {
	manage := auth.ScopeAdsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/ads/campaigns/{id}/targeting", s.inBrand(auth.ScopeAnalyticsRead, s.getAdTargeting))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ads/campaigns/{id}/bidding", s.inBrand(manage, s.setAdBidding))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ads/campaigns/{id}/audiences", s.inBrand(manage, s.setAdAudience))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ads/campaigns/{id}/countries", s.inBrand(manage, s.setAdCountry))
}

// adTargeterFor opens the campaign's connection as an AdTargetingManager,
// answering 422 when the platform can't change targeting here.
func (s *Server) adTargeterFor(w http.ResponseWriter, r *http.Request, ws, integrationID string) (*connectors.Opened, connectors.AdTargetingManager, bool) {
	o, err := s.integrations.Open(r.Context(), s.pool, ws, integrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		writeError(w, r, http.StatusConflict, "not_active", "the campaign's ad platform is no longer connected")
		return nil, nil, false
	}
	if err != nil {
		s.connectorFailed(w, r, err)
		return nil, nil, false
	}
	t, ok := o.Connector.(connectors.AdTargetingManager)
	if !ok {
		writeError(w, r, http.StatusUnprocessableEntity, "connector_invalid", o.Connector.Definition().Name+" targeting can't be changed here yet")
		return nil, nil, false
	}
	return o, t, true
}

func (s *Server) getAdTargeting(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !isUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "campaign not found")
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
	t, ok := o.Connector.(connectors.AdTargetingManager)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"supported": false, "bidding_methods": []string{}})
		return
	}
	got, err := t.CampaignTargeting(r.Context(), o.Client, o.Creds, acct, external)
	if err != nil {
		s.connectorFailed(w, r, err)
		return
	}
	methods := t.BiddingMethods()
	if methods == nil {
		methods = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"supported": true, "targeting": got, "bidding_methods": methods})
}

// queueAdTargeting checks the campaign and the platform, then records the
// change and answers 202.
func (s *Server) queueAdTargeting(w http.ResponseWriter, r *http.Request, kind string, after adsync.ChangeState, check func(t connectors.AdTargetingManager) bool) {
	a := accessFrom(r.Context())
	c, ok := s.loadAdCampaign(w, r, nil, a, r.PathValue("id"))
	if !ok {
		return
	}
	if c.status == connectors.AdRemoved {
		writeError(w, r, http.StatusConflict, "not_active", "the campaign was removed on the platform")
		return
	}
	if s.adChangeQueued(w, r, c.id, kind) {
		return
	}
	_, t, ok := s.adTargeterFor(w, r, a.WorkspaceID, c.integrationID)
	if !ok || (check != nil && !check(t)) {
		return
	}
	var out adChangeOut
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = s.changeAdCampaign(r.Context(), tx, a, c, kind, after, nil)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "change": out})
}

func (s *Server) setAdBidding(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Strategy        string   `json:"strategy"`
		TargetCPAMicros *int64   `json:"target_cpa_micros"`
		TargetROAS      *float64 `json:"target_roas"`
	}
	if !decode(w, r, &in) {
		return
	}
	b := connectors.AdBidding{Strategy: strings.TrimSpace(in.Strategy)}
	switch b.Strategy {
	case connectors.BidMaxConversions:
		if in.TargetROAS != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "maximize_conversions takes target_cpa_micros, not target_roas")
			return
		}
		if in.TargetCPAMicros != nil && (*in.TargetCPAMicros < 10_000 || *in.TargetCPAMicros > adBudgetMax) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "target_cpa_micros is at least 10000 (a cent) and at most 1000000000000 (a million)")
			return
		}
		b.TargetCPAMicros = in.TargetCPAMicros
	case connectors.BidMaxValue:
		if in.TargetCPAMicros != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "maximize_conversion_value takes target_roas, not target_cpa_micros")
			return
		}
		if in.TargetROAS != nil && (*in.TargetROAS < 0.01 || *in.TargetROAS > 1000) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "target_roas is a return on spend between 0.01 and 1000 (3 means three times what was spent)")
			return
		}
		b.TargetROAS = in.TargetROAS
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "strategy is maximize_conversions or maximize_conversion_value")
		return
	}
	s.queueAdTargeting(w, r, adsync.ChangeBidding, adsync.ChangeState{Bidding: &b}, func(t connectors.AdTargetingManager) bool {
		for _, m := range t.BiddingMethods() {
			if m == b.Strategy {
				return true
			}
		}
		writeError(w, r, http.StatusUnprocessableEntity, "connector_invalid", t.Definition().Name+" bidding can't be set to "+b.Strategy+" here")
		return false
	})
}

// targetingAction reads add or remove.
func targetingAction(w http.ResponseWriter, r *http.Request, action string) (bool, bool) {
	switch action {
	case "add":
		return true, true
	case "remove":
		return false, true
	}
	writeError(w, r, http.StatusBadRequest, "invalid_request", "action is add or remove")
	return false, false
}

func (s *Server) setAdAudience(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AudienceID   string `json:"audience_id"`
		AudienceName string `json:"audience_name"`
		Action       string `json:"action"`
	}
	if !decode(w, r, &in) {
		return
	}
	add, ok := targetingAction(w, r, in.Action)
	if !ok {
		return
	}
	id := strings.TrimSpace(in.AudienceID)
	if id == "" || len(id) > 40 || strings.ContainsAny(id, "/?#") {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "audience_id is the audience's id on the platform, from GET .../ads/audiences")
		return
	}
	kind := adsync.ChangeAudienceRemove
	if add {
		kind = adsync.ChangeAudienceAdd
	}
	s.queueAdTargeting(w, r, kind, adsync.ChangeState{AudienceID: id, AudienceName: trimTo(strings.TrimSpace(in.AudienceName), 200)}, nil)
}

func (s *Server) setAdCountry(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Country string `json:"country"`
		Action  string `json:"action"`
	}
	if !decode(w, r, &in) {
		return
	}
	add, ok := targetingAction(w, r, in.Action)
	if !ok {
		return
	}
	code, ok := connectors.CountryCode(in.Country)
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "country is a two-letter code such as AU")
		return
	}
	kind := adsync.ChangeCountryRemove
	if add {
		kind = adsync.ChangeCountryAdd
	}
	s.queueAdTargeting(w, r, kind, adsync.ChangeState{Country: code}, nil)
}
