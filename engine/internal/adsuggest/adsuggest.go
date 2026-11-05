// Package adsuggest suggests changes to ad campaigns and judges the ones a
// person approved (step 2 of the paid ads plan).
//
// After each ad sync it reads every campaign's recent days and suggests at
// most one change at a time: raise the daily budget of a campaign that
// spends it all at a good cost per conversion, lower it where conversions
// cost too much, pause one that spends without converting, resume a paused
// one that did well. Each suggestion waits for a person; an approved one is
// made as an ad_changes row (the API does that, as if the person had made
// the change) and is then measured. The window is the platform's learning
// period, then at least a week and long enough for about 30 conversions at
// the campaign's rate, plus the days conversions take to be reported; no
// verdict after six weeks is "not sure". A side that costs more than twice
// the target three days running ends the window early.
//
// The numbers are the platform's own conversions. Suggestions are tools
// that wait for a person; acting on them alone, again and again, is Cloud's
// Autopilot.
package adsuggest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Job reads one workspace's campaigns: it judges what has been measured
// long enough, then suggests.
const Job = "ads.suggest"

// Kinds of suggestion.
const (
	BudgetUp   = "budget_up"
	BudgetDown = "budget_down"
	Pause      = "pause"
	Resume     = "resume"
	// Keywords (Google search campaigns).
	KeywordAdd   = "keyword_add"
	NegativeAdd  = "negative_add"
	KeywordSplit = "keyword_split"
	// BiddingTarget gives a campaign that maximises conversions with no
	// target a target cost per conversion, once it has the conversions to
	// bid to one (the second rung of the bidding ladder).
	BiddingTarget = "bidding_target"
)

// SavingKinds are the kinds that can only spend less: a lower budget, a
// pause and a negative keyword. An agent may approve these on its own
// where an extension allows it (api.Options.AdAutoApprove); every other
// kind waits for a person.
func SavingKinds() []string { return []string{BudgetDown, Pause, NegativeAdd} }

// Saves says whether a kind can only spend less (SavingKinds).
func Saves(kind string) bool { return slices.Contains(SavingKinds(), kind) }

// BiddingNetworks are the platforms whose bidding Outflare can change
// (connectors.AdTargetingManager with bidding methods).
var BiddingNetworks = map[string]bool{"google_ads": true}

// KeywordNetworks are the platforms whose keywords Outflare can change
// (connectors.AdKeywordManager).
var KeywordNetworks = map[string]bool{"google_ads": true}

// Verdicts.
const (
	Worked     = "worked"
	DidNotWork = "did_not_work"
	NotSure    = "not_sure"
)

// Manageable are the platforms whose campaigns Outflare can change
// (connectors.AdsManager); only they get suggestions.
var Manageable = map[string]bool{"google_ads": true, "meta_ads": true, "microsoft_ads": true, "linkedin_ads": true, "tiktok_ads": true}

// The rules' numbers.
const (
	recentDays    = 14   // the days a campaign is judged on
	minConv       = 5.0  // conversions before cost per conversion means anything
	overTarget    = 1.3  // lower the budget above this times the target
	underTarget   = 0.9  // raise it at or below this times the target
	budgetStep    = 0.2  // a fifth, up or down: within what platforms learn from fast
	cappedDays    = 4    // days of the last 7 that spent nine tenths of the budget
	pauseSpend    = 3.0  // spend with no conversion, in targets, before a pause
	resumeTarget  = 0.8  // a paused campaign this far under target is worth resuming
	resumeMinConv = 10.0 // and had at least these conversions in 90 days
	quietDays     = 7    // no suggestion within a week of any change to the campaign
	wantConv      = 30.0 // conversions a window aims for
	minWindow     = 7    // days, at least, after the learning period
	maxWindow     = 42   // six weeks: not sure after that
	lagDays       = 3    // days platforms take to report most conversions
	stopFactor    = 2.0  // a side over twice the target ...
	stopRun       = 3    // ... this many days running ends the window early
	ladderDays    = 28   // a target is suggested on the last four weeks ...
	ladderConv    = 30.0 // ... once they have this many conversions
)

// Runner runs the job.
type Runner struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	Now  func() time.Time
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

type payload struct{}

// Register adds the job handler.
func (r *Runner) Register(q *jobs.Queue) {
	q.Handle(Job, func(ctx context.Context, j jobs.Job) error {
		if j.WorkspaceID == nil {
			return nil
		}
		_, err := r.Run(ctx, *j.WorkspaceID)
		return err
	})
}

// Enqueue queues a run for the workspace, at most one an hour.
func Enqueue(ctx context.Context, q jobs.Querier, ws string, now time.Time) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: Job, WorkspaceID: &ws, Payload: payload{},
		IdempotencyKey: "ads.suggest:" + ws + ":" + now.UTC().Format("2006-01-02T15"), MaxAttempts: 3})
	return err
}

// Result says what a run did.
type Result struct {
	Judged    int `json:"judged"`
	Suggested int `json:"suggested"`
	Expired   int `json:"expired"`
}

