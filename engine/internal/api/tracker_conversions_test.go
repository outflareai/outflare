package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// Tracker conversions by click id (plans/ad-tracker.md, T5a): a postback
// source's secret URL on the brand's own tracking domain records
// conversions in the click ledger, following its duplicate policy, click
// expiry and IP allowlist, and puts them on the person's timeline when the
// click's visit knew them.
func TestTrackerConversions(t *testing.T) {
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
	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Offer push", "channel": "paid_ads", "source": "acme", "redirect_links": true})
	l := owner.must(201, "POST", base+"/tracking-campaigns/"+c["code"].(string)+"/links", map[string]any{"url": "https://offers.northpeak.example/o1"})
	code := regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(l["redirect_url"].(string))[1]
	call := func(h, method, path string, form url.Values, addr string) *httptest.ResponseRecorder {
		t.Helper()
		var req *http.Request
		if method == "POST" {
			req = httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			req = httptest.NewRequest("GET", path, nil)
		}
		req.Host = h
		if addr != "" {
			req.RemoteAddr = addr
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	click := func() string {
		t.Helper()
		loc, _ := url.Parse(call(host, "GET", "/l/"+code, nil, "").Header().Get("Location"))
		return loc.Query().Get("ofclk")
	}

	// A source, shown with its postback URL once.
	src := owner.must(201, "POST", base+"/postback-sources", map[string]any{"name": "Acme Network", "currency": "eur"})
	pb, _ := src["postback_url"].(string)
	if !strings.HasPrefix(pb, "https://"+host+"/t/ofb_") || src["duplicate_policy"] != "ignore" || src["currency"] != "EUR" || src["needs_domain"] != false {
		t.Fatalf("source: %v", src)
	}
	token := strings.TrimPrefix(strings.SplitN(pb, "?", 2)[0], "https://"+host)
	if got := owner.must(200, "GET", base+"/postback-sources/"+src["id"].(string), nil); got["postback_url"] != nil {
		t.Fatalf("the secret was shown again: %v", got)
	}
	owner.must(409, "POST", base+"/postback-sources", map[string]any{"name": "acme network"})
	owner.must(400, "POST", base+"/postback-sources", map[string]any{"name": "Bad", "duplicate_policy": "merge"})
	owner.must(400, "POST", base+"/postback-sources", map[string]any{"name": "Bad", "ip_allowlist": []string{"not-an-ip"}})

	// A conversion, before the click is even written: the postback writes
	// waiting clicks first.
	k1 := click()
	rec := call(host, "GET", token+"?cid="+k1+"&payout=12.5&txid=T-1&type=Sale&p1=zone-9", nil, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"outcome":"accepted"`) {
		t.Fatalf("postback: %d %s", rec.Code, rec.Body)
	}
	list := owner.must(200, "GET", base+"/tracker-conversions", nil)["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("ledger: %v", list)
	}
	first := list[0].(map[string]any)
	if first["click_key"] != k1 || first["payout"] != "12.5" || first["currency"] != "EUR" || first["type"] != "sale" || first["status"] != "approved" ||
		first["campaign_id"] != c["id"] || first["params"].(map[string]any)["1"] != "zone-9" || first["record_id"] != nil {
		t.Fatalf("conversion: %v", first)
	}

	// The same transaction id again is ignored under the default policy.
	if rec := call(host, "GET", token+"?cid="+k1+"&payout=99&txid=T-1", nil, ""); !strings.Contains(rec.Body.String(), `"outcome":"duplicate"`) {
		t.Fatalf("duplicate: %s", rec.Body)
	}
	// "add" adds an upsell's payout; "update" updates the status.
	sid := src["id"].(string)
	owner.must(200, "PATCH", base+"/postback-sources/"+sid, map[string]any{"duplicate_policy": "add"})
	call(host, "POST", token, url.Values{"clickid": {k1}, "sum": {"7.5"}, "transaction_id": {"T-1"}}, "")
	owner.must(200, "PATCH", base+"/postback-sources/"+sid, map[string]any{"duplicate_policy": "update"})
	call(host, "GET", token+"?cid="+k1+"&payout=20&txid=T-1&status=declined&type=sale", nil, "")
	got := owner.must(200, "GET", base+"/tracker-conversions?source="+sid, nil)["data"].([]any)
	if len(got) != 1 || got[0].(map[string]any)["status"] != "declined" || got[0].(map[string]any)["payout"] != "20" || got[0].(map[string]any)["postbacks"] != float64(4) {
		t.Fatalf("after updates: %v", got)
	}
	// "new" records each one.
	owner.must(200, "PATCH", base+"/postback-sources/"+sid, map[string]any{"duplicate_policy": "new"})
	call(host, "GET", token+"?cid="+k1+"&payout=1&txid=T-1", nil, "")
	if n := len(owner.must(200, "GET", base+"/tracker-conversions", nil)["data"].([]any)); n != 2 {
		t.Fatalf("new policy: %d conversions", n)
	}

	// A conversion of a click whose visit knew the person goes on their
	// timeline.
	k2 := click()
	if err := e.srv.FlushClicks(ctx); err != nil {
		t.Fatal(err)
	}
	ana := owner.must(201, "POST", "/v1/workspaces/"+ws+"/records", map[string]any{"type": "person", "name": "Ana",
		"identities": []any{map[string]any{"namespace": "email", "value": "ana@acme.example"}}})["id"].(string)
	if _, err := e.pool.Exec(ctx, `WITH s AS (INSERT INTO site_sessions (workspace_id, brand_id, anonymous_id, record_id, started_at, last_seen_at)
			VALUES ($1, $2, 'anon-ana', $3, now(), now()) RETURNING id)
		UPDATE tracking_link_clicks SET session_id = (SELECT id FROM s) WHERE click_key = $4`, ws, brand, ana, k2); err != nil {
		t.Fatal(err)
	}
	if rec := call(host, "GET", token+"?cid="+k2+"&payout=3&txid=T-2&type=lead", nil, ""); rec.Code != 200 {
		t.Fatalf("person's postback: %d %s", rec.Code, rec.Body)
	}
	var onTimeline int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM customer_events WHERE workspace_id = $1 AND record_id = $2 AND name = 'tracker.converted'
		AND properties->>'payout_minor' = '300'`, ws, ana).Scan(&onTimeline); err != nil || onTimeline != 1 {
		t.Fatalf("timeline: %d, %v", onTimeline, err)
	}

	// Refused: a bad click id, an unknown one, the shared domain, a wrong
	// secret, an expired click, and an address off the allowlist.
	for name, want := range map[string]struct {
		host, path string
		status     int
	}{
		"no click id":    {host, token + "?payout=1", 400},
		"not a click id": {host, token + "?cid=abc", 400},
		"unknown click":  {host, token + "?cid=zzzzzzzzzzzzzzzz", 404},
		"shared domain":  {"", token + "?cid=" + k1, 404},
		"wrong secret":   {host, "/t/ofb_not_a_real_secret?cid=" + k1, 404},
		"bad status":     {host, token + "?cid=" + k1 + "&status=maybe", 400},
		"bad payout":     {host, token + "?cid=" + k1 + "&payout=lots", 400},
	} {
		if rec := call(want.host, "GET", want.path, nil, ""); rec.Code != want.status {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if _, err := e.pool.Exec(ctx, `UPDATE tracking_link_clicks SET clicked_at = now() - interval '40 days' WHERE click_key = $1`, k1); err != nil {
		t.Fatal(err)
	}
	if rec := call(host, "GET", token+"?cid="+k1+"&txid=T-9", nil, ""); rec.Code != 404 || !strings.Contains(rec.Body.String(), "expiry") {
		t.Fatalf("expired click: %d %s", rec.Code, rec.Body)
	}
	owner.must(200, "PATCH", base+"/postback-sources/"+sid, map[string]any{"ip_allowlist": []string{"203.0.113.0/24"}})
	if rec := call(host, "GET", token+"?cid="+k2+"&txid=T-3", nil, "198.51.100.1:4000"); rec.Code != 403 {
		t.Fatalf("off the allowlist: %d", rec.Code)
	}
	if rec := call(host, "GET", token+"?cid="+k2+"&txid=T-3", nil, "203.0.113.9:4000"); rec.Code != 200 {
		t.Fatalf("on the allowlist: %d %s", rec.Code, rec.Body)
	}

	// Rotating the secret retires the old URL; another brand sees nothing.
	rot := owner.must(200, "POST", base+"/postback-sources/"+sid+"/rotate", nil)
	if rec := call(host, "GET", token+"?cid="+k2+"&txid=T-4", nil, "203.0.113.9:4000"); rec.Code != 404 {
		t.Fatalf("old secret: %d", rec.Code)
	}
	newToken := strings.TrimPrefix(strings.SplitN(rot["postback_url"].(string), "?", 2)[0], "https://"+host)
	if rec := call(host, "GET", newToken+"?cid="+k2+"&txid=T-4", nil, "203.0.113.9:4000"); rec.Code != 200 {
		t.Fatalf("new secret: %d %s", rec.Code, rec.Body)
	}
	other := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Repocket"})
	if n := len(owner.must(200, "GET", "/v1/brands/"+other["id"].(string)+"/tracker-conversions", nil)["data"].([]any)); n != 0 {
		t.Fatalf("another brand sees %d conversions", n)
	}
	owner.must(404, "GET", "/v1/brands/"+other["id"].(string)+"/postback-sources/"+sid, nil)

	for _, name := range []string{"postback_source.created", "postback_source.rotated", "tracker_conversion.created", "tracker_conversion.updated"} {
		found := false
		for _, ev := range events() {
			found = found || ev.Name == name
		}
		if !found {
			t.Errorf("no %s event", name)
		}
	}
}
