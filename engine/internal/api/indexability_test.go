package api_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/indexability"
)

func TestIndexability(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		page := func(head string) {
			io.WriteString(w, `<html><head><title>A page with a long enough title</title>`+head+`</head><body><a href="/">home</a></body></html>`)
		}
		switch r.URL.Path {
		case "/robots.txt":
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, "User-agent: *\nSitemap: http://"+r.Host+"/sitemap.xml\n")
		case "/sitemap.xml":
			w.Header().Set("Content-Type", "application/xml")
			io.WriteString(w, `<urlset><url><loc>http://`+r.Host+`/</loc></url><url><loc>http://`+r.Host+`/a</loc></url>`+
				`<url><loc>http://`+r.Host+`/old</loc></url><url><loc>http://`+r.Host+`/c</loc></url></urlset>`)
		case "/":
			io.WriteString(w, `<html><head><title>Home page title that is long enough</title></head><body>`+
				`<a href="/a">a</a><a href="/b">b</a><a href="/old">old</a><a href="/noidx">noidx</a></body></html>`)
		case "/a", "/b", "/c":
			page("")
		case "/noidx":
			page(`<meta name="robots" content="noindex">`)
		case "/old":
			http.Redirect(w, r, "/a", http.StatusMovedPermanently)
		default:
			http.NotFound(w, r)
		}
	}))
	defer site.Close()
	u := func(path string) string { return site.URL + path }

	id := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": u("/")})["id"].(string)
	path := base + "/seo-sites/" + id + "/indexability"

	// Before anything: every source is missing, and the report says how
	// to add each.
	r := owner.must(200, "GET", path, nil)
	if r["summary"].(map[string]any)["pages"] != 0.0 || len(r["gaps"].([]any)) != 3 || len(r["history"].([]any)) != 0 {
		t.Fatalf("empty: %v", r)
	}

	e.runCrawls(t)
	rid := owner.must(202, "POST", base+"/seo-sites/"+id+"/crawls", nil)["id"].(string)
	waitFor(t, owner, base+"/crawls/"+rid, func(m map[string]any) bool { return m["status"] == "done" })

	// Search Console and an SEO tool stand in through stored rows: the
	// report only reads what the syncs stored.
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})["id"].(string)
	newest := time.Now().UTC().AddDate(0, 0, -3).Truncate(24 * time.Hour)
	exec(`INSERT INTO search_syncs (integration_id, site, workspace_id, host, oldest_day, newest_day, last_run_at) VALUES ($1, 'sc-domain:127.0.0.1', $2, '127.0.0.1', $3, $4, now())`,
		conn, ws, newest.AddDate(0, 0, -60), newest)
	for _, p := range []string{"/", "/noidx", "/gone"} {
		exec(`INSERT INTO search_daily (workspace_id, integration_id, site, host, day, page, query, clicks, impressions, ctr, position)
			VALUES ($1, $2, 'sc-domain:127.0.0.1', '127.0.0.1', $3, $4, 'northpeak', 2, 40, 0.05, 4)`, ws, conn, newest.AddDate(0, 0, -1), u(p))
	}
	// Outside the 28 days: doesn't count.
	exec(`INSERT INTO search_daily (workspace_id, integration_id, site, host, day, page, query, clicks, impressions, ctr, position)
		VALUES ($1, $2, 'sc-domain:127.0.0.1', '127.0.0.1', $3, $4, 'northpeak', 2, 40, 0.05, 4)`, ws, conn, newest.AddDate(0, 0, -40), u("/a"))
	inspect := func(p, verdict, coverage, canonical string) {
		exec(`INSERT INTO url_inspections (workspace_id, integration_id, site, host, url, verdict, coverage, google_canonical, user_canonical)
			VALUES ($1, $2, 'sc-domain:127.0.0.1', '127.0.0.1', $3, $4, $5, $6, $3)`, ws, conn, u(p), verdict, coverage, canonical)
	}
	inspect("/a", "NEUTRAL", "Crawled - currently not indexed", "")
	inspect("/gone", "NEUTRAL", "Not found (404)", "")
	inspect("/c", "PASS", "Submitted and indexed", u("/a"))
	exec(`INSERT INTO seo_snapshots (workspace_id, integration_id, connector, host, report, day, key, url, metrics)
		VALUES ($1, $2, 'ahrefs', '127.0.0.1', 'keywords', $3, 'northpeak boots', $4, '{"traffic": 30}'),
		       ($1, $2, 'ahrefs', '127.0.0.1', 'keywords', $3, 'northpeak jackets', $4, '{"traffic": 12}')`, ws, conn, newest, u("/b"))

	// Take today's snapshot, as a crawl or a Search Console sync does.
	e.runServerJobs(t)
	if err := indexability.Enqueue(ctx, e.pool, ws, id, "test"); err != nil {
		t.Fatal(err)
	}
	r = waitFor(t, owner, path, func(m map[string]any) bool {
		h := m["history"].([]any)
		return len(h) == 1 && h[0].(map[string]any)["indexed"] == 4.0
	})
	sum := r["summary"].(map[string]any)
	// /, /a, /b, /c, /old, /noidx crawled; /gone only known to Google.
	if sum["pages"] != 7.0 || sum["crawled"] != 6.0 || sum["indexable"] != 4.0 || sum["in_sitemap"] != 4.0 ||
		sum["indexed"] != 4.0 || sum["likely_indexed"] != 1.0 || sum["not_indexed"] != 1.0 || sum["unknown"] != 1.0 {
		t.Fatalf("summary: %v", sum)
	}
	if len(r["gaps"].([]any)) != 0 {
		t.Fatalf("gaps: %v", r["gaps"])
	}
	src := r["sources"].(map[string]any)
	if src["seo_tool"].(map[string]any)["connector"] != "ahrefs" || src["search_console"].(map[string]any)["inspected"] != 3.0 ||
		src["crawl"].(map[string]any)["sitemaps"] != true {
		t.Fatalf("sources: %v", src)
	}
	counts := map[string]float64{}
	for _, x := range r["issues"].([]any) {
		i := x.(map[string]any)
		counts[i["id"].(string)] = i["pages"].(float64)
	}
	if counts["indexable_not_indexed"] != 1 || counts["sitemap_not_indexable"] != 1 || counts["indexed_not_indexable"] != 1 ||
		counts["google_canonical_differs"] != 1 || counts["not_found_by_crawl"] != 1 || counts["sources_disagree"] != 1 ||
		counts["missing_from_sitemap"] != 1 {
		t.Fatalf("issues: %v", counts)
	}

	// Each page, with what every source said.
	pages := map[string]map[string]any{}
	all := owner.must(200, "GET", path+"/pages", nil)
	for _, x := range all["data"].([]any) {
		p := x.(map[string]any)
		pages[p["url"].(string)] = p
	}
	has := func(p, issue string) bool { return slices.Contains(pages[u(p)]["issues"].([]any), any(issue)) }
	switch {
	case all["total"] != 7.0:
		t.Fatalf("total: %v", all["total"])
	case pages[u("/")]["verdict"] != "indexed" || pages[u("/")]["impressions"] != 40.0:
		t.Fatalf("/: %v", pages[u("/")])
	case pages[u("/a")]["verdict"] != "not_indexed" || !has("/a", "indexable_not_indexed") || pages[u("/a")]["impressions"] != 0.0:
		t.Fatalf("/a: %v", pages[u("/a")])
	case pages[u("/b")]["verdict"] != "likely_indexed" || pages[u("/b")]["seo_keywords"] != 2.0 || pages[u("/b")]["seo_traffic"] != 42.0 || !has("/b", "missing_from_sitemap"):
		t.Fatalf("/b: %v", pages[u("/b")])
	case pages[u("/c")]["verdict"] != "indexed" || !has("/c", "google_canonical_differs"):
		t.Fatalf("/c: %v", pages[u("/c")])
	case pages[u("/old")]["indexable"] != false || pages[u("/old")]["not_indexable_reason"] != "redirect" || !has("/old", "sitemap_not_indexable"):
		t.Fatalf("/old: %v", pages[u("/old")])
	case pages[u("/noidx")]["not_indexable_reason"] != "noindex" || !has("/noidx", "indexed_not_indexable"):
		t.Fatalf("/noidx: %v", pages[u("/noidx")])
	case pages[u("/gone")]["crawled"] != false || !has("/gone", "not_found_by_crawl") || !has("/gone", "sources_disagree"):
		t.Fatalf("/gone: %v", pages[u("/gone")])
	}
	// Pages with an error come first.
	if first := all["data"].([]any)[0].(map[string]any)["url"]; first != u("/a") && first != u("/old") {
		t.Fatalf("first: %v", first)
	}

	// Filters and paging.
	if l := owner.must(200, "GET", path+"/pages?verdict=not_indexed", nil)["data"].([]any); len(l) != 1 || l[0].(map[string]any)["url"] != u("/a") {
		t.Fatalf("not indexed: %v", l)
	}
	if l := owner.must(200, "GET", path+"/pages?issue=sitemap_not_indexable", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("issue: %v", l)
	}
	if l := owner.must(200, "GET", path+"/pages?indexable=false", nil); l["total"] != 2.0 {
		t.Fatalf("not indexable: %v", l)
	}
	if l := owner.must(200, "GET", path+"/pages?q=NOIDX", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("search: %v", l)
	}
	p1 := owner.must(200, "GET", path+"/pages?limit=5", nil)
	if len(p1["data"].([]any)) != 5 || p1["next_offset"] != 5.0 {
		t.Fatalf("page 1: %v", p1)
	}
	if p2 := owner.must(200, "GET", path+"/pages?limit=5&offset=5", nil); len(p2["data"].([]any)) != 2 || p2["next_offset"] != nil {
		t.Fatalf("page 2: %v", p2)
	}
	owner.must(400, "GET", path+"/pages?verdict=maybe", nil)
	owner.must(400, "GET", path+"/pages?issue=nope", nil)
	owner.must(400, "GET", path+"/pages?indexable=yes", nil)
	owner.must(404, "GET", base+"/seo-sites/00000000-0000-0000-0000-000000000000/indexability", nil)

	// Another snapshot the same day replaces the first.
	if err := indexability.Enqueue(ctx, e.pool, ws, id, "again"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if h := owner.must(200, "GET", path, nil)["history"].([]any); len(h) != 1 {
		t.Fatalf("history: %v", h)
	}
}
