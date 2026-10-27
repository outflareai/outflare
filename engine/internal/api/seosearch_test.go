package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/track"
)

// fakeSearchConsole is Search Console for the domain property
// sc-domain:example.com. rows answers a day by page and query; inspect a
// URL inspection's verdict and coverage.
type fakeSearchConsole struct {
	rows        func(day string) [][]any // page, query, clicks, impressions, position
	inspect     func(page string) (verdict, coverage, googleCanonical string)
	inspections atomic.Int64
	pageCalls   atomic.Int64
}

func (f *fakeSearchConsole) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	enc := json.NewEncoder(w)
	switch {
	case r.URL.Path == "/": // the token URL is replaced whole
		enc.Encode(map[string]any{"access_token": "gat", "refresh_token": "grt", "expires_in": 3600, "token_type": "Bearer"})
	case r.URL.Path == "/v1/userinfo":
		enc.Encode(map[string]any{"sub": "1", "email": "jo@example.com"})
	case r.URL.Path == "/webmasters/v3/sites":
		enc.Encode(map[string]any{"siteEntry": []any{map[string]any{"siteUrl": "sc-domain:example.com", "permissionLevel": "siteOwner"}}})
	case strings.HasSuffix(r.URL.Path, "/searchAnalytics/query"):
		var q struct {
			StartDate  string   `json:"startDate"`
			Dimensions []string `json:"dimensions"`
			StartRow   int      `json:"startRow"`
		}
		json.NewDecoder(r.Body).Decode(&q)
		rows := []any{}
		if len(q.Dimensions) == 2 {
			f.pageCalls.Add(1)
			if q.StartRow == 0 && f.rows != nil {
				for _, x := range f.rows(q.StartDate) {
					rows = append(rows, map[string]any{"keys": []any{x[0], x[1]}, "clicks": x[2], "impressions": x[3], "position": x[4]})
				}
			}
		}
		enc.Encode(map[string]any{"rows": rows})
	case r.URL.Path == "/v1/urlInspection/index:inspect":
		f.inspections.Add(1)
		var q struct {
			URL string `json:"inspectionUrl"`
		}
		json.NewDecoder(r.Body).Decode(&q)
		verdict, coverage, canonical := "PASS", "Submitted and indexed", q.URL
		if f.inspect != nil {
			verdict, coverage, canonical = f.inspect(q.URL)
		}
		enc.Encode(map[string]any{"inspectionResult": map[string]any{
			"inspectionResultLink": "https://search.google.com/search-console/inspect",
			"indexStatusResult": map[string]any{"verdict": verdict, "coverageState": coverage, "googleCanonical": canonical,
				"userCanonical": q.URL, "lastCrawlTime": "2026-10-20T08:00:00Z", "sitemap": []any{"https://www.example.com/sitemap.xml"}},
			"mobileUsabilityResult": map[string]any{"verdict": "PASS"}}})
	case strings.HasSuffix(r.URL.Path, "/sitemaps"):
		enc.Encode(map[string]any{"sitemap": []any{map[string]any{"path": "https://www.example.com/sitemap.xml", "lastSubmitted": "2026-10-01T00:00:00Z",
			"errors": "2", "warnings": "0", "contents": []any{map[string]any{"type": "web", "submitted": "40", "indexed": "31"}}}}})
	default:
		http.NotFound(w, r)
	}
}

// connectSearchConsole signs in to the fake Search Console and returns the
// connection's id.
func connectSearchConsole(t *testing.T, e *env, owner *client, base string, f *fakeSearchConsole) string {
	t.Helper()
	g := httptest.NewServer(f)
	t.Cleanup(g.Close)
	for _, id := range []string{"google_search_console", "google_search_console.token", "google.userinfo"} {
		e.integrations.Client.BaseURLs[id] = g.URL
	}
	e.integrations.OAuthApps = map[string]connectors.OAuthApp{"google_search_console": {ClientID: "cid", ClientSecret: "secret"}}
	link := owner.must(200, "GET", base+"/integrations/oauth/google_search_console/authorize?format=json", nil)["url"].(string)
	u, _ := url.Parse(link)
	cb := follow(t, owner, "/v1/integrations/oauth/callback?code=abc&state="+url.QueryEscape(u.Query().Get("state")))
	id := cb.Query().Get("integration")
	if id == "" {
		t.Fatalf("callback: %s", cb)
	}
	return id
}

