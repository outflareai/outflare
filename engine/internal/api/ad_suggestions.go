package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adsuggest"
	"github.com/outflareai/outflare/engine/internal/adsync"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Paid ads suggestions (step 2 of the paid ads plan): the suggestions job
// (adsuggest) reads each campaign after a sync and suggests one change at
// a time. A person approves one, which makes the change exactly as the
// campaign calls do (an ad_changes row, its audit event and the job that
// calls the platform) and starts its measuring window, or dismisses it.
// Agents change campaigns through the campaign calls, which follow their
// grants (actions.go, adsHold); approving here is a person's.

func (s *Server) routesAdSuggestions(mux *http.ServeMux) {
	manage := auth.ScopeAdsManage
	mux.HandleFunc("GET /v1/brands/{brand}/ads/suggestions", s.onBrand(auth.ScopeAnalyticsRead, s.listAdSuggestions))
	mux.HandleFunc("GET /v1/workspaces/{ws}/ads/suggestions", s.inBrand(auth.ScopeAnalyticsRead, s.listAdSuggestions))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/suggestions/refresh", s.onBrand(manage, s.refreshAdSuggestions))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ads/suggestions/refresh", s.inBrand(manage, s.refreshAdSuggestions))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/suggestions/{id}/approve", s.onBrand(manage, s.approveAdFinding))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ads/suggestions/{id}/approve", s.inBrand(manage, s.approveAdFinding))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/suggestions/{id}/dismiss", s.onBrand(manage, s.dismissAdFinding))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ads/suggestions/{id}/dismiss", s.inBrand(manage, s.dismissAdFinding))
	mux.HandleFunc("GET /v1/brands/{brand}/ads/recommendations", s.onBrand(auth.ScopeAnalyticsRead, s.listAdRecommendations))
	mux.HandleFunc("GET /v1/workspaces/{ws}/ads/recommendations", s.inBrand(auth.ScopeAnalyticsRead, s.listAdRecommendations))
	// Alerts (adsync/alerts.go) are findings with autonomy alert; a person
	// acknowledges one instead of approving or dismissing it. The same
	// rows are the findings queue (ad_findings.go), served by the same
	// handlers.
	mux.HandleFunc("POST /v1/brands/{brand}/ads/suggestions/{id}/acknowledge", s.onBrand(manage, s.acknowledgeAdFinding))
}

