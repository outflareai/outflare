package api_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/fraud"
	"github.com/outflareai/outflare/engine/internal/geoip/geoiptest"
)

// First-party fraud signals (plans/ad-tracker.md, B7a): a click from a
// hosting network or an address clicking over and over is flagged, still
// goes on, and counts against no cap; a conversion inherits its click's
// reasons and is flagged when it comes too soon after the click.
func TestTrackerFraudSignals(t *testing.T) {
	geoiptest.Use(t)
	defer fraud.SetHostingForTests(fraud.HostingNetwork{ASN: 64502, Name: "Acme Cloud Hosting", Source: "https://example.com/AS64502"})()
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
	cpath := base + "/tracking-campaigns/" + c["code"].(string)
	capped := owner.must(201, "POST", cpath+"/links", map[string]any{"url": "https://northpeak.example/own"})
	plain := owner.must(201, "POST", cpath+"/links", map[string]any{"url": "https://northpeak.example/other", "content": "other"})
	codeOf := func(l map[string]any) string {
		return regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(l["redirect_url"].(string))[1]
	}
	owner.must(200, "PUT", cpath+"/links/"+capped["id"].(string)+"/destinations", map[string]any{"destinations": []any{
		map[string]any{"url": "https://offers.northpeak.example/a", "daily_click_cap": 1},
	}})
	click := func(addr, code string) string {
		t.Helper()
		req := httptest.NewRequest("GET", "/l/"+code, nil)
		req.Host = host
		req.RemoteAddr = addr
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		loc, _ := url.Parse(rec.Header().Get("Location"))
		return loc.Path + "?" + loc.Query().Get("ofclk")
	}
	split := func(s string) (path, key string) {
		p, k, _ := strings.Cut(s, "?")
		return p, k
	}

	// A click from a hosting network goes to the capped destination but
	// doesn't use its cap; a person's click then does.
	path, hostKey := split(click(geoiptest.HostIP+":51000", codeOf(capped)))
	if path != "/a" {
		t.Fatalf("a hosting click went to %s", path)
	}
	path, personKey := split(click(geoiptest.SpringfieldIP+":51000", codeOf(capped)))
	if path != "/a" {
		t.Fatalf("a person's click after a hosting click went to %s: the hosting click used the cap", path)
	}
	if path, _ = split(click("192.0.2.30:51000", codeOf(capped))); path != "/own" {
		t.Fatalf("a click past the cap went to %s", path)
	}

	// An address and browser clicking a link over and over: after the
	// tenth click in an hour, each is flagged.
	for range 12 {
		click("192.0.2.40:51000", codeOf(plain))
	}
	if err := e.srv.FlushClicks(ctx); err != nil {
		t.Fatal(err)
	}
	var reasons []string
	if err := e.pool.QueryRow(ctx, `SELECT fraud_reasons FROM tracking_link_clicks WHERE click_key = $1`, hostKey).Scan(&reasons); err != nil ||
		len(reasons) != 1 || reasons[0] != fraud.DatacenterNetwork {
		t.Fatalf("hosting click reasons = %v, %v", reasons, err)
	}
	links := map[string]map[string]any{}
	for _, x := range owner.must(200, "GET", cpath+"/links", nil)["data"].([]any) {
		m := x.(map[string]any)
		links[m["id"].(string)] = m
	}
	if l := links[plain["id"].(string)]; l["suspect_clicks"] != float64(2) || l["clicks"] != float64(12) {
		t.Fatalf("repeat-clicked link: %v", l)
	}
	if l := links[capped["id"].(string)]; l["suspect_clicks"] != float64(1) || l["clicks"] != float64(3) {
		t.Fatalf("capped link: %v", l)
	}

	// Conversions inherit their click's reasons; one within seconds of
	// its click is flagged too. A person's conversion half an hour later
	// isn't.
	src := owner.must(201, "POST", base+"/postback-sources", map[string]any{"name": "Acme Network"})
	token := strings.TrimPrefix(strings.SplitN(src["postback_url"].(string), "?", 2)[0], "https://"+host)
	postback := func(query string) {
		t.Helper()
		req := httptest.NewRequest("GET", token+"?"+query, nil)
		req.Host = host
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("postback %s: %d %s", query, rec.Code, rec.Body)
		}
	}
	postback("cid=" + hostKey + "&txid=F-1&payout=5")
	postback("cid=" + personKey + "&txid=F-2&payout=5&converted_at=" + url.QueryEscape(time.Now().Add(30*time.Minute).UTC().Format(time.RFC3339)))
	suspect := owner.must(200, "GET", base+"/tracker-conversions?suspect=true", nil)["data"].([]any)
	if len(suspect) != 1 {
		t.Fatalf("suspect conversions: %v", suspect)
	}
	got := suspect[0].(map[string]any)
	if r := got["fraud_reasons"].([]any); got["transaction_id"] != "F-1" || len(r) != 2 || r[0] != fraud.DatacenterNetwork || r[1] != fraud.FastConversion {
		t.Fatalf("suspect conversion: %v", got)
	}
	clean := owner.must(200, "GET", base+"/tracker-conversions?suspect=false", nil)["data"].([]any)
	if len(clean) != 1 || clean[0].(map[string]any)["transaction_id"] != "F-2" || len(clean[0].(map[string]any)["fraud_reasons"].([]any)) != 0 {
		t.Fatalf("clean conversions: %v", clean)
	}
	owner.must(400, "GET", base+"/tracker-conversions?suspect=maybe", nil)

	// A direct click (T8) from a hosting network is flagged too.
	ts := owner.must(201, "POST", base+"/traffic-sources", map[string]any{"name": "Acme Native"})
	direct := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Native", "channel": "paid_ads", "source": "acme", "traffic_source": ts["id"]})
	owner.must(201, "POST", base+"/tracking-campaigns/"+direct["code"].(string)+"/links", map[string]any{"url": "https://northpeak.example/winter"})
	sk := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "Northpeak", "origins": []string{"https://northpeak.example"}})
	const directKey = "h0sting0direct00"
	ev := map[string]any{"type": "page", "anonymousId": "eve", "messageId": "fraud-direct", "properties": map[string]any{"ofc": direct["code"], "ofclk": directKey, "direct": true},
		"context": map[string]any{"page": map[string]any{"url": "https://northpeak.example/winter?ofc=" + direct["code"].(string), "path": "/winter"}}}
	b, _ := json.Marshal(map[string]any{"key": sk["key"], "batch": []any{ev}})
	req := httptest.NewRequest("POST", "/v1/site/events", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	req.Header.Set("Origin", "https://northpeak.example")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15")
	req.RemoteAddr = geoiptest.HostIP + ":51000"
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("site event: %d %s", rec.Code, rec.Body)
	}
	if err := e.pool.QueryRow(ctx, `SELECT fraud_reasons FROM tracking_link_clicks WHERE click_key = $1 AND direct`, directKey).Scan(&reasons); err != nil ||
		len(reasons) != 1 || reasons[0] != fraud.DatacenterNetwork {
		t.Fatalf("direct hosting click reasons = %v, %v", reasons, err)
	}

	// The brand's user agent filters (B7b) flag a direct click too; site
	// events keep no address, so address filters can't.
	owner.must(201, "POST", base+"/click-filters", map[string]any{"kind": "user_agent", "value": "Version/18.1 Safari"})
	const filteredKey = "f1ltered0direct0"
	ev["anonymousId"], ev["messageId"] = "frank", "fraud-direct-2"
	ev["properties"].(map[string]any)["ofclk"] = filteredKey
	b, _ = json.Marshal(map[string]any{"key": sk["key"], "batch": []any{ev}})
	req = httptest.NewRequest("POST", "/v1/site/events", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	req.Header.Set("Origin", "https://northpeak.example")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15")
	req.RemoteAddr = geoiptest.SpringfieldIP + ":51000"
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("site event: %d %s", rec.Code, rec.Body)
	}
	if err := e.pool.QueryRow(ctx, `SELECT fraud_reasons FROM tracking_link_clicks WHERE click_key = $1 AND direct`, filteredKey).Scan(&reasons); err != nil ||
		len(reasons) != 1 || reasons[0] != fraud.FilterList {
		t.Fatalf("direct filtered click reasons = %v, %v", reasons, err)
	}
}
