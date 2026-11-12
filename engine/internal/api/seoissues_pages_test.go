package api_test

import (
	"context"
	"testing"
)

func TestCrawlIssuePageCountsAndLocaleFold(t *testing.T) {
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

	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "website": "northpeak.example"})
	site := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://northpeak.example/", "brand_id": brand["id"]})["id"].(string)
	run := one(`INSERT INTO crawl_runs (workspace_id, site_id, host, trigger, max_pages, delay_ms, obey_robots, status, finished_at, health_score, issues_errors, issues_warnings, issues_notices)
		VALUES ($1, $2, 'northpeak.example', 'manual', 100, 0, true, 'done', now(), 90, 5, 3, 1) RETURNING id::text`, ws, site)
	page := func(url string) string {
		return one(`INSERT INTO crawl_pages (workspace_id, run_id, url, depth, source, status, http_status, content_type)
			VALUES ($1, $2, $3, 1, 'link', 'fetched', 200, 'text/html') RETURNING id::text`, ws, run, url)
	}
	de := page("https://northpeak.example/de/products/a")
	fr := page("https://northpeak.example/fr/products/b")
	blog := page("https://northpeak.example/blog/hello")
	// Five warning hits on two product pages (hreflang counted per pair), plus one notice.
	exec(`INSERT INTO crawl_issues (workspace_id, run_id, page_id, rule, severity, template, detail) VALUES
		($1, $2, $3, 'hreflang_no_return', 'warning', '/de/products/*', 'fr'),
		($1, $2, $3, 'hreflang_no_return', 'warning', '/de/products/*', 'es'),
		($1, $2, $4, 'hreflang_no_return', 'warning', '/fr/products/*', 'de'),
		($1, $2, $3, 'page_broken', 'error', '/de/products/*', ''),
		($1, $2, $5, 'title_too_long', 'notice', '/blog/*', '')`, ws, run, de, fr, blog)

	got := owner.must(200, "GET", base+"/crawls/"+run, nil)
	iss := got["issues"].(map[string]any)
	if iss["errors"] != 5.0 || iss["warnings"] != 3.0 || iss["notices"] != 1.0 {
		t.Fatalf("stored hits left alone: %v", iss)
	}
	if iss["error_pages"] != 1.0 || iss["warning_pages"] != 2.0 || iss["notice_pages"] != 1.0 {
		t.Fatalf("unique pages: %v", iss)
	}

	plain := owner.must(200, "GET", base+"/crawls/"+run+"/issues", nil)
	folded := owner.must(200, "GET", base+"/crawls/"+run+"/issues?fold_locales=true", nil)
	var href map[string]any
	for _, row := range folded["data"].([]any) {
		m := row.(map[string]any)
		if m["id"] == "hreflang_no_return" {
			href = m
		}
	}
	if href == nil || href["pages"] != 2.0 || len(href["templates"].([]any)) != 1 {
		t.Fatalf("folded hreflang: %v", href)
	}
	tpl := href["templates"].([]any)[0].(map[string]any)
	if tpl["template"] != "/{locale}/products/*" || tpl["pages"] != 2.0 || len(tpl["locales"].([]any)) != 2 {
		t.Fatalf("folded template: %v", tpl)
	}

	var plainTpls int
	for _, row := range plain["data"].([]any) {
		if row.(map[string]any)["id"] == "hreflang_no_return" {
			plainTpls = len(row.(map[string]any)["templates"].([]any))
		}
	}
	if plainTpls != 2 {
		t.Fatalf("unfolded still has two templates: %d", plainTpls)
	}

	pages := owner.must(200, "GET", base+"/crawls/"+run+"/issues/hreflang_no_return?template=/{locale}/products/*&locale=de", nil)
	if len(pages["data"].([]any)) != 2 {
		t.Fatalf("de pages should be the two hits on the German page: %v", pages["data"])
	}
	pages = owner.must(200, "GET", base+"/crawls/"+run+"/issues/hreflang_no_return?template=/{locale}/products/*", nil)
	if len(pages["data"].([]any)) != 3 {
		t.Fatalf("all locale hits: %v", pages["data"])
	}
	// No template query must still list every page for the rule. A nil
	// text[] bind used to make this come back empty.
	pages = owner.must(200, "GET", base+"/crawls/"+run+"/issues/page_broken", nil)
	if len(pages["data"].([]any)) != 1 {
		t.Fatalf("unfiltered page_broken: %v", pages["data"])
	}

	siteGot := owner.must(200, "GET", base+"/seo-sites/"+site, nil)
	last := siteGot["last_crawl"].(map[string]any)["issues"].(map[string]any)
	if last["error_pages"] != 1.0 || last["warning_pages"] != 2.0 {
		t.Fatalf("site last crawl: %v", last)
	}
}
