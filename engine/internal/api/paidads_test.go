package api_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/paidads"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Paid ads in cost per new paying customer: the brand's economics, the
// ledger's performance and acquisitions reports, the set-up path and the
// status blockers.
func TestPaidAdsEconomicsPerformanceAndSetup(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	exec := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	brand := exec(`SELECT outflare_first_brand($1)::text`, ws)
	base := "/v1/brands/" + brand + "/ads"
	day := 24 * time.Hour
	now := time.Now().UTC().Truncate(time.Second)

	// Before any ad account: the economics are the defaults, the set-up
	// path has nothing done beyond the Google template step, and the
	// status has no ads blocker.
	econ := owner.must(200, "GET", base+"/economics", nil)
	if econ["set"] != false || econ["acquisition_window_days"] != 30.0 || econ["shadow_mode"] != true || econ["target_cost_per_payer_micros"] != nil {
		t.Fatalf("defaults: %v", econ)
	}
	owner.must(404, "GET", "/v1/brands/00000000-0000-4000-8000-000000000001/ads/economics", nil)
	blockers := func() map[string]map[string]any {
		t.Helper()
		out := map[string]map[string]any{}
		for _, b := range owner.must(200, "GET", "/v1/workspaces/"+ws+"/status", nil)["blockers"].([]any) {
			out[b.(map[string]any)["code"].(string)] = b.(map[string]any)
		}
		return out
	}
	if b := blockers(); b["ads_economics_missing"] != nil {
		t.Fatalf("no ads yet: %v", b)
	}

	// A Google Ads account on the brand with a campaign, an ad group and an
	// ad; a person who clicked the ad, signed up and paid twice.
	integ := exec(`INSERT INTO integrations (brand_id, workspace_id, connector, name, credentials, status) VALUES ($1, $2, 'google_ads', 'Google Ads', '\x00', 'connected') RETURNING id::text`, brand, ws)
	acct := exec(`INSERT INTO ad_accounts (brand_id, workspace_id, integration_id, connector, external_id, name, currency) VALUES ($1, $2, $3, 'google_ads', '1234567890', 'Northpeak', 'USD') RETURNING id::text`, brand, ws, integ)
	spring := exec(`INSERT INTO ad_entities (brand_id, workspace_id, ad_account_id, external_id, name, status) VALUES ($1, $2, $3, '111', 'Spring search', 'active') RETURNING id::text`, brand, ws, acct)
	group := exec(`INSERT INTO ad_entities (brand_id, workspace_id, ad_account_id, level, parent_id, external_id, name, status) VALUES ($1, $2, $3, 'ad_group', $4, '55', 'Intent', 'active') RETURNING id::text`, brand, ws, acct, spring)
	exec(`INSERT INTO ad_entities (brand_id, workspace_id, ad_account_id, level, parent_id, external_id, name, status, details) VALUES ($1, $2, $3, 'ad', $4, '9', 'RSA one', 'active', '{"templated": false}') RETURNING id::text`, brand, ws, acct, group)
	exec(`INSERT INTO ad_metrics_daily (ad_entity_id, day, workspace_id, currency, spend_micros, clicks, conversions, brand_id) VALUES ($1, current_date - 1, $2, 'USD', 50000000, 40, 2, $3) RETURNING 'x'`, spring, ws, brand)
	ana := exec(`INSERT INTO records (brand_id, workspace_id, record_type, name) VALUES ($1, $2, 'person', 'Ana') RETURNING id::text`, brand, ws)
	exec(`INSERT INTO site_sessions (brand_id, workspace_id, record_id, anonymous_id, started_at, utm_source, utm_medium, channel, click_ids, ad)
		VALUES ($1, $2, $3, 'anon-ana', $4, 'google', 'cpc', 'paid_ads', '{"gclid": "Cj0test"}', '{"campaign": "111", "adset": "55", "ad": "9"}') RETURNING id::text`, brand, ws, ana, now.Add(-3*day))
	exec(`INSERT INTO customer_events (brand_id, workspace_id, record_id, kind, name, occurred_at, message_id) VALUES ($1, $2, $3, 'track', 'signed_up', $4, gen_random_uuid()::text) RETURNING id::text`, brand, ws, ana, now.Add(-2*day))
	exec(`INSERT INTO customer_events (brand_id, workspace_id, record_id, kind, name, properties, occurred_at, message_id) VALUES ($1, $2, $3, 'track', 'payment.succeeded', '{"amount_minor": 4900, "currency": "USD", "payment_id": "p1"}', $4, gen_random_uuid()::text) RETURNING id::text`, brand, ws, ana, now.Add(-day))
	exec(`INSERT INTO customer_events (brand_id, workspace_id, record_id, kind, name, properties, occurred_at, message_id) VALUES ($1, $2, $3, 'track', 'payment.succeeded', '{"amount_minor": 4900, "currency": "USD", "payment_id": "p2"}', $4, gen_random_uuid()::text) RETURNING id::text`, brand, ws, ana, now.Add(-time.Hour))
	ledger := &paidads.Ledger{Pool: e.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Settle: time.Millisecond}
	if res, err := ledger.Tick(ctx); err != nil || res.Acquisitions != 1 || res.Revenue != 2 {
		t.Fatalf("ledger: %+v %v", res, err)
	}

	// An ad account and no target: the status says so.
	if b := blockers()["ads_economics_missing"]; b == nil || b["area"] != "ads" || b["fix"].(map[string]any)["tool"] != "set_ad_economics" {
		t.Fatalf("economics missing: %v", blockers())
	}

	// Setting the economics. A token with analytics.manage sets numbers but
	// never turns shadow mode off; a person does. There is no switch that
	// lets savings apply on their own: only an extension's hook can.
	tok := &client{t: t, h: e.h, bearer: owner.must(201, "POST", "/v1/workspaces/"+ws+"/api-tokens", map[string]any{"name": "Agent", "scopes": []string{"analytics.manage", "analytics.read", "customers.read"}})["token"].(string)}
	owner.must(400, "PUT", base+"/economics", map[string]any{"auto_apply_savings": true})
	code, body := tok.do("PUT", base+"/economics", map[string]any{"shadow_mode": false})
	if code != 403 || body["error"].(map[string]any)["code"] != "person_only" {
		t.Fatalf("agent turning shadow off: %d %v", code, body)
	}
	if out := tok.must(200, "PUT", base+"/economics", map[string]any{"acquisition_window_days": 45, "target_cost_per_payer_micros": 1_000_000}); out["acquisition_window_days"] != 45.0 || out["set"] != true {
		t.Fatalf("token sets numbers: %v", out)
	}
	owner.must(400, "PUT", base+"/economics", map[string]any{"acquisition_window_days": 0})
	owner.must(400, "PUT", base+"/economics", map[string]any{"currency": "dollars"})
	margin := 40
	out := owner.must(200, "PUT", base+"/economics", map[string]any{"target_cost_per_payer_micros": 60_000_000, "target_cost_per_signup_micros": 10_000_000, "gross_margin_percent": margin,
		"acquisition_window_days": 30, "shadow_mode": false})
	if _, ok := out["auto_apply_savings"]; ok || out["target_cost_per_payer_micros"] != 60_000_000.0 || out["shadow_mode"] != false || out["gross_margin_percent"] != 40.0 {
		t.Fatalf("set: %v", out)
	}
	if out := owner.must(200, "PUT", base+"/economics", map[string]any{"target_cost_per_signup_micros": nil}); out["target_cost_per_signup_micros"] != nil || out["target_cost_per_payer_micros"] != 60_000_000.0 {
		t.Fatalf("null clears a target: %v", out)
	}
	if b := blockers(); b["ads_economics_missing"] != nil || b["ads_over_target"] != nil {
		t.Fatalf("with a target under which the brand sits: %v", b)
	}

	// Performance: the campaign brought one sign-up and one new payer for
	// its spend; the ad group under it the same; a one-day window drops
	// them; bad input is refused.
	perf := owner.must(200, "GET", base+"/performance?from="+now.Add(-7*day).Format(time.DateOnly)+"&to="+now.Format(time.DateOnly), nil)
	if perf["window_days"] != 30.0 || perf["target_cost_per_payer_micros"] != 60_000_000.0 {
		t.Fatalf("performance head: %v", perf)
	}
	rows := perf["data"].([]any)
	if len(rows) != 1 {
		t.Fatalf("rows: %v", rows)
	}
	row := rows[0].(map[string]any)
	if row["id"] != spring || row["signups"] != 1.0 || row["new_payers"] != 1.0 || row["cost_per_new_payer_micros"] != 50_000_000.0 || row["target_status"] != "under" ||
		row["platform_conversions"] != 2.0 || len(row["new_revenue"].([]any)) != 1 || len(row["lifetime_revenue"].([]any)) != 1 || row["trend"] != "new" {
		t.Fatalf("spring: %v", row)
	}
	groups := owner.must(200, "GET", base+"/performance?level=ad_group&campaign_id="+spring, nil)["data"].([]any)
	if len(groups) != 1 || groups[0].(map[string]any)["id"] != group || groups[0].(map[string]any)["new_payers"] != 1.0 || groups[0].(map[string]any)["campaign_id"] != spring {
		t.Fatalf("groups: %v", groups)
	}
	narrow := owner.must(200, "GET", base+"/performance?window_days=1", nil)
	if narrow["window_days"] != 1.0 || narrow["data"].([]any)[0].(map[string]any)["new_payers"] != 0.0 {
		t.Fatalf("narrow window: %v", narrow)
	}
	owner.must(400, "GET", base+"/performance?level=keyword", nil)
	owner.must(400, "GET", base+"/performance?window_days=91", nil)
	owner.must(400, "GET", base+"/performance?from=2026-02-01&to=2026-01-01", nil)
	tok.must(200, "GET", base+"/performance", nil)

	// Acquisitions: Ana, through Spring search, who paid twice.
	acqs := owner.must(200, "GET", base+"/acquisitions", nil)
	list := acqs["data"].([]any)
	if len(list) != 1 || acqs["next_cursor"] != nil {
		t.Fatalf("acquisitions: %v", acqs)
	}
	a := list[0].(map[string]any)
	if a["record_name"] != "Ana" || a["payments"] != 2.0 || a["bucket"] != "payer" || a["campaign"].(map[string]any)["name"] != "Spring search" || a["ad"].(map[string]any)["external_id"] != "9" || a["matched_by"] != "template" {
		t.Fatalf("Ana: %v", a)
	}
	if l := owner.must(200, "GET", base+"/acquisitions?customers=returning&campaign_id="+spring, nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("returning: %v", l)
	}
	if l := owner.must(200, "GET", base+"/acquisitions?from="+now.Add(-2*day).Format(time.DateOnly), nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("clicked before from: %v", l)
	}
	if paged := owner.must(200, "GET", base+"/acquisitions?limit=1", nil); paged["next_cursor"] == nil {
		t.Fatalf("a full page has a cursor: %v", paged)
	}
	code, body = owner.do("GET", base+"/acquisitions?cursor=nonsense", nil)
	if code != 400 || body["error"].(map[string]any)["code"] != "invalid_cursor" {
		t.Fatalf("bad cursor: %d %v", code, body)
	}
	viewer := &client{t: t, h: e.h, bearer: owner.must(201, "POST", "/v1/workspaces/"+ws+"/api-tokens", map[string]any{"name": "Reader", "scopes": []string{"analytics.read"}})["token"].(string)}
	viewer.must(403, "GET", base+"/acquisitions", nil)

	// The set-up path: the ad account and the economics are done, the
	// Google ad without the template and the consent setting are not.
	setup := owner.must(200, "GET", base+"/setup", nil)
	steps := map[string]map[string]any{}
	for _, st := range setup["steps"].([]any) {
		steps[st.(map[string]any)["step"].(string)] = st.(map[string]any)
	}
	if setup["total"] != 9.0 || steps["ad_account"]["done"] != true || steps["economics"]["done"] != true || steps["events"]["done"] != true ||
		steps["tracking_template"]["done"] != false || steps["consent"]["done"] != false || steps["site_script"]["done"] != false || setup["ready"] != false {
		t.Fatalf("setup: %v", setup)
	}
	owner.must(200, "PUT", base+"/settings", map[string]any{"consent_mode": "opt_in"})
	exec(`UPDATE ad_entities SET details = '{"templated": true}' WHERE level = 'ad' AND workspace_id = $1 RETURNING id::text`, ws)
	setup = owner.must(200, "GET", base+"/setup", nil)
	for _, st := range setup["steps"].([]any) {
		steps[st.(map[string]any)["step"].(string)] = st.(map[string]any)
	}
	if steps["consent"]["done"] != true || steps["tracking_template"]["done"] != true || steps["knowledge_sheet"]["fix"].(map[string]any)["tool"] != "get_brand_context" {
		t.Fatalf("setup after fixes: %v", setup)
	}

	// Over target: a tiny target with the campaign's spend worth far more
	// than three targets.
	owner.must(200, "PUT", base+"/economics", map[string]any{"target_cost_per_payer_micros": 1_000_000})
	if b := blockers()["ads_over_target"]; b == nil || b["severity"] != "warning" || b["count"] != 1.0 || b["fix"].(map[string]any)["tool"] != "get_ads_performance" {
		t.Fatalf("over target: %v", blockers())
	}

	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
	}
	if names["ad_economics.updated"] < 4 || names["ad_acquisition.recorded"] != 1 || names["ad_revenue.recorded"] != 2 {
		t.Fatalf("events: %v", names)
	}
	for _, ev := range got() {
		if ev.Name == "api.called" && ev.ErrorCode == "person_only" {
			return
		}
	}
	t.Fatalf("no api.called with person_only: %v", names)
}
