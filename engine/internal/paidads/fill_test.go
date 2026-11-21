package paidads

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

// A click that landed with only a gclid is written with its campaign
// unknown; once the harvest has the click (and its watermark moved), the
// next run fills in the acquisition and the touch of its payments and
// refund.
func TestLedgerFillsCampaignOnceClickHarvested(t *testing.T) {
	got := track.Capture(t)
	f := newFixture(t)
	ctx := context.Background()
	day := 24 * time.Hour
	eve := f.person("Eve")
	f.q(`INSERT INTO site_sessions (brand_id, workspace_id, record_id, anonymous_id, started_at, utm_source, utm_medium, channel, click_ids)
		VALUES ($1, $2, $3, 'anon-eve', $4, 'google', 'cpc', 'paid_ads', '{"gclid": "Cj0late"}') RETURNING id::text`, f.brand, f.ws, eve, f.now.Add(-3*day))
	f.event(eve, nil, "signed_up", f.now.Add(-3*day+time.Hour), `{}`)
	f.event(eve, nil, "payment.succeeded", f.now.Add(-2*day), `{"amount_minor": 5000, "currency": "USD", "payment_id": "eve_1"}`)
	f.event(eve, nil, "payment.succeeded", f.now.Add(-1*day), `{"amount_minor": 3000, "currency": "USD", "payment_id": "eve_2"}`)
	f.event(eve, nil, "payment.refunded", f.now.Add(-2*time.Hour), `{"amount_minor": 1000, "currency": "USD", "payment_id": "eve_2"}`)

	l := f.ledger()
	res, err := l.RunWorkspace(ctx, f.ws)
	if err != nil {
		t.Fatal(err)
	}
	if res.Acquisitions != 1 || res.Revenue != 3 || res.Filled != 0 {
		t.Fatalf("first run: %+v", res)
	}
	acq := func() (campaign, group, ad *string, matched, keyword, ext string) {
		t.Helper()
		if err := f.pool.QueryRow(ctx, `SELECT campaign_entity_id::text, ad_group_entity_id::text, ad_entity_id::text, matched_by, keyword, campaign_external_id
			FROM ad_acquisitions WHERE brand_id = $1 AND record_id = $2`, f.brand, eve).Scan(&campaign, &group, &ad, &matched, &keyword, &ext); err != nil {
			t.Fatal(err)
		}
		return
	}
	touches := func() []*string {
		t.Helper()
		rows, err := f.pool.Query(ctx, `SELECT touch_campaign_entity_id::text FROM ad_revenue WHERE brand_id = $1 AND record_id = $2 ORDER BY occurred_at`, f.brand, eve)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []*string
		for rows.Next() {
			var s *string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}
	if c, _, _, matched, _, _ := acq(); c != nil || matched != "utm" {
		t.Fatalf("before the harvest: %v %s", c, matched)
	}
	for _, x := range touches() {
		if x != nil {
			t.Fatalf("a touch before the harvest: %v", *x)
		}
	}

	// Google reports the click. Until the harvest's watermark moves, the
	// pass doesn't look again.
	f.exec(`INSERT INTO ad_clicks (workspace_id, brand_id, ad_account_id, click_id, day, campaign_external_id, ad_group_external_id, ad_external_id, keyword)
		VALUES ($1, $2, $3, 'Cj0late', current_date - 3, '111', '55', '9', 'trail shoes')`, f.ws, f.brand, f.acct)
	if res, err := l.RunWorkspace(ctx, f.ws); err != nil || res.Filled != 0 {
		t.Fatalf("a run with the same watermark: %+v %v", res, err)
	}
	f.exec(`INSERT INTO ad_syncs (integration_id, workspace_id, last_clicks_day) SELECT integration_id, workspace_id, current_date - 1 FROM ad_accounts WHERE id = $1`, f.acct)
	res, err = l.RunWorkspace(ctx, f.ws)
	if err != nil {
		t.Fatal(err)
	}
	if res.Events != 0 || res.Filled != 4 {
		t.Fatalf("fill run: %+v", res)
	}
	c, g, a, matched, keyword, ext := acq()
	if c == nil || *c != f.spring || g == nil || *g != f.group || a == nil || *a != f.ad || matched != "click_id" || keyword != "trail shoes" || ext != "111" {
		t.Fatalf("filled acquisition: %v %v %v %s %q %q", c, g, a, matched, keyword, ext)
	}
	for i, x := range touches() {
		if x == nil || *x != f.spring {
			t.Fatalf("touch %d: %v", i, x)
		}
	}
	// Nothing left to fill, and nothing changes.
	f.exec(`UPDATE ad_syncs SET last_clicks_day = current_date WHERE workspace_id = $1`, f.ws)
	if res, err := l.RunWorkspace(ctx, f.ws); err != nil || res.Filled != 0 {
		t.Fatalf("a run after the fill: %+v %v", res, err)
	}
	filled := 0
	for _, ev := range got() {
		if ev.Name == "paidads.campaigns_filled" {
			filled++
			if ev.Props["acquisitions"] != 1 || ev.Props["revenue"] != 3 {
				t.Fatalf("event: %+v", ev.Props)
			}
		}
	}
	if filled != 1 {
		t.Fatalf("campaigns_filled events: %d", filled)
	}

	// The report now credits Spring search: the first payment is new
	// revenue, the second less its refund is returning revenue by click.
	rows, err := Performance(ctx, f.pool, PerformanceQuery{WorkspaceID: f.ws, BrandID: f.brand, Level: LevelCampaign, From: f.now.Add(-7 * day), To: f.now, Econ: Defaults(f.ws, f.brand, "USD")})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == f.spring && (r.Signups != 1 || r.NewPayers != 1 || len(r.NewRevenue) != 1 || r.NewRevenue[0].AmountMinor != 5000 ||
			len(r.ReturningRevenue) != 1 || r.ReturningRevenue[0].AmountMinor != 2000 || r.ROAS != nil) {
			t.Fatalf("spring: %+v", r)
		}
	}
}