// Run judges and suggests for one workspace.
func (r *Runner) Run(ctx context.Context, ws string) (Result, error) {
	start := r.now()
	var res Result
	var err error
	defer func() {
		outcome := "success"
		if err != nil {
			outcome = "error"
			track.Fail(ctx, r.Log, track.Event{Name: "ad_suggestions.failed", WorkspaceID: ws, ActorType: "system", Surface: "job", Outcome: track.Error}, "internal", err)
		} else {
			track.Emit(ctx, track.Event{Name: "ad_suggestions.refreshed", WorkspaceID: ws, ActorType: "system", Surface: "job", Outcome: track.Success,
				Props: map[string]any{"judged": res.Judged, "suggested": res.Suggested, "expired": res.Expired}})
		}
		metrics.AdSuggestRun(outcome, r.now().Sub(start))
	}()
	if res.Judged, err = r.judge(ctx, ws); err != nil {
		return res, err
	}
	res.Suggested, res.Expired, err = r.suggest(ctx, ws)
	return res, err
}

// lastDay is the last whole day: today is still being reported.
func (r *Runner) lastDay() time.Time {
	t := r.now().UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
}

// campaign is one campaign's numbers as the rules read them.
type campaign struct {
	id, accountID, connector, currency, status string
	brand                                      *string
	budget, target                             *int64
	spend14, spend90                           int64
	conv14, conv90                             float64
	capped                                     int
	acctCPA                                    *int64
	// How the platform bids, its target, and the last four weeks.
	bidding        string
	platformTarget *int64
	spend28        int64
	conv28         float64
}

type proposal struct {
	kind, reason string
	evidence     map[string]any
	proposed     map[string]any
}

// rules decides at most one suggestion for a campaign.
func rules(c campaign) *proposal {
	ev := map[string]any{"days": recentDays, "spend_micros": c.spend14, "conversions": c.conv14, "currency": c.currency}
	if c.target != nil {
		ev["target_cost_per_conversion_micros"] = *c.target
	}
	if c.budget != nil {
		ev["daily_budget_micros"] = *c.budget
	}
	var cpa float64
	if c.conv14 > 0 {
		cpa = float64(c.spend14) / c.conv14
		ev["cost_per_conversion_micros"] = int64(math.Round(cpa))
	}
	money := func(m float64) string { return fmt.Sprintf("%.2f %s", m/1e6, c.currency) }
	switch c.status {
	case "active":
		// Spending with nothing to show: three targets' worth, or three
		// times what a conversion costs across the account.
		limit := c.target
		if limit == nil {
			limit = c.acctCPA
		}
		if c.conv14 == 0 && limit != nil && float64(c.spend14) >= pauseSpend*float64(*limit) {
			return &proposal{kind: Pause, evidence: ev, proposed: map[string]any{"status": "paused"},
				reason: fmt.Sprintf("Spent %s in %d days with no conversions, more than three times what a conversion should cost (%s).", money(float64(c.spend14)), recentDays, money(float64(*limit)))}
		}
		if c.target == nil || c.budget == nil || *c.budget <= 0 || c.conv14 < minConv {
			return nil
		}
		t := float64(*c.target)
		if cpa > overTarget*t {
			nb := step(*c.budget, -budgetStep)
			return &proposal{kind: BudgetDown, evidence: ev, proposed: map[string]any{"daily_budget_micros": nb},
				reason: fmt.Sprintf("A conversion cost %s over %d days against a target of %s. Lower the daily budget by a fifth, to %s.", money(cpa), recentDays, money(t), money(float64(nb)))}
		}
		if cpa <= underTarget*t && c.capped >= cappedDays {
			ev["days_budget_spent"] = c.capped
			nb := step(*c.budget, budgetStep)
			return &proposal{kind: BudgetUp, evidence: ev, proposed: map[string]any{"daily_budget_micros": nb},
				reason: fmt.Sprintf("A conversion cost %s against a target of %s, and the budget ran out on %d of the last 7 days. Raise it by a fifth, to %s.", money(cpa), money(t), c.capped, money(float64(nb)))}
		}
	case "paused":
		if c.target == nil || c.conv90 < resumeMinConv {
			return nil
		}
		cpa90 := float64(c.spend90) / c.conv90
		if cpa90 <= resumeTarget*float64(*c.target) {
			ev["days"], ev["spend_micros"], ev["conversions"], ev["cost_per_conversion_micros"] = 90, c.spend90, c.conv90, int64(math.Round(cpa90))
			return &proposal{kind: Resume, evidence: ev, proposed: map[string]any{"status": "active"},
				reason: fmt.Sprintf("While it ran in the last 90 days a conversion cost %s, well under the target of %s.", money(cpa90), money(float64(*c.target)))}
		}
	}
	return nil
}

