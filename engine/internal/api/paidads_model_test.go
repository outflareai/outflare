package api_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/attribution"
	"github.com/outflareai/outflare/engine/internal/paidads"
)

// The performance read under an attribution model: beside the last paid
// click, each row gets the share of credit its visits earned under the
// model asked for.
func TestPaidAdsPerformanceUnderAModel(t *testing.T) {
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

	// Ana clicked an ad of Spring search, came back from a search engine,
	// then signed up and paid.
	integ := exec(`INSERT INTO integrations (brand_id, workspace_id, connector, name, credentials, status) VALUES ($1, $2, 'google_ads', 'Google Ads', '\x00', 'connected') RETURNING id::text`, brand, ws)
	acct := exec(`INSERT INTO ad_accounts (brand_id, workspace_id, integration_id, connector, external_id, name, currency) VALUES ($1, $2, $3, 'google_ads', '1234567890', 'Northpeak', 'USD') RETURNING id::text`, brand, ws, integ)
	spring := exec(`INSERT INTO ad_entities (brand_id, workspace_id, ad_account_id, external_id, name, status) VALUES ($1, $2, $3, '111', 'Spring search', 'active') RETURNING id::text`, brand, ws, acct)
	exec(`INSERT INTO ad_metrics_daily (ad_entity_id, day, workspace_id, currency, spend_micros, clicks, conversions, brand_id) VALUES ($1, current_date - 1, $2, 'USD', 50000000, 40, 2, $3) RETURNING 'x'`, spring, ws, brand)
	ana := exec(`INSERT INTO records (brand_id, workspace_id, record_type, name) VALUES ($1, $2, 'person', 'Ana') RETURNING id::text`, brand, ws)
	exec(`INSERT INTO site_sessions (brand_id, workspace_id, record_id, anonymous_id, started_at, last_seen_at, utm_source, utm_medium, channel, click_ids, ad)
		VALUES ($1, $2, $3, 'anon-ana', $4, $4, 'google', 'cpc', 'paid_ads', '{"gclid": "Cj0test"}', '{"campaign": "111"}') RETURNING id::text`, brand, ws, ana, now.Add(-4*day))
	exec(`INSERT INTO site_sessions (brand_id, workspace_id, record_id, anonymous_id, started_at, last_seen_at, referrer_host, channel)
		VALUES ($1, $2, $3, 'anon-ana', $4, $4, 'www.google.com', 'seo') RETURNING id::text`, brand, ws, ana, now.Add(-3*day))
	exec(`INSERT INTO customer_events (brand_id, workspace_id, record_id, kind, name, occurred_at, message_id) VALUES ($1, $2, $3, 'track', 'signed_up', $4, gen_random_uuid()::text) RETURNING id::text`, brand, ws, ana, now.Add(-2*day))
	exec(`INSERT INTO customer_events (brand_id, workspace_id, record_id, kind, name, properties, occurred_at, message_id) VALUES ($1, $2, $3, 'track', 'payment.succeeded', '{"amount_minor": 4900, "currency": "USD", "payment_id": "p1"}', $4, gen_random_uuid()::text) RETURNING id::text`, brand, ws, ana, now.Add(-day))
	run := &attribution.Runner{Pool: e.pool, Log: slog.New(slog.DiscardHandler), Settle: time.Minute}
	if _, _, err := run.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := (&paidads.Ledger{Pool: e.pool, Log: slog.New(slog.DiscardHandler), Settle: time.Millisecond}).Tick(ctx); err != nil {
		t.Fatal(err)
	}

	read := func(query string) (map[string]any, map[string]any) {
		t.Helper()
		perf := owner.must(200, "GET", base+"/performance"+query, nil)
		rows := perf["data"].([]any)
		if len(rows) != 1 || rows[0].(map[string]any)["id"] != spring {
			t.Fatalf("rows: %v", rows)
		}
		return perf, rows[0].(map[string]any)
	}
	// Without a model the row carries none of its numbers.
	perf, row := read("")
	if perf["model"] != "last_paid_click" || row["model_signups"] != nil || len(row["model_revenue"].([]any)) != 0 || row["model_roas"] != nil || row["platform_conversions"] != 2.0 {
		t.Fatalf("no model: %v %v", perf["model"], row)
	}
	if _, same := read("?model=last_paid_click"); same["model_signups"] != nil {
		t.Fatalf("last paid click named: %v", same)
	}
	// First touch gives the ad everything.
	perf, row = read("?model=first_touch")
	money := row["model_revenue"].([]any)
	if perf["model"] != "first_touch" || row["model_signups"] != 1.0 || row["model_cost_per_signup_micros"] != 50_000_000.0 || len(money) != 1 ||
		money[0].(map[string]any)["amount_minor"] != 4900.0 || row["model_roas"] != 0.98 {
		t.Fatalf("first touch: %v", row)
	}
	// Linear shares it with the search visit.
	_, row = read("?model=linear")
	if row["model_signups"] != 0.5 || row["model_cost_per_signup_micros"] != 100_000_000.0 || row["model_revenue"].([]any)[0].(map[string]any)["amount_minor"] != 2450.0 || row["model_roas"] != 0.49 {
		t.Fatalf("linear: %v", row)
	}
	// The last visit that wasn't direct was the search, so the ad gets nothing.
	_, row = read("?model=last_non_direct")
	if row["model_signups"] != 0.0 || row["model_cost_per_signup_micros"] != nil || len(row["model_revenue"].([]any)) != 0 || row["model_roas"] != 0.0 {
		t.Fatalf("last non-direct: %v", row)
	}
	// The last paid click still counts her, whatever the model.
	if row["signups"] != 1.0 || row["new_payers"] != 1.0 {
		t.Fatalf("last paid click: %v", row)
	}
	owner.must(400, "GET", base+"/performance?model=nope", nil)
	owner.must(400, "GET", base+"/performance?model=Not+A+Model", nil)
}