func TestSearchConsolePageByPage(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	site := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.example.com/"})
	sb := base + "/seo-sites/" + site["id"].(string)

	// Nothing connected: empty answers that say so.
	if s := owner.must(200, "GET", sb+"/search", nil); s["source"] != nil || len(s["data"].([]any)) != 0 {
		t.Fatalf("no source: %v", s)
	}
	owner.must(200, "GET", sb+"/indexing", nil)
	owner.must(200, "GET", sb+"/sitemaps", nil)

	f := &fakeSearchConsole{
		rows: func(day string) [][]any {
			return [][]any{
				{"https://www.example.com/pricing", "proxy pricing", 10, 100, 3.0},
				{"https://www.example.com/pricing", "cheap proxies", 2, 50, 9.0},
				{"https://www.example.com/blog/proxies", "cheap proxies", 1, 80, 12.0},
				// Another host of the property is not this site's.
				{"https://docs.example.com/start", "proxy docs", 5, 20, 1.0},
			}
		},
		inspect: func(page string) (string, string, string) {
			if strings.Contains(page, "/blog/") {
				return "NEUTRAL", "Crawled - currently not indexed", ""
			}
			if strings.Contains(page, "/pricing") {
				return "PASS", "Submitted and indexed", "https://www.example.com/prices"
			}
			return "PASS", "Submitted and indexed", page
		},
	}
	sc := connectSearchConsole(t, e, owner, base, f)
	owner.must(200, "POST", base+"/analytics/"+sc+"/sync", nil)
	if n := f.pageCalls.Load(); n != 90 {
		t.Fatalf("first run reads 90 days by page and query: %d", n)
	}

	s := owner.must(200, "GET", sb+"/search", nil)
	src := s["source"].(map[string]any)
	if src["property"] != "sc-domain:example.com" || src["integration_id"] != sc {
		t.Fatalf("source: %v", src)
	}
	rows := s["data"].([]any)
	top := rows[0].(map[string]any)
	if len(rows) != 2 || top["page"] != "https://www.example.com/pricing" || top["clicks"].(float64) != 12*28 || top["query"] != nil {
		t.Fatalf("pages over 28 days: %v", rows)
	}
	// Position is weighted by impressions: (3*100 + 9*50) / 150.
	if p := top["position"].(float64); p < 4.99 || p > 5.01 {
		t.Fatalf("position: %v", p)
	}
	if tot := s["totals"].(map[string]any); tot["impressions"].(float64) != 230*28 {
		t.Fatalf("totals: %v", tot)
	}
	q := owner.must(200, "GET", sb+"/search?group=query&sort=impressions&limit=1", nil)["data"].([]any)
	if len(q) != 1 || q[0].(map[string]any)["query"] != "cheap proxies" {
		t.Fatalf("queries: %v", q)
	}
	pq := owner.must(200, "GET", sb+"/search?group=page_query&keyword="+url.QueryEscape("cheap proxies"), nil)["data"].([]any)
	if len(pq) != 2 {
		t.Fatalf("pages for a query: %v", pq)
	}
	if c := owner.must(200, "GET", sb+"/search?contains=blog", nil)["data"].([]any); len(c) != 1 {
		t.Fatalf("contains: %v", c)
	}
	h := owner.must(200, "GET", sb+"/search/history?page="+url.QueryEscape("https://www.example.com/pricing"), nil)["data"].([]any)
	if len(h) != 28 || h[27].(map[string]any)["clicks"].(float64) != 12 {
		t.Fatalf("history: %v", h)
	}
	owner.must(400, "GET", sb+"/search?group=nope", nil)
	owner.must(400, "GET", sb+"/search?from=2026-10-10&to=2026-10-01", nil)

	ix := owner.must(200, "GET", sb+"/indexing", nil)
	sum := ix["summary"].(map[string]any)
	if sum["inspected"].(float64) != 2 || sum["not_indexed"].(float64) != 1 || sum["canonical_differs"].(float64) != 1 {
		t.Fatalf("indexing summary: %v", sum)
	}
	first := ix["data"].([]any)[0].(map[string]any)
	if first["url"] != "https://www.example.com/blog/proxies" || first["indexed"] != false || first["coverage"] != "Crawled - currently not indexed" {
		t.Fatalf("not indexed first: %v", first)
	}
	if n := len(owner.must(200, "GET", sb+"/indexing?state=canonical_differs", nil)["data"].([]any)); n != 1 {
		t.Fatalf("canonical differs: %d", n)
	}
	sm := owner.must(200, "GET", sb+"/sitemaps", nil)["data"].([]any)
	if len(sm) != 1 || sm[0].(map[string]any)["errors"].(float64) != 2 || sm[0].(map[string]any)["indexed"].(float64) != 31 {
		t.Fatalf("sitemaps: %v", sm)
	}

	// A second run re-reads the recent days and goes further back; pages
	// inspected lately wait their turn. A verdict that changes is kept.
	f.pageCalls.Store(0)
	before := f.inspections.Load()
	owner.must(200, "POST", base+"/analytics/"+sc+"/sync", nil)
	if n := f.pageCalls.Load(); n != 4+14 {
		t.Fatalf("second run: %d calls", n)
	}
	if f.inspections.Load() != before {
		t.Fatal("pages inspected this week are not inspected again")
	}
	if _, err := e.pool.Exec(context.Background(), `UPDATE url_inspections SET inspected_at = now() - interval '8 days'`); err != nil {
		t.Fatal(err)
	}
	f.inspect = func(string) (string, string, string) { return "FAIL", "Not found (404)", "" }
	owner.must(200, "POST", base+"/analytics/"+sc+"/sync", nil)
	ch := owner.must(200, "GET", sb+"/indexing?state=changed", nil)["data"].([]any)
	if len(ch) != 2 || ch[1].(map[string]any)["previous_verdict"] != "PASS" || ch[1].(map[string]any)["url"] != "https://www.example.com/pricing" {
		t.Fatalf("changed verdicts: %v", ch)
	}
	if src := owner.must(200, "GET", sb+"/search", nil)["source"].(map[string]any); src["oldest_day"] != time.Now().UTC().AddDate(0, 0, -1-89-28).Format(time.DateOnly) {
		t.Fatalf("backfill reaches back: %v", src)
	}

	// Another workspace sees none of it.
	ows := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Other"})["id"].(string)
	osite := owner.must(201, "POST", "/v1/workspaces/"+ows+"/seo-sites", map[string]any{"url": "https://www.example.com/"})
	if s := owner.must(200, "GET", "/v1/workspaces/"+ows+"/seo-sites/"+osite["id"].(string)+"/search", nil); s["source"] != nil {
		t.Fatalf("another workspace: %v", s)
	}
	owner.must(404, "GET", base+"/seo-sites/"+osite["id"].(string)+"/search", nil)

	synced := 0
	for _, ev := range got() {
		if ev.Name == "search_console.synced" && ev.WorkspaceID == ws {
			synced++
			if ev.Props["integration_id"] != sc {
				t.Fatalf("event: %+v", ev)
			}
		}
	}
	if synced != 3 {
		t.Fatalf("search_console.synced events: %d", synced)
	}
}
