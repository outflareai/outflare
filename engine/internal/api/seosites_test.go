package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// runCrawls works crawl jobs until the test ends.
func (e *env) runCrawls(t *testing.T) {
	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 20 * time.Millisecond
	e.crawl.Register(q)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { q.Run(ctx, 2); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })
}

// pagesByPath indexes a crawl's pages by path.
func pagesByPath(c *client, path string) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, p := range c.must(200, "GET", path, nil)["data"].([]any) {
		p := p.(map[string]any)
		u, _ := url.Parse(p["url"].(string))
		out[u.Path] = p
	}
	return out
}

func TestSEOSitesVerifyAndCrawl(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	var token atomic.Value
	token.Store("")
	var uas atomic.Value
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uas.Store(r.Header.Get("User-Agent"))
		html := func(body string) {
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, "<html><body>"+body+"</body></html>")
		}
		switch r.URL.Path {
		case "/robots.txt":
			io.WriteString(w, "User-agent: *\nDisallow: /secret\nSitemap: http://"+r.Host+"/sitemap.xml\n")
		case "/sitemap.xml":
			w.Header().Set("Content-Type", "application/xml")
			io.WriteString(w, `<urlset><url><loc>http://`+r.Host+`/a</loc></url><url><loc>https://elsewhere.example/x</loc></url></urlset>`)
		case "/.well-known/outflare-site-verification.txt":
			if v := token.Load().(string); v != "" {
				io.WriteString(w, v+"\n")
				return
			}
			http.NotFound(w, r)
		case "/":
			html(`<a href="/a">a</a> <a href="/b">b</a> <a href="/secret/x">secret</a> <a href="/redir">r</a> <a href="/gone">gone</a> <a href="mailto:x@example.com">m</a>`)
		case "/a":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<html lang="en"><head><title>Page A</title><meta name="description" content="About A.">`+
				`<link rel="canonical" href="/a"><script type="application/ld+json">{"@type":"Product"}</script></head>`+
				`<body><h1>A</h1><img src="x.png"><a href="/">home</a> <a href="/gone">broken</a> <a href="https://elsewhere.example/" rel="sponsored">ad</a></body></html>`)
		case "/b":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<html><head><meta name="robots" content="nofollow"></head><body><a href="/c">c</a></body></html>`)
		case "/redir":
			http.Redirect(w, r, "/b", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer site.Close()

	owner.must(400, "POST", base+"/seo-sites", map[string]any{})
	owner.must(400, "POST", base+"/seo-sites", map[string]any{"url": "ftp://example.com"})
	owner.must(400, "POST", base+"/seo-sites", map[string]any{"url": site.URL, "max_pages": 500})
	s := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": site.URL})
	id := s["id"].(string)
	if s["verified"] != false || s["max_pages"] != 100.0 || s["obey_robots"] != true || s["limits"].(map[string]any)["max_pages"] != 100.0 {
		t.Fatalf("new site: %v", s)
	}
	owner.must(400, "POST", base+"/seo-sites", map[string]any{"url": site.URL + "/other"})
	if n := len(owner.must(200, "GET", base+"/seo-sites", nil)["data"].([]any)); n != 1 {
		t.Fatalf("%d sites", n)
	}

	// Ignoring robots.txt and big crawls wait for proof of ownership.
	owner.must(400, "PATCH", base+"/seo-sites/"+id, map[string]any{"obey_robots": false})
	owner.must(400, "PATCH", base+"/seo-sites/"+id, map[string]any{"crawl_delay_ms": 500})
	e1 := owner.must(409, "POST", base+"/seo-sites/"+id+"/verify", nil)
	if e1["error"].(map[string]any)["code"] != "verification_failed" {
		t.Fatalf("verify: %v", e1)
	}
	v := s["verification"].(map[string]any)
	token.Store(v["file_body"].(string))
	s = owner.must(200, "POST", base+"/seo-sites/"+id+"/verify", nil)
	if s["verified"] != true || s["verify_method"] != "file" || s["limits"].(map[string]any)["may_ignore_robots"] != true {
		t.Fatalf("verified: %v", s)
	}
	owner.must(200, "PATCH", base+"/seo-sites/"+id, map[string]any{"max_pages": 1000, "crawl_delay_ms": 250, "schedule": "weekly"})

	// One crawl at a time.
	run := owner.must(202, "POST", base+"/seo-sites/"+id+"/crawls", nil)
	rid := run["id"].(string)
	if run["status"] != "queued" || run["max_pages"] != 1000.0 {
		t.Fatalf("run: %v", run)
	}
	if c := owner.must(409, "POST", base+"/seo-sites/"+id+"/crawls", nil)["error"].(map[string]any)["code"]; c != "crawl_running" {
		t.Fatalf("second crawl: %v", c)
	}
	e.runCrawls(t)
	run = waitFor(t, owner, base+"/crawls/"+rid, func(m map[string]any) bool { return m["status"] != "queued" && m["status"] != "running" })
	if run["status"] != "done" || run["reason"] != "done" {
		t.Fatalf("finished: %v", run)
	}
	if ua, _ := uas.Load().(string); !strings.Contains(ua, "OutflareBot/1.0") {
		t.Fatalf("user agent %q", ua)
	}

	pages := pagesByPath(owner, base+"/crawls/"+rid+"/pages")
	for path, want := range map[string]string{"/": "fetched", "/a": "fetched", "/b": "fetched", "/redir": "fetched", "/gone": "fetched", "/secret/x": "blocked"} {
		if p := pages[path]; p == nil || p["status"] != want {
			t.Errorf("%s: %v", path, p)
		}
	}
	if pages["/secret/x"]["reason"] != "robots" || pages["/gone"]["http_status"] != 404.0 || pages["/b"]["nofollow"] != true ||
		!strings.HasSuffix(pages["/redir"]["redirect_to"].(string), "/b") || pages["/a"]["source"] != "sitemap" {
		t.Fatalf("pages: %v", pages)
	}
	if pages["/c"] != nil {
		t.Fatal("followed a nofollow page's link")
	}
	// On-page facts and links, in and out.
	pa := pages["/a"]
	if pa["title"] != "Page A" || pa["meta_description"] != "About A." || pa["lang"] != "en" || pa["images_no_alt"] != 1.0 ||
		pa["schema_types"].([]any)[0] != "Product" || pa["h1"].([]any)[0] != "A" || !strings.HasSuffix(pa["canonical"].(string), "/a") {
		t.Fatalf("page a: %v", pa)
	}
	if pages["/gone"]["links_in"] != 2.0 {
		t.Fatalf("links in: %v", pages["/gone"])
	}
	one := owner.must(200, "GET", base+"/crawls/"+rid+"/pages/"+pa["id"].(string), nil)
	out := one["links_out"].([]any)
	if len(out) != 3 || out[1].(map[string]any)["http_status"] != 404.0 || out[2].(map[string]any)["internal"] != false ||
		out[2].(map[string]any)["nofollow"] != true || len(one["links_in"].([]any)) != 1 {
		t.Fatalf("page detail: %v", one)
	}
	owner.must(404, "GET", base+"/crawls/"+rid+"/pages/999999999", nil)
	owner.must(404, "GET", base+"/crawls/"+rid+"/pages/nope", nil)
	if b := owner.must(200, "GET", base+"/crawls/"+rid+"/pages?http_status=404", nil)["data"].([]any); len(b) != 1 {
		t.Fatalf("404 filter: %v", b)
	}
	if run["pages_blocked"] != 1.0 || run["pages_fetched"] != 5.0 {
		t.Fatalf("counts: %v", run)
	}
	// The audit: issues by rule and template, the pages behind each, and a score.
	if run["health_score"] == nil || run["issues"].(map[string]any)["errors"].(float64) < 2 {
		t.Fatalf("audit: %v", run)
	}
	issues := map[string]map[string]any{}
	for _, i := range owner.must(200, "GET", base+"/crawls/"+rid+"/issues", nil)["data"].([]any) {
		issues[i.(map[string]any)["id"].(string)] = i.(map[string]any)
	}
	if issues["page_broken"]["pages"] != 1.0 || issues["link_broken"]["pages"] != 2.0 || issues["page_broken"]["fix"] == "" {
		t.Fatalf("issues: %v", issues)
	}
	broken := owner.must(200, "GET", base+"/crawls/"+rid+"/issues/page_broken", nil)["data"].([]any)
	if len(broken) != 1 || !strings.HasSuffix(broken[0].(map[string]any)["url"].(string), "/gone") || broken[0].(map[string]any)["http_status"] != 404.0 {
		t.Fatalf("broken pages: %v", broken)
	}
	owner.must(404, "GET", base+"/crawls/"+rid+"/issues/no_such_rule", nil)
	owner.must(400, "GET", base+"/crawls/"+rid+"/issues?severity=bad", nil)
	for _, i := range owner.must(200, "GET", base+"/crawls/"+rid+"/issues?severity=notice", nil)["data"].([]any) {
		if i.(map[string]any)["severity"] != "notice" {
			t.Fatalf("severity filter: %v", i)
		}
	}
	if rules := owner.must(200, "GET", base+"/seo-rules", nil)["data"].([]any); len(rules) < 20 {
		t.Fatalf("rules: %d", len(rules))
	}
	if b := owner.must(200, "GET", base+"/crawls/"+rid+"/pages?status=blocked", nil)["data"].([]any); len(b) != 1 {
		t.Fatalf("blocked filter: %v", b)
	}
	if p := owner.must(200, "GET", base+"/crawls/"+rid+"/pages?limit=2", nil); p["next_cursor"] == nil {
		t.Fatalf("paging: %v", p)
	}
	owner.must(409, "POST", base+"/crawls/"+rid+"/stop", nil)
	if l := owner.must(200, "GET", base+"/crawls?site="+id, nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("crawls: %v", l)
	}
	if lc := owner.must(200, "GET", base+"/seo-sites/"+id, nil)["last_crawl"].(map[string]any); lc["id"] != rid {
		t.Fatalf("last crawl: %v", lc)
	}

	// The owner may audit what robots.txt hides from crawlers.
	owner.must(200, "PATCH", base+"/seo-sites/"+id, map[string]any{"obey_robots": false})
	r2 := owner.must(202, "POST", base+"/seo-sites/"+id+"/crawls", nil)["id"].(string)
	waitFor(t, owner, base+"/crawls/"+r2, func(m map[string]any) bool { return m["status"] == "done" })
	if p := pagesByPath(owner, base+"/crawls/"+r2+"/pages")["/secret/x"]; p["status"] != "fetched" {
		t.Fatalf("robots ignored: %v", p)
	}

	// Hosts on the opt-out list are never crawled.
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO crawl_optouts (host) VALUES ('127.0.0.1')`); err != nil {
		t.Fatal(err)
	}
	if c := owner.must(409, "POST", base+"/seo-sites/"+id+"/crawls", nil)["error"].(map[string]any)["code"]; c != "crawl_opted_out" {
		t.Fatalf("opted out: %v", c)
	}
	owner.must(204, "DELETE", base+"/seo-sites/"+id, nil)
	owner.must(404, "GET", base+"/seo-sites/"+id, nil)
	owner.must(404, "GET", base+"/crawls/"+rid, nil)

	seen := map[string]bool{}
	for _, ev := range got() {
		seen[ev.Name+"/"+ev.Outcome] = true
	}
	for _, name := range []string{"seo_site.created/success", "seo_site.verified/success", "seo_site.updated/success",
		"crawl.started/success", "crawl.finished/success", "seo_site.deleted/success"} {
		if !seen[name] {
			t.Errorf("no %s event", name)
		}
	}
}

func TestSEOSiteSimulatedInTestWorkspace(t *testing.T) {
	e := freshEnv(t)
	owner, _ := setupOwner(t, e.h)
	ws := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Sandbox", "test": true})["id"].(string)
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Example", "website": "example.com"})

	// The site joins the brand whose domain it is, and passes the check
	// without one.
	s := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "www.example.com"})
	if s["brand_id"] != brand["id"] || s["start_url"] != "https://www.example.com/" || s["host"] != "www.example.com" {
		t.Fatalf("site: %v", s)
	}
	if s = owner.must(200, "POST", base+"/seo-sites/"+s["id"].(string)+"/verify", nil); s["verify_method"] != "test" {
		t.Fatalf("verify: %v", s)
	}
	if n := len(owner.must(200, "GET", base+"/seo-sites?brand=example", nil)["data"].([]any)); n != 1 {
		t.Fatalf("by brand: %d", n)
	}
	e.runCrawls(t)
	rid := owner.must(202, "POST", base+"/seo-sites/"+s["id"].(string)+"/crawls", nil)["id"].(string)
	run := waitFor(t, owner, base+"/crawls/"+rid, func(m map[string]any) bool { return m["status"] == "done" || m["status"] == "failed" })
	if run["status"] != "done" {
		t.Fatalf("run: %v", run)
	}
	pages := pagesByPath(owner, base+"/crawls/"+rid+"/pages")
	if pages["/private/report"]["status"] != "blocked" || pages["/missing"]["http_status"] != 404.0 || pages["/blog/"]["noindex"] != true ||
		pages["/about"]["status"] != "fetched" || pages["/old-about"]["http_status"] != 301.0 {
		t.Fatalf("pages: %v", pages)
	}

	// Stopping a crawl keeps what it read.
	other := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://another.example"})
	owner.must(200, "PATCH", base+"/seo-sites/"+other["id"].(string), map[string]any{"status": "paused"})
	owner.must(409, "POST", base+"/seo-sites/"+other["id"].(string)+"/crawls", nil)
}

// fakeGeonode is Geonode's Scraper API: it reads any page except those
// under /still-blocked, which it is refused too.
type fakeGeonode struct{}

func (fakeGeonode) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("X-Api-Key") != "gn-key" {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"code": "UNAUTHORIZED", "message": "bad key", "retryable": false}`)
		return
	}
	switch r.URL.Path {
	case "/v1/usage/concurrency":
		io.WriteString(w, `{"work_concurrency_in_use": 0, "work_concurrency_limit": 5}`)
	case "/v1/extract":
		var req struct {
			URL      string `json:"url"`
			RenderJS bool   `json:"render_js"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if strings.Contains(req.URL, "/app") {
			title := "Loading"
			if req.RenderJS {
				title = "Built by JS"
			}
			json.NewEncoder(w).Encode(map[string]any{
				"data":           map[string]any{"html": `<html><head><title>` + title + `</title></head><body><h1>App</h1><a href="/from-js">more</a></body></html>`},
				"metadata":       map[string]any{"http_status": 200, "url": req.URL},
				"tokens_charged": 2,
			})
			return
		}
		if strings.Contains(req.URL, "/still-blocked") {
			w.WriteHeader(http.StatusUnprocessableEntity)
			io.WriteString(w, `{"error": {"code": "PERMANENT_BLOCK", "message": "blocked", "retryable": false}, "tokens_charged": 0}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data":           map[string]any{"html": `<html><head><title>Shop</title></head><body><h1>Shop</h1><a href="/">home</a></body></html>`},
			"metadata":       map[string]any{"http_status": 200, "url": req.URL, "render_js": false, "formats": []string{"html"}, "processing_mode": "sync"},
			"tokens_charged": 1,
		})
	case "/v1/search":
		io.WriteString(w, `{"engine": "google", "tokens_charged": 1, "results": [{"position": 1, "title": "Acme", "url": "https://acme.example/"}]}`)
	default:
		http.NotFound(w, r)
	}
}

