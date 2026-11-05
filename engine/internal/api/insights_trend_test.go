package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

// TestTrendsAndDashboards covers trends (series from events, visits,
// payments and ad numbers, by day, with a breakdown, a formula and the
// period before) and dashboards (tiles of saved insights and notes, laid
// out on a grid). Both are audited.
func TestTrendsAndDashboards(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	other := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Other brand"})["id"].(string)
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana"})["id"].(string)
	bo := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Bo"})["id"].(string)

	day := 24 * time.Hour
	today := time.Now().UTC().Truncate(day)
	d := func(n int) time.Time { return today.Add(-time.Duration(n)*day + time.Hour) }
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Ten days ago and three days ago: visits, sign-ups with a plan, payments.
	for _, v := range []struct {
		anon     string
		record   any
		at       time.Time
		campaign string
	}{
		{"b1", ana, d(10), "brand-search"}, {"b2", bo, d(10), "brand-search"}, {"b3", nil, d(3), "competitors"}, {"b4", nil, d(3), "competitors"}, {"b5", nil, d(3), "competitors"},
	} {
		exec(`INSERT INTO site_sessions (workspace_id, brand_id, anonymous_id, record_id, started_at, last_seen_at, click_ids, channel, utm_campaign, landing_path)
			VALUES ($1, $2, $3, $4, $5, $5, '{"gclid":"x"}', 'paid_ads', $6, '/')`, ws, brand, v.anon, v.record, v.at, v.campaign)
	}
	exec(`INSERT INTO customer_events (workspace_id, brand_id, record_id, kind, name, occurred_at, properties)
		VALUES ($1, $2, $3, 'track', 'signed_up', $4, '{"plan": "pro", "seats": 5}'), ($1, $2, $5, 'track', 'signed_up', $6, '{"plan": "free", "seats": 1}')`,
		ws, brand, ana, d(10), bo, d(3))
	exec(`INSERT INTO transactions (workspace_id, brand_id, record_id, kind, amount_minor, brand_amount_minor, source, source_id, occurred_at)
		VALUES ($1, $2, $3, 'payment', 4900, 4900, 'stripe', 'in_1', $4), ($1, $2, $3, 'payment', 4900, 4900, 'stripe', 'in_2', $5), ($1, $2, $6, 'payment', 900, 900, 'stripe', 'in_3', $5)`,
		ws, brand, ana, d(10), d(3), bo)
	// Ad numbers: one Google campaign of this brand, one of the other brand.
	var integ, account string
	if err := e.pool.QueryRow(ctx, `INSERT INTO integrations (workspace_id, connector, name, credentials) VALUES ($1, 'google_ads', 'Google Ads', '\x00') RETURNING id::text`, ws).Scan(&integ); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO ad_accounts (workspace_id, integration_id, brand_id, connector, external_id, name, currency)
		VALUES ($1, $2, $3, 'google_ads', 'acc-1', 'Northpeak Google', 'USD') RETURNING id::text`, ws, integ, brand).Scan(&account); err != nil {
		t.Fatal(err)
	}
	var campaign, otherCampaign string
	if err := e.pool.QueryRow(ctx, `INSERT INTO ad_entities (workspace_id, ad_account_id, external_id, name, status)
		VALUES ($1, $2, 'c-1', 'Brand search', 'active') RETURNING id::text`, ws, account).Scan(&campaign); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO ad_entities (workspace_id, brand_id, ad_account_id, external_id, name, status)
		VALUES ($1, $2, $3, 'c-2', 'Other', 'active') RETURNING id::text`, ws, other, account).Scan(&otherCampaign); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO ad_metrics_daily (ad_entity_id, day, workspace_id, currency, spend_micros, impressions, clicks)
		VALUES ($1, $3::date, $2, 'USD', 20000000, 1000, 40), ($1, $4::date, $2, 'USD', 10000000, 500, 20), ($5, $3::date, $2, 'USD', 99000000, 9, 9)`,
		campaign, ws, d(10).Format(time.DateOnly), d(3).Format(time.DateOnly), otherCampaign)

	from := today.Add(-14 * day).Format(time.DateOnly)
	to := today.Format(time.DateOnly)
	trend := map[string]any{
		"date_range": map[string]any{"from": from, "to": to},
		"interval":   "day",
		"series": []map[string]any{
			{"kind": "ad", "measure": "spend", "network": "google"},
			{"kind": "visit"},
			{"kind": "event", "event": "signed_up"},
			{"kind": "payment", "measure": "sum"},
			{"kind": "event", "event": "signed_up", "measure": "average", "property": "seats"},
		},
		"formula": "A / C",
		"compare": true,
	}
	res := owner.must(200, "POST", base+"/insights/trend", trend)
	series := res["series"].([]any)
	total := func(i int) float64 {
		v := series[i].(map[string]any)["total"]
		if v == nil {
			return -1
		}
		return v.(float64)
	}
	if len(series) != 5 || total(0) != 30 || total(1) != 5 || total(2) != 2 || total(3) != 107 || total(4) != 3 {
		t.Fatalf("totals: %v %v %v %v %v", total(0), total(1), total(2), total(3), total(4))
	}
	if series[0].(map[string]any)["currency"] != "USD" || series[3].(map[string]any)["currency"] != "brand" || series[3].(map[string]any)["label"] != "Revenue" {
		t.Fatalf("series: %v %v", series[0], series[3])
	}
	points := series[1].(map[string]any)["points"].([]any)
	if len(points) != 15 {
		t.Fatalf("points: %d", len(points))
	}
	var sum float64
	for _, p := range points {
		if v := p.(map[string]any)["value"]; v != nil {
			sum += v.(float64)
		}
	}
	if sum != 5 {
		t.Fatalf("points sum: %v", sum)
	}
	if f := res["formula"].(map[string]any); f["total"] != 15.0 {
		t.Fatalf("formula: %v", f)
	}
	prev := res["previous"].(map[string]any)
	if prev["series"].([]any)[1].(map[string]any)["total"] != nil {
		t.Fatalf("previous: %v", prev)
	}

	// Broken down by campaign, visits split 2 and 3.
	trend["series"] = []map[string]any{{"kind": "visit"}}
	trend["breakdown"] = "utm_campaign"
	delete(trend, "formula")
	res = owner.must(200, "POST", base+"/insights/trend", trend)
	groups := res["breakdown"].([]any)
	if len(groups) != 2 || groups[0].(map[string]any)["value"] != "competitors" || groups[0].(map[string]any)["series"].([]any)[0].(map[string]any)["total"] != 3.0 {
		t.Fatalf("breakdown: %v", groups)
	}
	// Weeks and months are accepted and the default interval follows the range.
	trend["interval"] = "month"
	delete(trend, "breakdown")
	if ps := owner.must(200, "POST", base+"/insights/trend", trend)["series"].([]any)[0].(map[string]any)["points"].([]any); len(ps) < 1 || len(ps) > 2 {
		t.Fatalf("months: %d", len(ps))
	}

	for _, bad := range []map[string]any{
		{"series": []map[string]any{}},
		{"series": []map[string]any{{"kind": "visit", "measure": "sum", "property": "x"}}},
		{"series": []map[string]any{{"kind": "event", "event": "signed_up"}}, "formula": "A / B"},
		{"series": []map[string]any{{"kind": "event", "event": "signed_up"}}, "formula": "A +"},
		{"series": []map[string]any{{"kind": "ad", "measure": "people"}}},
		{"series": []map[string]any{{"kind": "ad"}}, "breakdown": "device"},
		{"series": []map[string]any{{"kind": "visit"}}, "interval": "hour"},
		{"series": []map[string]any{{"kind": "visit"}}, "display": "pie"},
	} {
		owner.must(400, "POST", base+"/insights/trend", bad)
	}

	// Saved trends and dashboards.
	saved := owner.must(201, "POST", base+"/insights", map[string]any{"kind": "trend", "name": "Revenue", "spec": map[string]any{
		"date_range": map[string]any{"preset": "last_30_days"}, "series": []map[string]any{{"kind": "payment", "measure": "sum"}}, "display": "number"}})
	insight := saved["id"].(string)
	if saved["kind"] != "trend" || saved["spec"].(map[string]any)["display"] != "number" {
		t.Fatalf("saved: %v", saved)
	}
	owner.must(400, "POST", base+"/insights", map[string]any{"kind": "trend", "name": "Bad", "spec": map[string]any{"series": []any{}}})
	if l := owner.must(200, "GET", base+"/insights?kind=trend", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("list: %v", l)
	}

	dash := owner.must(201, "POST", base+"/dashboards", map[string]any{"name": "Growth", "settings": map[string]any{"date_range": map[string]any{"preset": "last_7_days"}}})
	id := dash["id"].(string)
	owner.must(400, "POST", base+"/dashboards", map[string]any{"name": "Bad", "settings": map[string]any{"date_range": map[string]any{"preset": "never"}}})
	owner.must(400, "POST", base+"/dashboards", map[string]any{"name": ""})
	dash = owner.must(201, "POST", base+"/dashboards/"+id+"/tiles", map[string]any{"insight_id": insight, "w": 4, "h": 3})
	tiles := dash["tile_list"].([]any)
	if len(tiles) != 1 || tiles[0].(map[string]any)["insight"].(map[string]any)["name"] != "Revenue" || tiles[0].(map[string]any)["w"] != 4.0 {
		t.Fatalf("tile: %v", tiles)
	}
	dash = owner.must(201, "POST", base+"/dashboards/"+id+"/tiles", map[string]any{"note": "Weekly numbers for the team."})
	tiles = dash["tile_list"].([]any)
	if len(tiles) != 2 || tiles[1].(map[string]any)["y"] != 3.0 || dash["tiles"] != 2.0 {
		t.Fatalf("note tile: %v", dash)
	}
	tile := tiles[0].(map[string]any)["id"].(string)
	owner.must(400, "POST", base+"/dashboards/"+id+"/tiles", map[string]any{})
	owner.must(400, "POST", base+"/dashboards/"+id+"/tiles", map[string]any{"insight_id": insight, "x": 8, "w": 6})
	owner.must(400, "POST", base+"/dashboards/"+id+"/tiles", map[string]any{"insight_id": "00000000-0000-0000-0000-000000000001"})
	// Move a tile, both ways.
	up := owner.must(200, "PATCH", base+"/dashboards/"+id, map[string]any{"name": "Growth board", "layout": []map[string]any{{"id": tile, "x": 6, "y": 0, "w": 6, "h": 4}}})
	if up["name"] != "Growth board" || up["tile_list"].([]any)[0].(map[string]any)["x"] != 6.0 {
		t.Fatalf("layout: %v", up)
	}
	up = owner.must(200, "PATCH", base+"/dashboards/"+id+"/tiles/"+tile, map[string]any{"x": 0, "w": 12})
	if up["tile_list"].([]any)[0].(map[string]any)["w"] != 12.0 {
		t.Fatalf("resize: %v", up)
	}
	owner.must(400, "PATCH", base+"/dashboards/"+id+"/tiles/"+tile, map[string]any{"insight_id": insight})
	got1 := owner.must(200, "GET", base+"/dashboards/"+id, nil)
	if got1["settings"].(map[string]any)["date_range"].(map[string]any)["preset"] != "last_7_days" || len(got1["tile_list"].([]any)) != 2 {
		t.Fatalf("get: %v", got1)
	}
	if l := owner.must(200, "GET", base+"/dashboards", nil)["data"].([]any); len(l) != 1 || l[0].(map[string]any)["tiles"] != 2.0 {
		t.Fatalf("list: %v", l)
	}
	// Deleting the insight takes its tile with it; the other brand sees nothing.
	owner.must(404, "GET", "/v1/brands/"+other+"/dashboards/"+id, nil)
	owner.must(204, "DELETE", base+"/insights/"+insight, nil)
	if l := owner.must(200, "GET", base+"/dashboards/"+id, nil)["tile_list"].([]any); len(l) != 1 {
		t.Fatalf("after insight delete: %v", l)
	}
	owner.must(204, "DELETE", base+"/dashboards/"+id+"/tiles/"+tiles[1].(map[string]any)["id"].(string), nil)
	owner.must(404, "DELETE", base+"/dashboards/"+id+"/tiles/"+tile, nil)
	owner.must(204, "DELETE", base+"/dashboards/"+id, nil)
	owner.must(404, "GET", base+"/dashboards/"+id, nil)

	var audits int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action IN ('dashboard.created', 'dashboard.updated', 'dashboard.deleted')`,
		ws).Scan(&audits); err != nil || audits != 7 {
		t.Fatalf("audits: %d %v", audits, err)
	}
	if ev := named(got(), "insight.created"); len(ev) != 1 || ev[0].Props["kind"] != "trend" || ev[0].Props["display"] != "number" {
		t.Fatalf("event: %v", ev)
	}
}
