package api_test

import (
	"context"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"testing"

	"github.com/outflareai/outflare/engine/internal/fraud"
)

// A brand's click filter lists (plans/ad-tracker.md, B7b): an address, a
// range or a piece of a user agent on the list flags a redirect click as
// filter_list; the click still goes on.
func TestClickFilters(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	host := ownTrackingDomain(t, e, ws, brand, "go.northpeak.example")
	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Push", "channel": "paid_ads", "source": "acme", "redirect_links": true})
	l := owner.must(201, "POST", base+"/tracking-campaigns/"+c["id"].(string)+"/links", map[string]any{"url": "https://northpeak.example/own"})
	code := regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(l["redirect_url"].(string))[1]
	safari := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15"
	click := func(addr, ua string) string {
		t.Helper()
		req := httptest.NewRequest("GET", "/l/"+code, nil)
		req.Host = host
		req.RemoteAddr = addr
		req.Header.Set("User-Agent", ua)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		loc, err := url.Parse(rec.Header().Get("Location"))
		if err != nil || loc.Host != "northpeak.example" {
			t.Fatalf("click from %s went to %q", addr, rec.Header().Get("Location"))
		}
		return loc.Query().Get("ofclk")
	}

	// The lists: an address, a range written loosely, a user agent piece.
	owner.must(201, "POST", base+"/click-filters", map[string]any{"kind": "ip", "value": "198.51.100.7", "note": "Competitor's office"})
	r := owner.must(201, "POST", base+"/click-filters", map[string]any{"kind": "ip", "value": " 203.0.113.9/24 "})
	if r["value"] != "203.0.113.0/24" {
		t.Fatalf("range kept as %v", r["value"])
	}
	ua := owner.must(201, "POST", base+"/click-filters", map[string]any{"kind": "user_agent", "value": "HeadlessChrome"})
	if ua["value"] != "headlesschrome" {
		t.Fatalf("user agent kept as %v", ua["value"])
	}
	owner.must(409, "POST", base+"/click-filters", map[string]any{"kind": "ip", "value": "203.0.113.0/24"})
	for _, bad := range []map[string]any{
		{"kind": "ip", "value": "not an address"},
		{"kind": "ip", "value": "10.0.0.0/4"},
		{"kind": "user_agent", "value": "ab"},
		{"kind": "referrer", "value": "example.com"},
	} {
		owner.must(400, "POST", base+"/click-filters", bad)
	}
	list := owner.must(200, "GET", base+"/click-filters", nil)["data"].([]any)
	if len(list) != 3 || list[0].(map[string]any)["value"] != "198.51.100.7" || list[0].(map[string]any)["note"] != "Competitor's office" {
		t.Fatalf("list: %v", list)
	}

	// Clicks: on the list by address, by range and by user agent are
	// flagged and still go on; another isn't.
	byAddr := click("198.51.100.7:51000", safari)
	byRange := click("203.0.113.200:51000", safari)
	byAgent := click("192.0.2.10:51000", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/131.0.0.0 Safari/537.36")
	clean := click("192.0.2.11:51000", safari)
	if err := e.srv.FlushClicks(ctx); err != nil {
		t.Fatal(err)
	}
	reasons := func(key string) []string {
		t.Helper()
		var out []string
		if err := e.pool.QueryRow(ctx, `SELECT fraud_reasons FROM tracking_link_clicks WHERE click_key = $1`, key).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	for name, key := range map[string]string{"address": byAddr, "range": byRange, "user agent": byAgent} {
		if !slices.Contains(reasons(key), fraud.FilterList) {
			t.Fatalf("a click on the list by %s wasn't flagged: %v", name, reasons(key))
		}
	}
	if r := reasons(clean); len(r) != 0 {
		t.Fatalf("a clean click was flagged: %v", r)
	}

	// Removing an entry takes effect on the next click.
	owner.must(204, "DELETE", base+"/click-filters/"+ua["id"].(string), nil)
	owner.must(404, "DELETE", base+"/click-filters/"+ua["id"].(string), nil)
	again := click("192.0.2.10:51000", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/131.0.0.0 Safari/537.36")
	if err := e.srv.FlushClicks(ctx); err != nil {
		t.Fatal(err)
	}
	if r := reasons(again); slices.Contains(r, fraud.FilterList) {
		t.Fatalf("a removed filter still flagged: %v", r)
	}
}
