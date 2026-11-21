package api_test

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestSEOMoney(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	near := func(a any, b float64) bool {
		f, ok := a.(float64)
		return ok && math.Abs(f-b) < 1e-6
	}

	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme Proxies", "website": "https://example.com"})["id"].(string)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Other Example", "website": "https://other.example"})["id"].(string)
	money := "/v1/brands/" + brand + "/seo/money"

	// Nothing stored: empty lists, no dates.
	empty := owner.must(200, "GET", money, nil)
	if empty["search_through"] != nil || len(empty["positions"].([]any)) != 0 || empty["rpc_source"] != "none" {
		t.Fatalf("empty: %v", empty)
	}
	owner.must(400, "GET", money+"?rpc=-1", nil)

	integ := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "name": "Search", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})["id"].(string)
	newest := time.Now().UTC().AddDate(0, 0, -3).Truncate(24 * time.Hour)
	day := func(n int) time.Time { return newest.AddDate(0, 0, -n) }
	q := `INSERT INTO search_daily (workspace_id, brand_id, integration_id, site, host, day, page, query, clicks, impressions, ctr, position)
		VALUES ($1, $2, $3, 'sc-domain:example.com', 'example.com', $4, $5, $6, $7, $8, 0, $9)`
	// Position 6 with 2% CTR: worth moving to 3, and under its expected CTR.
	exec(q, ws, brand, integ, day(0), "https://example.com/a", "residential proxy", 20, 1000, 6)
	// Names the brand: left out unless asked for.
	exec(q, ws, brand, integ, day(1), "https://www.example.com/", "acme proxies login", 10, 5000, 5)
	// Past 20: neither list.
	exec(q, ws, brand, integ, day(1), "https://example.com/b", "proxy list", 0, 200, 25)
	// Older than the window.
	exec(q, ws, brand, integ, day(40), "https://example.com/a", "residential proxy", 0, 9000, 8)
	// Another brand's data never shows here.
	exec(q, ws, other, integ, day(0), "https://other.example/x", "other query", 0, 9000, 9)

	exec(`INSERT INTO analytics_daily (workspace_id, brand_id, integration_id, connector, site, host, report, day, key, clicks, impressions, position)
		VALUES ($1, $2, $3, 'google_search_console', 'sc-domain:example.com', 'example.com', 'traffic', $4, '', 100, 6200, 5)`, ws, brand, integ, day(0))
	conv := `INSERT INTO organic_conversions (workspace_id, brand_id, integration_id, site, day, host, path, canonical_path, organic_sessions, signups, payments, revenue_usd)
		VALUES ($1, $2, $3, '1', $4, 'www.example.com', '/a', '/a', 10, 1, 1, $5)`
	exec(conv, ws, brand, integ, day(0), 500)
	exec(conv, ws, brand, integ, day(30), 300)
	seg := `INSERT INTO search_segments (workspace_id, brand_id, integration_id, site, host, day, page, country, device, clicks, impressions, position)
		VALUES ($1, $2, $3, 'sc-domain:example.com', 'example.com', $4, 'https://example.com/a', 'usa', 'DESKTOP', $5, 1000, $6)`
	exec(seg, ws, brand, integ, day(0), 20, 6)
	exec(seg, ws, brand, integ, day(30), 100, 4)

	r := owner.must(200, "GET", money, nil)
	if r["search_through"] != newest.Format(time.DateOnly) || r["rpc_source"] != "measured" || !near(r["rpc"], 5) {
		t.Fatalf("rate: %v %v %v", r["search_through"], r["rpc_source"], r["rpc"])
	}
	pos := r["positions"].([]any)
	if len(pos) != 1 {
		t.Fatalf("positions: %v", pos)
	}
	p := pos[0].(map[string]any)
	if p["query"] != "residential proxy" || !near(p["extra_clicks"], 80) || !near(p["extra_revenue"], 400) || p["target_position"] != 3.0 || !near(p["expected_ctr"], 0.04) {
		t.Fatalf("position row: %v", p)
	}
	low := r["low_ctr"].([]any)
	if len(low) != 1 || !near(low[0].(map[string]any)["extra_clicks"], 20) {
		t.Fatalf("low ctr: %v", low)
	}
	lost := r["losing_pages"].([]any)
	if len(lost) != 1 {
		t.Fatalf("losing: %v", lost)
	}
	l := lost[0].(map[string]any)
	if l["path"] != "/a" || l["host"] != "example.com" || !near(l["lost_clicks"], 80) || !near(l["modeled_loss"], 400) || !near(l["revenue_change"], 200) {
		t.Fatalf("losing page: %v", l)
	}
	tot := r["totals"].(map[string]any)
	if !near(tot["lost"], 400) || !near(tot["positions_4_20"], 400) || !near(tot["low_ctr"], 100) {
		t.Fatalf("totals: %v", tot)
	}

	// The brand's own searches when asked for; a rate of one's own.
	r = owner.must(200, "GET", money+"?branded=true&rpc=2", nil)
	if r["rpc_source"] != "set" || !near(r["rpc"], 2) || len(r["positions"].([]any)) != 2 {
		t.Fatalf("branded: %v", r["positions"])
	}
	if first := r["positions"].([]any)[0].(map[string]any); first["query"] != "acme proxies login" || !near(first["extra_revenue"], 980) {
		t.Fatalf("branded first: %v", first)
	}

	// Another brand: only its own data.
	o := owner.must(200, "GET", "/v1/brands/"+other+"/seo/money", nil)
	if len(o["losing_pages"].([]any)) != 0 {
		t.Fatalf("other brand: %v", o)
	}
}
