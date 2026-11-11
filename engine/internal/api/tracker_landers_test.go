package api_test

import (
	"context"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// Landers (plans/ad-tracker.md, T6b): a list of destinations with a lander
// sends a click to the lander, and the lander's calls to action (/click and
// /click/N, with the click id) on to its offers; each lander click is kept,
// and gives the click the offer its conversions are credited to.
func TestTrackerLanders(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	host := ownTrackingDomain(t, e, ws, brand, "go.northpeak.example")

	src := owner.must(201, "POST", base+"/postback-sources", map[string]any{"name": "Acme Network", "click_id_param": "aff_sub"})
	token := strings.TrimPrefix(strings.SplitN(src["postback_url"].(string), "?", 2)[0], "https://"+host)
	a := owner.must(201, "POST", base+"/offers", map[string]any{"name": "Offer A", "url": "https://offers.northpeak.example/a", "source_id": src["id"]})
	b := owner.must(201, "POST", base+"/offers", map[string]any{"name": "Offer B", "url": "https://offers.northpeak.example/b", "source_id": src["id"]})
	l := owner.must(201, "POST", base+"/landers", map[string]any{"name": "Best of 2026", "url": "https://reviews.northpeak.example/best?c={click_id}", "ctas": 2})
	if l["click_url"] != "https://"+host+"/click" || len(l["cta_urls"].([]any)) != 2 || l["cta_urls"].([]any)[1] != "https://"+host+"/click/2" ||
		!strings.Contains(l["script"].(string), "https://"+host+"/click.js") || l["needs_domain"] != false {
		t.Fatalf("lander: %v", l)
	}
	for name, body := range map[string]map[string]any{
		"ctas":  {"name": "x1", "url": "https://northpeak.example/x", "ctas": 0},
		"url":   {"name": "x2", "url": "not a page"},
		"token": {"name": "x3", "url": "https://northpeak.example/x?s={nope}"},
	} {
		if st, _ := owner.do("POST", base+"/landers", body); st != 400 {
			t.Errorf("%s: %d, want 400", name, st)
		}
	}
	owner.must(409, "POST", base+"/landers", map[string]any{"name": "best of 2026", "url": "https://northpeak.example/x"})

	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Review push", "channel": "paid_ads", "source": "acme", "redirect_links": true})
	cpath := base + "/tracking-campaigns/" + c["code"].(string)
	link := owner.must(201, "POST", cpath+"/links", map[string]any{"url": "https://northpeak.example/own"})
	code := regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(link["redirect_url"].(string))[1]
	dpath := cpath + "/links/" + link["id"].(string) + "/destinations"
	owner.must(400, "PUT", dpath, map[string]any{"destinations": []any{map[string]any{"offer_id": a["id"], "lander_id": l["id"]}}})
	dests := owner.must(200, "PUT", dpath, map[string]any{"destinations": []any{
		map[string]any{"lander_id": l["id"]}, map[string]any{"offer_id": a["id"]}, map[string]any{"offer_id": b["id"]},
	}})["destinations"].([]any)
	if d := dests[0].(map[string]any); d["lander_id"] != l["id"] || d["lander_name"] != "Best of 2026" || d["url"] != "https://reviews.northpeak.example/best?c={click_id}" {
		t.Fatalf("destinations: %v", dests)
	}

	get := func(h, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Host = h
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	follow := func(path string) *url.URL {
		t.Helper()
		rec := get(host, path)
		if rec.Code != 302 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		loc, err := url.Parse(rec.Header().Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		return loc
	}

	// The click goes to the lander, never straight to an offer.
	var key string
	for range 5 {
		loc := follow("/l/" + code)
		key = loc.Query().Get("ofclk")
		if loc.Host != "reviews.northpeak.example" || loc.Query().Get("c") != key {
			t.Fatalf("a click went to %s, not the lander", loc)
		}
	}

	// Its calls to action go on to the offers: /click/N to the Nth, /click
	// by weight, each with the click id and its network's parameter.
	if loc := follow("/click/1?ofclk=" + key); loc.Path != "/a" || loc.Query().Get("aff_sub") != key || loc.Query().Get("ofclk") != key {
		t.Fatalf("/click/1 went to %s", loc)
	}
	if loc := follow("/click?ofclk=" + key); loc.Path != "/a" && loc.Path != "/b" {
		t.Fatalf("/click went to %s", loc)
	}
	if loc := follow("/click/2?ofclk=" + key); loc.Path != "/b" {
		t.Fatalf("/click/2 went to %s", loc)
	}
	if loc := follow("/click/9?ofclk=" + key); loc.Path != "/a" && loc.Path != "/b" {
		t.Fatalf("/click/9 (no such call to action) went to %s", loc)
	}

	// The conversion is credited to the offer of the last lander click
	// before it.
	follow("/click/2?ofclk=" + key)
	if rec := get(host, token+"?cid="+key+"&payout=3&txid=L-1"); rec.Code != 200 {
		t.Fatalf("postback: %d %s", rec.Code, rec.Body)
	}
	conv := owner.must(200, "GET", base+"/tracker-conversions?offer="+b["id"].(string), nil)["data"].([]any)
	if len(conv) != 1 || conv[0].(map[string]any)["transaction_id"] != "L-1" {
		t.Fatalf("offer B's conversions: %v", conv)
	}

	// Without a known click id, a recent lander click or the brand's own
	// domain, /click goes nowhere.
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"no click id":    get(host, "/click"),
		"unknown click":  get(host, "/click?ofclk=zzzzzzzzzzzzzzzz"),
		"shared domain":  get("", "/click?ofclk="+key),
		"bad call":       get(host, "/click/abc?ofclk="+key),
		"call too large": get(host, "/click/101?ofclk="+key),
	} {
		if rec.Code != 404 {
			t.Errorf("%s: %d, want 404", name, rec.Code)
		}
	}
	if _, err := e.pool.Exec(ctx, `UPDATE tracking_link_clicks SET lander_id = NULL WHERE click_key = $1`, key); err != nil {
		t.Fatal(err)
	}
	if rec := get(host, "/click?ofclk="+key); rec.Code != 404 {
		t.Errorf("a click that didn't go to a lander: %d, want 404", rec.Code)
	}

	// The script adds the click id to the page's /click links.
	if js := get(host, "/click.js").Body.String(); !strings.Contains(js, "ofclk") || !strings.Contains(js, "/click") {
		t.Fatalf("script: %s", js)
	}

	// The lander clicks are kept, by call to action.
	clicks := owner.must(200, "GET", base+"/lander-clicks?lander="+l["id"].(string), nil)["data"].([]any)
	if len(clicks) != 5 {
		t.Fatalf("lander clicks: %d, want 5: %v", len(clicks), clicks)
	}
	if m := clicks[0].(map[string]any); m["cta"] != float64(2) || m["offer_id"] != b["id"] || m["click_key"] != key {
		t.Fatalf("latest lander click: %v", m)
	}
	page := owner.must(200, "GET", base+"/lander-clicks?limit=2", nil)
	if len(page["data"].([]any)) != 2 || page["next_cursor"] == nil {
		t.Fatalf("page: %v", page)
	}
	if got := owner.must(200, "GET", base+"/landers/"+l["id"].(string), nil); got["lander_clicks_today"] != float64(5) || got["destinations"] != float64(1) {
		t.Fatalf("lander after its clicks: %v", got)
	}

	// A new URL goes to the destinations; a lander in use can't be archived.
	owner.must(200, "PATCH", base+"/landers/"+l["id"].(string), map[string]any{"url": "https://reviews.northpeak.example/best-2", "ctas": 3})
	dests = owner.must(200, "GET", dpath, nil)["destinations"].([]any)
	if dests[0].(map[string]any)["url"] != "https://reviews.northpeak.example/best-2" {
		t.Fatalf("destination after the lander's new URL: %v", dests)
	}
	owner.must(400, "PATCH", base+"/landers/"+l["id"].(string), map[string]any{"status": "archived"})

	// Another brand sees none of it.
	other := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Repocket"})
	obase := "/v1/brands/" + other["id"].(string)
	if n := len(owner.must(200, "GET", obase+"/landers", nil)["data"].([]any)); n != 0 {
		t.Fatalf("another brand sees %d landers", n)
	}
	if n := len(owner.must(200, "GET", obase+"/lander-clicks", nil)["data"].([]any)); n != 0 {
		t.Fatalf("another brand sees %d lander clicks", n)
	}
	owner.must(404, "GET", obase+"/landers/"+l["id"].(string), nil)

	found := map[string]bool{}
	for _, ev := range events() {
		found[ev.Name] = true
	}
	for _, name := range []string{"tracker_lander.created", "tracker_lander.updated"} {
		if !found[name] {
			t.Errorf("no %s event", name)
		}
	}
}
