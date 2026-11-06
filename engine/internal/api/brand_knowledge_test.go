package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// Brand knowledge: a brand's website is read through the workspace's own
// Geonode connection (the home page, its sitemap and the pages about the
// offer) and the workspace's own model writes what it offers, each bullet
// with its page; a person edits it, and empty About and Who it's for are
// filled from it.
func TestBrandKnowledge(t *testing.T) {
	e := freshEnv(t)
	events := track.Capture(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	var read []string
	site := httptest.NewServer(geonodeJobs(nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/usage/concurrency":
			io.WriteString(w, `{"work_concurrency_in_use": 0, "work_concurrency_limit": 5}`)
			return
		case "/v1/extract":
		default:
			http.NotFound(w, r)
			return
		}
		var req struct {
			URL string `json:"url"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		read = append(read, req.URL)
		page, status := "", 404
		switch req.URL {
		case "https://northpeak.example/":
			page, status = `<html><head><title>Northpeak</title></head><body><nav><a href="/pricing">Pricing</a> <a href="/features">Features</a> <a href="/blog/news">Blog</a> <a href="https://other.example/x">Partner</a></nav>
				<p>Northpeak sells residential proxies to data teams.</p></body></html>`, 200
		case "https://northpeak.example/sitemap.xml":
			page, status = `<?xml version="1.0"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>https://northpeak.example/integrations</loc></url><url><loc>https://northpeak.example/pricing</loc></url></urlset>`, 200
		case "https://northpeak.example/pricing":
			page, status = `<html><head><title>Pricing</title></head><body><p>Plans from $49 a month, pay as you go.</p></body></html>`, 200
		case "https://northpeak.example/features":
			page, status = `<html><head><title>Features</title></head><body><p>Rotating IPs in 190 countries. Sticky sessions.</p></body></html>`, 200
		case "https://northpeak.example/integrations":
			page, status = `<html><head><title>Integrations</title></head><body><p>Works with Scrapy, Puppeteer and Playwright.</p></body></html>`, 200
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"html": page}, "metadata": map[string]any{"http_status": status, "url": req.URL}, "tokens_charged": 1,
		})
	}))
	t.Cleanup(site.Close)
	e.integrations.Client.BaseURLs["geonode"] = site.URL
	var bodies []map[string]any
	judge := `{"summary":"Northpeak sells residential proxies to data teams.","industry":"Proxy networks",
		"lists":{"products":[{"text":"Residential proxy plans from $49 a month","page":2}],"services":[],
		"features":[{"text":"Rotating IPs in 190 countries","page":3},{"text":"Sticky sessions","page":3},{"text":"Made up","page":99}],
		"integrations":[{"text":"Scrapy","page":4},{"text":"Puppeteer","page":4}],"capabilities":[],
		"audiences":[{"text":"Data teams","page":1}],"use_cases":[{"text":"Web scraping","page":1}]}}`
	or := httptest.NewServer(fakeOpenRouter{bodies: &bodies, judge: &judge})
	t.Cleanup(or.Close)
	e.integrations.Client.BaseURLs["openrouter"] = or.URL

	// Without a page reader nothing is queued and the read says so.
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "website": "northpeak.example"})
	owner.must(409, "POST", base+"/brands/northpeak/knowledge/read", nil)

	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "geonode", "credentials": map[string]string{"api_key": "gn-key"}})
	conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "openrouter", "credentials": map[string]string{"api_key": "sk-or-good"}})["id"].(string)
	owner.must(200, "PUT", base+"/ai-settings", map[string]any{"integration_id": conn, "model": "openai/gpt-5"})
	owner.must(202, "POST", base+"/brands/northpeak/knowledge/read", nil)
	var bid string
	if err := e.pool.QueryRow(ctx, `SELECT id::text FROM brands WHERE workspace_id = $1 AND handle = 'northpeak'`, ws).Scan(&bid); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.ReadBrandKnowledge(ctx, ws, bid); err != nil {
		t.Fatal(err)
	}
	// The home page, the sitemap and the pages about the offer were read;
	// the blog and the other site weren't.
	for _, u := range []string{"https://northpeak.example/", "https://northpeak.example/sitemap.xml", "https://northpeak.example/pricing", "https://northpeak.example/features", "https://northpeak.example/integrations"} {
		if !contains(read, u) {
			t.Fatalf("%s not read: %v", u, read)
		}
	}
	if contains(read, "https://northpeak.example/blog/news") || contains(read, "https://other.example/x") {
		t.Fatalf("read too much: %v", read)
	}
	prompt := bodies[len(bodies)-1]["messages"].([]any)[1].(map[string]any)["content"].(string)
	if !strings.Contains(prompt, `<page number="1" url="https://northpeak.example/"`) || !strings.Contains(prompt, "Sticky sessions") {
		t.Fatalf("prompt: %s", prompt)
	}

	c := owner.must(200, "GET", base+"/brands/northpeak/context", nil)
	k := c["knowledge"].(map[string]any)
	lists := k["lists"].(map[string]any)
	if k["summary"] != "Northpeak sells residential proxies to data teams." || k["industry"] != "Proxy networks" || c["knowledge_pages"] != 4.0 || c["knowledge_error"] != "" || c["knowledge_read_at"] == nil {
		t.Fatalf("knowledge: %v", c)
	}
	features := lists["features"].([]any)
	if len(features) != 3 || features[0].(map[string]any)["url"] != "https://northpeak.example/features" || features[2].(map[string]any)["url"] != "" {
		t.Fatalf("features: %v", features)
	}
	if len(lists["integrations"].([]any)) != 2 || len(lists["services"].([]any)) != 0 || len(k["pages"].([]any)) != 4 {
		t.Fatalf("lists: %v", lists)
	}
	// Empty About and Who it's for are filled in from the read.
	if c["about"] != "Northpeak sells residential proxies to data teams." || c["audience"] != "Data teams" {
		t.Fatalf("context filled: %v %v", c["about"], c["audience"])
	}
	if !containsEvent(events(), "brand.knowledge_read") {
		t.Fatal("no brand.knowledge_read event")
	}

	// A person edits it; an unknown list is refused.
	k["lists"].(map[string]any)["features"] = []map[string]any{{"text": "Rotating IPs in 190 countries", "url": "https://northpeak.example/features"}}
	k["summary"] = "Proxies for data teams."
	c = owner.must(200, "PUT", base+"/brands/northpeak/knowledge", k)
	if k2 := c["knowledge"].(map[string]any); k2["summary"] != "Proxies for data teams." || len(k2["lists"].(map[string]any)["features"].([]any)) != 1 {
		t.Fatalf("edited: %v", c["knowledge"])
	}
	owner.must(400, "PUT", base+"/brands/northpeak/knowledge", map[string]any{"summary": "x", "lists": map[string]any{"prices": []any{}}})
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func containsEvent(list []track.Event, name string) bool {
	for _, ev := range list {
		if ev.Name == name {
			return true
		}
	}
	return false
}
