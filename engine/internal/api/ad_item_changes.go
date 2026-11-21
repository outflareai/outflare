package api

// Changes below the campaign (plans/ad-tracker.md, B8: ad set and ad
// pause and budget): a person, or an agent within its grants, pauses or
// resumes one ad group (Meta's ad set) or one ad, and sets an ad group's
// own daily budget, on the workspace's own ad account. Each is an
// ad_changes row on the item's campaign, with the ad group or ad named
// in its after, carried out by the findings job (adsuggest.JobApply)
// like the ad pauses a finding applies. A pause, and a lower budget, can
// only spend less; a resume and a raise spend more, so an agent's always
// wait for a person (adsHold).

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/actionctx"
	"github.com/outflareai/outflare/engine/internal/adsuggest"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

func (s *Server) routesAdItemChanges(mux *http.ServeMux) {
	manage := auth.ScopeAdsManage
	mux.HandleFunc("POST /v1/brands/{brand}/ads/items/{id}/pause", s.onBrand(manage, func(w http.ResponseWriter, r *http.Request) { s.setAdItemStatus(w, r, false) }))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/items/{id}/resume", s.onBrand(manage, func(w http.ResponseWriter, r *http.Request) { s.setAdItemStatus(w, r, true) }))
	mux.HandleFunc("PATCH /v1/brands/{brand}/ads/items/{id}", s.onBrand(manage, s.updateAdItem))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/items/{id}/bid", s.onBrand(manage, func(w http.ResponseWriter, r *http.Request) { s.setAdUnitBid(w, r, false) }))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/keywords/{id}/bid", s.onBrand(manage, func(w http.ResponseWriter, r *http.Request) { s.setAdUnitBid(w, r, true) }))
}

// adItemRow is an ad group or an ad as a change reads it.
type adItemRow struct {
	id, externalID, name, status, level string
	budget                              *int64
	// groupExternal is the platform's id of the ad group: the item's own,
	// or the ad's parent.
	groupExternal string
	campaignID    string
}

// what is the item's kind as a person reads it.
func (i adItemRow) what() string {
	if i.level == connectors.AdLevelAd {
		return "ad"
	}
	return "ad group"
}

// loadAdItem reads one ad group or ad of the workspace with its campaign,
// answering the error itself when it can't. Whether the caller may change
// it is the campaign's to say (loadAdCampaign).
func (s *Server) loadAdItem(w http.ResponseWriter, r *http.Request, id string) (adItemRow, bool) {
	var i adItemRow
	notFound := func() (adItemRow, bool) {
		writeError(w, r, http.StatusNotFound, "not_found", "ad group or ad not found")
		return i, false
	}
	if !isUUID(id) {
		return notFound()
	}
	var group, campaign *string
	err := s.pool.QueryRow(r.Context(), `
		SELECT e.id, e.external_id, e.name, e.status, e.level, e.daily_budget_micros,
			CASE WHEN e.level = 'ad' THEN g.external_id ELSE e.external_id END,
			CASE WHEN e.level = 'ad' THEN gc.id ELSE c.id END
		FROM ad_entities e
		LEFT JOIN ad_entities g ON e.level = 'ad' AND g.id = e.parent_id AND g.level = 'ad_group'
		LEFT JOIN ad_entities gc ON gc.id = g.parent_id AND gc.level = 'campaign'
		LEFT JOIN ad_entities c ON e.level = 'ad_group' AND c.id = e.parent_id AND c.level = 'campaign'
		WHERE e.workspace_id = $1 AND e.id = $2 AND e.level IN ('ad_group', 'ad')`, accessFrom(r.Context()).WorkspaceID, id).
		Scan(&i.id, &i.externalID, &i.name, &i.status, &i.level, &i.budget, &group, &campaign)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (group == nil || campaign == nil)) {
		return notFound()
	}
	if err != nil {
		s.internal(w, r, err)
		return i, false
	}
	i.groupExternal, i.campaignID = *group, *campaign
	return i, true
}

// change is the item as an ad_changes row names it.
func (i adItemRow) change() adsuggest.Change {
	ch := adsuggest.Change{AdGroupID: i.groupExternal, Name: trimTo(i.name, 200)}
	if i.level == connectors.AdLevelAd {
		ch.AdID = i.externalID
	}
	return ch
}

