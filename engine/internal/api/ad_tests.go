package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adsuggest"
	"github.com/outflareai/outflare/engine/internal/adsync"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Tests inside the ad networks (step 4 of the paid ads plan): a person
// starts a test on one campaign (or tests a suggestion instead of making
// it), the network splits the campaign's traffic or audience between the
// campaign and a trial, Outflare reads both arms after each sync and gives
// a verdict, and the person keeps the trial's change or rolls it back. The
// network calls run in the adsync job (JobTest); every decision here is a
// person's, like approving a suggestion.

func (s *Server) routesAdTests(mux *http.ServeMux) {
	manage := auth.ScopeAdsManage
	mux.HandleFunc("GET /v1/brands/{brand}/ads/tests", s.onBrand(auth.ScopeAnalyticsRead, s.listAdTests))
	mux.HandleFunc("GET /v1/workspaces/{ws}/ads/tests", s.inBrand(auth.ScopeAnalyticsRead, s.listAdTests))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/tests", s.onBrand(manage, s.createAdTest))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ads/tests", s.inBrand(manage, s.createAdTest))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/tests/{id}/keep", s.onBrand(manage, s.endAdTest("keep")))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ads/tests/{id}/keep", s.inBrand(manage, s.endAdTest("keep")))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/tests/{id}/roll-back", s.onBrand(manage, s.endAdTest("roll_back")))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ads/tests/{id}/roll-back", s.inBrand(manage, s.endAdTest("roll_back")))
}

type adTestOut struct {
	ID              string              `json:"id"`
	CampaignID      string              `json:"campaign_id"`
	CampaignName    string              `json:"campaign_name"`
	AccountID       string              `json:"account_id"`
	AccountName     string              `json:"account_name"`
	Connector       string              `json:"connector"`
	Currency        string              `json:"currency"`
	BrandID         *string             `json:"brand_id"`
	SuggestionID    *string             `json:"suggestion_id"`
	Name            string              `json:"name"`
	Kind            string              `json:"kind"`
	Status          string              `json:"status"`
	SplitPercent    int                 `json:"split_percent"`
	Control         adsync.TestSettings `json:"control"`
	Trial           adsync.TestSettings `json:"trial"`
	PlatformTestID  *string             `json:"platform_test_id"`
	TrialCampaignID *string             `json:"trial_campaign_id"`
	StartsOn        string              `json:"starts_on"`
	EndsOn          string              `json:"ends_on"`
	MeasureFrom     string              `json:"measure_from"`
	WindowNote      string              `json:"window_note"`
	Result          map[string]any      `json:"result"`
	Verdict         string              `json:"verdict"`
	VerdictNote     string              `json:"verdict_note"`
	Decision        string              `json:"decision"`
	Error           *string             `json:"error"`
	CreatedBy       *string             `json:"created_by"`
	DecidedBy       *string             `json:"decided_by"`
	DecidedAt       *time.Time          `json:"decided_at"`
	CheckedAt       *time.Time          `json:"checked_at"`
	JudgedAt        *time.Time          `json:"judged_at"`
	EndedAt         *time.Time          `json:"ended_at"`
	CreatedAt       time.Time           `json:"created_at"`
	UpdatedAt       time.Time           `json:"updated_at"`
}

const adTestCols = `x.id, e.id, e.name, a.id, a.name, a.connector, a.currency, x.brand_id, x.suggestion_id, x.name, x.kind, x.status, x.split_percent,
	x.control, x.trial, x.platform_test_id, x.trial_campaign_id, to_char(x.starts_on, 'YYYY-MM-DD'), to_char(x.ends_on, 'YYYY-MM-DD'),
	to_char(x.measure_from, 'YYYY-MM-DD'), x.window_note, x.result, x.verdict, x.verdict_note, x.decision, x.error, x.created_by, x.decided_by,
	x.decided_at, x.checked_at, x.judged_at, x.ended_at, x.created_at, x.updated_at`

const adTestFrom = `FROM ad_tests x JOIN ad_entities e ON e.id = x.ad_entity_id JOIN ad_accounts a ON a.id = e.ad_account_id`

func scanAdTest(row pgx.Row) (adTestOut, error) {
	var v adTestOut
	var control, trial, res []byte
	err := row.Scan(&v.ID, &v.CampaignID, &v.CampaignName, &v.AccountID, &v.AccountName, &v.Connector, &v.Currency, &v.BrandID, &v.SuggestionID,
		&v.Name, &v.Kind, &v.Status, &v.SplitPercent, &control, &trial, &v.PlatformTestID, &v.TrialCampaignID, &v.StartsOn, &v.EndsOn,
		&v.MeasureFrom, &v.WindowNote, &res, &v.Verdict, &v.VerdictNote, &v.Decision, &v.Error, &v.CreatedBy, &v.DecidedBy,
		&v.DecidedAt, &v.CheckedAt, &v.JudgedAt, &v.EndedAt, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return v, err
	}
	v.Result = map[string]any{}
	_ = json.Unmarshal(control, &v.Control)
	_ = json.Unmarshal(trial, &v.Trial)
	_ = json.Unmarshal(res, &v.Result)
	return v, nil
}

