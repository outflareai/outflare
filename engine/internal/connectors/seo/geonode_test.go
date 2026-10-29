package seo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

func TestGeonode(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "k" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/v1/usage/concurrency":
			io.WriteString(w, `{"work_concurrency_in_use": 1, "work_concurrency_limit": 10}`)
		case "/v1/extract":
			json.NewDecoder(r.Body).Decode(&body)
			if body["url"] == "https://example.com/blocked" {
				w.WriteHeader(422)
				io.WriteString(w, `{"error": {"code": "PAYMENT_REQUIRED", "message": "no tokens", "retryable": false}, "tokens_charged": 0}`)
				return
			}
			io.WriteString(w, `{"data": {"html": "<h1>Hi</h1>"}, "metadata": {"http_status": 200, "url": "https://example.com/final"}, "tokens_charged": 2}`)
		}
	}))
	defer srv.Close()
	cl := &c.Client{BaseURLs: map[string]string{"geonode": srv.URL}}
	g := Geonode{}
	if d := g.Definition(); !d.Has(c.CapCrawlFetch) || d.Category != c.CategorySEO {
		t.Fatalf("definition: %+v", d)
	}
	if _, err := g.Test(context.Background(), cl, c.Creds{"api_key": "bad"}); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("bad key: %v", err)
	}
	acct, err := g.Test(context.Background(), cl, c.Creds{"api_key": "k"})
	if err != nil || acct.Details["concurrency"] != "10" {
		t.Fatalf("test: %+v %v", acct, err)
	}
	p, err := g.FetchPage(context.Background(), cl, c.Creds{"api_key": "k"}, "https://example.com/a", true)
	if err != nil || p.Status != 200 || p.HTML != "<h1>Hi</h1>" || p.URL != "https://example.com/final" || p.Cost.Units != 2 {
		t.Fatalf("page: %+v %v", p, err)
	}
	// Pages behind a bot check take 25 seconds or more in a browser: Geonode
	// gets 60 to read one.
	if body["render_js"] != true || body["processing_mode"] != "sync" || body["timeout"] != 60000.0 {
		t.Fatalf("request: %v", body)
	}
	if _, err := g.FetchPage(context.Background(), cl, c.Creds{"api_key": "k"}, "https://example.com/blocked", false); c.KindOf(err) != c.ErrQuota {
		t.Fatalf("out of tokens: %v", err)
	}
}

func TestGeonodeWebSearch(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = nil
		json.NewDecoder(r.Body).Decode(&body)
		switch body["query"] {
		case "broke":
			w.WriteHeader(402)
			io.WriteString(w, `{"error": {"code": "PAYMENT_REQUIRED"}}`)
		case "wrapped":
			io.WriteString(w, `{"data": {"engine": "brave", "results": [{"title": "Acme", "link": "https://acme.example/"}]}}`)
		default:
			io.WriteString(w, `{"engine": "google", "tokens_charged": 1, "correction": "acme payroll", "suggestions": ["acme payroll au"],
				"results": [{"position": 1, "title": " Acme Payroll ", "url": "https://www.Acme.example/payroll", "snippet": "Payroll for teams."},
				{"position": 2, "title": "No URL"}, {"position": 3, "title": "Northpeak", "url": "https://northpeak.example"}]}`)
		}
	}))
	defer srv.Close()
	cl := &c.Client{BaseURLs: map[string]string{"geonode": srv.URL}}
	g := Geonode{}
	creds := c.Creds{"api_key": "k"}
	var _ c.WebSearcher = g
	if !g.Definition().Has(c.CapWebSearch) {
		t.Fatal("no web search capability")
	}
	r, err := g.WebSearch(context.Background(), cl, creds, c.WebQuery{Query: " acme payrol ", Locale: "en-AU", Page: 2, TimeRange: "month"})
	if err != nil || r.Engine != "google" || len(r.Results) != 2 || r.Correction != "acme payroll" || r.Cost.Units != 1 || r.Cost.Estimated {
		t.Fatalf("search: %+v %v", r, err)
	}
	if got := r.Results[0]; got.Domain != "acme.example" || got.Title != "Acme Payroll" || got.Position != 1 || got.Snippet != "Payroll for teams." {
		t.Fatalf("first: %+v", got)
	}
	if body["query"] != "acme payrol" || body["locale"] != "en-AU" || body["page"] != 2.0 || body["time_range"] != "month" {
		t.Fatalf("request: %v", body)
	}
	// Answers wrapped in data, without a price, are read and estimated.
	r, err = g.WebSearch(context.Background(), cl, creds, c.WebQuery{Query: "wrapped"})
	if err != nil || r.Engine != "brave" || len(r.Results) != 1 || r.Results[0].URL != "https://acme.example/" || r.Results[0].Position != 1 || !r.Cost.Estimated || r.Cost.USD == 0 {
		t.Fatalf("wrapped: %+v %v", r, err)
	}
	if _, ok := body["page"]; ok {
		t.Fatalf("page 1 sent: %v", body)
	}
	if _, err := g.WebSearch(context.Background(), cl, creds, c.WebQuery{Query: "broke"}); c.KindOf(err) != c.ErrQuota {
		t.Fatalf("out of tokens: %v", err)
	}
	if _, err := g.WebSearch(context.Background(), cl, creds, c.WebQuery{Query: "  "}); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("empty: %v", err)
	}
}