// adItemChangeQueued answers 409 when one of the kinds is already queued
// for the item, so a retry or a second click queues one change.
func (s *Server) adItemChangeQueued(w http.ResponseWriter, r *http.Request, c adCampaignRow, i adItemRow, kinds ...string) bool {
	ch := i.change()
	var queued bool
	if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM ad_changes WHERE ad_entity_id = $1 AND kind = ANY($2) AND status = 'queued'
		AND COALESCE(after->>'ad_group_id', '') = $3 AND COALESCE(after->>'ad_id', '') = $4)`, c.id, kinds, ch.AdGroupID, ch.AdID).Scan(&queued); err != nil {
		s.internal(w, r, err)
		return true
	}
	if queued {
		writeError(w, r, http.StatusConflict, "not_active", "a change to this "+i.what()+" is already queued; GET .../ads/changes shows it")
		return true
	}
	return false
}

// queueAdItemChange records the change with its audit event and queues
// the job that makes it.
func (s *Server) queueAdItemChange(w http.ResponseWriter, r *http.Request, a *access, c adCampaignRow, i adItemRow, kind string, before, after adsuggest.Change, props map[string]any) {
	var out adChangeOut
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		id, err := adsuggest.QueueChange(r.Context(), tx, a.WorkspaceID, c.brand, c.id, kind, before, after, actorRef(a))
		if err != nil {
			return err
		}
		if action := actionctx.ID(r.Context()); action != "" {
			if _, err := tx.Exec(r.Context(), `UPDATE ad_changes SET action_id = $2 WHERE id = $1`, id, action); err != nil {
				return err
			}
		}
		data := map[string]any{"change_id": id, "campaign_id": c.id, "external_id": c.externalID, "connector": c.connector, "brand_id": c.brand,
			"item_id": i.id, "level": i.level, "before": before, "after": after}
		props["connector"], props["kind"], props["level"] = c.connector, kind, i.level
		if err := recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, adsuggest.ActionFor(kind), "ad_campaign", c.id, data, props); err != nil {
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

// openAdItemConnector opens the campaign's platform connection for a
// change below the campaign, answering the error itself.
func (s *Server) openAdItemConnector(w http.ResponseWriter, r *http.Request, a *access, c adCampaignRow) (connectors.Connector, bool) {
	o, err := s.integrations.Open(r.Context(), s.pool, a.WorkspaceID, c.integrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		writeError(w, r, http.StatusConflict, "not_active", "the campaign's ad platform is no longer connected")
		return nil, false
	}
	if err != nil {
		s.connectorFailed(w, r, err)
		return nil, false
	}
	return o.Connector, true
}

// setAdItemStatus pauses or resumes one ad group or one ad.
func (s *Server) setAdItemStatus(w http.ResponseWriter, r *http.Request, active bool) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	i, ok := s.loadAdItem(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	c, ok := s.loadAdCampaign(w, r, nil, a, i.campaignID)
	if !ok {
		return
	}
	want := connectors.AdPaused
	if active {
		want = connectors.AdActive
	}
	switch {
	case i.status == connectors.AdRemoved || c.status == connectors.AdRemoved:
		writeError(w, r, http.StatusConflict, "not_active", "the "+i.what()+" was removed on the platform")
		return
	case i.status == want:
		writeError(w, r, http.StatusConflict, "not_active", "the "+i.what()+" is already "+want)
		return
	}
	pause, resume := adsuggest.ChangeAdGroupPause, adsuggest.ChangeAdGroupResume
	if i.level == connectors.AdLevelAd {
		pause, resume = adsuggest.ChangeAdPause, adsuggest.ChangeAdResume
	}
	kind := pause
	if active {
		kind = resume
	}
	if s.adItemChangeQueued(w, r, c, i, pause, resume) {
		return
	}
	conn, ok := s.openAdItemConnector(w, r, a, c)
	if !ok {
		return
	}
	can := false
	if i.level == connectors.AdLevelAd {
		_, can = conn.(connectors.AdStatusManager)
	} else {
		_, can = conn.(connectors.AdGroupManager)
	}
	if !can {
		writeError(w, r, http.StatusUnprocessableEntity, "connector_invalid", conn.Definition().Name+" "+i.what()+"s can be read here but not paused or resumed yet")
		return
	}
	before, after := i.change(), i.change()
	before.Status, after.Status = i.status, want
	s.queueAdItemChange(w, r, a, c, i, kind, before, after, map[string]any{})
}

// updateAdItem sets an ad group's own daily budget.
func (s *Server) updateAdItem(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	var in struct {
		DailyBudgetMicros *int64 `json:"daily_budget_micros"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.DailyBudgetMicros == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "daily_budget_micros is required: the ad group's daily budget in micros of its account's currency")
		return
	}
	if *in.DailyBudgetMicros < 10_000 || *in.DailyBudgetMicros > adBudgetMax {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "daily_budget_micros is at least 10000 (a cent) and at most 1000000000000 (a million)")
		return
	}
	i, ok := s.loadAdItem(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	c, ok := s.loadAdCampaign(w, r, nil, a, i.campaignID)
	if !ok {
		return
	}
	switch {
	case i.level != connectors.AdLevelGroup:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "an ad has no budget of its own: set its ad group's, or its campaign's")
		return
	case i.status == connectors.AdRemoved || c.status == connectors.AdRemoved:
		writeError(w, r, http.StatusConflict, "not_active", "the ad group was removed on the platform")
		return
	case i.budget == nil:
		writeError(w, r, http.StatusConflict, "not_active", "this ad group has no daily budget of its own: its campaign holds the budget, so change it there")
		return
	case *i.budget == *in.DailyBudgetMicros:
		writeError(w, r, http.StatusConflict, "not_active", "the ad group's daily budget is already that")
		return
	}
	if s.adItemChangeQueued(w, r, c, i, adsuggest.ChangeAdGroupBudget) {
		return
	}
	conn, ok := s.openAdItemConnector(w, r, a, c)
	if !ok {
		return
	}
	if _, can := conn.(connectors.AdGroupBudgetManager); !can {
		writeError(w, r, http.StatusUnprocessableEntity, "connector_invalid", conn.Definition().Name+" ad groups have no budget that can be set here")
		return
	}
	before, after := i.change(), i.change()
	before.DailyBudgetMicros, after.DailyBudgetMicros = i.budget, in.DailyBudgetMicros
	s.queueAdItemChange(w, r, a, c, i, adsuggest.ChangeAdGroupBudget, before, after, map[string]any{"daily_budget_micros": *in.DailyBudgetMicros, "raise": *in.DailyBudgetMicros > *i.budget})
}

