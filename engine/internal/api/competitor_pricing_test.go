package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// pricingSite is Geonode reading pricing pages: the competitor's page is
// built in the browser (a plain read shows no prices), the brand's is a
// table, and one page has no prices at all.
type pricingSite struct {
	mu    sync.Mutex
	rival string
}

func (f *pricingSite) page(url string, render bool) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case url == "https://ghostpeak.example/pricing" && render:
		return f.rival
	case url == "https://northpeak.example/pricing":
		return `<html><body><table><tr><th>Plan</th><th>Price</th></tr><tr><td>Residential</td><td>$3.50 / GB</td></tr></table></body></html>`
	case url == "https://ghostpeak.example/about":
		return `<html><body><h2>About us</h2><p>We make maps.</p></body></html>`
	}
	return `<html><body><div id="root"></div></body></html>`
}

func (f *pricingSite) set(page string) {
	f.mu.Lock()
	f.rival = page
	f.mu.Unlock()
}

func (f *pricingSite) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(geonodeJobs(nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Api-Key") != "gn-key" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"code": "UNAUTHORIZED", "message": "bad key", "retryable": false}`)
			return
		}
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
			URL      string `json:"url"`
			RenderJS bool   `json:"render_js"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		json.NewEncoder(w).Encode(map[string]any{
			"data":           map[string]any{"html": f.page(req.URL, req.RenderJS)},
			"metadata":       map[string]any{"http_status": 200, "url": req.URL},
			"tokens_charged": 1,
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

const rivalCards = `<html><body><nav><h3>Menu</h3></nav><h1>Pricing</h1>
	<div><h3>Starter</h3><p><span>$</span><span>19</span><span>/mo</span></p></div>
	<div><h3>Pro</h3><p>$49 per month</p></div></body></html>`

// Prices: a pricing page for the brand and each tracked competitor, on its
// own site, read weekly through the workspace's page reader (in a browser
// when a plain read shows no prices), with every change kept.
func TestCompetitorPricing(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://www.northpeak.example"})["id"].(string)
	exec := func(q string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, q+` RETURNING id::text`, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	site := exec(`INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, 'northpeak.example', 'https://northpeak.example/', 'tok')`, ws, brand)
	rival := exec(`INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, status) VALUES ($1, $2, $3, 'ghostpeak.example', 'Ghostpeak', 'tracked')`, ws, site, brand)
	gone := exec(`INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, status) VALUES ($1, $2, $3, 'gone.example', 'Gone', 'dismissed')`, ws, site, brand)
	root := "/v1/brands/" + brand + "/competition/pricing"

	got := owner.must(200, "GET", root, nil)
	entries := got["entries"].([]any)
	if got["needs_reader"] != true || len(entries) != 2 || entries[0].(map[string]any)["competitor_id"] != nil ||
		entries[0].(map[string]any)["host"] != "northpeak.example" || entries[1].(map[string]any)["competitor_id"] != rival {
		t.Fatalf("pricing: %v", got)
	}
	owner.must(409, "POST", root+"/refresh", nil)

	// Only a page on its own site; only a tracked competitor.
	owner.must(400, "PUT", root, map[string]any{"competitor_id": rival, "url": "https://other.example/pricing"})
	owner.must(400, "PUT", root, map[string]any{"competitor_id": rival, "url": "ftp://ghostpeak.example/pricing"})
	owner.must(409, "PUT", root, map[string]any{"competitor_id": gone, "url": "https://gone.example/pricing"})
	owner.must(404, "PUT", root, map[string]any{"competitor_id": "00000000-0000-0000-0000-000000000001", "url": "https://ghostpeak.example/pricing"})

	fake := &pricingSite{rival: rivalCards}
	e.integrations.Client.BaseURLs["geonode"] = fake.server(t).URL
	gn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "geonode", "credentials": map[string]string{"api_key": "gn-key"}})["id"].(string)
	owner.must(200, "PUT", root, map[string]any{"competitor_id": rival, "url": "https://ghostpeak.example/pricing#plans"})
	got = owner.must(200, "PUT", root, map[string]any{"url": "https://northpeak.example/pricing"})
	page := func(got map[string]any, i int) map[string]any {
		t.Helper()
		p, _ := got["entries"].([]any)[i].(map[string]any)["page"].(map[string]any)
		if p == nil {
			t.Fatalf("no page %d: %v", i, got)
		}
		return p
	}
	if p := page(got, 1); p["url"] != "https://ghostpeak.example/pricing" || p["last_read_at"] != nil || got["needs_reader"] != false {
		t.Fatalf("set: %v", got)
	}

	// The first read: the competitor's page needs the browser.
	if err := e.srv.ReadPricingPages(ctx, ws, gn); err != nil {
		t.Fatal(err)
	}
	got = owner.must(200, "GET", root, nil)
	mine, theirs := page(got, 0), page(got, 1)
	if plans := theirs["plans"].([]any); len(plans) != 2 || plans[0].(map[string]any)["name"] != "Starter" || plans[0].(map[string]any)["price"] != 19.0 ||
		plans[0].(map[string]any)["period"] != "month" || len(theirs["changes"].([]any)) != 0 || theirs["last_read_at"] == nil {
		t.Fatalf("their plans: %v", theirs)
	}
	if plans := mine["plans"].([]any); len(plans) != 1 || plans[0].(map[string]any)["period"] != "gb" {
		t.Fatalf("my plans: %v", mine)
	}

	// A week later: Pro is up and Team is new. The same plans again keep
	// nothing new.
	fake.set(`<html><body><h3>Starter</h3><p>$19/mo</p><h3>Pro</h3><p>$59/mo</p><h3>Team</h3><p>$99/mo</p></body></html>`)
	due := func() {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `UPDATE pricing_pages SET next_read_at = now() - interval '1 minute' WHERE workspace_id = $1`, ws); err != nil {
			t.Fatal(err)
		}
		if err := e.srv.ReadPricingPages(ctx, ws, gn); err != nil {
			t.Fatal(err)
		}
	}
	due()
	due()
	theirs = page(owner.must(200, "GET", root, nil), 1)
	kinds := map[string]string{}
	for _, c := range theirs["changes"].([]any) {
		c := c.(map[string]any)
		kinds[c["plan"].(string)] = c["kind"].(string)
	}
	if kinds["Pro"] != "price_up" || kinds["Team"] != "added" || len(kinds) != 2 || theirs["changed_at"] == nil {
		t.Fatalf("changes: %v", theirs)
	}
	history := owner.must(200, "GET", root+"/"+theirs["id"].(string)+"/history", nil)["reads"].([]any)
	if len(history) != 2 {
		t.Fatalf("history: %v", history)
	}
	owner.must(404, "GET", root+"/"+rival+"/history", nil)

	// Read now: every page was read today, so none is read again until
	// a day has passed.
	r := owner.must(202, "POST", root+"/refresh", nil)
	if r["pages"] != 0.0 || r["skipped"] != 2.0 || r["queued"] != false {
		t.Fatalf("refresh: %v", r)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE pricing_pages SET last_read_at = now() - interval '2 days' WHERE competitor_id IS NULL`); err != nil {
		t.Fatal(err)
	}
	if r := owner.must(202, "POST", root+"/refresh", nil); r["pages"] != 1.0 || r["queued"] != true {
		t.Fatalf("refresh: %v", r)
	}

	// A page with no prices says so; clearing a page stops reading it.
	owner.must(200, "PUT", root, map[string]any{"competitor_id": rival, "url": "https://ghostpeak.example/about"})
	if err := e.srv.ReadPricingPages(ctx, ws, gn); err != nil {
		t.Fatal(err)
	}
	if p := page(owner.must(200, "GET", root, nil), 1); p["last_error"] != "no_prices_found" {
		t.Fatalf("no prices: %v", p)
	}
	got = owner.must(200, "PUT", root, map[string]any{"competitor_id": rival, "url": ""})
	if got["entries"].([]any)[1].(map[string]any)["page"] != nil {
		t.Fatalf("cleared: %v", got)
	}

	seen := map[string]bool{}
	for _, ev := range events() {
		seen[ev.Name] = true
	}
	for _, name := range []string{"pricing_page.set", "pricing_page.read", "pricing_page.changed", "pricing_page.read_failed", "pricing.refresh_requested", "pricing_page.removed"} {
		if !seen[name] {
			t.Errorf("no %s event", name)
		}
	}
}
