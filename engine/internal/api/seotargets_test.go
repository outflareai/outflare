package api_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestPageTargets(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	var described atomic.Bool
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		desc := "Plans for every team."
		if described.Load() {
			desc = "Proxy plans and pricing for every team."
		}
		switch r.URL.Path {
		case "/":
			io.WriteString(w, `<html><head><title>Proxy pricing plans</title><meta name="description" content="`+desc+`"></head><body><h1>Proxy plans</h1><a href="/blog">blog</a></body></html>`)
		case "/blog":
			io.WriteString(w, `<html><head><title>Blog</title></head><body><h1>Proxy blog</h1><a href="/">home</a></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer page.Close()
	id := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": page.URL + "/"})["id"].(string)
	sb := base + "/seo-sites/" + id
	e.runCrawls(t)
	crawl := func() {
		rid := owner.must(202, "POST", sb+"/crawls", nil)["id"].(string)
		waitFor(t, owner, base+"/crawls/"+rid, func(m map[string]any) bool { return m["status"] == "done" })
	}
	crawl()
	keywordFixes := func() map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, f := range owner.must(200, "GET", base+"/fixes?source=seo_keywords&subject_id="+id, nil)["data"].([]any) {
			f := f.(map[string]any)
			out[f["rule"].(map[string]any)["id"].(string)] = f
		}
		return out
	}

	// A target is checked against the latest crawl at once.
	tg := owner.must(200, "PUT", sb+"/targets", map[string]any{"url": page.URL + "/#top", "keyword": "  Proxy   PRICING ", "secondary": []string{"Proxy plans", "proxy plans"}})
	checks := tg["checks"].(map[string]any)
	if tg["keyword"] != "proxy pricing" || tg["url"] != page.URL+"/" || tg["status"] != "set" || len(tg["secondary"].([]any)) != 1 ||
		checks["in_title"] != true || checks["in_h1"] != false || checks["in_description"] != false {
		t.Fatalf("target: %v", tg)
	}
	tid := tg["id"].(string)
	fx := keywordFixes()
	if len(fx) != 2 || fx["target_not_in_h1"]["group"] != page.URL+"/" || fx["target_not_in_description"]["status"] != "open" ||
		fx["target_not_in_h1"]["rule"].(map[string]any)["title"] != "Target keyword not in the H1" {
		t.Fatalf("keyword fixes: %v", fx)
	}
	owner.must(400, "PUT", sb+"/targets", map[string]any{"url": "https://elsewhere.example/", "keyword": "x"})
	owner.must(400, "PUT", sb+"/targets", map[string]any{"url": page.URL + "/blog"})
	owner.must(400, "PUT", sb+"/targets", map[string]any{"url": page.URL + "/blog", "keyword": strings.Repeat("a", 201)})
	secret := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Reader", "scopes": []string{"seo.read"}})["token"].(string)
	reader := &client{t: t, h: e.h, bearer: secret}
	reader.must(200, "GET", sb+"/targets", nil)
	reader.must(403, "PUT", sb+"/targets", map[string]any{"url": page.URL + "/", "keyword": "x"})

	// The prompt for a keyword fix names the page and its keyword.
	p := owner.must(200, "GET", base+"/fixes/"+fx["target_not_in_description"]["id"].(string)+"/prompt", nil)
	if pr := p["prompt"].(string); !strings.Contains(pr, "search intelligence") || !strings.Contains(pr, "[proxy pricing]") || len(p["pages"].([]any)) != 1 {
		t.Fatalf("prompt: %v", p)
	}

	// A keyword the H1 has confirms that fix; the description's stays.
	owner.must(200, "PATCH", sb+"/targets/"+tid, map[string]any{"keyword": "proxy plans"})
	fx = keywordFixes()
	if fx["target_not_in_h1"]["status"] != "done" || fx["target_not_in_h1"]["confirmed_by"] == nil || fx["target_not_in_description"]["status"] != "open" {
		t.Fatalf("after the keyword changed: %v", fx)
	}
	owner.must(400, "PATCH", sb+"/targets/"+tid, map[string]any{"keyword": ""})
	owner.must(400, "PATCH", sb+"/targets/"+tid, map[string]any{"url": page.URL + "/blog"})

	// The page is fixed; a crawl checked by verify confirms it.
	described.Store(true)
	res := owner.must(202, "POST", base+"/fixes/"+fx["target_not_in_description"]["id"].(string)+"/verify", nil)
	waitFor(t, owner, base+"/crawls/"+res["crawl"].(map[string]any)["id"].(string), func(m map[string]any) bool { return m["status"] == "done" })
	if f := keywordFixes()["target_not_in_description"]; f["status"] != "done" || f["confirmed_by"] == nil {
		t.Fatalf("verified: %v", f)
	}
	// A target set on the blog page is found; removing it confirms its fixes.
	blog := owner.must(200, "PUT", sb+"/targets", map[string]any{"url": page.URL + "/blog", "keyword": "proxy guides"})
	if keywordFixes()["target_not_in_title"]["status"] != "open" {
		t.Fatal("blog title fix")
	}
	owner.must(204, "DELETE", sb+"/targets/"+blog["id"].(string), nil)
	owner.must(404, "DELETE", sb+"/targets/"+blog["id"].(string), nil)
	if f := keywordFixes()["target_not_in_title"]; f["status"] != "done" {
		t.Fatalf("removed target: %v", f)
	}

	// Search Console suggests the query that brings each page the most
	// clicks, for pages with no target.
	f := &fakeSearchConsole{rows: func(day string) [][]any {
		return [][]any{
			{"https://www.example.com/pricing", "proxy pricing", 10, 100, 3.0},
			{"https://www.example.com/pricing", "cheap proxies", 2, 50, 9.0},
			{"https://www.example.com/blog/proxies", "cheap proxies", 1, 80, 12.0},
			{"https://www.example.com/rare", "rare query", 0, 0, 40.0},
		}
	}}
	sc := connectSearchConsole(t, e, owner, base, f)
	other := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.example.com/"})["id"].(string)
	ob := base + "/seo-sites/" + other
	if l := owner.must(200, "GET", ob+"/targets", nil); l["suggested"].(float64) != 0 {
		t.Fatalf("before a sync: %v", l)
	}
	owner.must(200, "POST", base+"/analytics/"+sc+"/sync", nil)
	l := owner.must(200, "GET", ob+"/targets", nil)
	list := l["data"].([]any)
	if l["suggested"].(float64) != 2 || list[0].(map[string]any)["keyword"] != "proxy pricing" || list[0].(map[string]any)["status"] != "suggested" ||
		list[1].(map[string]any)["url"] != "https://www.example.com/blog/proxies" {
		t.Fatalf("suggestions: %v", l)
	}
	owner.must(200, "PUT", ob+"/targets", map[string]any{"url": "https://www.example.com/blog/proxies", "status": "dismissed"})
	acc := owner.must(200, "POST", ob+"/targets/accept", nil)
	a := acc["data"].([]any)
	if acc["accepted"].(float64) != 1 || a[0].(map[string]any)["origin"] != "suggested" || a[0].(map[string]any)["search"].(map[string]any)["clicks"].(float64) != 280 {
		t.Fatalf("accepted: %v", acc)
	}
	if n := len(owner.must(200, "GET", ob+"/targets?status=suggested", nil)["data"].([]any)); n != 0 {
		t.Fatalf("suggestions left: %d", n)
	}
	if n := len(owner.must(200, "GET", ob+"/targets?status=dismissed", nil)["data"].([]any)); n != 1 {
		t.Fatalf("dismissed: %d", n)
	}
	owner.must(400, "POST", ob+"/targets/accept", map[string]any{"urls": []string{"https://elsewhere.example/"}})
	owner.must(400, "GET", ob+"/targets?status=nope", nil)

	// Another workspace sees none of it.
	ows := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Other"})["id"].(string)
	owner.must(404, "GET", "/v1/workspaces/"+ows+"/seo-sites/"+id+"/targets", nil)
	owner.must(404, "PATCH", "/v1/workspaces/"+ows+"/seo-sites/"+id+"/targets/"+tid, map[string]any{"keyword": "x"})

	set := 0
	for _, ev := range got() {
		if ev.Name == "page_target.set" && ev.WorkspaceID == ws {
			set++
		}
	}
	if set != 2 {
		t.Fatalf("page_target.set events: %d", set)
	}
}