var adTestStatuses = []string{"starting", "running", "judged", "ending", "kept", "rolled_back", "failed"}

func (s *Server) listAdTests(w http.ResponseWriter, r *http.Request) {
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
		if !slices.Contains(adTestStatuses, v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "status is starting, running, judged, ending, kept, rolled_back or failed")
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
	rows, err := s.pool.Query(r.Context(), `SELECT `+adTestCols+` `+adTestFrom+` WHERE `+where+`
		ORDER BY CASE x.status WHEN 'judged' THEN 0 WHEN 'running' THEN 1 WHEN 'starting' THEN 1 WHEN 'ending' THEN 1 ELSE 2 END, x.created_at DESC, x.id
		LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (adTestOut, error) { return scanAdTest(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out == nil {
		out = []adTestOut{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// adTesterFor checks that the campaign's network runs tests of kind,
// without calling it.
func (s *Server) adTesterFor(w http.ResponseWriter, r *http.Request, ws, integrationID, kind string) bool {
	o, err := s.integrations.Open(r.Context(), s.pool, ws, integrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		writeError(w, r, http.StatusConflict, "not_active", "the campaign's ad platform is no longer connected")
		return false
	}
	if err != nil {
		s.connectorFailed(w, r, err)
		return false
	}
	t, ok := o.Connector.(connectors.AdTester)
	if !ok {
		writeError(w, r, http.StatusUnprocessableEntity, "connector_invalid", o.Connector.Definition().Name+" tests can't be run from Outflare yet; make the change and Outflare measures it before and after")
		return false
	}
	if !slices.Contains(t.TestKinds(), kind) {
		words := map[string]string{connectors.AdTestBudget: "a daily budget", connectors.AdTestBidding: "a target cost per conversion"}
		writeError(w, r, http.StatusUnprocessableEntity, "connector_invalid", o.Connector.Definition().Name+" tests here can't try "+words[kind])
		return false
	}
	return true
}

// adTestPlan is a test about to be started.
type adTestPlan struct {
	kind       string
	trial      adsync.TestSettings
	split      int
	name       string
	suggestion *string
}

// checkAdTest validates a test for campaign c, answering the error itself
// when it can't run.
func (s *Server) checkAdTest(w http.ResponseWriter, r *http.Request, a *access, c adCampaignRow, p *adTestPlan) bool {
	if c.status != connectors.AdActive {
		writeError(w, r, http.StatusConflict, "not_active", "a test needs the campaign running; it is "+c.status)
		return false
	}
	switch p.kind {
	case connectors.AdTestBudget:
		b := p.trial.DailyBudgetMicros
		if b == nil || *b < 10_000 || *b > adBudgetMax {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "a budget test needs daily_budget_micros: the trial's daily budget, at least 10000 (a cent)")
			return false
		}
		if c.budget != nil && *c.budget == *b {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "the trial's daily budget is the campaign's already")
			return false
		}
		p.trial.TargetCPAMicros = nil
	case connectors.AdTestBidding:
		t := p.trial.TargetCPAMicros
		if t == nil || *t < 10_000 || *t > adBudgetMax {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "a bidding test needs target_cpa_micros: the cost per conversion the trial bids to, at least 10000 (a cent)")
			return false
		}
		p.trial.DailyBudgetMicros = nil
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is budget or bidding")
		return false
	}
	if p.split == 0 {
		p.split = 50
	}
	if p.split < 10 || p.split > 90 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "split_percent, the trial's share, is between 10 and 90")
		return false
	}
	var live bool
	if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM ad_tests WHERE ad_entity_id = $1 AND status IN ('starting', 'running', 'judged', 'ending'))`, c.id).Scan(&live); err != nil {
		s.internal(w, r, err)
		return false
	}
	if live {
		writeError(w, r, http.StatusConflict, "not_active", "the campaign already has a test; keep it or roll it back first")
		return false
	}
	if p.name == "" {
		p.name = map[string]string{connectors.AdTestBudget: "Budget test: ", connectors.AdTestBidding: "Bid target test: "}[p.kind] + c.name
	}
	if len(p.name) > 200 {
		p.name = p.name[:200]
	}
	return s.adTesterFor(w, r, a.WorkspaceID, c.integrationID, p.kind)
}