type adSuggestionOut struct {
	ID           string         `json:"id"`
	CampaignID   string         `json:"campaign_id"`
	CampaignName string         `json:"campaign_name"`
	AccountID    string         `json:"account_id"`
	AccountName  string         `json:"account_name"`
	Connector    string         `json:"connector"`
	Currency     string         `json:"currency"`
	BrandID      *string        `json:"brand_id"`
	Kind         string         `json:"kind"`
	Status       string         `json:"status"`
	Reason       string         `json:"reason"`
	Evidence     map[string]any `json:"evidence"`
	Proposed     map[string]any `json:"proposed"`
	ChangeID     *string        `json:"change_id"`
	TestID       *string        `json:"test_id"`
	DecidedBy    *string        `json:"decided_by"`
	DecidedAt    *time.Time     `json:"decided_at"`
	MeasureFrom  *string        `json:"measure_from"`
	MeasureUntil *string        `json:"measure_until"`
	WindowNote   string         `json:"window_note"`
	Verdict      string         `json:"verdict"`
	VerdictNote  string         `json:"verdict_note"`
	Result       map[string]any `json:"result"`
	JudgedAt     *time.Time     `json:"judged_at"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	// The findings queue (plans/paid-ads/plan.md): the autonomy class
	// (auto, approve, alert), the unit the evidence is in, the expected
	// change in cost per new paying customer, whether the rule only
	// recorded it (shadow) or applied it on its own, when a person
	// reverted it and with which change, and the rule that made it.
	Autonomy       string     `json:"autonomy"`
	Unit           string     `json:"unit"`
	ImpactMicros   *int64     `json:"impact_micros"`
	Shadow         bool       `json:"shadow"`
	AppliedAuto    bool       `json:"applied_auto"`
	RevertedAt     *time.Time `json:"reverted_at"`
	RevertChangeID *string    `json:"revert_change_id"`
	Rule           string     `json:"rule"`
	RuleVersion    int        `json:"rule_version"`
}

const adSuggestionCols = `x.id, e.id, e.name, a.id, a.name, a.connector, a.currency, x.brand_id, x.kind, x.status, x.reason, x.evidence, x.proposed, x.change_id, x.test_id,
	x.decided_by, x.decided_at, to_char(x.measure_from, 'YYYY-MM-DD'), to_char(x.measure_until, 'YYYY-MM-DD'), x.window_note, x.verdict, x.verdict_note, x.result,
	x.judged_at, x.created_at, x.updated_at,
	x.autonomy, x.unit, x.impact_micros, x.shadow, x.applied_auto, x.reverted_at, x.revert_change_id, x.rule, x.rule_version`

const adSuggestionFrom = `FROM ad_suggestions x JOIN ad_entities e ON e.id = x.ad_entity_id JOIN ad_accounts a ON a.id = e.ad_account_id`

func scanAdSuggestion(row pgx.Row) (adSuggestionOut, error) {
	var v adSuggestionOut
	var ev, pr, res []byte
	err := row.Scan(&v.ID, &v.CampaignID, &v.CampaignName, &v.AccountID, &v.AccountName, &v.Connector, &v.Currency, &v.BrandID, &v.Kind, &v.Status,
		&v.Reason, &ev, &pr, &v.ChangeID, &v.TestID, &v.DecidedBy, &v.DecidedAt, &v.MeasureFrom, &v.MeasureUntil, &v.WindowNote, &v.Verdict, &v.VerdictNote, &res,
		&v.JudgedAt, &v.CreatedAt, &v.UpdatedAt,
		&v.Autonomy, &v.Unit, &v.ImpactMicros, &v.Shadow, &v.AppliedAuto, &v.RevertedAt, &v.RevertChangeID, &v.Rule, &v.RuleVersion)
	if err != nil {
		return v, err
	}
	v.Evidence, v.Proposed, v.Result = map[string]any{}, map[string]any{}, map[string]any{}
	_ = json.Unmarshal(ev, &v.Evidence)
	_ = json.Unmarshal(pr, &v.Proposed)
	_ = json.Unmarshal(res, &v.Result)
	return v, nil
}

var adSuggestionStatuses = map[string]bool{"open": true, "dismissed": true, "expired": true, "measuring": true, "testing": true, "judged": true, "acknowledged": true, "shadow": true}

var adSuggestionAutonomies = map[string]bool{"auto": true, "approve": true, "alert": true}

// adSuggestionKind matches a kind filter: a lever (budget_up) or an alert
// (alert.spend_anomaly), lowercase.
var adSuggestionKind = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)?$`)

// adSuggestionsOrder is the suggestions list's order: open first, then
// measuring and testing, judged, and the rest, newest first.
const adSuggestionsOrder = `ORDER BY CASE x.status WHEN 'open' THEN 0 WHEN 'measuring' THEN 1 WHEN 'testing' THEN 1 WHEN 'judged' THEN 2 ELSE 3 END, x.updated_at DESC, x.id`

func (s *Server) listAdSuggestions(w http.ResponseWriter, r *http.Request) {
	s.listAdQueue(w, r, adSuggestionsOrder)
}