// ladder suggests a target cost per conversion for a campaign that
// maximises conversions without one, at what a conversion cost over the
// last four weeks, so the platform keeps its volume while it learns to
// hold the cost.
func ladder(c campaign) *proposal {
	if c.status != "active" || !BiddingNetworks[c.connector] || c.bidding != "maximize_conversions" || c.platformTarget != nil || c.conv28 < ladderConv {
		return nil
	}
	cpa := int64(math.Ceil(float64(c.spend28)/c.conv28/10_000)) * 10_000
	if cpa < 10_000 {
		return nil
	}
	ev := map[string]any{"days": ladderDays, "spend_micros": c.spend28, "conversions": c.conv28, "currency": c.currency,
		"cost_per_conversion_micros": cpa, "bidding_strategy": c.bidding}
	return &proposal{kind: BiddingTarget, evidence: ev, proposed: map[string]any{"strategy": "maximize_conversions", "target_cpa_micros": cpa},
		reason: fmt.Sprintf("It bids for as many conversions as it can with no target, and had %.0f in the last %d days at %.2f %s each. Bid to a target of %.2f %s so it holds that cost.",
			c.conv28, ladderDays, float64(cpa)/1e6, c.currency, float64(cpa)/1e6, c.currency)}
}

// step moves a budget by a fraction, to whole cents.
func step(budget int64, by float64) int64 {
	v := int64(math.Round(float64(budget)*(1+by)/10_000)) * 10_000
	if v < 10_000 {
		v = 10_000
	}
	return v
}

func (r *Runner) suggest(ctx context.Context, ws string) (made, expired int, err error) {
	end := r.lastDay().Format(time.DateOnly)
	rows, err := r.Pool.Query(ctx, `
		WITH acct AS (
			SELECT e.ad_account_id, (sum(m.spend_micros) / NULLIF(sum(m.conversions), 0))::bigint AS cpa
			FROM ad_metrics_daily m JOIN ad_entities e ON e.id = m.ad_entity_id
			WHERE m.workspace_id = $1 AND e.level = 'campaign' AND m.day > $2::date - 90 AND m.day <= $2::date
			GROUP BY e.ad_account_id)
		SELECT e.id, a.id, a.connector, a.currency, e.status, COALESCE(e.brand_id, a.brand_id), e.daily_budget_micros,
			COALESCE(e.target_cost_per_conversion_micros, a.target_cost_per_conversion_micros, e.platform_target_cpa_micros),
			COALESCE(sum(m.spend_micros) FILTER (WHERE m.day > $2::date - 14), 0)::bigint,
			COALESCE(sum(m.conversions) FILTER (WHERE m.day > $2::date - 14), 0)::float8,
			COALESCE(sum(m.spend_micros), 0)::bigint, COALESCE(sum(m.conversions), 0)::float8,
			count(*) FILTER (WHERE m.day > $2::date - 7 AND e.daily_budget_micros > 0 AND m.spend_micros >= e.daily_budget_micros * 9 / 10)::int,
			max(acct.cpa), e.bidding_strategy, e.platform_target_cpa_micros,
			COALESCE(sum(m.spend_micros) FILTER (WHERE m.day > $2::date - 28), 0)::bigint,
			COALESCE(sum(m.conversions) FILTER (WHERE m.day > $2::date - 28), 0)::float8
		FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id
		LEFT JOIN ad_metrics_daily m ON m.ad_entity_id = e.id AND m.day > $2::date - 90 AND m.day <= $2::date
		LEFT JOIN acct ON acct.ad_account_id = a.id
		WHERE e.workspace_id = $1 AND e.level = 'campaign' AND e.status IN ('active', 'paused') AND a.status = 'active' AND a.sync_on
			AND NOT EXISTS (SELECT 1 FROM ad_changes c WHERE c.ad_entity_id = e.id AND c.created_at > now() - make_interval(days => $3))
			AND NOT EXISTS (SELECT 1 FROM ad_suggestions s WHERE s.ad_entity_id = e.id AND s.status IN ('measuring', 'testing'))
			AND NOT EXISTS (SELECT 1 FROM ad_tests t WHERE t.ad_entity_id = e.id AND t.status IN ('starting', 'running', 'judged', 'ending'))
		GROUP BY e.id, a.id`, ws, end, quietDays)
	if err != nil {
		return 0, 0, err
	}
	camps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (campaign, error) {
		var c campaign
		err := row.Scan(&c.id, &c.accountID, &c.connector, &c.currency, &c.status, &c.brand, &c.budget, &c.target,
			&c.spend14, &c.conv14, &c.spend90, &c.conv90, &c.capped, &c.acctCPA, &c.bidding, &c.platformTarget, &c.spend28, &c.conv28)
		return c, err
	})
	if err != nil {
		return 0, 0, err
	}
	kws, err := r.keywordProposals(ctx, ws, end)
	if err != nil {
		return 0, 0, err
	}
	// What this run still finds true; open suggestions it doesn't expire.
	keep := map[string]bool{}
	err = pgx.BeginFunc(ctx, r.Pool, func(tx pgx.Tx) error {
		made = 0
		for _, c := range camps {
			if !Manageable[c.connector] {
				continue
			}
			var list []*proposal
			if p := rules(c); p != nil {
				list = append(list, p)
			} else if p := ladder(c); p != nil {
				list = append(list, p)
			}
			for _, p := range kws[c.id] {
				if c.status == "active" {
					list = append(list, p)
				}
			}
			for _, p := range list {
				ev, _ := json.Marshal(p.evidence)
				pr, _ := json.Marshal(p.proposed)
				var id string
				var inserted bool
				if err := tx.QueryRow(ctx, `INSERT INTO ad_suggestions (workspace_id, brand_id, ad_entity_id, kind, reason, evidence, proposed)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
				ON CONFLICT (ad_entity_id, kind) WHERE status = 'open'
				DO UPDATE SET reason = EXCLUDED.reason, evidence = EXCLUDED.evidence, proposed = EXCLUDED.proposed, updated_at = now()
				RETURNING id, xmax = 0`, ws, c.brand, c.id, p.kind, p.reason, ev, pr).Scan(&id, &inserted); err != nil {
					return err
				}
				keep[id] = true
				if !inserted {
					continue
				}
				made++
				metrics.AdSuggested(c.connector, p.kind)
				data := map[string]any{"suggestion_id": id, "campaign_id": c.id, "connector": c.connector, "kind": p.kind, "proposed": p.proposed}
				if c.brand != nil {
					data["brand_id"] = *c.brand
				}
				if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "ad_suggestion.created",
					TargetType: "ad_suggestion", TargetID: id, Data: data, Props: map[string]any{"connector": c.connector, "kind": p.kind}}); err != nil {
					return err
				}
			}
		}
		ids := make([]string, 0, len(keep))
		for id := range keep {
			ids = append(ids, id)
		}
		tag, err := tx.Exec(ctx, `UPDATE ad_suggestions SET status = 'expired', updated_at = now()
			WHERE workspace_id = $1 AND status = 'open' AND NOT (id = ANY($2::uuid[]))`, ws, ids)
		expired = int(tag.RowsAffected())
		return err
	})
	return made, expired, err
}

