package api_test

import (
	"context"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/fraud"
)

// The honeypot (plans/ad-tracker.md, B7b): the lander script adds a link no
// person can see or reach; following it flags the click the lander was
// opened with, and answers the same empty 204 whatever happened.
func TestTrackerHoneypot(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	host := ownTrackingDomain(t, e, ws, brand, "go.northpeak.example")
	l := owner.must(201, "POST", base+"/landers", map[string]any{"name": "Quiz", "url": "https://reviews.northpeak.example/quiz", "ctas": 1})
	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Quiz push", "channel": "paid_ads", "source": "acme", "redirect_links": true})
	cpath := base + "/tracking-campaigns/" + c["id"].(string)
	link := owner.must(201, "POST", cpath+"/links", map[string]any{"url": "https://northpeak.example/own"})
	owner.must(200, "PUT", cpath+"/links/"+link["id"].(string)+"/destinations", map[string]any{"destinations": []any{map[string]any{"lander_id": l["id"]}}})
	code := regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(link["redirect_url"].(string))[1]
	get := func(h, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Host = h
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}

	// The script carries the hidden link, out of sight, sound and tab order.
	script := get(host, "/click.js").Body.String()
	for _, want := range []string{`/hp?ofclk=`, `aria-hidden`, `tabIndex=-1`, `nofollow`, `left:-10000px`} {
		if !strings.Contains(script, want) {
			t.Fatalf("script lacks %q: %s", want, script)
		}
	}

	// A click lands on the lander; following the hidden link flags it.
	loc, _ := url.Parse(get(host, "/l/"+code).Header().Get("Location"))
	key := loc.Query().Get("ofclk")
	if loc.Host != "reviews.northpeak.example" || key == "" {
		t.Fatalf("click went to %v", loc)
	}
	reasons := func() []string {
		t.Helper()
		var out []string
		if err := e.pool.QueryRow(ctx, `SELECT fraud_reasons FROM tracking_link_clicks WHERE click_key = $1`, key).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	rec := get(host, "/hp?ofclk="+key)
	if rec.Code != 204 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("honeypot: %d %v", rec.Code, rec.Header())
	}
	if r := reasons(); !slices.Equal(r, []string{fraud.Honeypot}) {
		t.Fatalf("reasons after the honeypot: %v", r)
	}
	// Again, it is flagged once; an unknown click, a bad key and another
	// domain get the same empty answer and change nothing.
	get(host, "/hp?ofclk="+key)
	if r := reasons(); len(r) != 1 {
		t.Fatalf("flagged twice: %v", r)
	}
	for _, path := range []string{"/hp?ofclk=zzzzzzzzzzzzzzzz", "/hp?ofclk=not-a-key", "/hp"} {
		if rec := get(host, path); rec.Code != 204 {
			t.Fatalf("%s: %d", path, rec.Code)
		}
	}
	// A second click, hit on a domain that isn't the brand's, stays clean.
	loc2, _ := url.Parse(get(host, "/l/"+code).Header().Get("Location"))
	key2 := loc2.Query().Get("ofclk")
	if rec := get("elsewhere.example", "/hp?ofclk="+key2); rec.Code != 204 {
		t.Fatalf("other domain: %d", rec.Code)
	}
	if err := e.srv.FlushClicks(ctx); err != nil {
		t.Fatal(err)
	}
	var other []string
	if err := e.pool.QueryRow(ctx, `SELECT fraud_reasons FROM tracking_link_clicks WHERE click_key = $1`, key2).Scan(&other); err != nil || len(other) != 0 {
		t.Fatalf("a hit on another domain flagged the click: %v %v", other, err)
	}
}
