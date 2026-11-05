package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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
	mux.HandleFunc("GET /v1/workspaces/{ws}/ads/suggestions", s.inBrand(auth.ScopeAnalyticsRead, s.listAdSuggestions))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ads/suggestions/refresh", s.inBrand(manage, s.refreshAdSuggestions))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ads/suggestions/{id}/approve", s.inBrand(manage, s.approveAdSuggestion))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ads/suggestions/{id}/dismiss", s.inBrand(manage, s.dismissAdSuggestion))
	mux.HandleFunc("GET /v1/workspaces/{ws}/ads/recommendations", s.inBrand(auth.ScopeAnalyticsRead, s.listAdRecommendations))
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
}

const adSuggestionCols = `x.id, e.id, e.name, a.id, a.name, a.connector, a.currency, x.brand_id, x.kind, x.status, x.reason, x.evidence, x.proposed, x.change_id, x.test_id,
	x.decided_by, x.decided_at, to_char(x.measure_from, 'YYYY-MM-DD'), to_char(x.measure_until, 'YYYY-MM-DD'), x.window_note, x.verdict, x.verdict_note, x.result,
	x.judged_at, x.created_at, x.updated_at`

const adSuggestionFrom = `FROM ad_suggestions x JOIN ad_entities e ON e.id = x.ad_entity_id JOIN ad_accounts a ON a.id = e.ad_account_id`

func scanAdSuggestion(row pgx.Row) (adSuggestionOut, error) {
	var v adSuggestionOut
	var ev, pr, res []byte
	err := row.Scan(&v.ID, &v.CampaignID, &v.CampaignName, &v.AccountID, &v.AccountName, &v.Connector, &v.Currency, &v.BrandID, &v.Kind, &v.Status,
		&v.Reason, &ev, &pr, &v.ChangeID, &v.TestID, &v.DecidedBy, &v.DecidedAt, &v.MeasureFrom, &v.MeasureUntil, &v.WindowNote, &v.Verdict, &v.VerdictNote, &res,
		&v.JudgedAt, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return v, err
	}
	v.Evidence, v.Proposed, v.Result = map[string]any{}, map[string]any{}, map[string]any{}
	_ = json.Unmarshal(ev, &v.Evidence)
	_ = json.Unmarshal(pr, &v.Proposed)
	_ = json.Unmarshal(res, &v.Result)
	return v, nil
}

var adSuggestionStatuses = map[string]bool{"open": true, "dismissed": true, "expired": true, "measuring": true, "testing": true, "judged": true}

func (s *Server) listAdSuggestions(w http.ResponseWriter, r *http.Request) {
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
			writeError(w, r, http.StatusBadRequest, "invalid_request", "status is open, dismissed, expired, measuring, testing or judged")
			return
		}
		args = append(args, v)
		where += ` AND x.status = $` + strconv.Itoa(len(args))
	}
	where += brand.sql(`x.brand_id`, &args)
	limit, ok := pageLimit(w, r, 100, 500)
	if !ok {
		return
	}
	args = append(args, limit)
	rows, err := s.pool.Query(r.Context(), `SELECT `+adSuggestionCols+` `+adSuggestionFrom+` WHERE `+where+`
		ORDER BY CASE x.status WHEN 'open' THEN 0 WHEN 'measuring' THEN 1 WHEN 'testing' THEN 1 WHEN 'judged' THEN 2 ELSE 3 END, x.updated_at DESC, x.id
		LIMIT $`+strconv.Itoa(len(args)), args...)
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
	if err := adsuggest.Enqueue(r.Context(), s.pool, a.WorkspaceID, time.Now()); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

