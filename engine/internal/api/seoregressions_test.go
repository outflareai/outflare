package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/seointel"
)

func TestSEORegressions(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	site := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.example.com/"})["id"].(string)
	sb := base + "/seo-sites/" + site
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	check := func() seointel.RegressionChanges {
		t.Helper()
		ch, err := seointel.RegressionsForSites(ctx, e.pool, ws, []string{site})
		if err != nil {
			t.Fatal(err)
		}
		return ch
	}

	// Nothing stored yet: nothing to compare.
	if ch := check(); ch.Found != 0 {
		t.Fatalf("empty: %+v", ch)
	}

	// A connection stands in for Search Console and PostHog: the checks
	// only read what the syncs stored.
	conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})["id"].(string)
	newest := time.Now().UTC().AddDate(0, 0, -3).Truncate(24 * time.Hour)
	exec(`INSERT INTO search_syncs (integration_id, site, workspace_id, host, oldest_day, newest_day, last_run_at) VALUES ($1, 'sc-domain:example.com', $2, 'example.com', $3, $4, now())`,
		conn, ws, newest.AddDate(0, 0, -60), newest)
	for d := 0; d < 35; d++ {
		day := newest.AddDate(0, 0, -d)
		pricing, blog := 10, 4 // pricing: 70 a week, then 7; blog steady at 28
		if d < 7 {
			pricing = 1
		}
		exec(`INSERT INTO search_daily (workspace_id, integration_id, site, host, day, page, query, clicks, impressions, ctr, position)
			VALUES ($1, $2, 'sc-domain:example.com', 'example.com', $3, 'https://www.example.com/pricing', 'proxy pricing', $4, 100, 0.1, 3),
			       ($1, $2, 'sc-domain:example.com', 'example.com', $3, 'https://www.example.com/blog', 'proxy blog', $5, 100, 0.04, 8)`, ws, conn, day, pricing, blog)
	}
	exec(`INSERT INTO url_inspections (workspace_id, integration_id, site, host, url, verdict, coverage, previous_verdict, verdict_changed_at)
		VALUES ($1, $2, 'sc-domain:example.com', 'www.example.com', 'https://www.example.com/blog', 'NEUTRAL', 'Crawled - currently not indexed', 'PASS', now())`, ws, conn)
	var tracked string
	if err := e.pool.QueryRow(ctx, `INSERT INTO tracked_keywords (workspace_id, site_id, keyword) VALUES ($1, $2, 'proxy pricing') RETURNING id::text`, ws, site).Scan(&tracked); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO rank_observations (workspace_id, tracked_id, day, position, depth, engine) VALUES ($1, $2, $3, 3, 100, 'google'), ($1, $2, $4, 12, 100, 'google')`,
		ws, tracked, newest.AddDate(0, 0, -7), newest)
	exec(`INSERT INTO crawl_runs (workspace_id, site_id, host, status, trigger, max_pages, delay_ms, obey_robots, health_score, created_at)
		VALUES ($1, $2, 'www.example.com', 'done', 'schedule', 100, 0, true, 92, now() - interval '7 days'),
		       ($1, $2, 'www.example.com', 'done', 'schedule', 100, 0, true, 75, now())`, ws, site)
	exec(`INSERT INTO page_conversion_syncs (integration_id, site, workspace_id, events, oldest_day, newest_day) VALUES ($1, '1', $2, '{signed_up}', $3, $4)`,
		conn, ws, newest.AddDate(0, 0, -60), newest)
	for d := 0; d < 35; d++ {
		conv := 2
		if d < 7 {
			conv = 0
		}
		exec(`INSERT INTO page_conversions (workspace_id, integration_id, site, day, host, path, sessions, visitors, conversions)
			VALUES ($1, $2, '1', $3, 'www.example.com', '/pricing', 30, 30, $4)`, ws, conn, newest.AddDate(0, 0, -d), conv)
	}

	ch := check()
	if ch.Found != 5 || ch.New != 5 {
		t.Fatalf("found: %+v", ch)
	}
	if again := check(); again.Found != 0 || again.New != 0 || again.Resolved != 0 {
		t.Fatalf("the same regressions again: %+v", again)
	}
	list := owner.must(200, "GET", sb+"/regressions", nil)["data"].([]any)
	byKind := map[string]map[string]any{}
	for _, x := range list {
		x := x.(map[string]any)
		byKind[x["kind"].(string)] = x
		if x["fix_id"] == nil || x["status"] != "open" {
			t.Fatalf("regression: %v", x)
		}
	}
	if c := byKind["search_clicks_dropped"]; c["subject"] != "https://www.example.com/pricing" || c["before"].(float64) != 70 || c["after"].(float64) != 7 {
		t.Fatalf("clicks: %v", c)
	}
	if r := byKind["rank_dropped"]; r["subject"] != "proxy pricing (US, desktop)" || r["before"].(float64) != 3 || r["after"].(float64) != 12 {
		t.Fatalf("rank: %v", r)
	}
	if byKind["page_deindexed"]["subject"] != "https://www.example.com/blog" || byKind["health_dropped"]["after"].(float64) != 75 ||
		byKind["conversions_dropped"]["subject"] != "https://www.example.com/pricing" {
		t.Fatalf("regressions: %v", byKind)
	}
	p := owner.must(200, "GET", base+"/fixes/"+byKind["rank_dropped"]["fix_id"].(string)+"/prompt", nil)
	if p["prompt"] == nil {
		t.Fatalf("prompt: %v", p)
	}
	owner.must(400, "GET", sb+"/regressions?kind=traffic", nil)

	// The keyword recovers and the page is back in the index.
	exec(`UPDATE rank_observations SET position = 4 WHERE tracked_id = $1 AND day = $2`, tracked, newest)
	exec(`UPDATE url_inspections SET verdict = 'PASS' WHERE workspace_id = $1`, ws)
	if ch := check(); ch.Resolved != 2 || ch.Confirmed != 2 {
		t.Fatalf("recovered: %+v", ch)
	}
	if n := len(owner.must(200, "GET", sb+"/regressions", nil)["data"].([]any)); n != 3 {
		t.Fatalf("open: %d", n)
	}
	res := owner.must(200, "GET", sb+"/regressions?status=resolved", nil)["data"].([]any)
	if len(res) != 2 || res[0].(map[string]any)["resolved_at"] == nil || res[0].(map[string]any)["fix_id"] != nil {
		t.Fatalf("resolved: %v", res)
	}
	fx := owner.must(200, "GET", base+"/fixes?source=seo_keywords&subject_id="+site+"&status=done", nil)["data"].([]any)
	if len(fx) != 2 {
		t.Fatalf("confirmed fixes: %v", fx)
	}

	// Another workspace sees none of it.
	other := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Other"})["id"].(string)
	owner.must(404, "GET", "/v1/workspaces/"+other+"/seo-sites/"+site+"/regressions", nil)
}
