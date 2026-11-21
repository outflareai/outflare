package api

// Exclusions on a campaign (plans/ad-tracker.md, B8: placement and site
// blocks): a person, or an agent within its grants, stops a campaign
// showing on one placement (a Zeropark target, a Google Display site or
// app) or lets it show there again. Each is an ad_changes row
// (exclusion_add, exclusion_remove) carried out by the findings job
// (adsuggest.JobApply) on the workspace's own connection, like the
// exclusions a finding applies. Adding one can only spend less; taking one
// out spends again, so an agent's always waits for a person (adsHold).

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/actionctx"
	"github.com/outflareai/outflare/engine/internal/adsuggest"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

func (s *Server) routesAdExclusions(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/brands/{brand}/ads/campaigns/{id}/exclusions", s.onBrand(auth.ScopeAdsManage, s.setAdExclusion))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/campaigns/{id}/bid", s.onBrand(auth.ScopeAdsManage, s.setAdBid))
}

func (s *Server) setAdExclusion(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	var in struct {
		Kind   string `json:"kind"`
		Value  string `json:"value"`
		Detail string `json:"detail"`
		Label  string `json:"label"`
		Action string `json:"action"`
	}
	if !decode(w, r, &in) {
		return
	}
	add, ok := targetingAction(w, r, in.Action)
	if !ok {
		return
	}
	x := connectors.AdExclusion{Kind: strings.TrimSpace(in.Kind), Value: strings.TrimSpace(in.Value), Detail: strings.TrimSpace(in.Detail),
		Label: trimTo(strings.TrimSpace(in.Label), 200)}
	if x.Kind == "" || x.Value == "" || len(x.Value) > 500 || len(x.Detail) > 100 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind and value say what to exclude: a placement and its address or id, for example")
		return
	}
	a := accessFrom(r.Context())
	c, ok := s.loadAdCampaign(w, r, nil, a, r.PathValue("id"))
	if !ok {
		return
	}
	if c.status == connectors.AdRemoved {
		writeError(w, r, http.StatusConflict, "not_active", "the campaign was removed on the platform")
		return
	}
	o, err := s.integrations.Open(r.Context(), s.pool, a.WorkspaceID, c.integrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		writeError(w, r, http.StatusConflict, "not_active", "the campaign's ad platform is no longer connected")
		return
	}
	if err != nil {
		s.connectorFailed(w, r, err)
		return
	}
	m, ok := o.Connector.(connectors.AdExclusionManager)
	if !ok {
		writeError(w, r, http.StatusUnprocessableEntity, "connector_invalid", o.Connector.Definition().Name+" exclusions can't be changed here yet")
		return
	}
	if kinds := m.ExclusionKinds(); !slices.Contains(kinds, x.Kind) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", o.Connector.Definition().Name+" takes these exclusions: "+strings.Join(kinds, ", "))
		return
	}
	kind := adsuggest.ChangeExclusionRemove
	if add {
		kind = adsuggest.ChangeExclusionAdd
	}
	// One change at a time for the same exclusion, so a retry queues one.
	var queued bool
	if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM ad_changes WHERE ad_entity_id = $1 AND kind IN ('exclusion_add', 'exclusion_remove')
		AND status = 'queued' AND after->'exclusion'->>'kind' = $2 AND after->'exclusion'->>'value' = $3)`, c.id, x.Kind, x.Value).Scan(&queued); err != nil {
		s.internal(w, r, err)
		return
	}
	if queued {
		writeError(w, r, http.StatusConflict, "not_active", "a change to this exclusion is already queued; GET .../ads/changes shows it")
		return
	}
	var out adChangeOut
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		after := adsuggest.Change{Exclusion: &x}
		id, err := adsuggest.QueueChange(r.Context(), tx, a.WorkspaceID, c.brand, c.id, kind, adsuggest.Change{}, after, actorRef(a))
		if err != nil {
			return err
		}
		if action := actionctx.ID(r.Context()); action != "" {
			if _, err := tx.Exec(r.Context(), `UPDATE ad_changes SET action_id = $2 WHERE id = $1`, id, action); err != nil {
				return err
			}
		}
		data := map[string]any{"change_id": id, "campaign_id": c.id, "external_id": c.externalID, "connector": c.connector, "brand_id": c.brand, "after": after}
		if err := recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, adsuggest.ActionFor(kind), "ad_campaign", c.id, data,
			map[string]any{"connector": c.connector, "kind": kind, "exclusion_kind": x.Kind}); err != nil {
			return err
		}
		out, err = scanAdChange(tx.QueryRow(r.Context(), `SELECT `+adChangeCols+` `+adChangeFrom+` WHERE c.id = $1`, id))
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "change": out})
}

// setAdBid sets a campaign's bid, or the bid of one of its placements
// (plans/ad-tracker.md, B8: bids). It is queued as a bid change the
// findings job carries out: the job reads what the network bids now,
// keeps it as the change's before, and, with only_lower, refuses to raise
// it. An agent's change without only_lower always waits for a person
// (adsHold).
func (s *Server) setAdBid(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	var in struct {
		BidMicros *int64 `json:"bid_micros"`
		Placement string `json:"placement"`
		OnlyLower bool   `json:"only_lower"`
	}
	if !decode(w, r, &in) {
		return
	}
	placement := strings.TrimSpace(in.Placement)
	if in.BidMicros == nil || *in.BidMicros <= 0 || len(placement) > 500 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "bid_micros is the new bid in millionths of the account's currency, more than nothing")
		return
	}
	a := accessFrom(r.Context())
	c, ok := s.loadAdCampaign(w, r, nil, a, r.PathValue("id"))
	if !ok {
		return
	}
	if c.status == connectors.AdRemoved {
		writeError(w, r, http.StatusConflict, "not_active", "the campaign was removed on the platform")
		return
	}
	o, err := s.integrations.Open(r.Context(), s.pool, a.WorkspaceID, c.integrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		writeError(w, r, http.StatusConflict, "not_active", "the campaign's ad platform is no longer connected")
		return
	}
	if err != nil {
		s.connectorFailed(w, r, err)
		return
	}
	m, ok := o.Connector.(connectors.AdBidManager)
	if !ok {
		writeError(w, r, http.StatusUnprocessableEntity, "connector_invalid", o.Connector.Definition().Name+" bids can't be set here yet")
		return
	}
	if lo, hi := m.BidRange(); *in.BidMicros < lo || *in.BidMicros > hi {
		writeError(w, r, http.StatusBadRequest, "invalid_request", o.Connector.Definition().Name+" takes bids from "+formatMicros(lo)+" to "+formatMicros(hi))
		return
	}
	var queued bool
	if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM ad_changes WHERE ad_entity_id = $1 AND kind = 'bid' AND status = 'queued'
		AND COALESCE(after->'bid'->>'placement', '') = $2)`, c.id, placement).Scan(&queued); err != nil {
		s.internal(w, r, err)
		return
	}
	if queued {
		writeError(w, r, http.StatusConflict, "not_active", "a bid change for this is already queued; GET .../ads/changes shows it")
		return
	}
	var out adChangeOut
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		after := adsuggest.Change{Bid: &adsuggest.BidChange{Micros: *in.BidMicros, Placement: placement, OnlyLower: in.OnlyLower}}
		id, err := adsuggest.QueueChange(r.Context(), tx, a.WorkspaceID, c.brand, c.id, adsuggest.ChangeBid, adsuggest.Change{}, after, actorRef(a))
		if err != nil {
			return err
		}
		if action := actionctx.ID(r.Context()); action != "" {
			if _, err := tx.Exec(r.Context(), `UPDATE ad_changes SET action_id = $2 WHERE id = $1`, id, action); err != nil {
				return err
			}
		}
		data := map[string]any{"change_id": id, "campaign_id": c.id, "external_id": c.externalID, "connector": c.connector, "brand_id": c.brand, "after": after}
		if err := recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, adsuggest.ActionFor(adsuggest.ChangeBid), "ad_campaign", c.id, data,
			map[string]any{"connector": c.connector, "kind": adsuggest.ChangeBid, "bid_micros": *in.BidMicros, "placement": placement != "", "only_lower": in.OnlyLower}); err != nil {
			return err
		}
		out, err = scanAdChange(tx.QueryRow(r.Context(), `SELECT `+adChangeCols+` `+adChangeFrom+` WHERE c.id = $1`, id))
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "change": out})
}