// startAdTest records a test in tx, with its audit event, and queues the
// job that starts it on the network.
func (s *Server) startAdTest(ctx context.Context, tx pgx.Tx, a *access, c adCampaignRow, p adTestPlan) (string, error) {
	// Plan the window on the campaign's last four weeks.
	var conv float64
	var target *int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT sum(conversions) FROM ad_metrics_daily WHERE ad_entity_id = $1 AND day > current_date - 29), 0)::float8,
			(SELECT platform_target_cpa_micros FROM ad_entities WHERE id = $1)`, c.id).Scan(&conv, &target); err != nil {
		return "", err
	}
	settle, days, note := adsuggest.SplitWindow(p.kind, conv/28, p.split)
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	end := start.AddDate(0, 0, days-1)
	from := start.AddDate(0, 0, settle)
	control, _ := json.Marshal(adsync.TestSettings{DailyBudgetMicros: c.budget, TargetCPAMicros: target})
	trial, _ := json.Marshal(p.trial)
	var actor *string
	if a.ActorID != "" {
		actor = &a.ActorID
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO ad_tests (workspace_id, brand_id, ad_entity_id, suggestion_id, name, kind, split_percent, control, trial,
			starts_on, ends_on, measure_from, window_note, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) RETURNING id`,
		a.WorkspaceID, c.brand, c.id, p.suggestion, p.name, p.kind, p.split, control, trial, start, end, from, note, actor).Scan(&id); err != nil {
		return "", err
	}
	if err := adsync.EnqueueTest(ctx, tx, a.WorkspaceID, id, adsync.TestStart, ""); err != nil {
		return "", err
	}
	props := map[string]any{"connector": c.connector, "kind": p.kind, "split_percent": p.split, "days": days, "from_suggestion": p.suggestion != nil}
	return id, recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "ad_test.created", "ad_test", id,
		map[string]any{"test_id": id, "campaign_id": c.id, "external_id": c.externalID, "connector": c.connector, "brand_id": c.brand, "kind": p.kind,
			"split_percent": p.split, "trial": p.trial, "suggestion_id": p.suggestion}, props)
}

func (s *Server) createAdTest(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if a.ActorType != "human" {
		writeError(w, r, http.StatusForbidden, "forbidden", "only a person starts a test, in Outflare under Paid ads, Tests")
		return
	}
	var in struct {
		CampaignID        string `json:"campaign_id"`
		Kind              string `json:"kind"`
		DailyBudgetMicros *int64 `json:"daily_budget_micros"`
		TargetCPAMicros   *int64 `json:"target_cpa_micros"`
		SplitPercent      int    `json:"split_percent"`
		Name              string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	c, ok := s.loadAdCampaign(w, r, nil, a, in.CampaignID)
	if !ok {
		return
	}
	p := adTestPlan{kind: in.Kind, trial: adsync.TestSettings{DailyBudgetMicros: in.DailyBudgetMicros, TargetCPAMicros: in.TargetCPAMicros},
		split: in.SplitPercent, name: in.Name}
	if !s.checkAdTest(w, r, a, c, &p) {
		return
	}
	var out adTestOut
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		id, err := s.startAdTest(r.Context(), tx, a, c, p)
		if err != nil {
			return err
		}
		out, err = scanAdTest(tx.QueryRow(r.Context(), `SELECT `+adTestCols+` `+adTestFrom+` WHERE x.id = $1`, id))
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}

// endAdTest keeps a test's trial or rolls it back.
func (s *Server) endAdTest(decision string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a := accessFrom(r.Context())
		if a.ActorType != "human" {
			writeError(w, r, http.StatusForbidden, "forbidden", "only a person keeps or rolls back a test, in Outflare under Paid ads, Tests")
			return
		}
		id := r.PathValue("id")
		if !isUUID(id) {
			writeError(w, r, http.StatusNotFound, "not_found", "test not found")
			return
		}
		v, err := scanAdTest(s.pool.QueryRow(r.Context(), `SELECT `+adTestCols+` `+adTestFrom+` WHERE x.workspace_id = $1 AND x.id = $2`, a.WorkspaceID, id))
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.sees(v.BrandID)) {
			writeError(w, r, http.StatusNotFound, "not_found", "test not found")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if !a.writes(v.BrandID) {
			writeError(w, r, http.StatusForbidden, "forbidden", "you can only decide tests for your own brands")
			return
		}
		if v.Status != "running" && v.Status != "judged" {
			writeError(w, r, http.StatusConflict, "not_active", "this test is "+v.Status+"; only a running or judged test is kept or rolled back")
			return
		}
		action := map[string]string{"keep": "ad_test.kept", "roll_back": "ad_test.rolled_back"}[decision]
		var out adTestOut
		err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
			tag, err := tx.Exec(r.Context(), `UPDATE ad_tests SET status = 'ending', decision = $2, decided_by = $3, decided_at = now(), error = NULL, updated_at = now()
				WHERE id = $1 AND status IN ('running', 'judged')`, v.ID, decision, a.ActorID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return errAdSuggestionTaken("this test was decided a moment ago")
			}
			if err := adsync.EnqueueTest(r.Context(), tx, a.WorkspaceID, v.ID, adsync.TestEnd, ""); err != nil {
				return err
			}
			if err := recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, action, "ad_test", v.ID,
				map[string]any{"test_id": v.ID, "campaign_id": v.CampaignID, "connector": v.Connector, "kind": v.Kind, "verdict": v.Verdict, "brand_id": v.BrandID},
				map[string]any{"connector": v.Connector, "kind": v.Kind, "verdict": v.Verdict}); err != nil {
				return err
			}
			out, err = scanAdTest(tx.QueryRow(r.Context(), `SELECT `+adTestCols+` `+adTestFrom+` WHERE x.id = $1`, v.ID))
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
}
