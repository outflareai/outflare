package adsuggest

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/db"
	"github.com/outflareai/outflare/engine/internal/paidads"
	"github.com/outflareai/outflare/engine/internal/track"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

// fixture is a workspace with a brand, a Google Ads account on it with
// two search campaigns, and a shared account no brand owns.
type fixture struct {
	t                                  *testing.T
	pool                               *pgxpool.Pool
	ws, brand, acct, spring, brandCamp string
	sharedAcct, shared                 string
	run                                *Runner
}

func (f *fixture) q(sql string, args ...any) string {
	f.t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
	return id
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{t: t, pool: testPool(t)}
	f.ws = f.q(`INSERT INTO workspaces (name, slug) VALUES ('Findings', 'fi-' || substr(gen_random_uuid()::text, 1, 8)) RETURNING id::text`)
	f.brand = f.q(`INSERT INTO brands (workspace_id, name, handle, website, currency) VALUES ($1, 'Northpeak', 'northpeak', 'https://northpeak.example', 'USD') RETURNING id::text`, f.ws)
	integ := f.q(`INSERT INTO integrations (brand_id, workspace_id, connector, name, credentials) VALUES ($1, $2, 'google_ads', 'Google Ads', '\x00') RETURNING id::text`, f.brand, f.ws)
	f.acct = f.q(`INSERT INTO ad_accounts (brand_id, workspace_id, integration_id, connector, external_id, name, currency) VALUES ($1, $2, $3, 'google_ads', '123', 'Northpeak', 'USD') RETURNING id::text`, f.brand, f.ws, integ)
	f.spring = f.q(`INSERT INTO ad_entities (brand_id, workspace_id, ad_account_id, external_id, name, status, objective, daily_budget_micros) VALUES ($1, $2, $3, '7', 'Spring search', 'active', 'search', 30000000) RETURNING id::text`, f.brand, f.ws, f.acct)
	f.brandCamp = f.q(`INSERT INTO ad_entities (brand_id, workspace_id, ad_account_id, external_id, name, status, objective, daily_budget_micros) VALUES ($1, $2, $3, '8', 'Northpeak brand', 'active', 'search', 10000000) RETURNING id::text`, f.brand, f.ws, f.acct)
	shared := f.q(`INSERT INTO integrations (workspace_id, connector, name, credentials) VALUES ($1, 'meta_ads', 'Meta', '\x00') RETURNING id::text`, f.ws)
	f.sharedAcct = f.q(`INSERT INTO ad_accounts (workspace_id, integration_id, connector, external_id, name, currency) VALUES ($1, $2, 'meta_ads', 'act_1', 'Shared', 'USD') RETURNING id::text`, f.ws, shared)
	f.shared = f.q(`INSERT INTO ad_entities (workspace_id, ad_account_id, external_id, name, status, daily_budget_micros, target_cost_per_conversion_micros) VALUES ($1, $2, '9', 'Everyone', 'active', 30000000, 10000000) RETURNING id::text`, f.ws, f.sharedAcct)
	f.run = &Runner{Pool: f.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return f
}

// days writes a campaign's numbers for days ago from..to (1 is yesterday).
func (f *fixture) days(id string, from, to int, spend int64, conv float64) {
	f.t.Helper()
	f.exec(`INSERT INTO ad_metrics_daily (ad_entity_id, day, workspace_id, currency, spend_micros, clicks, impressions, conversions)
		SELECT $1, current_date - d, $2, 'USD', $3, 20, 400, $4 FROM generate_series($5::int, $6::int) d
		ON CONFLICT (ad_entity_id, day) DO UPDATE SET spend_micros = EXCLUDED.spend_micros, conversions = EXCLUDED.conversions`, id, f.ws, spend, conv, to, from)
}

// payers gives a campaign n new paying customers a day for days ago
// from..to, each clicking the day before they paid.
func (f *fixture) payers(id string, from, to, n int) {
	f.t.Helper()
	for d := to; d <= from; d++ {
		for i := 0; i < n; i++ {
			rec := f.q(`INSERT INTO records (brand_id, workspace_id, record_type, name) VALUES ($1, $2, 'person', 'Payer') RETURNING id::text`, f.brand, f.ws)
			f.exec(`INSERT INTO ad_acquisitions (workspace_id, brand_id, record_id, connector, ad_account_id, campaign_entity_id, clicked_at, window_days, signed_up_at, first_paid_at, currency)
				VALUES ($1, $2, $3, 'google_ads', $4, $5, now() - make_interval(days => $6::int + 1), 30, now() - make_interval(days => $6::int + 1), now() - make_interval(days => $6::int), 'USD')`,
				f.ws, f.brand, rec, f.acct, id, d)
		}
	}
}

func (f *fixture) econ(target int64, shadow bool) {
	f.t.Helper()
	e := paidads.Defaults(f.ws, f.brand, "USD")
	e.TargetCostPerPayerMicros, e.ShadowMode = &target, shadow
	if _, err := paidads.Save(context.Background(), f.pool, e, nil); err != nil {
		f.t.Fatal(err)
	}
}

type row struct {
	id, kind, status, autonomy, unit, rule string
	shadow, appliedAuto                    bool
	impact                                 *int64
	changeID                               *string
}

func (f *fixture) rows(campaign string) map[string]row {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT id::text, kind, status, autonomy, unit, rule, shadow, applied_auto, impact_micros, change_id::text
		FROM ad_suggestions WHERE ad_entity_id = $1 AND status NOT IN ('expired', 'dismissed') ORDER BY created_at`, campaign)
	if err != nil {
		f.t.Fatal(err)
	}
	out := map[string]row{}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(&x.id, &x.kind, &x.status, &x.autonomy, &x.unit, &x.rule, &x.shadow, &x.appliedAuto, &x.impact, &x.changeID)
		return x, err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	for _, x := range list {
		out[x.kind] = x
	}
	return out
}

// The run on a brand judged in payers: shadow mode records what it would
// do, a person's queue gets it once shadow is off, savings apply on their
// own once the brand lets them, one lever per campaign per cycle, winners
// are protected, a broken-tracking alert stops every change, and a brand
// without a target is judged on the network's conversions.
func TestRunOnPayers(t *testing.T) {
	got := track.Capture(t)
	f := newFixture(t)
	ctx := context.Background()
	// Spring: 30 a day and a payer a day: 30 a payer against a target of
	// 10. The brand campaign: 10 a day, three payers a day: the winner.
	f.days(f.spring, 40, 1, 30_000_000, 2)
	f.payers(f.spring, 40, 1, 1)
	f.days(f.brandCamp, 40, 1, 10_000_000, 4)
	f.payers(f.brandCamp, 40, 1, 3)
	// The shared account, no brand: a good cost per network conversion
	// with the budget run out every day.
	f.days(f.shared, 40, 1, 30_000_000, 5)
	f.econ(10_000_000, true)

	res, err := f.run.Run(ctx, f.ws)
	if err != nil {
		t.Fatal(err)
	}
	if res.Shadowed == 0 || res.Applied != 0 {
		t.Fatalf("shadow run: %+v", res)
	}
	r := f.rows(f.spring)
	cut := r[BudgetDown]
	if cut.status != "shadow" || !cut.shadow || cut.unit != UnitPayers || cut.autonomy != "approve" || cut.rule != RuleBudgetCut || cut.impact == nil || *cut.impact >= 0 {
		t.Fatalf("shadow cut: %+v (%v)", cut, r)
	}
	if _, ok := f.rows(f.brandCamp)[BudgetDown]; ok {
		t.Fatal("the winner was cut")
	}
	// The shared account's campaign has no brand: an open raise on the
	// network's conversions.
	sr := f.rows(f.shared)[BudgetUp]
	if sr.status != "open" || sr.unit != UnitPlatform || sr.autonomy != "approve" {
		t.Fatalf("shared raise: %+v", sr)
	}

	// Shadow off, no extension's hook: the cut waits for a person and
	// nothing is applied; the shadow row is gone.
	f.econ(10_000_000, false)
	if res, err = f.run.Run(ctx, f.ws); err != nil || res.Suggested == 0 || res.Applied != 0 {
		t.Fatalf("live run: %+v %v", res, err)
	}
	r = f.rows(f.spring)
	if cut = r[BudgetDown]; cut.status != "open" || cut.shadow || cut.autonomy != "approve" {
		t.Fatalf("open cut: %+v", cut)
	}
	var shadows int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM ad_suggestions WHERE ad_entity_id = $1 AND status = 'shadow'`, f.spring).Scan(&shadows); err != nil || shadows != 0 {
		t.Fatalf("shadow rows left: %d %v", shadows, err)
	}

	if n := f.q(`SELECT count(*)::text FROM ad_changes WHERE workspace_id = $1`, f.ws); n != "0" {
		t.Fatalf("no hook, %s changes", n)
	}

	// Savings on their own where an extension's hook allows them: the cut
	// is made at once as a change, queued for the ad sync's job, and
	// measured. The hook is never asked about a change that spends more.
	var asked []string
	paidads.SetAutoApprove(func(_ context.Context, w, b, kind string) bool {
		asked = append(asked, kind)
		return w == f.ws && b == f.brand && paidads.Saves(kind)
	})
	t.Cleanup(func() { paidads.SetAutoApprove(nil) })
	f.exec(`DELETE FROM ad_suggestions WHERE workspace_id = $1`, f.ws)
	if res, err = f.run.Run(ctx, f.ws); err != nil || res.Applied != 1 {
		t.Fatalf("auto run: %+v %v", res, err)
	}
	if len(asked) == 0 {
		t.Fatal("the hook wasn't asked")
	}
	for _, k := range asked {
		if !paidads.Saves(k) {
			t.Fatalf("the hook was asked about %s: %v", k, asked)
		}
	}
	cut = f.rows(f.spring)[BudgetDown]
	if cut.status != "measuring" || !cut.appliedAuto || cut.autonomy != "auto" || cut.changeID == nil {
		t.Fatalf("applied cut: %+v", cut)
	}
	var kind, status string
	var after []byte
	var jobsQueued int
	if err := f.pool.QueryRow(ctx, `SELECT kind, status, after FROM ad_changes WHERE id = $1`, *cut.changeID).Scan(&kind, &status, &after); err != nil || kind != ChangeBudget || status != "queued" || string(after) != `{"daily_budget_micros": 24000000}` {
		t.Fatalf("the change: %s %s %s %v", kind, status, after, err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'ads.change' AND idempotency_key = $1`, "ads.change:"+*cut.changeID).Scan(&jobsQueued); err != nil || jobsQueued != 1 {
		t.Fatalf("the change job: %d %v", jobsQueued, err)
	}
	var from, until *time.Time
	if err := f.pool.QueryRow(ctx, `SELECT measure_from, measure_until FROM ad_suggestions WHERE id = $1`, cut.id).Scan(&from, &until); err != nil || from == nil || until == nil || until.Sub(*from) < 14*24*time.Hour {
		t.Fatalf("window: %v %v %v", from, until, err)
	}
	// One lever per campaign per cycle: nothing more while it measures.
	if res, err = f.run.Run(ctx, f.ws); err != nil || res.Applied != 0 || res.Suggested != 0 {
		t.Fatalf("busy run: %+v %v", res, err)
	}

	// Tracking broken: no change-class finding on the brand at all.
	f.exec(`DELETE FROM ad_suggestions WHERE workspace_id = $1`, f.ws)
	f.exec(`DELETE FROM ad_changes WHERE workspace_id = $1`, f.ws)
	f.exec(`INSERT INTO ad_suggestions (workspace_id, brand_id, ad_entity_id, kind, autonomy, rule, reason) VALUES ($1, $2, $3, 'alert.tracking_broken', 'alert', 'alert.tracking_broken', 'x')`, f.ws, f.brand, f.brandCamp)
	if res, err = f.run.Run(ctx, f.ws); err != nil || res.Applied != 0 {
		t.Fatalf("blocked run: %+v %v", res, err)
	}
	if _, ok := f.rows(f.spring)[BudgetDown]; ok {
		t.Fatal("a change was found while tracking is broken")
	}
	if sr := f.rows(f.shared)[BudgetUp]; sr.status != "open" {
		t.Fatalf("another brand's campaign still gets its finding: %+v", sr)
	}

	names := map[string]int{}
	autoEvents := 0
	for _, ev := range got() {
		names[ev.Name]++
		if ev.Name == "ad_campaign.budget_changed" && ev.Props["auto"] == true {
			autoEvents++
		}
		if ev.Name == "ad_suggestion.created" && ev.Props["unit"] == nil {
			t.Fatalf("created without a unit: %+v", ev)
		}
	}
	for _, n := range []string{"ad_suggestion.created", "ad_suggestion.applied", "ad_suggestions.refreshed", "ad_campaign.budget_changed"} {
		if names[n] == 0 {
			t.Fatalf("no %s event: %v", n, names)
		}
	}
	if autoEvents != 1 {
		t.Fatalf("auto changes: %d", autoEvents)
	}
}

// Verdicts in payers: a cut measured over its window against as many
// days before, judged worked when a payer got cheaper, with the brand
// finding written; a raise over twice the target three days running is
// judged early; Performance Max gets no keyword findings and a window of
// days 15 to 42.
func TestJudgeOnPayers(t *testing.T) {
	got := track.Capture(t)
	f := newFixture(t)
	ctx := context.Background()
	f.econ(10_000_000, false)
	// Before the change (days 37 to 21 ago): 30 a day, a payer a day.
	// After (days 17 to 1): 24 a day, a payer a day: cheaper.
	f.days(f.spring, 37, 21, 30_000_000, 1)
	f.payers(f.spring, 37, 21, 1)
	f.days(f.spring, 17, 1, 24_000_000, 1)
	f.payers(f.spring, 17, 1, 1)
	changeID := f.q(`INSERT INTO ad_changes (workspace_id, brand_id, ad_entity_id, kind, before, after, status, created_at, applied_at)
		VALUES ($1, $2, $3, 'budget', '{"daily_budget_micros": 30000000}', '{"daily_budget_micros": 24000000}', 'applied', now() - interval '20 days', now() - interval '20 days') RETURNING id::text`, f.ws, f.brand, f.spring)
	sid := f.q(`INSERT INTO ad_suggestions (workspace_id, brand_id, ad_entity_id, kind, status, autonomy, unit, rule, reason, change_id, applied_auto, measure_from, measure_until)
		VALUES ($1, $2, $3, 'budget_down', 'measuring', 'auto', 'payers', 'budget_cut', 'Cut?', $4, true, current_date - 17, current_date - 3) RETURNING id::text`, f.ws, f.brand, f.spring, changeID)
	res, err := f.run.Run(ctx, f.ws)
	if err != nil || res.Judged != 1 {
		t.Fatalf("judge: %+v %v", res, err)
	}
	var verdict, note string
	var result []byte
	if err := f.pool.QueryRow(ctx, `SELECT verdict, verdict_note, result FROM ad_suggestions WHERE id = $1`, sid).Scan(&verdict, &note, &result); err != nil || verdict != Worked || note == "" {
		t.Fatalf("verdict: %s %q %s %v", verdict, note, result, err)
	}
	var text, confidence, scope string
	var review time.Time
	if err := f.pool.QueryRow(ctx, `SELECT text, confidence, scope::text, review_at FROM brand_findings WHERE brand_id = $1`, f.brand).Scan(&text, &confidence, &scope, &review); err != nil {
		t.Fatalf("brand finding: %v", err)
	}
	if confidence != "medium" || scope != `{"channel": "paid_ads"}` || review.Before(time.Now().Add(89*24*time.Hour)) || len(text) == 0 {
		t.Fatalf("brand finding: %q %s %s %v", text, confidence, scope, review)
	}

	// Early stop: a raise whose campaign costs over twice the target
	// three days running since the change.
	f.exec(`DELETE FROM ad_metrics_daily WHERE ad_entity_id = $1`, f.brandCamp)
	f.days(f.brandCamp, 10, 1, 50_000_000, 1)
	f.payers(f.brandCamp, 10, 1, 1) // 50 a payer: five times the target
	raiseChange := f.q(`INSERT INTO ad_changes (workspace_id, brand_id, ad_entity_id, kind, before, after, status, created_at, applied_at)
		VALUES ($1, $2, $3, 'budget', '{"daily_budget_micros": 10000000}', '{"daily_budget_micros": 12000000}', 'applied', now() - interval '6 days', now() - interval '6 days') RETURNING id::text`, f.ws, f.brand, f.brandCamp)
	rid := f.q(`INSERT INTO ad_suggestions (workspace_id, brand_id, ad_entity_id, kind, status, autonomy, unit, rule, reason, change_id, measure_from, measure_until)
		VALUES ($1, $2, $3, 'budget_up', 'measuring', 'approve', 'payers', 'budget_raise', 'Raise?', $4, current_date - 5, current_date + 20) RETURNING id::text`, f.ws, f.brand, f.brandCamp, raiseChange)
	if res, err = f.run.Run(ctx, f.ws); err != nil || res.Judged != 1 {
		t.Fatalf("early judge: %+v %v", res, err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT verdict, verdict_note FROM ad_suggestions WHERE id = $1`, rid).Scan(&verdict, &note); err != nil || verdict != DidNotWork || note[:13] != "Stopped early" {
		t.Fatalf("early verdict: %s %q %v", verdict, note, err)
	}

	// Performance Max: keyword findings never, and a window of days 15 to 42.
	pmax := f.q(`INSERT INTO ad_entities (brand_id, workspace_id, ad_account_id, external_id, name, status, objective, daily_budget_micros) VALUES ($1, $2, $3, '10', 'PMax', 'active', 'performance_max', 30000000) RETURNING id::text`, f.brand, f.ws, f.acct)
	group := f.q(`INSERT INTO ad_entities (brand_id, workspace_id, ad_account_id, level, parent_id, external_id, name, status) VALUES ($1, $2, $3, 'ad_group', $4, '101', 'Assets', 'active') RETURNING id::text`, f.brand, f.ws, f.acct, pmax)
	f.days(pmax, 30, 1, 20_000_000, 1)
	f.payers(pmax, 30, 1, 1)
	f.exec(`INSERT INTO ad_search_terms_daily (workspace_id, ad_account_id, ad_group_id, day, term, currency, spend_micros, clicks, conversions, brand_id)
		VALUES ($1, $2, $3, current_date - 3, 'free crm download', 'USD', 90000000, 60, 0, outflare_first_brand($1::uuid))`, f.ws, f.acct, group)
	if _, err = f.run.Run(ctx, f.ws); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.rows(pmax)[NegativeAdd]; ok {
		t.Fatal("a negative keyword on Performance Max")
	}
	pid := f.q(`INSERT INTO ad_suggestions (workspace_id, brand_id, ad_entity_id, kind, status, autonomy, unit, rule, reason) VALUES ($1, $2, $3, 'budget_down', 'open', 'approve', 'payers', 'budget_cut', 'x') RETURNING id::text`, f.ws, f.brand, pmax)
	if err := pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error { return Started(ctx, tx, pid, BudgetDown, 0, time.Now().UTC()) }); err != nil {
		t.Fatal(err)
	}
	var mf, mu time.Time
	if err := f.pool.QueryRow(ctx, `SELECT measure_from, measure_until FROM ad_suggestions WHERE id = $1`, pid).Scan(&mf, &mu); err != nil || int(mu.Sub(mf).Hours()/24) != 27 || int(time.Until(mf).Hours()/24) < 13 {
		t.Fatalf("pmax window: %v to %v %v", mf, mu, err)
	}

	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
	}
	if names["ad_suggestion.judged"] != 2 || names["finding.recorded"] != 2 {
		t.Fatalf("events: %v", names)
	}
}

// The hourly key: a second refresh within the hour queues nothing and
// says when the next may be.
func TestEnqueueOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 9, 10, 20, 0, 0, time.UTC)
	queued, next, err := EnqueueOnce(ctx, f.pool, f.ws, now)
	if err != nil || !queued || !next.Equal(time.Date(2026, 11, 9, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("first: %v %v %v", queued, next, err)
	}
	if queued, next, err = EnqueueOnce(ctx, f.pool, f.ws, now.Add(10*time.Minute)); err != nil || queued || next.Hour() != 11 {
		t.Fatalf("second: %v %v %v", queued, next, err)
	}
	if queued, _, err = EnqueueOnce(ctx, f.pool, f.ws, now.Add(time.Hour)); err != nil || !queued {
		t.Fatalf("next hour: %v %v", queued, err)
	}
}
