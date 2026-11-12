package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adsuggest"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/paidads"
)

// The findings queue by brand (plans/paid-ads/plan.md, "API"): the same
// rows and handlers as the brand's ads/suggestions routes, ranked for a person
// (open decisions by expected impact first, then alerts, then what was
// applied on its own this week, then what is measuring and judged), with
// approve, dismiss, acknowledge and revert. Revert makes the inverse
// change of an applied finding (restore the budget, resume what was
// paused, take the negative or exclusion out, re-enable the keyword or
// ad, put the target back) as an ad_changes row carried out by the same
// jobs, and marks the finding reverted. A refresh within the hour is a
// no-op and says so.

func (s *Server) routesAdFindings(mux *http.ServeMux) {
	read, manage := auth.ScopeAnalyticsRead, auth.ScopeAdsManage
	mux.HandleFunc("GET /v1/brands/{brand}/ads/findings", s.onBrand(read, s.listAdFindings))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/findings/refresh", s.onBrand(manage, s.refreshAdSuggestions))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/findings/{id}/approve", s.onBrand(manage, s.approveAdFinding))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/findings/{id}/dismiss", s.onBrand(manage, s.dismissAdFinding))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/findings/{id}/acknowledge", s.onBrand(manage, s.acknowledgeAdFinding))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/findings/{id}/revert", s.onBrand(manage, s.revertAdFinding))
}

// adFindingsOrder ranks the queue: open decisions by impact (negative is
// better, so the biggest saving first), then open alerts, then savings
// applied on their own in the last 7 days, then measuring and judged,
// then the rest (shadow, dismissed, expired, acknowledged).
const adFindingsOrder = `ORDER BY CASE
		WHEN x.status = 'open' AND x.autonomy = 'approve' THEN 0
		WHEN x.status = 'open' AND x.autonomy = 'alert' THEN 1
		WHEN x.applied_auto AND x.reverted_at IS NULL AND x.decided_at >= now() - interval '7 days' THEN 2
		WHEN x.status = 'measuring' OR x.status = 'testing' THEN 3
		WHEN x.status = 'judged' THEN 4
		WHEN x.status = 'open' THEN 2
		ELSE 5 END,
	CASE WHEN x.status = 'open' AND x.autonomy = 'approve' THEN x.impact_micros END ASC NULLS LAST, x.updated_at DESC, x.id`

func (s *Server) listAdFindings(w http.ResponseWriter, r *http.Request) {
	s.listAdQueue(w, r, adFindingsOrder)
}

// refreshAnswer is what a refresh says: whether it queued a run, and when
// the next may be when it didn't (one an hour).
type refreshAnswer struct {
	Queued bool      `json:"queued"`
	NextAt time.Time `json:"next_at"`
}

// loadAdFinding reads one of the findings the caller may decide, in one
// of the statuses, answering the error itself when it can't. On a brand
// route only the route's brand's are seen.
func (s *Server) loadAdFinding(w http.ResponseWriter, r *http.Request, a *access, statuses ...string) (adSuggestionOut, bool) {
	id := r.PathValue("id")
	if !isUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "finding not found")
		return adSuggestionOut{}, false
	}
	v, err := scanAdSuggestion(s.pool.QueryRow(r.Context(), `SELECT `+adSuggestionCols+` `+adSuggestionFrom+` WHERE x.workspace_id = $1 AND x.id = $2`, a.WorkspaceID, id))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.sees(v.BrandID)) {
		writeError(w, r, http.StatusNotFound, "not_found", "finding not found")
		return v, false
	}
	if err != nil {
		s.internal(w, r, err)
		return v, false
	}
	if !a.writes(v.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", "you can only decide findings for your own brands")
		return v, false
	}
	for _, st := range statuses {
		if v.Status == st {
			return v, true
		}
	}
	writeError(w, r, http.StatusConflict, "not_active", "this finding is "+v.Status+", not "+statuses[0])
	return v, false
}

