package api_test

import (
	"context"
	"testing"
	"time"
)

func TestSEOTechnical(t *testing.T) {
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
	one := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}

	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Tech Example", "website": "https://example.com"})["id"].(string)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Other Example", "website": "https://other.example"})["id"].(string)
	report := "/v1/brands/" + brand + "/seo/technical"

	if r := owner.must(200, "GET", report, nil); len(r["rules"].([]any)) != 0 || len(r["sites"].([]any)) != 0 {
		t.Fatalf("empty: %v", r)
	}

	site := one(`INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, 'example.com', 'https://example.com/', 'tok') RETURNING id::text`, ws, brand)
	otherSite := one(`INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, 'other.example', 'https://other.example/', 'tok') RETURNING id::text`, ws, other)
	run := func(site, host string, ago int) string {
		return one(`INSERT INTO crawl_runs (workspace_id, site_id, host, trigger, max_pages, delay_ms, obey_robots, status, finished_at)
			VALUES ($1, $2, $3, 'manual', 100, 0, true, 'done', now() - make_interval(days => $4)) RETURNING id::text`, ws, site, host, ago)
	}
	prev, cur := run(site, "example.com", 8), run(site, "example.com", 1)
	page := func(run, path string) string {
		return one(`INSERT INTO crawl_pages (workspace_id, run_id, url, depth, source, status, http_status) VALUES ($1, $2, $3, 1, 'link', 'fetched', 200) RETURNING id::text`, ws, run, "https://example.com"+path)
	}
	issue := func(run, page, rule, sev, tpl string) {
		exec(`INSERT INTO crawl_issues (workspace_id, run_id, page_id, rule, severity, template) VALUES ($1, $2, $3, $4, $5, $6)`, ws, run, page, rule, sev, tpl)
	}
	// The crawl before: /a and /old broken; /de/a has no issue.
	pa, pold := page(prev, "/a"), page(prev, "/old")
	page(prev, "/de/a")
	issue(prev, pa, "broken", "error", "/a")
	issue(prev, pold, "broken", "error", "/old")
	// The latest: /a still broken, /old fixed, /de/a newly broken (same
	// template as /a once the language is taken out), /new broken but
	// not reached before, so not counted as new. Two occurrences on one
	// page count once.
	ca, cde, cnew := page(cur, "/a"), page(cur, "/de/a"), page(cur, "/new")
	page(cur, "/old")
	issue(cur, ca, "broken", "error", "/a")
	exec(`INSERT INTO crawl_issues (workspace_id, run_id, page_id, rule, severity, template, detail) VALUES ($1, $2, $3, 'broken', 'error', '/a', 'a second broken link')`, ws, cur, ca)
	issue(cur, cde, "broken", "error", "/de/a")
	issue(cur, cnew, "broken", "error", "/new")
	issue(cur, ca, "title_missing", "warning", "/a")
	exec(`INSERT INTO crawl_robots (run_id, host, status, body) VALUES ($1, 'example.com', 200, 'User-agent: *'), ($2, 'example.com', 200, 'User-agent: *\nDisallow: /')`, prev, cur)

	// Another brand's crawl never shows.
	or := run(otherSite, "other.example", 1)
	issue(or, one(`INSERT INTO crawl_pages (workspace_id, run_id, url, depth, source, status, http_status) VALUES ($1, $2, 'https://other.example/x', 1, 'link', 'fetched', 500) RETURNING id::text`, ws, or), "server_error", "error", "/x")

	// Clicks put the warning on /a ahead by clicks? Both rules touch /a;
	// broken also touches /de/a.
	integ := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "name": "Search", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})["id"].(string)
	day := time.Now().UTC().AddDate(0, 0, -3).Truncate(24 * time.Hour)
	seg := `INSERT INTO search_segments (workspace_id, brand_id, integration_id, site, host, day, page, country, device, clicks, impressions, position)
		VALUES ($1, $2, $3, 'sc-domain:example.com', 'example.com', $4, $5, 'usa', 'DESKTOP', $6, 1000, 3)`
	exec(seg, ws, brand, integ, day, "https://example.com/a", 50)
	exec(seg, ws, brand, integ, day, "https://example.com/de/a", 20)

	exec(`INSERT INTO page_vitals (workspace_id, brand_id, host, path, device, lcp_ms, cls, occurred_at)
		SELECT $1::uuid, $2::uuid, 'example.com', '/slow', 'mobile', 5200, 0.3, now() - interval '2 days' FROM generate_series(1, 25)
		UNION ALL SELECT $1, $2, 'example.com', '/fast', 'desktop', 1200, 0.01, now() - interval '2 days' FROM generate_series(1, 25)
		UNION ALL SELECT $1, $2, 'example.com', '/rare', 'desktop', 9000, 0.9, now() - interval '2 days' FROM generate_series(1, 5)`, ws, brand)

	r := owner.must(200, "GET", report, nil)
	sites := r["sites"].([]any)
	if len(sites) != 1 {
		t.Fatalf("sites: %v", sites)
	}
	s := sites[0].(map[string]any)
	if s["crawl_id"] != cur || s["previous_crawl_id"] != prev || len(s["robots_changed"].([]any)) != 1 {
		t.Fatalf("site: %v", s)
	}
	rules := map[string]map[string]any{}
	var order []string
	for _, x := range r["rules"].([]any) {
		m := x.(map[string]any)
		rules[m["rule"].(string)] = m
		order = append(order, m["rule"].(string))
	}
	if _, ok := rules["server_error"]; ok {
		t.Fatalf("another brand's issue: %v", order)
	}
	b := rules["broken"]
	if b == nil || b["pages"] != 3.0 || b["previous_pages"] != 2.0 || b["new_pages"] != 1.0 || b["fixed_pages"] != 1.0 || b["templates"] != 2.0 || b["clicks"] != 70.0 {
		t.Fatalf("broken: %v", b)
	}
	if top := b["top_pages"].([]any); len(top) != 2 || top[0].(map[string]any)["url"] != "https://example.com/a" {
		t.Fatalf("top pages: %v", top)
	}
	if w := rules["title_missing"]; w == nil || w["pages"] != 1.0 || w["clicks"] != 50.0 || w["new_pages"] != 1.0 {
		t.Fatalf("title: %v", w)
	}
	if order[0] != "broken" {
		t.Fatalf("order: %v", order)
	}
	sum := r["summary"].(map[string]any)
	if sum["errors"] != 3.0 || sum["warnings"] != 1.0 || sum["new_pages"] != 2.0 || sum["fixed_pages"] != 1.0 || sum["robots_changed"] != 1.0 || sum["poor_vitals"] != 1.0 {
		t.Fatalf("summary: %v", sum)
	}
	vit := r["vitals"].([]any)
	if len(vit) != 2 {
		t.Fatalf("vitals: %v", vit)
	}
	if v := vit[0].(map[string]any); v["path"] != "/slow" || len(v["poor"].([]any)) != 2 {
		t.Fatalf("worst vitals: %v", v)
	}

	// The other brand sees only its own site.
	o := owner.must(200, "GET", "/v1/brands/"+other+"/seo/technical", nil)
	if len(o["sites"].([]any)) != 1 || len(o["rules"].([]any)) != 1 {
		t.Fatalf("other brand: %v", o)
	}
}
