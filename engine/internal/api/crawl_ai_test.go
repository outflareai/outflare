package api_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestCrawlAIAccess(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	var llms atomic.Bool
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			io.WriteString(w, "User-agent: *\nAllow: /\n\nUser-agent: PerplexityBot\nUser-agent: GPTBot\nDisallow: /\n\nUser-agent: OAI-SearchBot\nDisallow: /private\n")
		case "/llms.txt":
			if llms.Load() {
				w.Header().Set("Content-Type", "text/markdown")
				io.WriteString(w, "# Acme\n\n> Proxies for teams.\n")
				return
			}
			// A site that answers every path with its app counts as none.
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, "<html><body>app</body></html>")
		case "/":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<html><body><a href="/private/p">p</a></body></html>`)
		case "/private/p":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<html><body>private</body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer site.Close()

	id := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": site.URL})["id"].(string)
	crawl := func() string {
		t.Helper()
		rid := owner.must(202, "POST", base+"/seo-sites/"+id+"/crawls", nil)["id"].(string)
		e.runCrawls(t)
		waitFor(t, owner, base+"/crawls/"+rid, func(m map[string]any) bool { return m["status"] == "done" })
		return rid
	}
	rid := crawl()

	acc := owner.must(200, "GET", base+"/crawls/"+rid+"/ai-access", nil)
	bots := map[string]map[string]any{}
	for _, b := range acc["bots"].([]any) {
		bots[b.(map[string]any)["token"].(string)] = b.(map[string]any)
	}
	if acc["checked"] != true || acc["search_blocked"] != true || acc["llms_txt"].(map[string]any)["found"] != false {
		t.Fatalf("access: %v", acc)
	}
	if p := bots["PerplexityBot"]; p["root_allowed"] != false || p["blocked_pages"] != 2.0 || p["pages"] != 2.0 || p["purpose"] != "search" {
		t.Fatalf("PerplexityBot: %v", p)
	}
	if o := bots["OAI-SearchBot"]; o["root_allowed"] != true || o["blocked_pages"] != 1.0 {
		t.Fatalf("OAI-SearchBot: %v", o)
	}
	if g := bots["GPTBot"]; g["blocked_pages"] != 2.0 || g["purpose"] != "training" {
		t.Fatalf("GPTBot: %v", g)
	}
	if c := bots["ClaudeBot"]; c["blocked_pages"] != 0.0 || c["root_allowed"] != true {
		t.Fatalf("ClaudeBot: %v", c)
	}

	// The audit names the search crawlers kept from each page, and no
	// llms.txt; training crawlers alone are not a problem.
	issues := map[string]map[string]any{}
	for _, i := range owner.must(200, "GET", base+"/crawls/"+rid+"/issues", nil)["data"].([]any) {
		issues[i.(map[string]any)["id"].(string)] = i.(map[string]any)
	}
	if issues["ai_search_blocked"]["pages"] != 2.0 || issues["llms_txt_missing"]["pages"] != 1.0 || issues["ai_search_blocked"]["category"] != "ai_visibility" {
		t.Fatalf("issues: %v", issues)
	}
	pages := owner.must(200, "GET", base+"/crawls/"+rid+"/issues/ai_search_blocked", nil)["data"].([]any)
	details := map[string]bool{}
	for _, p := range pages {
		details[p.(map[string]any)["detail"].(string)] = true
	}
	if !details["PerplexityBot"] || !details["OAI-SearchBot, PerplexityBot"] {
		t.Fatalf("blocked pages: %v", pages)
	}

	// With an llms.txt the next crawl finds it.
	llms.Store(true)
	rid = crawl()
	if l := owner.must(200, "GET", base+"/crawls/"+rid+"/ai-access", nil)["llms_txt"].(map[string]any); l["found"] != true || l["bytes"].(float64) < 10 {
		t.Fatalf("llms.txt: %v", l)
	}
	owner.must(404, "GET", base+"/crawls/00000000-0000-0000-0000-000000000000/ai-access", nil)
}