func TestSEOSiteFallbackForBlockedPages(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	var token atomic.Value
	token.Store("")
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		challenge := func() {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `<html><head><title>Just a moment...</title></head><body>challenge-platform</body></html>`)
		}
		switch r.URL.Path {
		case "/robots.txt":
			io.WriteString(w, "User-agent: *\nDisallow: /secret\n")
		case "/.well-known/outflare-site-verification.txt":
			io.WriteString(w, token.Load().(string))
		case "/":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<html><body><a href="/shop">shop</a> <a href="/still-blocked">x</a> <a href="/secret/y">s</a> <a href="/app">app</a></body></html>`)
		case "/app":
			// Built by scripts: nothing to read as served.
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<html><head><title>Loading</title></head><body><div id="root"></div><script src="/app.js"></script></body></html>`)
		case "/from-js":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<html><body><p>From scripts.</p></body></html>`)
		case "/shop", "/still-blocked", "/secret/y":
			challenge()
		default:
			http.NotFound(w, r)
		}
	}))
	defer site.Close()

	s := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": site.URL + "/"})
	id := s["id"].(string)
	e.runCrawls(t)
	crawl := func() (map[string]any, map[string]map[string]any) {
		rid := owner.must(202, "POST", base+"/seo-sites/"+id+"/crawls", nil)["id"].(string)
		run := waitFor(t, owner, base+"/crawls/"+rid, func(m map[string]any) bool { return m["status"] == "done" })
		return run, pagesByPath(owner, base+"/crawls/"+rid+"/pages")
	}

	// Not for a site the workspace has not proved it owns.
	run, pages := crawl()
	if pages["/shop"]["http_status"] != 403.0 || pages["/shop"]["fetched_via"] != nil || run["pages_fallback"] != 0.0 ||
		run["pages_firewalled"] != 2.0 || run["fallback_hint"] != "verify_site" {
		t.Fatalf("unverified: %v %v", run, pages["/shop"])
	}
	if pages["/app"]["title"] != "Loading" || pages["/app"]["fetched_via"] != nil {
		t.Fatalf("unverified app: %v", pages["/app"])
	}

	// Proved, but the workspace has no Geonode connection of its own:
	// the pages stay blocked and the crawl says what to connect.
	token.Store(s["verification"].(map[string]any)["file_body"].(string))
	owner.must(200, "POST", base+"/seo-sites/"+id+"/verify", nil)
	run, pages = crawl()
	if pages["/shop"]["http_status"] != 403.0 || run["pages_fallback"] != 0.0 || run["fallback_hint"] != "connect_geonode" {
		t.Fatalf("not connected: %v %v", run, pages["/shop"])
	}

	owner.must(422, "POST", base+"/integrations", map[string]any{"connector": "geonode", "credentials": map[string]string{"api_key": "wrong"}})
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "geonode", "credentials": map[string]string{"api_key": "gn-key"}})
	run, pages = crawl()
	shop := pages["/shop"]
	if shop["http_status"] != 200.0 || shop["fetched_via"] != "geonode" || shop["title"] != "Shop" {
		t.Fatalf("shop: %v", shop)
	}
	// A page the fallback cannot read either keeps the site's answer.
	if p := pages["/still-blocked"]; p["http_status"] != 403.0 || p["fetched_via"] != nil {
		t.Fatalf("still blocked: %v", p)
	}
	// A page its scripts build is read with them run, and its links followed.
	if p := pages["/app"]; p["title"] != "Built by JS" || p["fetched_via"] != "geonode" || pages["/from-js"] == nil {
		t.Fatalf("app: %v", p)
	}
	// robots.txt is never got around.
	if p := pages["/secret/y"]; p["status"] != "blocked" || p["fetched_via"] != nil {
		t.Fatalf("secret: %v", p)
	}
	if run["pages_fallback"] != 2.0 || run["pages_firewalled"] != 2.0 || run["fallback_hint"] != "" {
		t.Fatalf("run: %v", run)
	}

	seen := map[string]bool{}
	for _, ev := range got() {
		seen[ev.Name+"/"+ev.Outcome+"/"+ev.ErrorCode] = true
	}
	for _, name := range []string{"crawl.page_fallback/success/", "crawl.page_fallback/error/fallback_failed"} {
		if !seen[name] {
			t.Errorf("no %s event", name)
		}
	}
}

func TestFixesFollowCrawls(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	var fixed atomic.Bool
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		title := ""
		if fixed.Load() {
			title = "<title>A proper title here</title>"
		}
		switch r.URL.Path {
		case "/":
			io.WriteString(w, `<html><head><title>Home page title</title></head><body><a href="/p/one">1</a><a href="/p/two">2</a><a href="/p/three">3</a></body></html>`)
		case "/p/one", "/p/two", "/p/three":
			io.WriteString(w, `<html><head>`+title+`</head><body><a href="/">home</a></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer site.Close()
	id := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": site.URL + "/"})["id"].(string)
	e.runCrawls(t)
	crawl := func() {
		rid := owner.must(202, "POST", base+"/seo-sites/"+id+"/crawls", nil)["id"].(string)
		waitFor(t, owner, base+"/crawls/"+rid, func(m map[string]any) bool { return m["status"] == "done" })
	}
	fixFor := func(rule string) map[string]any {
		for _, f := range owner.must(200, "GET", base+"/fixes?source=seo_audit&subject_id="+id, nil)["data"].([]any) {
			if f := f.(map[string]any); f["rule"].(map[string]any)["id"] == rule {
				return f
			}
		}
		return nil
	}

	crawl()
	f := fixFor("title_missing")
	if f == nil || f["group"] != "/p/*" || f["count"] != 3.0 || f["unit"] != "pages" || f["status"] != "open" || f["rule"].(map[string]any)["fix"] == "" ||
		f["subject"].(map[string]any)["id"] != id || f["subject"].(map[string]any)["type"] != "seo_site" {
		t.Fatalf("fix: %v", f)
	}
	fid := f["id"].(string)
	owner.must(400, "PATCH", base+"/fixes/"+fid, map[string]any{"status": "later"})
	owner.must(400, "PATCH", base+"/fixes/"+fid, map[string]any{"assignee_id": "00000000-0000-0000-0000-000000000000"})
	f = owner.must(200, "PATCH", base+"/fixes/"+fid, map[string]any{"status": "in_progress", "assignee_id": "me", "note": "Template fix in the CMS"})
	if f["status"] != "in_progress" || f["assignee"] == nil || f["note"] != "Template fix in the CMS" {
		t.Fatalf("taken: %v", f)
	}
	owner.must(404, "GET", base+"/fixes/not-a-fix", nil)

	// The next complete crawl that no longer finds it confirms it.
	fixed.Store(true)
	crawl()
	f = owner.must(200, "GET", base+"/fixes/"+fid, nil)
	if f["status"] != "done" || f["confirmed_by"] == nil || f["count"] != 0.0 {
		t.Fatalf("confirmed: %v", f)
	}
	// And one that finds it again reopens it.
	fixed.Store(false)
	crawl()
	if f = owner.must(200, "GET", base+"/fixes/"+fid, nil); f["status"] != "open" || f["confirmed_by"] != nil {
		t.Fatalf("reopened: %v", f)
	}
	if l := owner.must(200, "GET", base+"/fixes?status=ignored", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("filter: %v", l)
	}
	owner.must(400, "GET", base+"/fixes?source=crm", nil)
	// Deleting the site takes its fixes with it.
	owner.must(204, "DELETE", base+"/seo-sites/"+id, nil)
	if l := owner.must(200, "GET", base+"/fixes", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("after delete: %v", l)
	}

	seen := map[string]bool{}
	for _, ev := range got() {
		seen[ev.Name] = true
	}
	for _, name := range []string{"fix.updated", "fix.confirmed", "fix.reopened"} {
		if !seen[name] {
			t.Errorf("no %s event", name)
		}
	}
}