// listAdQueue lists the findings queue (ad_suggestions) in order: the
// suggestions routes and the findings routes (ad_findings.go) differ only
// in it.
func (s *Server) listAdQueue(w http.ResponseWriter, r *http.Request, order string) {
	a := accessFrom(r.Context())
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	args := []any{a.WorkspaceID}
	where := `x.workspace_id = $1`
	if v := q.Get("connector"); v != "" {
		args = append(args, v)
		where += ` AND a.connector = $` + strconv.Itoa(len(args))
	}
	for _, f := range []struct{ param, col, what string }{{"account_id", "a.id", "an ad account's id"}, {"campaign_id", "e.id", "a campaign's id"}} {
		if v := q.Get(f.param); v != "" {
			if !isUUID(v) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", f.param+" is "+f.what)
				return
			}
			args = append(args, v)
			where += ` AND ` + f.col + ` = $` + strconv.Itoa(len(args))
		}
	}
	if v := q.Get("status"); v != "" {
		if !adSuggestionStatuses[v] {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "status is open, dismissed, expired, measuring, testing, judged, acknowledged or shadow")
			return
		}
		args = append(args, v)
		where += ` AND x.status = $` + strconv.Itoa(len(args))
	}
	if v := q.Get("autonomy"); v != "" {
		if !adSuggestionAutonomies[v] {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "autonomy is auto, approve or alert")
			return
		}
		args = append(args, v)
		where += ` AND x.autonomy = $` + strconv.Itoa(len(args))
	}
	if v := q.Get("kind"); v != "" {
		if !adSuggestionKind.MatchString(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is a finding kind, such as budget_down or alert.target_stale")
			return
		}
		args = append(args, v)
		where += ` AND x.kind = $` + strconv.Itoa(len(args))
	}
	where += brand.sql(`x.brand_id`, &args)
	limit, ok := pageLimit(w, r, 100, 500)
	if !ok {
		return
	}
	args = append(args, limit)
	rows, err := s.pool.Query(r.Context(), `SELECT `+adSuggestionCols+` `+adSuggestionFrom+` WHERE `+where+` `+order+` LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (adSuggestionOut, error) { return scanAdSuggestion(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out == nil {
		out = []adSuggestionOut{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (s *Server) refreshAdSuggestions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	queued, next, err := adsuggest.EnqueueOnce(r.Context(), s.pool, a.WorkspaceID, time.Now())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, refreshAnswer{Queued: queued, NextAt: next})
}

// approveCampaignChange makes a finding's change of the kinds the
// campaign calls know (pause, resume, budget, keywords, bidding) exactly
// as those calls do, and starts measuring it.
func (s *Server) approveCampaignChange(w http.ResponseWriter, r *http.Request, a *access, v adSuggestionOut, c adCampaignRow, auto bool) {
	var kind string
	var after adsync.ChangeState
	switch v.Kind {
	case adsuggest.Pause, adsuggest.Resume:
		kind, after.Status = adsync.ChangePause, connectors.AdPaused
		if v.Kind == adsuggest.Resume {
			kind, after.Status = adsync.ChangeResume, connectors.AdActive
		}
		if c.status == after.Status {
			writeError(w, r, http.StatusConflict, "not_active", "the campaign is already "+after.Status)
			return
		}
	case adsuggest.KeywordAdd, adsuggest.NegativeAdd, adsuggest.KeywordSplit:
		kind = v.Kind
		str := func(k string) string { x, _ := v.Proposed[k].(string); return x }
		after = adsync.ChangeState{Keyword: str("keyword"), MatchType: str("match_type"), AdGroupID: str("ad_group_id"), NewAdGroupName: str("new_ad_group_name")}
		if after.Keyword == "" || (kind != adsync.ChangeNegativeAdd && after.AdGroupID == "") {
			writeError(w, r, http.StatusConflict, "not_active", "the suggestion names no keyword or ad group")
			return
		}
		if !s.keywordManagerFor(w, r, a.WorkspaceID, c.integrationID) {
			return
		}
	case adsuggest.BiddingTarget:
		kind = adsync.ChangeBidding
		t, ok := v.Proposed["target_cpa_micros"].(float64)
		if !ok || t < 10_000 || int64(t) > adBudgetMax {
			writeError(w, r, http.StatusConflict, "not_active", "the suggestion has no target to bid to")
			return
		}
		target := int64(t)
		after.Bidding = &connectors.AdBidding{Strategy: connectors.BidMaxConversions, TargetCPAMicros: &target}
		if _, _, ok := s.adTargeterFor(w, r, a.WorkspaceID, c.integrationID); !ok {
			return
		}
	default:
		kind = adsync.ChangeBudget
		b, ok := v.Proposed["daily_budget_micros"].(float64)
		if !ok || b < 10_000 || int64(b) > adBudgetMax {
			writeError(w, r, http.StatusConflict, "not_active", "the suggestion has no daily budget to set")
			return
		}
		nb := int64(b)
		after.DailyBudgetMicros = &nb
	}
	if s.adChangeQueued(w, r, c.id, kind) || !s.adsManagerFor(w, r, a.WorkspaceID, c.integrationID) {
		return
	}
	var out adSuggestionOut
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		ch, err := s.changeAdCampaign(r.Context(), tx, a, c, kind, after, nil)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(r.Context(), `UPDATE ad_suggestions SET status = 'measuring', change_id = $2, decided_by = $3, decided_at = now(), updated_at = now()
			WHERE id = $1 AND status = 'open'`, v.ID, ch.ID, a.ActorID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errAdSuggestionTaken("this suggestion was decided a moment ago")
		}
		if err := adsuggest.Started(r.Context(), tx, v.ID, v.Kind, 0, time.Now().UTC()); err != nil {
			return err
		}
		if err := recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "ad_suggestion.approved", "ad_suggestion", v.ID,
			map[string]any{"suggestion_id": v.ID, "campaign_id": v.CampaignID, "connector": v.Connector, "kind": v.Kind, "change_id": ch.ID, "brand_id": v.BrandID, "auto": auto},
			map[string]any{"connector": v.Connector, "kind": v.Kind, "auto": auto}); err != nil {
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

// SetAdAutoApprove replaces the hook that lets agents approve saving
// suggestions (Options.AdAutoApprove), for tests.
func (s *Server) SetAdAutoApprove(fn func(ctx context.Context, workspaceID, brandID, kind string) bool) {
	s.adAutoApprove = fn
}

// testAdSuggestion starts a test of a budget suggestion: the trial gets
// the suggested budget, and the suggestion is judged by the test.
func (s *Server) testAdSuggestion(w http.ResponseWriter, r *http.Request, a *access, v adSuggestionOut, c adCampaignRow) {
	var p adTestPlan
	switch v.Kind {
	case adsuggest.BudgetUp, adsuggest.BudgetDown:
		b, ok := v.Proposed["daily_budget_micros"].(float64)
		if !ok {
			writeError(w, r, http.StatusConflict, "not_active", "the suggestion has no daily budget to set")
			return
		}
		nb := int64(b)
		p = adTestPlan{kind: connectors.AdTestBudget, trial: adsync.TestSettings{DailyBudgetMicros: &nb}, suggestion: &v.ID}
	case adsuggest.BiddingTarget:
		t, ok := v.Proposed["target_cpa_micros"].(float64)
		if !ok {
			writeError(w, r, http.StatusConflict, "not_active", "the suggestion has no target to bid to")
			return
		}
		nt := int64(t)
		p = adTestPlan{kind: connectors.AdTestBidding, trial: adsync.TestSettings{TargetCPAMicros: &nt}, suggestion: &v.ID}
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "only a budget or bidding suggestion can be tested; approve this one without test")
		return
	}
	if !s.checkAdTest(w, r, a, c, &p) {
		return
	}
	var out adSuggestionOut
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		id, err := s.startAdTest(r.Context(), tx, a, c, p)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(r.Context(), `UPDATE ad_suggestions SET status = 'testing', test_id = $2, decided_by = $3, decided_at = now(), updated_at = now()
			WHERE id = $1 AND status = 'open'`, v.ID, id, a.ActorID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errAdSuggestionTaken("this suggestion was decided a moment ago")
		}
		if err := recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "ad_suggestion.approved", "ad_suggestion", v.ID,
			map[string]any{"suggestion_id": v.ID, "campaign_id": v.CampaignID, "connector": v.Connector, "kind": v.Kind, "test_id": id, "brand_id": v.BrandID},
			map[string]any{"connector": v.Connector, "kind": v.Kind, "test": true}); err != nil {
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

// errAdSuggestionTaken ends a transaction that found its row already changed.
type errAdSuggestionTaken string

func (e errAdSuggestionTaken) Error() string { return string(e) }

// keywordManagerFor checks that the campaign's platform can change
// keywords, without calling it.
func (s *Server) keywordManagerFor(w http.ResponseWriter, r *http.Request, ws, integrationID string) bool {
	o, err := s.integrations.Open(r.Context(), s.pool, ws, integrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		writeError(w, r, http.StatusConflict, "not_active", "the campaign's ad platform is no longer connected")
		return false
	}
	if err != nil {
		s.connectorFailed(w, r, err)
		return false
	}
	if _, ok := o.Connector.(connectors.AdKeywordManager); !ok {
		writeError(w, r, http.StatusUnprocessableEntity, "connector_invalid", o.Connector.Definition().Name+" keywords can't be changed here yet")
		return false
	}
	return true
}

// listAdRecommendations asks the platform live for its own
// recommendations for one account (Google), shown beside Outflare's.
func (s *Server) listAdRecommendations(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	o, acct, ok := s.adAccountOpen(w, r, a.WorkspaceID, r.URL.Query().Get("account_id"))
	if !ok {
		return
	}
	k, ok := o.Connector.(connectors.AdKeywordManager)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"data": []any{}, "supported": false})
		return
	}
	list, err := k.Recommendations(r.Context(), o.Client, o.Creds, acct)
	if err != nil {
		s.connectorFailed(w, r, err)
		return
	}
	if list == nil {
		list = []connectors.AdRecommendation{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "supported": true})
}