// setAdUnitBid sets a bid made by hand below the campaign (plans/
// ad-tracker.md, B8: manual CPC): an ad group's default bid for a click,
// or one keyword's. It is a bid change like a campaign's (ad_exclusions.go,
// setAdBid), on the item's campaign, naming the ad group and keyword: the
// job reads the bid in effect now, keeps it as the change's before, and,
// with only_lower, refuses to raise it. A campaign that bids
// automatically ignores these bids, so it is refused before anything is
// queued. An agent's change without only_lower always waits for a person
// (adsHold).
func (s *Server) setAdUnitBid(w http.ResponseWriter, r *http.Request, keyword bool) {
	if !s.needIntegrations(w, r) {
		return
	}
	var in struct {
		BidMicros *int64 `json:"bid_micros"`
		OnlyLower bool   `json:"only_lower"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.BidMicros == nil || *in.BidMicros <= 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "bid_micros is the new bid in millionths of the account's currency, more than nothing")
		return
	}
	a := accessFrom(r.Context())
	// What the bid is for: the ad group, and the keyword in it.
	after := adsuggest.Change{Bid: &adsuggest.BidChange{Micros: *in.BidMicros, OnlyLower: in.OnlyLower}}
	var campaignID, what, itemID, level string
	if keyword {
		k, ok := s.loadAdChild(r.Context(), w, r, `SELECT k.id, k.external_id, k.text, k.status, g.external_id, c.id, k.match_type
			FROM ad_keywords k JOIN ad_entities g ON g.id = k.ad_group_id LEFT JOIN ad_entities c ON c.id = g.parent_id AND c.level = 'campaign'
			WHERE k.workspace_id = $1 AND k.id = $2`, r.PathValue("id"), "keyword")
		if !ok {
			return
		}
		after.AdGroupID, after.KeywordID, after.Keyword, after.MatchType = k.groupExternal, k.externalID, k.name, k.extra
		campaignID, what, itemID, level = k.campaignID, "keyword", k.id, "keyword"
	} else {
		i, ok := s.loadAdItem(w, r, r.PathValue("id"))
		if !ok {
			return
		}
		switch {
		case i.level != connectors.AdLevelGroup:
			writeError(w, r, http.StatusBadRequest, "invalid_request", "an ad has no bid of its own: set its ad group's, or a keyword's")
			return
		case i.status == connectors.AdRemoved:
			writeError(w, r, http.StatusConflict, "not_active", "the ad group was removed on the platform")
			return
		}
		ch := i.change()
		after.AdGroupID, after.Name = ch.AdGroupID, ch.Name
		campaignID, what, itemID, level = i.campaignID, "ad group", i.id, i.level
	}
	c, ok := s.loadAdCampaign(w, r, nil, a, campaignID)
	if !ok {
		return
	}
	if c.status == connectors.AdRemoved {
		writeError(w, r, http.StatusConflict, "not_active", "the campaign was removed on the platform")
		return
	}
	conn, ok := s.openAdItemConnector(w, r, a, c)
	if !ok {
		return
	}
	m, can := conn.(connectors.AdUnitBidManager)
	if !can {
		writeError(w, r, http.StatusUnprocessableEntity, "connector_invalid", conn.Definition().Name+" "+what+" bids can't be set here yet")
		return
	}
	// A bid made by hand counts only where the campaign bids by hand.
	var strategy string
	if err := s.pool.QueryRow(r.Context(), `SELECT bidding_strategy FROM ad_entities WHERE id = $1`, c.id).Scan(&strategy); err != nil {
		s.internal(w, r, err)
		return
	}
	if strategy != "manual_cpc" {
		writeError(w, r, http.StatusConflict, "not_active", "this campaign bids automatically, so the platform ignores a bid set by hand on its ad groups and keywords; change how the campaign bids, or its target, instead")
		return
	}
	if lo, hi := m.UnitBidRange(); *in.BidMicros < lo || *in.BidMicros > hi {
		writeError(w, r, http.StatusBadRequest, "invalid_request", conn.Definition().Name+" takes bids from "+formatMicros(lo)+" to "+formatMicros(hi))
		return
	}
	var queued bool
	if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM ad_changes WHERE ad_entity_id = $1 AND kind = 'bid' AND status = 'queued'
		AND COALESCE(after->>'ad_group_id', '') = $2 AND COALESCE(after->>'keyword_id', '') = $3)`, c.id, after.AdGroupID, after.KeywordID).Scan(&queued); err != nil {
		s.internal(w, r, err)
		return
	}
	if queued {
		writeError(w, r, http.StatusConflict, "not_active", "a bid change for this "+what+" is already queued; GET .../ads/changes shows it")
		return
	}
	var out adChangeOut
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		id, err := adsuggest.QueueChange(r.Context(), tx, a.WorkspaceID, c.brand, c.id, adsuggest.ChangeBid, adsuggest.Change{}, after, actorRef(a))
		if err != nil {
			return err
		}
		if action := actionctx.ID(r.Context()); action != "" {
			if _, err := tx.Exec(r.Context(), `UPDATE ad_changes SET action_id = $2 WHERE id = $1`, id, action); err != nil {
				return err
			}
		}
		data := map[string]any{"change_id": id, "campaign_id": c.id, "external_id": c.externalID, "connector": c.connector, "brand_id": c.brand,
			"item_id": itemID, "level": level, "after": after}
		if err := recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, adsuggest.ActionFor(adsuggest.ChangeBid), "ad_campaign", c.id, data,
			map[string]any{"connector": c.connector, "kind": adsuggest.ChangeBid, "bid_micros": *in.BidMicros, "level": level, "only_lower": in.OnlyLower}); err != nil {
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
