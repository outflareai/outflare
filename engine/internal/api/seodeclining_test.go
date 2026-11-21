package api_test

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestSEODeclining(t *testing.T) {
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

	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Decline Example", "website": "https://example.com"})["id"].(string)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Other Example", "website": "https://other.example"})["id"].(string)
	report := "/v1/brands/" + brand + "/seo/declining"

	if r := owner.must(200, "GET", report, nil); r["from"] != nil || len(r["pages"].([]any)) != 0 {
		t.Fatalf("empty: %v", r)
	}
	owner.must(400, "GET", report+"?days=14", nil)

	integ := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "name": "Search", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})["id"].(string)
	newest := time.Now().UTC().AddDate(0, 0, -3).Truncate(24 * time.Hour)
	day := func(n int) time.Time { return newest.AddDate(0, 0, -n) }
	u := func(p string) string { return "https://example.com" + p }

	seg := `INSERT INTO search_segments (workspace_id, brand_id, integration_id, site, host, day, page, country, device, clicks, impressions, position)
		VALUES ($1, $2, $3, 'sc-domain:example.com', 'example.com', $4, $5, $6, 'DESKTOP', $7, $8, $9)`
	add := func(page, country string, d int, clicks, impr, pos float64) {
		exec(seg, ws, brand, integ, day(d), u(page), country, clicks, impr, pos)
	}
	// Day 1 is in the latest 7 days, 8 in the 7 before, 15 in the 7 before those.
	// /a: steady, then down: new.
	add("/a", "usa", 15, 90, 1000, 3)
	add("/a", "deu", 15, 10, 100, 3)
	add("/a", "usa", 8, 90, 1000, 3)
	add("/a", "deu", 8, 10, 100, 3)
	add("/a", "usa", 1, 30, 1000, 6)
	add("/a", "deu", 1, 10, 100, 3)
	// /b: down and down again: still declining.
	add("/b", "usa", 15, 300, 3000, 2)
	add("/b", "usa", 8, 150, 3000, 4)
	add("/b", "usa", 1, 60, 3000, 7)
	// /c: down, then up: recovering.
	add("/c", "usa", 15, 200, 2000, 2)
	add("/c", "usa", 8, 100, 2000, 4)
	add("/c", "usa", 1, 130, 2000, 3)
	// /d: too small to count.
	add("/d", "usa", 8, 8, 100, 9)
	add("/d", "usa", 1, 4, 100, 9)
	// Another brand's page never shows here.
	exec(seg, ws, other, integ, day(8), "https://other.example/x", "usa", 500, 5000, 2)

	q := `INSERT INTO search_daily (workspace_id, brand_id, integration_id, site, host, day, page, query, clicks, impressions, ctr, position)
		VALUES ($1, $2, $3, 'sc-domain:example.com', 'example.com', $4, $5, $6, $7, $8, 0, $9)`
	exec(q, ws, brand, integ, day(8), u("/a"), "proxy a", 80, 800, 3)
	exec(q, ws, brand, integ, day(1), u("/a"), "proxy a", 20, 800, 6)
	exec(q, ws, brand, integ, day(1), u("/a"), "proxy a2", 20, 200, 3)
	// Another page shows for /a's losing query.
	exec(q, ws, brand, integ, day(1), u("/e"), "proxy a", 5, 50, 4)
	// The newest stored day ends the periods.
	exec(q, ws, brand, integ, day(0), u("/z"), "z", 0, 1, 50)

	exec(`INSERT INTO analytics_daily (workspace_id, brand_id, integration_id, connector, site, host, report, day, key, clicks, impressions, position)
		VALUES ($1, $2, $3, 'google_search_console', 'sc-domain:example.com', 'example.com', 'traffic', $4, '', 1000, 10000, 4)`, ws, brand, integ, day(1))
	conv := `INSERT INTO organic_conversions (workspace_id, brand_id, integration_id, site, day, host, path, canonical_path, organic_sessions, signups, payments, revenue_usd)
		VALUES ($1, $2, $3, '1', $4, 'www.example.com', '/a', '/a', 10, $5, 1, $6)`
	exec(conv, ws, brand, integ, day(1), 2, 2000)
	exec(conv, ws, brand, integ, day(8), 5, 0)

	r := owner.must(200, "GET", report, nil)
	if r["to"] != newest.Format(time.DateOnly) || r["from"] != day(6).Format(time.DateOnly) || r["previous_to"] != day(7).Format(time.DateOnly) || !near(r["rpc"], 2) {
		t.Fatalf("periods: %v %v %v %v", r["from"], r["to"], r["previous_to"], r["rpc"])
	}
	sum := r["summary"].(map[string]any)
	if sum["new"] != 1.0 || sum["still"] != 1.0 || sum["recovering"] != 1.0 || !near(sum["clicks_lost"], 150) || !near(sum["modeled_loss"], 300) {
		t.Fatalf("summary: %v", sum)
	}
	by := map[string]map[string]any{}
	for _, p := range r["pages"].([]any) {
		by[p.(map[string]any)["path"].(string)] = p.(map[string]any)
	}
	if len(by) != 3 || by["/d"] != nil {
		t.Fatalf("pages: %v", by)
	}
	a := by["/a"]
	if a["cohort"] != "new" || !near(a["clicks"], 40) || !near(a["previous_clicks"], 100) || !near(a["modeled_loss"], 120) || a["driver"] != "position" {
		t.Fatalf("/a: %v", a)
	}
	if c := a["top_country"].(map[string]any); c["key"] != "usa" || !near(c["previous_clicks"], 90) {
		t.Fatalf("/a country: %v", c)
	}
	if q := a["top_query"].(map[string]any); q["key"] != "proxy a" {
		t.Fatalf("/a query: %v", q)
	}
	if a["competing_pages"] != 1.0 || !near(a["signups"], 2) || !near(a["previous_signups"], 5) {
		t.Fatalf("/a flags: %v", a)
	}
	if by["/b"]["cohort"] != "still" || by["/c"]["cohort"] != "recovering" || by["/c"]["modeled_loss"] != 0.0 {
		t.Fatalf("cohorts: %v %v", by["/b"], by["/c"])
	}

	// 28 days: everything above is in one window, set against nothing.
	if r := owner.must(200, "GET", report+"?days=28", nil); len(r["pages"].([]any)) != 0 {
		t.Fatalf("28 days: %v", r["pages"])
	}
}
