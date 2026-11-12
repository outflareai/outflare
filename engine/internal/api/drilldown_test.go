package api_test

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The drill-down report (plans/ad-tracker.md, B5a): visits, credited
// conversions, revenue and, where it splits, spend, grouped by up to five
// levels, with subtotals, filters, sorting, limits and CSV.
func TestDrilldownReport(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand + "/drilldown"
	exec := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO exchange_rates (currency, day, usd_per_unit) VALUES ('EUR', current_date, 1.1) ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	session := func(anon, channel, country, campaign string) string {
		ad := `{}`
		if campaign != "" {
			ad = `{"campaign": "` + campaign + `"}`
		}
		return exec(`INSERT INTO site_sessions (workspace_id, brand_id, anonymous_id, started_at, last_seen_at, landing_path, channel, country, ad)
			VALUES ($1, $2, $3, $4, $4, '/', $5, $6, $7::jsonb) RETURNING id::text`, ws, brand, anon, at, channel, country, ad)
	}
	s1 := session("a1", "paid_ads", "US", "c1")
	session("a2", "paid_ads", "US", "c1")
	s3 := session("a3", "paid_ads", "DE", "c2")
	session("a1", "seo", "US", "")
	credit := func(sess string, minor int64, currency string) {
		ev := exec(`INSERT INTO customer_events (workspace_id, brand_id, kind, name, anonymous_id, properties, context, occurred_at)
			VALUES ($1, $2, 'track', 'purchase', 'x', '{}', '{}', $3) RETURNING id::text`, ws, brand, at.Add(time.Hour))
		exec(`INSERT INTO attribution_conversions (event_id, workspace_id, brand_id, name, occurred_at, value_minor, currency)
			VALUES ($1, $2, $3, 'purchase', $4, $5, $6) RETURNING event_id::text`, ev, ws, brand, at.Add(time.Hour), minor, currency)
		exec(`INSERT INTO attribution_credits (event_id, workspace_id, brand_id, model, position, session_id, channel, weight)
			VALUES ($1, $2, $3, 'last_non_direct', 0, $4, 'paid_ads', 1) RETURNING event_id::text`, ev, ws, brand, sess)
	}
	credit(s1, 5000, "USD")
	credit(s3, 2000, "EUR") // 22 USD
	integ := exec(`INSERT INTO integrations (workspace_id, connector, name, credentials) VALUES ($1, 'google_ads', 'Google Ads', '\x00') RETURNING id::text`, ws)
	account := exec(`INSERT INTO ad_accounts (workspace_id, integration_id, brand_id, connector, external_id, name, currency)
		VALUES ($1, $2, $3, 'google_ads', 'acc-1', 'Northpeak', 'USD') RETURNING id::text`, ws, integ, brand)
	for _, c := range []struct {
		id    string
		spend int64
	}{{"c1", 30_000_000}, {"c2", 10_000_000}} {
		ent := exec(`INSERT INTO ad_entities (workspace_id, ad_account_id, level, external_id, name, status) VALUES ($1, $2, 'campaign', $3, $3, 'active') RETURNING id::text`,
			ws, account, c.id)
		exec(`INSERT INTO ad_metrics_daily (ad_entity_id, day, workspace_id, currency, spend_micros, impressions, clicks) VALUES ($1, '2026-10-05', $2, 'USD', $3, 100, 10) RETURNING ad_entity_id::text`,
			ent, ws, c.spend)
	}
	report := func(query string) map[string]any {
		t.Helper()
		return owner.must(200, "GET", base+"?from=2026-10-01&to=2026-10-10&"+query, nil)
	}
	metric := func(m any, name string) any { return m.(map[string]any)["metrics"].(map[string]any)[name] }
	rows := func(m any) []any { return m.(map[string]any)["rows"].([]any) }

	// Two levels: channel, then country, with subtotals and exact
	// visitors (a1 visited twice).
	r := report("group_by=channel,country")
	tot := r["totals"].(map[string]any)
	if tot["visits"] != float64(4) || tot["visitors"] != float64(3) || tot["conversions"] != float64(2) || tot["revenue"] != float64(72) ||
		tot["cost"] != nil || r["cost_available"] != false || r["cost_note"] == nil {
		t.Fatalf("totals: %v (%v, %v)", tot, r["cost_available"], r["cost_note"])
	}
	top := r["rows"].([]any)
	paid := top[0]
	if len(top) != 2 || paid.(map[string]any)["keys"].([]any)[0] != "paid_ads" || metric(paid, "visits") != float64(3) || metric(paid, "revenue") != float64(72) {
		t.Fatalf("channels: %v", top)
	}
	kids := rows(paid)
	if len(kids) != 2 || kids[0].(map[string]any)["keys"].([]any)[1] != "US" || metric(kids[0], "visits") != float64(2) || metric(kids[0], "revenue") != float64(50) ||
		metric(kids[1], "revenue") != float64(22) || metric(kids[0], "cr") != 0.5 {
		t.Fatalf("paid by country: %v", kids)
	}

	// By ad campaign, spend splits: cost, profit and ROI.
	r = report("group_by=ad_campaign&sort=-cost")
	if r["cost_available"] != true || r["totals"].(map[string]any)["cost"] != float64(40) {
		t.Fatalf("cost totals: %v", r["totals"])
	}
	c1 := r["rows"].([]any)[0]
	if c1.(map[string]any)["keys"].([]any)[0] != "c1" || metric(c1, "cost") != float64(30) || metric(c1, "profit") != float64(20) ||
		metric(c1, "roi") != 0.6667 || metric(c1, "cpa") != float64(30) {
		t.Fatalf("c1: %v", c1)
	}

	// Filters, exclusions, sort and limits.
	if n := report("group_by=channel&filter=country:US")["totals"].(map[string]any)["visits"]; n != float64(3) {
		t.Fatalf("US visits: %v", n)
	}
	if n := report("group_by=channel&exclude=channel:seo&filter=country:US&filter=country:DE")["totals"].(map[string]any)["visits"]; n != float64(3) {
		t.Fatalf("paid visits: %v", n)
	}
	r = report("group_by=country&sort=-revenue&limit=1")
	if len(r["rows"].([]any)) != 1 || r["truncated"] != true || r["rows"].([]any)[0].(map[string]any)["keys"].([]any)[0] != "US" {
		t.Fatalf("limit: %v", r["rows"])
	}
	// Days are the brand's, and conversions can count at their own time.
	if d := report("group_by=day&timezone=Pacific/Kiritimati")["rows"].([]any)[0].(map[string]any)["keys"].([]any)[0]; d != "2026-10-06" {
		t.Fatalf("day in Kiritimati: %v", d)
	}
	if n := report("group_by=channel&conversion_time=conversion")["totals"].(map[string]any)["conversions"]; n != float64(2) {
		t.Fatalf("conversions at their own time: %v", n)
	}

	// CSV: the deepest rows, one column per level.
	req := httptest.NewRequest("GET", base+"?from=2026-10-01&to=2026-10-10&group_by=channel,country&format=csv", nil)
	req.AddCookie(owner.cookie)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Body)
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if rec.Code != 200 || len(lines) != 4 || !strings.HasPrefix(lines[0], "channel,country,visits,visitors") || !strings.HasPrefix(lines[1], "paid_ads,US,2,") {
		t.Fatalf("csv: %d %q", rec.Code, body)
	}

	for _, bad := range []string{"group_by=planet", "group_by=channel,country,city,os,browser,device", "sort=-luck", "filter=country", "limit=0", "conversion_time=later"} {
		owner.must(400, "GET", base+"?"+bad, nil)
	}
}
