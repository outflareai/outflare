package paidads

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/db"
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

// fixture is a workspace with one brand, a Google Ads account with two
// campaigns (one with an ad group and an ad) and a person.
type fixture struct {
	t                                  *testing.T
	pool                               *pgxpool.Pool
	ws, brand, acct, spring, brandCamp string
	group, ad                          string
	now                                time.Time
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
	f := &fixture{t: t, pool: testPool(t), now: time.Now().UTC().Truncate(time.Second)}
	f.ws = f.q(`INSERT INTO workspaces (name, slug) VALUES ('Paid ads', 'pa-' || substr(gen_random_uuid()::text, 1, 8)) RETURNING id::text`)
	f.brand = f.q(`INSERT INTO brands (workspace_id, name, handle, website, currency) VALUES ($1, 'Northpeak', 'northpeak', 'https://northpeak.example', 'USD') RETURNING id::text`, f.ws)
	integ := f.q(`INSERT INTO integrations (brand_id, workspace_id, connector, name, credentials) VALUES ($1, $2, 'google_ads', 'Google Ads', '\x00') RETURNING id::text`, f.brand, f.ws)
	f.acct = f.q(`INSERT INTO ad_accounts (brand_id, workspace_id, integration_id, connector, external_id, name, currency) VALUES ($1, $2, $3, 'google_ads', '1234567890', 'Northpeak', 'USD') RETURNING id::text`, f.brand, f.ws, integ)
	f.spring = f.q(`INSERT INTO ad_entities (brand_id, workspace_id, ad_account_id, external_id, name, status) VALUES ($1, $2, $3, '111', 'Spring search', 'active') RETURNING id::text`, f.brand, f.ws, f.acct)
	f.brandCamp = f.q(`INSERT INTO ad_entities (brand_id, workspace_id, ad_account_id, external_id, name, status) VALUES ($1, $2, $3, '222', 'Brand search', 'active') RETURNING id::text`, f.brand, f.ws, f.acct)
	f.group = f.q(`INSERT INTO ad_entities (brand_id, workspace_id, ad_account_id, level, parent_id, external_id, name, status) VALUES ($1, $2, $3, 'ad_group', $4, '55', 'Intent', 'active') RETURNING id::text`, f.brand, f.ws, f.acct, f.spring)
	f.ad = f.q(`INSERT INTO ad_entities (brand_id, workspace_id, ad_account_id, level, parent_id, external_id, name, status) VALUES ($1, $2, $3, 'ad', $4, '9', 'RSA one', 'active') RETURNING id::text`, f.brand, f.ws, f.acct, f.group)
	return f
}

func (f *fixture) person(name string) string {
	return f.q(`INSERT INTO records (brand_id, workspace_id, record_type, name) VALUES ($1, $2, 'person', $3) RETURNING id::text`, f.brand, f.ws, name)
}

// visit is a paid visit at at: Google's gclid plus the template's ids.
func (f *fixture) visit(record *string, anon string, at time.Time, campaign, adset, ad string) string {
	return f.q(`INSERT INTO site_sessions (brand_id, workspace_id, record_id, anonymous_id, started_at, utm_source, utm_medium, utm_term, channel, click_ids, ad)
		VALUES ($1, $2, $3, $4, $5, 'google', 'cpc', 'hiking boots', 'paid_ads', '{"gclid": "Cj0test"}', jsonb_strip_nulls(jsonb_build_object('campaign', NULLIF($6, ''), 'adset', NULLIF($7, ''), 'ad', NULLIF($8, '')))) RETURNING id::text`,
		f.brand, f.ws, record, anon, at, campaign, adset, ad)
}

func (f *fixture) event(record string, anon *string, name string, at time.Time, props string) string {
	return f.q(`INSERT INTO customer_events (brand_id, workspace_id, record_id, anonymous_id, kind, name, properties, occurred_at, message_id)
		VALUES ($1, $2, $3, $4, 'track', $5, $6::jsonb, $7, gen_random_uuid()::text) RETURNING id::text`, f.brand, f.ws, record, anon, name, props, at)
}

func (f *fixture) ledger() *Ledger {
	return &Ledger{Pool: f.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Settle: time.Millisecond}
}

