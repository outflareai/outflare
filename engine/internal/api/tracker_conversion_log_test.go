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
)

// The tracker conversion log and webhooks (plans/ad-tracker.md, T5c): every
// postback and pixel call is logged with what it sent and why it was or
// wasn't recorded, and a recorded conversion is a tracker_conversion.created
// webhook with its payout and click.
func TestTrackerConversionLog(t *testing.T) {
	e, owner, ws, _ := webhookEnv(t)
	rc := newReceiver(t)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	host := ownTrackingDomain(t, e, ws, brand, "go.northpeak.example")
	owner.must(201, "POST", "/v1/workspaces/"+ws+"/webhooks", map[string]any{"url": rc.URL + "/hook", "events": []string{"tracker_conversion.*"}})
	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Offer push", "channel": "paid_ads", "source": "acme", "redirect_links": true})
	l := owner.must(201, "POST", base+"/tracking-campaigns/"+c["code"].(string)+"/links", map[string]any{"url": "https://offers.northpeak.example/o1"})
	code := regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(l["redirect_url"].(string))[1]
	get := func(h, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Host = h
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	loc, _ := url.Parse(get(host, "/l/"+code).Header().Get("Location"))
	key := loc.Query().Get("ofclk")
	src := owner.must(201, "POST", base+"/postback-sources", map[string]any{"name": "Acme Network"})
	token := strings.TrimPrefix(strings.SplitN(src["postback_url"].(string), "?", 2)[0], "https://"+host)

	// A recorded conversion, then calls that aren't recorded.
	if rec := get(host, token+"?cid="+key+"&payout=12.5&txid=L-1&type=sale&p3=widget-4"); rec.Code != 200 {
		t.Fatalf("postback: %d %s", rec.Code, rec.Body)
	}
	get(host, token+"?cid=zzzzzzzzzzzzzzzz&txid=L-2")
	get(host, token+"?payout=3")
	get("", token+"?cid="+key)
	get(host, token+"/pixel?cid="+key+"&txid=L-3")
	get(host, "/t/ofb_not_a_real_secret?cid="+key)

	logs := owner.must(200, "GET", base+"/tracker-conversion-log", nil)["data"].([]any)
	if len(logs) != 5 {
		t.Fatalf("log has %d calls, want 5 (the unknown secret isn't kept): %v", len(logs), logs)
	}
	by := map[string]map[string]any{}
	for _, x := range logs {
		m := x.(map[string]any)
		by[m["via"].(string)+":"+m["outcome"].(string)] = m
	}
	ok := by["postback:accepted"]
	if ok == nil || ok["conversion_id"] == nil || ok["click_key"] != key || ok["status"] != float64(200) ||
		ok["request"].(map[string]any)["p3"] != "widget-4" || ok["request"].(map[string]any)["payout"] != "12.5" {
		t.Fatalf("accepted call: %v", ok)
	}
	if m := by["postback:unknown_click"]; m == nil || m["status"] != float64(404) || !strings.Contains(m["reason"].(string), "click id") {
		t.Fatalf("unknown click: %v", by)
	}
	if m := by["postback:invalid"]; m == nil || !strings.Contains(m["reason"].(string), "cid") {
		t.Fatalf("invalid: %v", by)
	}
	if by["postback:wrong_domain"] == nil || by["pixel:accepted"] == nil {
		t.Fatalf("calls: %v", by)
	}
	if n := len(owner.must(200, "GET", base+"/tracker-conversion-log?via=pixel", nil)["data"].([]any)); n != 1 {
		t.Fatalf("pixel calls: %d", n)
	}
	if n := len(owner.must(200, "GET", base+"/tracker-conversion-log?outcome=unknown_click", nil)["data"].([]any)); n != 1 {
		t.Fatalf("unknown_click calls: %d", n)
	}
	page := owner.must(200, "GET", base+"/tracker-conversion-log?limit=2", nil)
	if len(page["data"].([]any)) != 2 || page["next_cursor"] == nil {
		t.Fatalf("page: %v", page)
	}
	owner.must(400, "GET", base+"/tracker-conversion-log?via=fax", nil)
	other := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Repocket"})
	if n := len(owner.must(200, "GET", "/v1/brands/"+other["id"].(string)+"/tracker-conversion-log", nil)["data"].([]any)); n != 0 {
		t.Fatalf("another brand sees %d calls", n)
	}

	// The two recorded conversions are webhooks, with the conversion's data.
	deadline := time.Now().Add(10 * time.Second)
	for rc.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if rc.count() != 2 {
		t.Fatalf("receiver got %d deliveries, want 2", rc.count())
	}
	found := false
	for _, b := range rc.bodies {
		var ev struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(b, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type == "tracker_conversion.created" && ev.Data["transaction_id"] == "L-1" {
			found = ev.Data["payout"] == "12.5" && ev.Data["click_key"] == key && ev.Data["campaign_id"] == c["id"] && ev.Data["type"] == "sale"
		}
	}
	if !found {
		t.Fatalf("webhook: %s", rc.bodies)
	}
}
