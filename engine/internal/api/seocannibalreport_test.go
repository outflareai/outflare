package api_test

import (
	"context"
	"net/url"
	"testing"
	"time"
)

func TestSEOCannibalisationReport(t *testing.T) {
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

	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Cannibal Example", "website": "https://example.com"})["id"].(string)
	site := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://example.com", "brand_id": brand})["id"].(string)
	report := "/v1/brands/" + brand + "/seo/cannibalisation"

	if r := owner.must(200, "GET", report, nil); len(r["clusters"].([]any)) != 0 || r["search_through"] != nil {
		t.Fatalf("empty: %v", r)
	}

	sc := connectSearchConsole(t, e, owner, base, &fakeSearchConsole{})
	newest := time.Now().UTC().AddDate(0, 0, -3).Truncate(24 * time.Hour)
	exec(`INSERT INTO search_syncs (integration_id, site, workspace_id, host, oldest_day, newest_day) VALUES ($1, 'sc-domain:example.com', $2, 'example.com', $3::date - 100, $3)
		ON CONFLICT (integration_id, site) DO UPDATE SET oldest_day = EXCLUDED.oldest_day, newest_day = EXCLUDED.newest_day`, sc, ws, newest)
	owner.must(200, "PUT", base+"/integrations/"+sc+"/accounts/"+url.PathEscape("sc-domain:example.com"), map[string]any{"brand_id": brand})
	day := func(n int) time.Time { return newest.AddDate(0, 0, -n) }
	row := `INSERT INTO search_daily (workspace_id, brand_id, integration_id, site, host, day, page, query, clicks, impressions, ctr, position)
		VALUES ($1, $2, $3, 'sc-domain:example.com', 'example.com', $4, $5, $6, $7, $8, 0, $9)`
	add := func(d int, page, q string, c, i, p float64) {
		exec(row, ws, brand, sc, day(d), "https://example.com"+page, q, c, i, p)
	}
	// Now: proxy pricing (two pages, as before), proxy list (new, across languages).
	add(1, "/a", "proxy pricing", 30, 200, 3)
	add(1, "/b", "proxy pricing", 5, 100, 8)
	add(1, "/c", "proxy list", 10, 100, 4)
	add(1, "/de/c", "proxy list", 2, 100, 9)
	// Before: proxy pricing (two pages) and old query, which no longer competes.
	add(30, "/a", "proxy pricing", 20, 200, 4)
	add(30, "/b", "proxy pricing", 8, 100, 7)
	add(30, "/x", "old query", 5, 100, 5)
	add(30, "/y", "old query", 5, 100, 6)

	exec(`INSERT INTO fixes (workspace_id, brand_id, source, subject_type, subject_id, rule, severity, group_key)
		VALUES ($1, $2, 'seo_keywords', 'seo_site', $3, 'keyword_cannibalised', 'warning', 'proxy pricing')`, ws, brand, site)

	r := owner.must(200, "GET", report, nil)
	by := map[string]map[string]any{}
	for _, c := range r["clusters"].([]any) {
		by[c.(map[string]any)["query"].(string)] = c.(map[string]any)
	}
	pricing, list := by["proxy pricing"], by["proxy list"]
	if pricing == nil || list == nil || len(by) != 2 {
		t.Fatalf("clusters: %v", r["clusters"])
	}
	if pricing["state"] != "ongoing" || pricing["keep"] != "https://example.com/a" || pricing["keep_reason"] != "clicks" ||
		pricing["locale"] != "same_locale" || pricing["split_clicks"] != 5.0 || pricing["fix"] == nil {
		t.Fatalf("proxy pricing: %v", pricing)
	}
	// /a converts 15% of its impressions; all 300 at that rate is 45, 10 more than now.
	if pricing["estimated_gain"] != 10.0 {
		t.Fatalf("gain: %v", pricing["estimated_gain"])
	}
	if list["state"] != "new" || list["locale"] != "cross_locale" || list["fix"] != nil {
		t.Fatalf("proxy list: %v", list)
	}
	res := r["resolved"].([]any)
	if len(res) != 1 || res[0].(map[string]any)["query"] != "old query" {
		t.Fatalf("resolved: %v", res)
	}
	sum := r["summary"].(map[string]any)
	if sum["open"] != 2.0 || sum["new"] != 1.0 || sum["resolved"] != 1.0 || sum["on_fixes"] != 1.0 || sum["cross_locale"] != 1.0 {
		t.Fatalf("summary: %v", sum)
	}

	// A person picks the other page; it holds, and clearing it goes back.
	primary := "/v1/brands/" + brand + "/seo-sites/" + site + "/cannibalisation/primary"
	owner.must(400, "PUT", primary, map[string]any{"query": "proxy pricing", "url": "https://other.example/a"})
	owner.must(200, "PUT", primary, map[string]any{"query": "proxy pricing", "url": "https://example.com/b"})
	r = owner.must(200, "GET", report, nil)
	for _, c := range r["clusters"].([]any) {
		c := c.(map[string]any)
		if c["query"] == "proxy pricing" && (c["keep"] != "https://example.com/b" || c["keep_reason"] != "chosen" || c["split_clicks"] != 30.0) {
			t.Fatalf("chosen: %v", c)
		}
	}
	owner.must(200, "PUT", primary, map[string]any{"query": "proxy pricing", "url": ""})
	r = owner.must(200, "GET", report, nil)
	for _, c := range r["clusters"].([]any) {
		c := c.(map[string]any)
		if c["query"] == "proxy pricing" && c["keep_reason"] != "clicks" {
			t.Fatalf("cleared: %v", c)
		}
	}
}
