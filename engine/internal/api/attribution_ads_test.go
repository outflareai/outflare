package api_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/attribution"
)

func TestAttributionAds(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	exec := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	now := time.Now()
	day := now.Add(-24 * time.Hour).Format(time.DateOnly)

	// Ana clicked Google campaign 111 (ad set 222) and paid; Ben clicked
	// campaign 333, which no connected account has, and didn't.
	ana := exec(`INSERT INTO records (workspace_id, record_type, name) VALUES ($1, 'person', 'Ana') RETURNING id::text`, ws)
	exec(`INSERT INTO site_sessions (workspace_id, record_id, anonymous_id, started_at, utm_source, utm_medium, utm_campaign, ad)
		VALUES ($1, $2, 'anon-1', $3, 'google', 'cpc', 'spring', '{"campaign": "111", "adset": "222"}') RETURNING id::text`, ws, ana, now.Add(-2*24*time.Hour))
	exec(`INSERT INTO site_sessions (workspace_id, anonymous_id, started_at, utm_source, utm_medium, ad)
		VALUES ($1, 'anon-2', $2, 'google', 'cpc', '{"campaign": "333"}') RETURNING id::text`, ws, now.Add(-time.Hour))
	exec(`INSERT INTO customer_events (workspace_id, record_id, kind, name, properties, occurred_at)
		VALUES ($1, $2, 'track', 'payment.succeeded', '{"amount_minor": 4900, "currency": "usd", "payment_id": "ch_test_ads"}', $3) RETURNING id::text`,
		ws, ana, now.Add(-time.Hour))

	// The Google Ads account syncs 111, and 999, which spent with no
	// tracked visit.
	integ := exec(`INSERT INTO integrations (workspace_id, connector, name, credentials) VALUES ($1, 'google_ads', 'Google Ads', '\x00') RETURNING id::text`, ws)
	acct := exec(`INSERT INTO ad_accounts (workspace_id, integration_id, connector, external_id, name, currency) VALUES ($1, $2, 'google_ads', '1234567890', 'Northpeak', 'USD') RETURNING id::text`, ws, integ)
	spring := exec(`INSERT INTO ad_entities (workspace_id, ad_account_id, external_id, name, status) VALUES ($1, $2, '111', 'Spring search', 'active') RETURNING id::text`, ws, acct)
	idle := exec(`INSERT INTO ad_entities (workspace_id, ad_account_id, external_id, name, status) VALUES ($1, $2, '999', 'Brand search', 'active') RETURNING id::text`, ws, acct)
	exec(`INSERT INTO ad_metrics_daily (ad_entity_id, day, workspace_id, currency, spend_micros, clicks, conversions) VALUES ($1, $2, $3, 'USD', 20000000, 10, 3) RETURNING 'x'`, spring, day, ws)
	exec(`INSERT INTO ad_metrics_daily (ad_entity_id, day, workspace_id, currency, spend_micros, clicks) VALUES ($1, $2, $3, 'USD', 5000000, 2) RETURNING 'x'`, idle, day, ws)

	run := &attribution.Runner{Pool: e.pool, Log: slog.New(slog.DiscardHandler), Settle: time.Minute}
	if _, _, err := run.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	byID := func(path string) map[string]map[string]any {
		t.Helper()
		res := owner.must(200, "GET", path, nil)
		out := map[string]map[string]any{}
		for _, x := range res["data"].([]any) {
			m := x.(map[string]any)
			out[m["id"].(string)] = m
		}
		return out
	}
	camps := byID(base + "/attribution/ads")
	s, b, o := camps["111"], camps["999"], camps["333"]
	if len(camps) != 3 || s == nil || b == nil || o == nil {
		t.Fatalf("campaigns = %v", camps)
	}
	if s["name"] != "Spring search" || s["connector"] != "google_ads" || s["ad_entity_id"] != spring || s["visits"] != float64(1) ||
		s["conversions"] != float64(1) || s["platform_conversions"] != float64(3) {
		t.Fatalf("spring = %v", s)
	}
	if rev := s["revenue"].([]any); len(rev) != 1 || rev[0].(map[string]any)["amount_cents"] != float64(4900) {
		t.Fatalf("spring revenue = %v", s["revenue"])
	}
	if cost := s["cost"].([]any); len(cost) != 1 || cost[0].(map[string]any)["amount_cents"] != float64(2000) || cost[0].(map[string]any)["currency"] != "USD" {
		t.Fatalf("spring cost = %v", s["cost"])
	}
	// Spend with no tracked visit: the template is probably missing.
	if b["visits"] != float64(0) || b["conversions"] != float64(0) || b["cost"].([]any)[0].(map[string]any)["amount_cents"] != float64(500) {
		t.Fatalf("brand search = %v", b)
	}
	// A tracked id no account has: first-party numbers only.
	if o["ad_entity_id"] != nil || o["name"] != "" || o["visits"] != float64(1) || len(o["cost"].([]any)) != 0 || o["platform_conversions"] != nil {
		t.Fatalf("other = %v", o)
	}

	// Ad sets have first-party numbers only: accounts sync campaigns.
	sets := byID(base + "/attribution/ads?level=adset")
	if len(sets) != 1 || sets["222"]["conversions"] != float64(1) || sets["222"]["ad_entity_id"] != nil {
		t.Fatalf("ad sets = %v", sets)
	}
	owner.must(400, "GET", base+"/attribution/ads?level=keyword", nil)
	owner.must(400, "GET", base+"/attribution/ads?model=nope", nil)
}