func TestLedgerSignupPaymentRepeatRefund(t *testing.T) {
	got := track.Capture(t)
	f := newFixture(t)
	ctx := context.Background()
	ana := f.person("Ana")
	day := 24 * time.Hour
	// Two paid clicks: Brand search 20 days before the sign-up, Spring
	// search (ad group 55, ad 9) 3 days before. The last one counts.
	f.visit(&ana, "anon-ana", f.now.Add(-20*day), "222", "", "")
	f.visit(&ana, "anon-ana", f.now.Add(-3*day), "111", "55", "9")
	f.event(ana, nil, "signed_up", f.now.Add(-2*day), `{}`)
	f.event(ana, nil, "payment.succeeded", f.now.Add(-1*day), `{"amount_minor": 4900, "currency": "usd", "payment_id": "pay_1"}`)
	f.event(ana, nil, "payment.succeeded", f.now.Add(-2*time.Hour), `{"amount_minor": 4900, "currency": "usd", "payment_id": "pay_2"}`)
	f.event(ana, nil, "payment.refunded", f.now.Add(-time.Hour), `{"amount_minor": 2000, "currency": "usd", "payment_id": "pay_1"}`)
	// Consent: Ana said yes to ad data.
	f.exec(`INSERT INTO consents (workspace_id, record_id, channel, purpose, status) VALUES ($1, $2, 'ads', 'ad_user_data', 'granted')`, f.ws, ana)

	res, err := f.ledger().RunWorkspace(ctx, f.ws)
	if err != nil {
		t.Fatal(err)
	}
	if res.Events != 4 || res.Acquisitions != 1 || res.Revenue != 3 {
		t.Fatalf("result: %+v", res)
	}
	var campaign, group, ad, matched, kind string
	var signedUp, firstPaid *time.Time
	var firstMinor *int64
	var consent bool
	if err := f.pool.QueryRow(ctx, `SELECT campaign_entity_id::text, ad_group_entity_id::text, ad_entity_id::text, matched_by, click_id_kind, signed_up_at, first_paid_at, first_payment_minor, ad_consent
		FROM ad_acquisitions WHERE brand_id = $1 AND record_id = $2`, f.brand, ana).Scan(&campaign, &group, &ad, &matched, &kind, &signedUp, &firstPaid, &firstMinor, &consent); err != nil {
		t.Fatal(err)
	}
	if campaign != f.spring || group != f.group || ad != f.ad || matched != "template" || kind != "gclid" || signedUp == nil || firstPaid == nil || firstMinor == nil || *firstMinor != 4900 || !consent {
		t.Fatalf("acquisition: %s %s %s %s %s %v %v %v %v", campaign, group, ad, matched, kind, signedUp, firstPaid, firstMinor, consent)
	}
	rows, err := f.pool.Query(ctx, `SELECT bucket, value_minor, acquisition_id IS NOT NULL, touch_campaign_entity_id::text FROM ad_revenue WHERE brand_id = $1 ORDER BY occurred_at`, f.brand)
	if err != nil {
		t.Fatal(err)
	}
	type rev struct {
		bucket string
		value  int64
		acq    bool
		touch  *string
	}
	var revs []rev
	for rows.Next() {
		var r rev
		if err := rows.Scan(&r.bucket, &r.value, &r.acq, &r.touch); err != nil {
			t.Fatal(err)
		}
		revs = append(revs, r)
	}
	rows.Close()
	if len(revs) != 3 || revs[0].bucket != "new" || revs[0].value != 4900 || !revs[0].acq || revs[0].touch == nil || *revs[0].touch != f.spring ||
		revs[1].bucket != "returning" || revs[1].value != 4900 || !revs[1].acq ||
		revs[2].bucket != "new" || revs[2].value != -2000 || !revs[2].acq {
		t.Fatalf("revenue: %+v", revs)
	}
	// A second run reads nothing new and changes nothing.
	if res, err := f.ledger().RunWorkspace(ctx, f.ws); err != nil || res.Events != 0 {
		t.Fatalf("second run: %+v %v", res, err)
	}
	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
	}
	if names["ad_acquisition.recorded"] != 1 || names["ad_revenue.recorded"] != 3 {
		t.Fatalf("events: %v", names)
	}

	// The reports.
	econ, err := Load(ctx, f.pool, f.ws, f.brand)
	if err != nil || econ.Set || econ.AcquisitionWindowDays != 30 || econ.Currency != "USD" {
		t.Fatalf("defaults: %+v %v", econ, err)
	}
	target := int64(60_000_000)
	econ.TargetCostPerPayerMicros = &target
	if econ, err = Save(ctx, f.pool, econ, nil); err != nil || !econ.Set || !econ.ShadowMode {
		t.Fatalf("save: %+v %v", econ, err)
	}
	f.exec(`INSERT INTO ad_metrics_daily (ad_entity_id, day, workspace_id, currency, spend_micros, clicks, conversions, brand_id) VALUES ($1, current_date - 1, $2, 'USD', 50000000, 40, 2, $3)`, f.spring, f.ws, f.brand)
	from, to := f.now.Add(-7*day), f.now
	rows2, err := Performance(ctx, f.pool, PerformanceQuery{WorkspaceID: f.ws, BrandID: f.brand, Level: LevelCampaign, From: from, To: to, Econ: econ})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]PerformanceRow{}
	for _, r := range rows2 {
		by[r.ID] = r
	}
	s := by[f.spring]
	if s.Signups != 1 || s.NewPayers != 1 || s.CostPerNewPayerMicros == nil || *s.CostPerNewPayerMicros != 50_000_000 || s.TargetStatus != "under" ||
		len(s.NewRevenue) != 1 || s.NewRevenue[0].AmountMinor != 2900 || len(s.ReturningRevenue) != 1 || s.ReturningRevenue[0].AmountMinor != 4900 ||
		len(s.LifetimeRevenue) != 1 || s.LifetimeRevenue[0].AmountMinor != 7800 || s.PaybackMonths == nil || s.Trend != "new" || s.PlatformConversions == nil {
		t.Fatalf("spring: %+v", s)
	}
	if b := by[f.brandCamp]; b.Signups != 0 || b.NewPayers != 0 || b.TargetStatus != "no_spend" {
		t.Fatalf("brand campaign: %+v", b)
	}
	groups, err := Performance(ctx, f.pool, PerformanceQuery{WorkspaceID: f.ws, BrandID: f.brand, Level: LevelAdGroup, From: from, To: to, CampaignID: f.spring, Econ: econ})
	if err != nil || len(groups) != 1 || groups[0].ID != f.group || groups[0].NewPayers != 1 || groups[0].CampaignID == nil || *groups[0].CampaignID != f.spring {
		t.Fatalf("groups: %+v %v", groups, err)
	}
	// A one-day window at read time: the click was a day before the
	// sign-up and two before the payment, so neither counts any more.
	narrow, err := Performance(ctx, f.pool, PerformanceQuery{WorkspaceID: f.ws, BrandID: f.brand, Level: LevelCampaign, From: from, To: to, WindowDays: 1, Econ: econ})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range narrow {
		if r.ID == f.spring && (r.Signups != 0 || r.NewPayers != 0 || r.WindowDays != 1) {
			t.Fatalf("narrow window: %+v", r)
		}
	}
	list, err := Acquisitions(ctx, f.pool, AcquisitionsQuery{WorkspaceID: f.ws, BrandID: f.brand, Customers: "returning"})
	if err != nil || len(list) != 1 || list[0].RecordName != "Ana" || list[0].Payments != 2 || list[0].Bucket != "payer" || list[0].Revenue[0].AmountMinor != 7800 || list[0].Campaign.Name != "Spring search" {
		t.Fatalf("acquisitions: %+v %v", list, err)
	}
	sig, err := Daily(ctx, f.pool, f.brand, []string{f.spring}, from, to)
	if err != nil {
		t.Fatal(err)
	}
	var totals Signal
	for _, s := range sig[f.spring] {
		totals.Signups += s.Signups
		totals.NewPayers += s.NewPayers
		totals.NewRevenue += s.NewRevenue
		totals.ReturningRevenue += s.ReturningRevenue
		totals.LifetimeRevenue += s.LifetimeRevenue
	}
	if totals.Signups != 1 || totals.NewPayers != 1 || totals.NewRevenue != 2900 || totals.ReturningRevenue != 4900 || totals.LifetimeRevenue != 7800 {
		t.Fatalf("signals: %+v", totals)
	}
	lag, err := ClickToPaymentLag(ctx, f.pool, f.brand)
	if err != nil || lag.Payers != 1 || lag.Median != 2*day || JudgementWindow(lag) != MinJudgementWindow {
		t.Fatalf("lag: %+v %v", lag, err)
	}
	if err := Shadow(ctx, f.pool, Finding{WorkspaceID: f.ws, BrandID: f.brand, EntityID: f.spring, Kind: "budget_down", Rule: "budget_cut"}); err != nil && err != ErrShadowNotReady {
		t.Fatalf("shadow: %v", err)
	}
}