// utm_campaign that is all digits is the campaign's id; any other is its
// name.
func TestResolveUTMCampaignByID(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	session := func(record, utm string) {
		f.q(`INSERT INTO site_sessions (brand_id, workspace_id, record_id, anonymous_id, started_at, utm_source, utm_medium, utm_campaign, channel)
			VALUES ($1, $2, $3, gen_random_uuid()::text, $4, 'google', 'cpc', $5, 'paid_ads') RETURNING id::text`, f.brand, f.ws, record, f.now.Add(-2*time.Hour), utm)
	}
	fay, gus, hal := f.person("Fay"), f.person("Gus"), f.person("Hal")
	session(fay, "222")
	session(gus, "spring SEARCH")
	session(hal, "999")
	for _, p := range []string{fay, gus, hal} {
		f.event(p, nil, "signed_up", f.now.Add(-time.Hour), `{}`)
	}
	if _, err := f.ledger().RunWorkspace(ctx, f.ws); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		record, campaign, ext string
	}{{fay, f.brandCamp, "222"}, {gus, f.spring, ""}, {hal, "", ""}} {
		var campaign *string
		var matched, ext string
		if err := f.pool.QueryRow(ctx, `SELECT campaign_entity_id::text, matched_by, campaign_external_id FROM ad_acquisitions WHERE brand_id = $1 AND record_id = $2`, f.brand, c.record).Scan(&campaign, &matched, &ext); err != nil {
			t.Fatal(err)
		}
		if (c.campaign == "") != (campaign == nil) || campaign != nil && *campaign != c.campaign || matched != "utm" || ext != c.ext {
			t.Fatalf("%s: %v %s %q", c.campaign, campaign, matched, ext)
		}
	}
}

func TestROAS(t *testing.T) {
	usd := func(v int64) Money { return Money{"USD", v} }
	eur := Money{"EUR", 500}
	near := func(p *float64, want float64) bool { return p != nil && math.Abs(*p-want) < 1e-9 }
	// $50 spent, $29 new and $49 returning: 1.56.
	if v := roas(50_000_000, "USD", []Money{usd(2900)}, []Money{usd(4900), eur}); !near(v, 1.56) {
		t.Fatalf("roas: %v", v)
	}
	if v := roas(0, "USD", []Money{usd(2900)}); v != nil {
		t.Fatalf("no spend: %v", *v)
	}
	if v := roas(10_000_000, "USD", []Money{eur}); v != nil {
		t.Fatalf("only another currency: %v", *v)
	}
	if v := roas(10_000_000, "USD", nil, nil); !near(v, 0) {
		t.Fatalf("no revenue: %v", v)
	}
}