// Window is how long a change is measured: the learning period, then at
// least a week and long enough for wantConv conversions at the rate
// before it, plus the conversion lag; capped at six weeks.
func Window(kind string, convPerDay float64) (learn, days int, note string) {
	switch kind {
	case BudgetUp, BudgetDown:
		learn = 3 // a fifth either way: platforms adjust within days
	case Resume, KeywordAdd, KeywordSplit:
		learn = 7 // a restarted campaign, a new keyword or ad group learns again
	case BiddingTarget:
		learn = 14 // Google's bidding takes one to two weeks to settle
	case NegativeAdd:
		learn = 1
	}
	need := minWindow
	if convPerDay > 0 {
		if n := int(math.Ceil(wantConv / convPerDay)); n > need {
			need = n
		}
	} else if kind != Pause {
		need = maxWindow
	}
	days = need + lagDays
	if learn+days > maxWindow {
		days = maxWindow - learn
	}
	note = fmt.Sprintf("%d days to settle, then %d days measured (at least a week, about %d conversions, and %d days for late conversions).", learn, days, int(wantConv), lagDays)
	if kind == Pause {
		note = fmt.Sprintf("%d days measured: what the account converts without it.", days)
	}
	return learn, days, note
}

// totals are a campaign's or account's numbers over some days.
type totals struct {
	Spend int64   `json:"spend_micros"`
	Conv  float64 `json:"conversions"`
	Days  int     `json:"days"`
}

func (t totals) cpa() float64 {
	if t.Conv <= 0 {
		return math.Inf(1)
	}
	return float64(t.Spend) / t.Conv
}

type measuring struct {
	id, kind, entity, account, connector string
	brand                                *string
	from, until                          time.Time
	target                               *int64
}