// loadAdSuggestion reads one open suggestion the caller may decide,
// answering the error itself when it can't.
func (s *Server) loadAdSuggestion(w http.ResponseWriter, r *http.Request, a *access) (adSuggestionOut, bool) {
	id := r.PathValue("id")
	if !isUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "suggestion not found")
		return adSuggestionOut{}, false
	}
	v, err := scanAdSuggestion(s.pool.QueryRow(r.Context(), `SELECT `+adSuggestionCols+` `+adSuggestionFrom+` WHERE x.workspace_id = $1 AND x.id = $2`, a.WorkspaceID, id))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.sees(v.BrandID)) {
		writeError(w, r, http.StatusNotFound, "not_found", "suggestion not found")
		return v, false
	}
	if err != nil {
		s.internal(w, r, err)
		return v, false
	}
	if !a.writes(v.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", "you can only decide suggestions for your own brands")
		return v, false
	}
	if v.Status != "open" {
		writeError(w, r, http.StatusConflict, "not_active", "this suggestion is "+v.Status+", not open")
		return v, false
	}
	return v, true
}

func (s *Server) approveAdSuggestion(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	// An optional body: {"test": true} tests a budget suggestion inside the
	// network (ad_tests.go) instead of making it.
	var in struct {
		Test bool `json:"test"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	v, ok := s.loadAdSuggestion(w, r, a)
	if !ok {
		return
	}
	// A person approves; an agent only a suggestion that saves money on a
	// brand's own campaign, where an extension lets that brand's savings
	// apply on their own (Options.AdAutoApprove).
	auto := a.ActorType != "human"
	if auto && (in.Test || !s.adAutoApproves(r.Context(), a.WorkspaceID, v)) {
		writeError(w, r, http.StatusForbidden, "forbidden", "only a person approves this suggestion, in Outflare under Paid ads, Suggestions; agents change campaigns with the campaign calls, which follow their grants")
		return
	}
	c, ok := s.loadAdCampaign(w, r, nil, a, v.CampaignID)
	if !ok {
		return
	}
	if in.Test {
		s.testAdSuggestion(w, r, a, v, c)
		return
	}
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
	if c.status == connectors.AdRemoved {
		writeError(w, r, http.StatusConflict, "not_active", "the campaign was removed on the platform")
		return
	}
	if s.adChangeQueued(w, r, c.id, kind) || !s.adsManagerFor(w, r, a.WorkspaceID, c.integrationID) {
		return
	}
	// The rate the window is planned on: the days the suggestion read.
	var rate float64
	if conv, ok := v.Evidence["conversions"].(float64); ok {
		if days, ok := v.Evidence["days"].(float64); ok && days > 0 {
			rate = conv / days
		}
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
		if err := adsuggest.Started(r.Context(), tx, v.ID, v.Kind, rate, time.Now().UTC()); err != nil {
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

// adAutoApproves says whether an agent may approve a suggestion without a
// person: only a kind that saves money, on a brand's campaign (never one
// every brand shares), when the extension's hook allows it for the brand.
func (s *Server) adAutoApproves(ctx context.Context, ws string, v adSuggestionOut) bool {
	return s.adAutoApprove != nil && adsuggest.Saves(v.Kind) && v.BrandID != nil && *v.BrandID != "" && s.adAutoApprove(ctx, ws, *v.BrandID, v.Kind)
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

func (s *Server) dismissAdSuggestion(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.loadAdSuggestion(w, r, a)
	if !ok {
		return
	}
	var out adSuggestionOut
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var actor *string
		if a.ActorID != "" {
			actor = &a.ActorID
		}
		tag, err := tx.Exec(r.Context(), `UPDATE ad_suggestions SET status = 'dismissed', decided_by = $2, decided_at = now(), updated_at = now()
			WHERE id = $1 AND status = 'open'`, v.ID, actor)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errAdSuggestionTaken("this suggestion was decided a moment ago")
		}
		if err := recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "ad_suggestion.dismissed", "ad_suggestion", v.ID,
			map[string]any{"suggestion_id": v.ID, "campaign_id": v.CampaignID, "connector": v.Connector, "kind": v.Kind, "brand_id": v.BrandID},
			map[string]any{"connector": v.Connector, "kind": v.Kind}); err != nil {
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