func TestLedgerWindowAnonymousAndReturningByClick(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	day := 24 * time.Hour

	// Bo: the only paid click is 31 days before the sign-up, outside the
	// 30-day window, so no acquisition.
	bo := f.person("Bo")
	f.visit(&bo, "anon-bo", f.now.Add(-31*day), "111", "", "")
	f.event(bo, nil, "signed_up", f.now.Add(-2*time.Hour), `{}`)

	// Cy: clicked anonymously (the visit has no record yet), then signed up
	// with that anonymous id and paid twice, the second time after another
	// paid click on Brand search: that payment is returning revenue by
	// click for Brand search, and lifetime revenue for Spring search.
	cy := f.person("Cy")
	anon := "anon-cy"
	f.visit(nil, anon, f.now.Add(-5*day), "111", "", "")
	f.event(cy, &anon, "signed_up", f.now.Add(-4*day), `{}`)
	f.event(cy, &anon, "payment.succeeded", f.now.Add(-3*day), `{"amount_minor": 1000, "currency": "USD", "payment_id": "cy_1"}`)
	f.visit(&cy, anon, f.now.Add(-2*day), "222", "", "")
	f.event(cy, &anon, "payment.succeeded", f.now.Add(-1*day), `{"amount_minor": 3000, "currency": "USD", "payment_id": "cy_2"}`)

	// Di: paid without signing up first (a server-side purchase), with a
	// click found only through the visitor linked to her record.
	di := f.person("Di")
	f.visit(nil, "anon-di", f.now.Add(-6*day), "", "", "")
	f.exec(`INSERT INTO site_visitors (workspace_id, anonymous_id, record_id, brand_id, first_seen_at, last_seen_at) VALUES ($1, 'anon-di', $2, $3, now(), now())`, f.ws, di, f.brand)
	f.event(di, nil, "payment.succeeded", f.now.Add(-time.Hour), `{"amount_minor": 9900, "currency": "USD", "payment_id": "di_1"}`)

	res, err := f.ledger().RunWorkspace(ctx, f.ws)
	if err != nil {
		t.Fatal(err)
	}
	if res.Events != 5 || res.Acquisitions != 2 || res.Revenue != 3 {
		t.Fatalf("result: %+v", res)
	}
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM ad_acquisitions WHERE record_id = $1`, bo).Scan(&n); err != nil || n != 0 {
		t.Fatalf("Bo acquired: %d %v", n, err)
	}
	var campaign string
	var signedUp, firstPaid *time.Time
	if err := f.pool.QueryRow(ctx, `SELECT campaign_entity_id::text, signed_up_at, first_paid_at FROM ad_acquisitions WHERE record_id = $1`, cy).Scan(&campaign, &signedUp, &firstPaid); err != nil {
		t.Fatal(err)
	}
	if campaign != f.spring || signedUp == nil || firstPaid == nil {
		t.Fatalf("Cy: %s %v %v", campaign, signedUp, firstPaid)
	}
	var touch *string
	var bucket string
	if err := f.pool.QueryRow(ctx, `SELECT r.bucket, r.touch_campaign_entity_id::text FROM ad_revenue r JOIN customer_events e ON e.id = r.event_id WHERE r.brand_id = $1 AND e.properties->>'payment_id' = 'cy_2'`, f.brand).Scan(&bucket, &touch); err != nil {
		t.Fatal(err)
	}
	if bucket != "returning" || touch == nil || *touch != f.brandCamp {
		t.Fatalf("Cy's second payment: %s %v", bucket, touch)
	}
	var matched string
	var diCampaign *string
	if err := f.pool.QueryRow(ctx, `SELECT matched_by, campaign_entity_id::text FROM ad_acquisitions WHERE record_id = $1 AND signed_up_at IS NULL AND first_paid_at IS NOT NULL`, di).Scan(&matched, &diCampaign); err != nil {
		t.Fatalf("Di: %v", err)
	}
	if matched != "utm" || diCampaign != nil {
		t.Fatalf("Di's click: %s %v", matched, diCampaign)
	}
	rows, err := Performance(ctx, f.pool, PerformanceQuery{WorkspaceID: f.ws, BrandID: f.brand, Level: LevelCampaign, From: f.now.Add(-7 * day), To: f.now, Econ: Defaults(f.ws, f.brand, "USD")})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		switch r.ID {
		case f.spring:
			if r.NewPayers != 1 || len(r.LifetimeRevenue) != 1 || r.LifetimeRevenue[0].AmountMinor != 4000 || len(r.ReturningRevenue) != 0 || r.TargetStatus != "no_target" {
				t.Fatalf("spring: %+v", r)
			}
		case f.brandCamp:
			if len(r.ReturningRevenue) != 1 || r.ReturningRevenue[0].AmountMinor != 3000 || r.NewPayers != 0 {
				t.Fatalf("brand: %+v", r)
			}
		}
	}
}

func TestAutonomyAndLevers(t *testing.T) {
	g := Caps("google_ads")
	shadow := Defaults("ws", "b", "USD")
	live := shadow
	live.ShadowMode = false
	cases := []struct {
		econ    Economics
		caps    Capabilities
		kind    string
		allowed bool
		want    string
	}{
		{shadow, g, "budget_down", true, AutonomyShadow},
		{shadow, g, "alert.target_stale", false, AutonomyAlert},
		// Without the extension's hook every change waits for a person.
		{live, g, "budget_down", false, AutonomyApprove},
		{live, g, "overspend_pause", false, AutonomyApprove},
		{live, g, "budget_down", true, AutonomyAuto},
		{live, g, "negative_add", true, AutonomyAuto},
		{live, g, "overspend_pause", true, AutonomyAuto},
		{live, g, "budget_up", true, AutonomyApprove},
		{live, Caps("meta_ads"), "negative_add", true, AutonomyApprove},
		{live, g, "keyword_split", true, AutonomyApprove},
	}
	for _, c := range cases {
		if got := Autonomy(c.econ, c.caps, c.kind, c.allowed); got != c.want {
			t.Errorf("Autonomy(%v %v %s %s) = %s, want %s", c.econ.ShadowMode, c.allowed, c.caps.Network, c.kind, got, c.want)
		}
	}
	if !Saves("pause") || Saves("resume") || !Saves("exclusion_add") || Saves("alert.x") || !IsAlert("alert.auto_apply_on") {
		t.Fatal("savings and alerts")
	}
	if g.Allows("keyword_split") || !g.Allows("draft_campaign") || Caps("tiktok_ads").Allows("negative_add") || !Caps("tiktok_ads").Allows("pause") || Caps("reddit_ads").Allows("pause") {
		t.Fatal("capabilities")
	}
	e := live
	margin := 40
	e.GrossMarginPercent = &margin
	if be := e.BreakEvenCostPerPayerMicros(20_000_000); be == nil || *be != 96_000_000 {
		t.Fatalf("break-even: %v", be)
	}
	if pb := e.PaybackMonthsFor(48_000_000, 20_000_000); pb == nil || *pb != 6 {
		t.Fatalf("payback: %v", pb)
	}
	tgt := int64(100)
	e.TargetCostPerPayerMicros = &tgt
	if e.TargetStatus(0, 0) != "no_spend" || e.TargetStatus(500, 0) != "over" || e.TargetStatus(1000, 10) != "on_target" || e.TargetStatus(500, 10) != "under" || e.TargetStatus(2000, 10) != "over" {
		t.Fatal("target status")
	}
}

// AutoApprove is false without a hook, never asks the hook about a change
// that spends more or a brandless finding, and otherwise says what the
// hook says.
func TestAutoApprove(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { SetAutoApprove(nil) })
	SetAutoApprove(nil)
	if AutoApproving() || AutoApprove(ctx, "ws", "b", "budget_down") {
		t.Fatal("no hook allowed a saving")
	}
	var asked []string
	SetAutoApprove(func(_ context.Context, ws, brand, kind string) bool {
		asked = append(asked, kind)
		return ws == "ws" && brand == "b"
	})
	if !AutoApproving() {
		t.Fatal("the hook isn't installed")
	}
	for _, k := range []string{"budget_up", "resume", "keyword_add", "target_loosen", "bidding_target", "draft_campaign", "experiment_start", "alert.pacing", "consolidate", "nope"} {
		if AutoApprove(ctx, "ws", "b", k) {
			t.Errorf("%s allowed", k)
		}
	}
	if AutoApprove(ctx, "ws", "", "pause") {
		t.Error("a brandless finding allowed")
	}
	if len(asked) != 0 {
		t.Fatalf("the hook was asked about %v", asked)
	}
	if !AutoApprove(ctx, "ws", "b", "budget_down") || !AutoApprove(ctx, "ws", "b", "overspend_pause") || AutoApprove(ctx, "other", "b", "pause") {
		t.Fatal("the hook's answers")
	}
	if Approver(nil).Allows(ctx, "ws", "b", "pause") {
		t.Fatal("a nil approver allowed a saving")
	}
}