func (r *Runner) judge(ctx context.Context, ws string) (int, error) {
	last := r.lastDay()
	// A change the platform refused was never made: nothing to measure.
	if _, err := r.Pool.Exec(ctx, `UPDATE ad_suggestions s SET status = 'judged', verdict = 'not_sure', judged_at = now(), updated_at = now(),
			verdict_note = 'The platform refused the change, so it was never made: ' || c.error
		FROM ad_changes c WHERE c.id = s.change_id AND s.workspace_id = $1 AND s.status = 'measuring' AND c.status = 'failed'`, ws); err != nil {
		return 0, err
	}
	rows, err := r.Pool.Query(ctx, `SELECT s.id, s.kind, s.ad_entity_id, a.id, a.connector, s.brand_id, s.measure_from, s.measure_until,
			COALESCE(e.target_cost_per_conversion_micros, a.target_cost_per_conversion_micros, e.platform_target_cpa_micros)
		FROM ad_suggestions s JOIN ad_entities e ON e.id = s.ad_entity_id JOIN ad_accounts a ON a.id = e.ad_account_id
		WHERE s.workspace_id = $1 AND s.status = 'measuring' AND s.measure_from IS NOT NULL AND s.measure_from <= $2`, ws, last)
	if err != nil {
		return 0, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (measuring, error) {
		var m measuring
		err := row.Scan(&m.id, &m.kind, &m.entity, &m.account, &m.connector, &m.brand, &m.from, &m.until, &m.target)
		return m, err
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range list {
		done, err := r.judgeOne(ctx, ws, m, last)
		if err != nil {
			return n, err
		}
		if done {
			n++
		}
	}
	return n, nil
}

// sum reads a campaign's (or, with whole, its account's) numbers between
// two days.
func (r *Runner) sum(ctx context.Context, ws, entity, account string, whole bool, from, to time.Time) (totals, error) {
	t := totals{Days: int(to.Sub(from).Hours()/24) + 1}
	q := `SELECT COALESCE(sum(spend_micros), 0)::bigint, COALESCE(sum(conversions), 0)::float8 FROM ad_metrics_daily
		WHERE workspace_id = $1 AND ad_entity_id = $2 AND day BETWEEN $3 AND $4`
	id := entity
	if whole {
		q = `SELECT COALESCE(sum(m.spend_micros), 0)::bigint, COALESCE(sum(m.conversions), 0)::float8
			FROM ad_metrics_daily m JOIN ad_entities e ON e.id = m.ad_entity_id
			WHERE m.workspace_id = $1 AND e.ad_account_id = $2 AND e.level = 'campaign' AND m.day BETWEEN $3 AND $4`
		id = account
	}
	err := r.Pool.QueryRow(ctx, q, ws, id, from, to).Scan(&t.Spend, &t.Conv)
	return t, err
}

// judgeOne gives a verdict once the window ends, or early when the
// changed side costs over twice the target three days running.
func (r *Runner) judgeOne(ctx context.Context, ws string, m measuring, last time.Time) (bool, error) {
	end := m.until
	early := false
	if last.Before(m.until) {
		if m.target == nil || m.kind == Pause || last.Sub(m.from) < (stopRun-1)*24*time.Hour {
			return false, nil
		}
		// Early stop: each of the last three days, the window so far costs
		// over twice the target.
		over := 0
		for d := 0; d < stopRun; d++ {
			to := last.AddDate(0, 0, -d)
			t, err := r.sum(ctx, ws, m.entity, m.account, false, m.from, to)
			if err != nil {
				return false, err
			}
			if t.Spend > 0 && t.cpa() > stopFactor*float64(*m.target) {
				over++
			}
		}
		if over < stopRun {
			return false, nil
		}
		end, early = last, true
	}
	days := int(end.Sub(m.from).Hours()/24) + 1
	// The same number of days just before the change.
	var changed time.Time
	if err := r.Pool.QueryRow(ctx, `SELECT COALESCE(c.applied_at, c.created_at) FROM ad_suggestions s JOIN ad_changes c ON c.id = s.change_id WHERE s.id = $1`, m.id).Scan(&changed); err != nil {
		return false, err
	}
	bEnd := time.Date(changed.Year(), changed.Month(), changed.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	bFrom := bEnd.AddDate(0, 0, -(days - 1))
	whole := m.kind == Pause
	before, err := r.sum(ctx, ws, m.entity, m.account, whole, bFrom, bEnd)
	if err != nil {
		return false, err
	}
	after, err := r.sum(ctx, ws, m.entity, m.account, whole, m.from, end)
	if err != nil {
		return false, err
	}
	verdict, note := Verdict(m.kind, before, after, m.target)
	if early {
		verdict, note = DidNotWork, "Stopped early: since the change, a conversion has cost more than twice the target three days running. "+note
	}
	res, _ := json.Marshal(map[string]any{"before": before, "after": after, "early": early})
	err = pgx.BeginFunc(ctx, r.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE ad_suggestions SET status = 'judged', verdict = $2, verdict_note = $3, result = $4, judged_at = now(), updated_at = now()
			WHERE id = $1 AND status = 'measuring'`, m.id, verdict, note, res)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		data := map[string]any{"suggestion_id": m.id, "campaign_id": m.entity, "connector": m.connector, "kind": m.kind, "verdict": verdict, "early": early}
		if m.brand != nil {
			data["brand_id"] = *m.brand
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "ad_suggestion.judged",
			TargetType: "ad_suggestion", TargetID: m.id, Data: data, Props: map[string]any{"connector": m.connector, "kind": m.kind, "verdict": verdict, "early": early}})
	})
	if err != nil {
		return false, err
	}
	metrics.AdSuggestionJudged(m.connector, m.kind, verdict)
	return true, nil
}

// Verdict compares the days after a change with as many days before it.
func Verdict(kind string, before, after totals, target *int64) (string, string) {
	// A resumed campaign had nothing running before: it is judged on its
	// own days against the target.
	if kind == Resume && after.Conv < minConv {
		return NotSure, fmt.Sprintf("Too few conversions to tell: %.0f since it restarted.", after.Conv)
	}
	if kind != Pause && kind != Resume && (before.Conv < minConv || after.Conv < minConv) {
		return NotSure, fmt.Sprintf("Too few conversions to tell: %.0f before and %.0f after.", before.Conv, after.Conv)
	}
	switch kind {
	case BudgetUp:
		extraConv := after.Conv - before.Conv
		extraSpend := after.Spend - before.Spend
		if extraConv <= 0 {
			return DidNotWork, fmt.Sprintf("More was spent (%+.2f) but conversions didn't rise (%.0f before, %.0f after).", float64(extraSpend)/1e6, before.Conv, after.Conv)
		}
		marginal := float64(extraSpend) / extraConv
		if target != nil {
			switch {
			case marginal <= 1.2*float64(*target):
				return Worked, fmt.Sprintf("%.0f more conversions at about %.2f each, within the target of %.2f.", extraConv, marginal/1e6, float64(*target)/1e6)
			case marginal > 1.5*float64(*target):
				return DidNotWork, fmt.Sprintf("The extra conversions cost about %.2f each, well over the target of %.2f.", marginal/1e6, float64(*target)/1e6)
			}
		}
		return NotSure, fmt.Sprintf("%.0f more conversions at about %.2f each.", extraConv, marginal/1e6)
	case BudgetDown:
		switch {
		case after.cpa() <= 0.95*before.cpa():
			return Worked, fmt.Sprintf("A conversion costs %.2f now, down from %.2f.", after.cpa()/1e6, before.cpa()/1e6)
		case after.cpa() >= 1.05*before.cpa():
			return DidNotWork, fmt.Sprintf("A conversion costs %.2f now, up from %.2f: spending less didn't make them cheaper.", after.cpa()/1e6, before.cpa()/1e6)
		}
		return NotSure, fmt.Sprintf("A conversion costs about the same: %.2f before, %.2f after.", before.cpa()/1e6, after.cpa()/1e6)
	case Pause:
		// The account without the campaign: fewer than nine tenths of its
		// conversions means the campaign mattered more than it showed.
		if before.Conv == 0 && after.Conv == 0 {
			return NotSure, "The account recorded no conversions before or after."
		}
		if after.Conv >= 0.9*before.Conv {
			return Worked, fmt.Sprintf("The account converted as well without it (%.0f before, %.0f after) and spent %.2f less.", before.Conv, after.Conv, float64(before.Spend-after.Spend)/1e6)
		}
		return DidNotWork, fmt.Sprintf("The account's conversions fell from %.0f to %.0f without it.", before.Conv, after.Conv)
	case BiddingTarget:
		// A target should hold the cost without losing many conversions.
		switch {
		case after.cpa() <= 1.05*before.cpa() && after.Conv >= 0.85*before.Conv:
			return Worked, fmt.Sprintf("A conversion costs %.2f (%.2f before), with %.0f conversions (%.0f before).", after.cpa()/1e6, before.cpa()/1e6, after.Conv, before.Conv)
		case after.Conv < 0.7*before.Conv || after.cpa() > 1.2*before.cpa():
			return DidNotWork, fmt.Sprintf("With the target the campaign got %.0f conversions (%.0f before) at %.2f each (%.2f before).", after.Conv, before.Conv, after.cpa()/1e6, before.cpa()/1e6)
		}
		return NotSure, fmt.Sprintf("A conversion costs %.2f (%.2f before), with %.0f conversions (%.0f before).", after.cpa()/1e6, before.cpa()/1e6, after.Conv, before.Conv)
	case KeywordAdd, KeywordSplit:
		// The campaign as a whole: more conversions at no worse a cost.
		switch {
		case after.Conv >= before.Conv && after.cpa() <= 1.05*before.cpa():
			return Worked, fmt.Sprintf("The campaign got %.0f conversions (%.0f before) at %.2f each (%.2f before).", after.Conv, before.Conv, after.cpa()/1e6, before.cpa()/1e6)
		case after.cpa() > 1.2*before.cpa():
			return DidNotWork, fmt.Sprintf("A conversion in the campaign costs %.2f now, up from %.2f.", after.cpa()/1e6, before.cpa()/1e6)
		}
		return NotSure, fmt.Sprintf("The campaign got %.0f conversions (%.0f before) at %.2f each (%.2f before).", after.Conv, before.Conv, after.cpa()/1e6, before.cpa()/1e6)
	case NegativeAdd:
		// Cheaper conversions without losing many.
		switch {
		case after.cpa() <= 0.97*before.cpa() && after.Conv >= 0.9*before.Conv:
			return Worked, fmt.Sprintf("A conversion in the campaign costs %.2f now, down from %.2f, with %.0f conversions (%.0f before).", after.cpa()/1e6, before.cpa()/1e6, after.Conv, before.Conv)
		case after.Conv < 0.8*before.Conv:
			return DidNotWork, fmt.Sprintf("The campaign's conversions fell from %.0f to %.0f: the search may have been worth more than it showed.", before.Conv, after.Conv)
		}
		return NotSure, fmt.Sprintf("A conversion in the campaign costs %.2f (%.2f before), with %.0f conversions (%.0f before).", after.cpa()/1e6, before.cpa()/1e6, after.Conv, before.Conv)
	case Resume:
		if target != nil && after.cpa() <= 1.1*float64(*target) {
			return Worked, fmt.Sprintf("%.0f conversions at %.2f each, within the target of %.2f.", after.Conv, after.cpa()/1e6, float64(*target)/1e6)
		}
		if target != nil && after.cpa() > 1.5*float64(*target) {
			return DidNotWork, fmt.Sprintf("A conversion costs %.2f, well over the target of %.2f.", after.cpa()/1e6, float64(*target)/1e6)
		}
		return NotSure, fmt.Sprintf("%.0f conversions at %.2f each.", after.Conv, after.cpa()/1e6)
	}
	return NotSure, ""
}

// Started sets the window of a suggestion whose change was made, in tx:
// the API calls it when a person approves. convPerDay is the campaign's
// rate over the days the suggestion read.
func Started(ctx context.Context, tx pgx.Tx, id, kind string, convPerDay float64, today time.Time) error {
	learn, days, note := Window(kind, convPerDay)
	d := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	from := d.AddDate(0, 0, learn)
	until := from.AddDate(0, 0, days-1)
	tag, err := tx.Exec(ctx, `UPDATE ad_suggestions SET measure_from = $2, measure_until = $3, window_note = $4, updated_at = now() WHERE id = $1`, id, from, until, note)
	if err == nil && tag.RowsAffected() == 0 {
		err = errors.New("suggestion not found")
	}
	return err
}

// The keyword rules' numbers, over the last 30 days.
const (
	keywordDays     = 30
	termMinConv     = 2.0  // a search that converted at least this often ...
	termGoodCPA     = 1.0  // ... at or under the target, becomes a keyword
	termMinClicks   = 10   // a search with this many clicks ...
	termWasteSpend  = 2.0  // ... and two targets' worth spent, no conversion: a negative
	splitMinConv    = 10.0 // a keyword with this many conversions ...
	splitSpendShare = 0.4  // ... and this share of its ad group's spend ...
	splitMinPeers   = 3    // ... in a group of at least this many keywords gets its own
)

// keywordProposals finds, for each active Google search campaign, the best
// search to add as a keyword, the worst to add as a negative, and the
// keyword most worth its own ad group: at most one of each.
func (r *Runner) keywordProposals(ctx context.Context, ws, end string) (map[string][]*proposal, error) {
	out := map[string][]*proposal{}
	// Searches, with the campaign's (else account's) cost per conversion
	// to judge them by.
	rows, err := r.Pool.Query(ctx, `
		WITH acct AS (
			SELECT e.ad_account_id, (sum(m.spend_micros) / NULLIF(sum(m.conversions), 0))::bigint AS cpa
			FROM ad_metrics_daily m JOIN ad_entities e ON e.id = m.ad_entity_id
			WHERE m.workspace_id = $1 AND e.level = 'campaign' AND m.day > $2::date - 90 AND m.day <= $2::date
			GROUP BY e.ad_account_id)
		SELECT c.id, a.connector, a.currency, g.external_id, g.name, t.term,
			COALESCE(sum(t.spend_micros), 0)::bigint, COALESCE(sum(t.clicks), 0)::bigint, COALESCE(sum(t.conversions), 0)::float8,
			COALESCE(c.target_cost_per_conversion_micros, a.target_cost_per_conversion_micros, c.platform_target_cpa_micros, max(acct.cpa)),
			EXISTS (SELECT 1 FROM ad_keywords k WHERE k.ad_group_id = g.id AND lower(k.text) = lower(t.term) AND k.status <> 'removed')
		FROM ad_search_terms_daily t JOIN ad_entities g ON g.id = t.ad_group_id JOIN ad_entities c ON c.id = g.parent_id
		JOIN ad_accounts a ON a.id = t.ad_account_id LEFT JOIN acct ON acct.ad_account_id = a.id
		WHERE t.workspace_id = $1 AND t.day > $2::date - $3::int AND t.day <= $2::date AND c.status = 'active' AND g.status = 'active'
		GROUP BY c.id, a.id, g.id, t.term`, ws, end, keywordDays)
	if err != nil {
		return nil, err
	}
	type term struct {
		campaign, connector, currency, group, groupName, text string
		spend, clicks                                         int64
		conv                                                  float64
		cpa                                                   *int64
		exists                                                bool
	}
	terms, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (term, error) {
		var t term
		err := row.Scan(&t.campaign, &t.connector, &t.currency, &t.group, &t.groupName, &t.text, &t.spend, &t.clicks, &t.conv, &t.cpa, &t.exists)
		return t, err
	})
	if err != nil {
		return nil, err
	}
	best, worst := map[string]term{}, map[string]term{}
	for _, t := range terms {
		if !KeywordNetworks[t.connector] || t.cpa == nil || t.exists || len(t.text) > 80 || len(strings.Fields(t.text)) > 10 {
			continue
		}
		if t.conv >= termMinConv && float64(t.spend)/t.conv <= termGoodCPA*float64(*t.cpa) {
			if b, ok := best[t.campaign]; !ok || t.conv > b.conv {
				best[t.campaign] = t
			}
		}
		if t.conv == 0 && t.clicks >= termMinClicks && float64(t.spend) >= termWasteSpend*float64(*t.cpa) {
			if w, ok := worst[t.campaign]; !ok || t.spend > w.spend {
				worst[t.campaign] = t
			}
		}
	}
	money := func(m float64, cur string) string { return fmt.Sprintf("%.2f %s", m/1e6, cur) }
	for id, t := range best {
		out[id] = append(out[id], &proposal{kind: KeywordAdd,
			evidence: map[string]any{"days": keywordDays, "search": t.text, "ad_group": t.groupName, "spend_micros": t.spend, "clicks": t.clicks, "conversions": t.conv,
				"cost_per_conversion_micros": int64(float64(t.spend) / t.conv), "target_cost_per_conversion_micros": *t.cpa, "currency": t.currency},
			proposed: map[string]any{"keyword": t.text, "match_type": "exact", "ad_group_id": t.group},
			reason: fmt.Sprintf("People searching %q converted %.0f times in %d days at %s each, but it isn't a keyword yet. Add it as an exact keyword in %s, so you bid on it directly.",
				t.text, t.conv, keywordDays, money(float64(t.spend)/t.conv, t.currency), t.groupName)})
	}
	for id, t := range worst {
		out[id] = append(out[id], &proposal{kind: NegativeAdd,
			evidence: map[string]any{"days": keywordDays, "search": t.text, "ad_group": t.groupName, "spend_micros": t.spend, "clicks": t.clicks, "conversions": 0.0,
				"target_cost_per_conversion_micros": *t.cpa, "currency": t.currency},
			proposed: map[string]any{"keyword": t.text, "match_type": "exact"},
			reason: fmt.Sprintf("People searching %q clicked %d times and cost %s in %d days with no conversion. Add it as an exact negative keyword to the campaign.",
				t.text, t.clicks, money(float64(t.spend), t.currency), keywordDays)})
	}
	// Keywords worth their own ad group.
	rows, err = r.Pool.Query(ctx, `
		WITH kw AS (
			SELECT k.id, k.ad_group_id, k.text, k.match_type, COALESCE(sum(m.spend_micros), 0)::bigint AS spend, COALESCE(sum(m.conversions), 0)::float8 AS conv
			FROM ad_keywords k LEFT JOIN ad_keyword_metrics_daily m ON m.keyword_id = k.id AND m.day > $2::date - $3::int AND m.day <= $2::date
			WHERE k.workspace_id = $1 AND k.status = 'active' GROUP BY k.id)
		SELECT c.id, a.connector, a.currency, g.external_id, g.name, kw.text, kw.spend, kw.conv,
			(sum(kw.spend) OVER (PARTITION BY kw.ad_group_id))::bigint, (count(*) OVER (PARTITION BY kw.ad_group_id))::int
		FROM kw JOIN ad_entities g ON g.id = kw.ad_group_id JOIN ad_entities c ON c.id = g.parent_id
		JOIN ad_accounts a ON a.id = g.ad_account_id
		WHERE c.status = 'active' AND g.status = 'active'`, ws, end, keywordDays)
	if err != nil {
		return nil, err
	}
	type kwRow struct {
		campaign, connector, currency, group, groupName, text string
		spend                                                 int64
		conv                                                  float64
		groupSpend                                            int64
		peers                                                 int
	}
	kwl, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (kwRow, error) {
		var k kwRow
		err := row.Scan(&k.campaign, &k.connector, &k.currency, &k.group, &k.groupName, &k.text, &k.spend, &k.conv, &k.groupSpend, &k.peers)
		return k, err
	})
	if err != nil {
		return nil, err
	}
	split := map[string]kwRow{}
	for _, k := range kwl {
		if !KeywordNetworks[k.connector] || k.conv < splitMinConv || k.peers < splitMinPeers || k.groupSpend <= 0 || float64(k.spend) < splitSpendShare*float64(k.groupSpend) {
			continue
		}
		if b, ok := split[k.campaign]; !ok || k.conv > b.conv {
			split[k.campaign] = k
		}
	}
	for id, k := range split {
		share := float64(k.spend) / float64(k.groupSpend)
		out[id] = append(out[id], &proposal{kind: KeywordSplit,
			evidence: map[string]any{"days": keywordDays, "keyword": k.text, "ad_group": k.groupName, "spend_micros": k.spend, "conversions": k.conv,
				"share_of_ad_group_spend": share, "keywords_in_ad_group": k.peers, "currency": k.currency},
			proposed: map[string]any{"keyword": k.text, "ad_group_id": k.group, "new_ad_group_name": k.text + " (exact)"},
			reason: fmt.Sprintf("%q brought %.0f conversions in %d days and takes %.0f%% of %s's spend, shared with %d other keywords. Give it its own ad group (with copies of the group's ads), so its ads and bids can be tuned for it.",
				k.text, k.conv, keywordDays, share*100, k.groupName, k.peers-1)})
	}
	return out, nil
}
