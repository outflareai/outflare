package api_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestInternalLinkSuggestions(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	var linked atomic.Bool
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		page := func(head, body string) { io.WriteString(w, `<html><head>`+head+`</head><body>`+body+`</body></html>`) }
		switch r.URL.Path {
		case "/":
			page(`<title>Home</title>`, `<a href="/pricing">p</a><a href="/blog/a">a</a><a href="/blog/b">b</a><a href="/blog/c">c</a><a href="/about">about</a>`)
		case "/pricing":
			page(`<title>Proxy pricing</title>`, `<h1>Pricing</h1><a href="/">home</a>`)
		case "/blog/a":
			link := ""
			if linked.Load() {
				link = `<a href="/pricing">proxy pricing</a>`
			}
			page(`<title>A guide to proxy prices</title>`, `<h1>Cheap proxy pricing, explained</h1><a href="/">home</a>`+link)
		case "/blog/b":
			page(`<title>Proxy pricing tips</title>`, `<a href="/pricing">pricing</a>`)
		case "/blog/c":
			page(`<title>Proxy pricing draft</title><meta name="robots" content="noindex">`, `<a href="/">home</a>`)
		case "/about":
			page(`<title>About us</title>`, `<a href="/">home</a>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer site.Close()
	id := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": site.URL + "/"})["id"].(string)
	sb := base + "/seo-sites/" + id
	e.runCrawls(t)
	crawl := func() {
		rid := owner.must(202, "POST", sb+"/crawls", nil)["id"].(string)
		waitFor(t, owner, base+"/crawls/"+rid, func(m map[string]any) bool { return m["status"] == "done" })
	}
	crawl()
	linkFix := func() map[string]any {
		for _, f := range owner.must(200, "GET", base+"/fixes?source=seo_keywords&subject_id="+id, nil)["data"].([]any) {
			if f := f.(map[string]any); f["rule"].(map[string]any)["id"] == "internal_link_opportunity" {
				return f
			}
		}
		return nil
	}

	// Without targets there is nothing to suggest.
	if l := owner.must(200, "GET", sb+"/internal-links", nil); len(l["data"].([]any)) != 0 {
		t.Fatalf("no targets: %v", l)
	}
	owner.must(200, "PUT", sb+"/targets", map[string]any{"url": site.URL + "/pricing", "keyword": "proxy pricing"})
	owner.must(200, "PUT", sb+"/targets", map[string]any{"url": site.URL + "/gone", "keyword": "gone"})
	l := owner.must(200, "GET", sb+"/internal-links", nil)
	data := l["data"].([]any)
	if len(data) != 1 || len(l["not_crawled"].([]any)) != 1 {
		t.Fatalf("internal links: %v", l)
	}
	tg := data[0].(map[string]any)
	from := tg["from"].([]any)
	// blog/b links already, blog/c is noindex, about is about something else.
	if tg["url"] != site.URL+"/pricing" || len(from) != 1 || from[0].(map[string]any)["url"] != site.URL+"/blog/a" || from[0].(map[string]any)["reason"] != "mentions" {
		t.Fatalf("suggestions: %v", tg)
	}
	f := linkFix()
	if f == nil || f["group"] != site.URL+"/pricing" || f["count"].(float64) != 1 || f["status"] != "open" {
		t.Fatalf("link fix: %v", f)
	}
	p := owner.must(200, "GET", base+"/fixes/"+f["id"].(string)+"/prompt", nil)
	if pr := p["prompt"].(string); !strings.Contains(pr, "[proxy pricing]") || !strings.Contains(pr, "/blog/a") {
		t.Fatalf("prompt: %v", p)
	}

	// Once the link is there, the next crawl confirms it.
	linked.Store(true)
	crawl()
	if f := linkFix(); f["status"] != "done" || f["confirmed_by"] == nil {
		t.Fatalf("linked: %v", f)
	}
	if n := len(owner.must(200, "GET", sb+"/internal-links", nil)["data"].([]any)); n != 0 {
		t.Fatalf("nothing left: %d", n)
	}
}