func (s *Server) approveAdFinding(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Test bool `json:"test"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	v, ok := s.loadAdFinding(w, r, a, "open")
	if !ok {
		return
	}
	s.approveFinding(w, r, a, v, in.Test)
}

func (s *Server) dismissAdFinding(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if v, ok := s.loadAdFinding(w, r, a, "open"); ok {
		s.decideFinding(w, r, a, v, "dismissed", "ad_suggestion.dismissed", "")
	}
}

func (s *Server) acknowledgeAdFinding(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.loadAdFinding(w, r, a, "open")
	if !ok {
		return
	}
	if v.Autonomy != "alert" {
		writeError(w, r, http.StatusConflict, "not_active", "only an alert is acknowledged; approve or dismiss this one")
		return
	}
	s.decideFinding(w, r, a, v, "acknowledged", "ad_suggestion.acknowledged", "")
}

// decideFinding closes an open finding as status (dismissed,
// acknowledged) with its audit event.
func (s *Server) decideFinding(w http.ResponseWriter, r *http.Request, a *access, v adSuggestionOut, status, action, _ string) {
	var out adSuggestionOut
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), `UPDATE ad_suggestions SET status = $3, decided_by = $2, decided_at = now(), updated_at = now() WHERE id = $1 AND status = 'open'`, v.ID, actorRef(a), status)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errAdSuggestionTaken("this finding was decided a moment ago")
		}
		if err := recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, action, "ad_suggestion", v.ID,
			map[string]any{"suggestion_id": v.ID, "campaign_id": v.CampaignID, "connector": v.Connector, "kind": v.Kind, "brand_id": v.BrandID, "rule": v.Rule},
			map[string]any{"connector": v.Connector, "kind": v.Kind, "rule": v.Rule}); err != nil {
			return err
		}
		out, err = scanAdSuggestion(tx.QueryRow(r.Context(), `SELECT `+adSuggestionCols+` `+adSuggestionFrom+` WHERE x.id = $1`, v.ID))
		return err
	})
	var conflict errAdSuggestionTaken
	if errors.As(err, &conflict) {
		writeError(w, r, http.StatusConflict, "not_active", string(conflict))
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// savingAllowed says whether an agent may approve a finding without a
// person: only a saving on a brand's own campaign, and only where the
// extension's hook (Options.AdAutoApprove) says so, by the same rule as
// paidads.AutoApprove. Without a hook only a person approves.
func (s *Server) savingAllowed(ctx context.Context, ws string, v adSuggestionOut) bool {
	if v.BrandID == nil {
		return false
	}
	return paidads.Approver(s.adAutoApprove).Allows(ctx, ws, *v.BrandID, v.Kind)
}

// approveFinding makes a finding's change and starts measuring it: the
// campaign kinds as the campaign calls do (an ad_changes row for the ad
// sync's job), the levers this layer added through the findings job.
func (s *Server) approveFinding(w http.ResponseWriter, r *http.Request, a *access, v adSuggestionOut, test bool) {
	// A person approves; an agent only a saving the extension's hook allows.
	auto := a.ActorType != "human"
	if auto && (test || !s.savingAllowed(r.Context(), a.WorkspaceID, v)) {
		writeError(w, r, http.StatusForbidden, "forbidden", "only a person approves this finding, in Outflare under Paid ads; agents change campaigns with the campaign calls, which follow their grants")
		return
	}
	if v.Autonomy == "alert" {
		writeError(w, r, http.StatusConflict, "not_active", "an alert has nothing to apply; acknowledge it")
		return
	}
	c, ok := s.loadAdCampaign(w, r, nil, a, v.CampaignID)
	if !ok {
		return
	}
	if test {
		s.testAdSuggestion(w, r, a, v, c)
		return
	}
	if c.status == connectors.AdRemoved {
		writeError(w, r, http.StatusConflict, "not_active", "the campaign was removed on the platform")
		return
	}
	switch v.Kind {
	case adsuggest.Pause, adsuggest.Resume, adsuggest.KeywordAdd, adsuggest.NegativeAdd, adsuggest.KeywordSplit, adsuggest.BiddingTarget, adsuggest.BudgetUp, adsuggest.BudgetDown:
		s.approveCampaignChange(w, r, a, v, c, auto)
		return
	case adsuggest.Consolidate:
		writeError(w, r, http.StatusConflict, "not_active", "merging ad groups is done in the network's own tools; dismiss this finding once it is done, and the next run judges the campaign")
		return
	}
	// The levers this layer added: a target move, a keyword or ad paused,
	// an exclusion.
	var cur adsuggest.Current
	cur.Status, cur.DailyBudgetMicros = c.status, c.budget
	if err := s.pool.QueryRow(r.Context(), `SELECT bidding_strategy, platform_target_cpa_micros, platform_target_roas FROM ad_entities WHERE id = $1`, c.id).Scan(&cur.Bidding, &cur.TargetCPAMicros, &cur.TargetROAS); err != nil {
		s.internal(w, r, err)
		return
	}
	kind, before, after, err := adsuggest.ChangeFor(v.Kind, v.Proposed, cur)
	if err != nil {
		writeError(w, r, http.StatusConflict, "not_active", err.Error())
		return
	}
	if s.adChangeQueued(w, r, c.id, kind) || !s.findingLeverFor(w, r, a.WorkspaceID, c.integrationID, kind) {
		return
	}
	var out adSuggestionOut
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		changeID, err := adsuggest.QueueChange(r.Context(), tx, a.WorkspaceID, c.brand, c.id, kind, before, after, actorRef(a))
		if err != nil {
			return err
		}
		if err := recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, adsuggest.ActionFor(kind), "ad_campaign", c.id,
			map[string]any{"change_id": changeID, "campaign_id": c.id, "external_id": c.externalID, "connector": c.connector, "brand_id": c.brand, "before": before, "after": after, "suggestion_id": v.ID},
			map[string]any{"connector": c.connector, "kind": kind, "auto": auto}); err != nil {
			return err
		}
		tag, err := tx.Exec(r.Context(), `UPDATE ad_suggestions SET status = 'measuring', change_id = $2, decided_by = $3, decided_at = now(), updated_at = now() WHERE id = $1 AND status = 'open'`, v.ID, changeID, actorRef(a))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errAdSuggestionTaken("this finding was decided a moment ago")
		}
		if err := adsuggest.Started(r.Context(), tx, v.ID, v.Kind, 0, time.Now().UTC()); err != nil {
			return err
		}
		if err := recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "ad_suggestion.approved", "ad_suggestion", v.ID,
			map[string]any{"suggestion_id": v.ID, "campaign_id": v.CampaignID, "connector": v.Connector, "kind": v.Kind, "change_id": changeID, "brand_id": v.BrandID, "auto": auto, "rule": v.Rule},
			map[string]any{"connector": v.Connector, "kind": v.Kind, "auto": auto, "rule": v.Rule}); err != nil {
			return err
		}
		out, err = scanAdSuggestion(tx.QueryRow(r.Context(), `SELECT `+adSuggestionCols+` `+adSuggestionFrom+` WHERE x.id = $1`, v.ID))
		return err
	})
	var conflict errAdSuggestionTaken
	if errors.As(err, &conflict) {
		writeError(w, r, http.StatusConflict, "not_active", string(conflict))
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}

// findingLeverFor checks that the campaign's network can make a change of
// kind (the findings job's kinds), without calling it.
func (s *Server) findingLeverFor(w http.ResponseWriter, r *http.Request, ws, integrationID, kind string) bool {
	o, err := s.integrations.Open(r.Context(), s.pool, ws, integrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		writeError(w, r, http.StatusConflict, "not_active", "the campaign's ad platform is no longer connected")
		return false
	}
	if err != nil {
		s.connectorFailed(w, r, err)
		return false
	}
	var ok bool
	var what string
	switch kind {
	case adsuggest.ChangeNegativeRemove, adsuggest.ChangeKeywordPause, adsuggest.ChangeKeywordResume:
		_, ok = o.Connector.(connectors.AdKeywordEditor)
		what = "keywords"
	case adsuggest.ChangeAdPause, adsuggest.ChangeAdResume:
		_, ok = o.Connector.(connectors.AdStatusManager)
		what = "ads"
	case adsuggest.ChangeTarget:
		_, ok = o.Connector.(connectors.AdTargetManager)
		what = "targets"
	case adsuggest.ChangeExclusionAdd, adsuggest.ChangeExclusionRemove:
		_, ok = o.Connector.(connectors.AdExclusionManager)
		what = "exclusions"
	case adsuggest.ChangeBidding:
		_, ok = o.Connector.(connectors.AdTargetingManager)
		what = "bidding"
	case adsuggest.ChangeKeywordAdd, adsuggest.ChangeNegativeAdd:
		_, ok = o.Connector.(connectors.AdKeywordManager)
		what = "keywords"
	default:
		_, ok = o.Connector.(connectors.AdsManager)
		what = "campaigns"
	}
	if !ok {
		writeError(w, r, http.StatusUnprocessableEntity, "connector_invalid", o.Connector.Definition().Name+" "+what+" can't be changed here yet")
		return false
	}
	return true
}

// revertAdFinding makes the inverse of an applied finding's change and
// marks the finding reverted.
func (s *Server) revertAdFinding(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.loadAdFinding(w, r, a, "measuring", "judged")
	if !ok {
		return
	}
	switch {
	case v.RevertedAt != nil:
		writeError(w, r, http.StatusConflict, "not_active", "this finding was already reverted")
		return
	case v.ChangeID == nil:
		writeError(w, r, http.StatusConflict, "not_active", "this finding made no change to revert")
		return
	}
	var changeKind, changeStatus string
	var beforeJSON, afterJSON []byte
	if err := s.pool.QueryRow(r.Context(), `SELECT kind, status, before, after FROM ad_changes WHERE id = $1 AND workspace_id = $2`, *v.ChangeID, a.WorkspaceID).Scan(&changeKind, &changeStatus, &beforeJSON, &afterJSON); err != nil {
		s.internal(w, r, err)
		return
	}
	if changeStatus != "applied" {
		writeError(w, r, http.StatusConflict, "not_active", "the change is "+changeStatus+", not applied; there is nothing to revert yet")
		return
	}
	var before, after adsuggest.Change
	_ = json.Unmarshal(beforeJSON, &before)
	_ = json.Unmarshal(afterJSON, &after)
	kind, inverse, err := adsuggest.Inverse(changeKind, before, after)
	if errors.Is(err, adsuggest.ErrNotApplicable) {
		writeError(w, r, http.StatusConflict, "not_active", "a "+changeKind+" change can't be reverted here; undo it in the network's own tools")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusConflict, "not_active", err.Error())
		return
	}
	c, ok := s.loadAdCampaign(w, r, nil, a, v.CampaignID)
	if !ok {
		return
	}
	// A keyword added is paused by the id the sync gave it.
	if changeKind == adsuggest.ChangeKeywordAdd {
		err := s.pool.QueryRow(r.Context(), `SELECT k.external_id FROM ad_keywords k JOIN ad_entities g ON g.id = k.ad_group_id
			WHERE g.ad_account_id = $1 AND g.external_id = $2 AND lower(k.text) = lower($3) AND k.status <> 'removed' ORDER BY k.updated_at DESC LIMIT 1`, c.accountID, inverse.AdGroupID, inverse.Keyword).Scan(&inverse.KeywordID)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusConflict, "not_active", "the keyword hasn't synced from the network yet; try after the next sync")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
	}
	if kind == adsuggest.ChangePause || kind == adsuggest.ChangeResume {
		if c.status == inverse.Status {
			writeError(w, r, http.StatusConflict, "not_active", "the campaign is already "+inverse.Status)
			return
		}
	}
	if s.adChangeQueued(w, r, c.id, kind) || !s.findingLeverFor(w, r, a.WorkspaceID, c.integrationID, kind) {
		return
	}
	cur := adsuggest.Change{Status: c.status, DailyBudgetMicros: c.budget}
	if kind == adsuggest.ChangeTarget || kind == adsuggest.ChangeBidding || kind == adsuggest.ChangeExclusionRemove || kind == adsuggest.ChangeNegativeRemove {
		cur = after
	}
	var out adSuggestionOut
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		changeID, err := adsuggest.QueueChange(r.Context(), tx, a.WorkspaceID, c.brand, c.id, kind, cur, inverse, actorRef(a))
		if err != nil {
			return err
		}
		if err := recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, adsuggest.ActionFor(kind), "ad_campaign", c.id,
			map[string]any{"change_id": changeID, "campaign_id": c.id, "external_id": c.externalID, "connector": c.connector, "brand_id": c.brand, "before": cur, "after": inverse, "reverts": *v.ChangeID, "suggestion_id": v.ID},
			map[string]any{"connector": c.connector, "kind": kind, "revert": true}); err != nil {
			return err
		}
		tag, err := tx.Exec(r.Context(), `UPDATE ad_suggestions SET reverted_at = now(), revert_change_id = $2, updated_at = now() WHERE id = $1 AND reverted_at IS NULL`, v.ID, changeID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errAdSuggestionTaken("this finding was reverted a moment ago")
		}
		if err := recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "ad_suggestion.reverted", "ad_suggestion", v.ID,
			map[string]any{"suggestion_id": v.ID, "campaign_id": v.CampaignID, "connector": v.Connector, "kind": v.Kind, "change_id": *v.ChangeID, "revert_change_id": changeID, "brand_id": v.BrandID, "rule": v.Rule, "applied_auto": v.AppliedAuto},
			map[string]any{"connector": v.Connector, "kind": v.Kind, "rule": v.Rule, "applied_auto": v.AppliedAuto}); err != nil {
			return err
		}
		out, err = scanAdSuggestion(tx.QueryRow(r.Context(), `SELECT `+adSuggestionCols+` `+adSuggestionFrom+` WHERE x.id = $1`, v.ID))
		return err
	})
	var conflict errAdSuggestionTaken
	if errors.As(err, &conflict) {
		writeError(w, r, http.StatusConflict, "not_active", string(conflict))
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}

// adFindingsBlocker adds ads_decisions_waiting to the workspace status:
// findings that wait for a person on the brands the caller sees.
func (s *Server) adFindingsBlocker(ctx context.Context, ws string, brandArg *string, bf func(string) string, page func(string) string, add func(statusBlocker)) error {
	items, n, err := s.statusItems(ctx, `
		SELECT x.id::text, e.name, x.kind, count(*) OVER ()::int FROM ad_suggestions x JOIN ad_entities e ON e.id = x.ad_entity_id
		WHERE x.workspace_id = $1 AND x.status = 'open' AND x.autonomy = 'approve' AND NOT x.shadow AND `+bf("x.brand_id")+`
		ORDER BY x.impact_micros ASC NULLS LAST, x.created_at LIMIT 5`, ws, brandArg)
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	add(statusBlocker{Code: "ads_decisions_waiting", Severity: "info", Area: "ads", Title: countOf(n, "paid ads decision waits", "paid ads decisions wait") + " for a person",
		Detail: "Changes Outflare proposes that can spend more (a budget raise, a resume, a keyword, a looser target) or restructure a campaign wait for a person's yes. list_ad_findings with status open and autonomy approve lists them with the numbers behind each; a person approves or dismisses them under Paid ads.",
		Count:  n, Items: items, Fix: statusFix{Tool: "list_ad_findings", Page: page("ads"), Who: "person"}})
	return nil
}