func TestGeonodeSERP(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/google/search/light" {
			w.WriteHeader(404)
			return
		}
		body = nil
		json.NewDecoder(r.Body).Decode(&body)
		switch body["query"] {
		case "broke":
			w.WriteHeader(402)
			io.WriteString(w, `{"code": "PAYMENT_REQUIRED", "message": "no balance", "retryable": false}`)
		case "blocked":
			w.WriteHeader(422)
			io.WriteString(w, `{"code": "UPSTREAM_BLOCKED", "message": "refused", "retryable": true}`)
		case "nothing":
			w.WriteHeader(422)
			io.WriteString(w, `{"code": "NO_RESULTS", "message": "none", "retryable": false}`)
		case "short":
			io.WriteString(w, `{"pages_requested": 10, "pages_returned": 1, "pages_stopped": "clock",
				"pages": [{"page": 1, "results": [{"position": 1, "title": "Rival", "url": "https://rival.example/"}]}]}`)
		default:
			io.WriteString(w, `{"job_id": "j", "pages_requested": 10, "pages_returned": 2, "pages_stopped": "no_results", "pages": [
				{"page": 1, "results": [{"position": 1, "title": " Rival ", "url": "https://rival.example/a"}, {"position": 2, "title": "No URL"}]},
				{"page": 2, "results": [{"position": 1, "title": "Acme", "url": "https://www.acme.example/pricing", "source_host": "www.acme.example"}]}]}`)
		}
	}))
	defer srv.Close()
	cl := &c.Client{BaseURLs: map[string]string{"geonode": srv.URL}}
	g := Geonode{}
	creds := c.Creds{"api_key": "k"}
	ctx := context.Background()
	var _ c.SERPReader = g
	if !g.Definition().Has(c.CapSERP) || g.SERPEngines()[0] != "google" {
		t.Fatal("not a SERP reader")
	}
	if e := g.EstimateSERP(c.SERPQuery{Depth: 100}); e.USD != 0.005 || !e.Estimated {
		t.Fatalf("estimate: %+v", e)
	}

	res, err := g.SERP(ctx, cl, creds, "google", c.SERPQuery{Keyword: "acme pricing", Country: "US", Device: "mobile", Depth: 100})
	if err != nil {
		t.Fatal(err)
	}
	// One request reads every page, from the country, in its language.
	if body["pages"] != float64(10) || body["device"] != "mobile" || body["locale"] != "en-US" || body["uule"] != "w+CAIQICINVW5pdGVkIFN0YXRlcw" || body["format"] != "json" {
		t.Fatalf("request: %v", body)
	}
	// Positions run on across pages; results without a URL are left out.
	if len(res.Items) != 2 || res.Items[0].Title != "Rival" || res.Items[1].Position != 2 || res.Items[1].Domain != "acme.example" || res.Partial {
		t.Fatalf("items: %+v", res)
	}
	if res.Cost.USD != 0.001 || res.Cost.Units != 2 {
		t.Fatalf("cost: %+v", res.Cost)
	}
	if _, err := g.SERP(ctx, cl, creds, "google", c.SERPQuery{Keyword: "acme", Country: "DE", Depth: 10}); err != nil || body["pages"] != float64(1) || body["locale"] != "de-DE" || body["device"] != "desktop" {
		t.Fatalf("germany: %v %v", body, err)
	}
	if res, err := g.SERP(ctx, cl, creds, "google", c.SERPQuery{Keyword: "short", Country: "US", Depth: 100}); err != nil || !res.Partial || res.Cost.Units != 1 {
		t.Fatalf("short: %+v %v", res, err)
	}
	if res, err := g.SERP(ctx, cl, creds, "google", c.SERPQuery{Keyword: "nothing", Country: "US", Depth: 100}); err != nil || len(res.Items) != 0 || res.Cost.USD != 0 {
		t.Fatalf("no results: %+v %v", res, err)
	}
	if _, err := g.SERP(ctx, cl, creds, "google", c.SERPQuery{Keyword: "broke", Country: "US"}); c.KindOf(err) != c.ErrQuota {
		t.Fatalf("broke: %v", err)
	}
	if _, err := g.SERP(ctx, cl, creds, "google", c.SERPQuery{Keyword: "blocked", Country: "US"}); c.KindOf(err) != c.ErrUnavailable {
		t.Fatalf("blocked: %v", err)
	}
	if _, err := g.SERP(ctx, cl, creds, "google", c.SERPQuery{Keyword: "x", Country: "ZZ"}); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("country: %v", err)
	}
	if _, err := g.SERP(ctx, cl, creds, "bing", c.SERPQuery{Keyword: "x", Country: "US"}); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("engine: %v", err)
	}
}
