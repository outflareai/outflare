package api_test

import (
	"context"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"
)

// A brand's IP lookup settings (plans/ad-tracker.md, B7c; decision 12): off
// by default; on, they need an ip.score connection, and the exact address
// of the clicks they want is kept, for flagged and sampled clicks to look
// up now and for the rest in case they convert. Off deletes the addresses.
func TestIPLookupSettings(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	host := ownTrackingDomain(t, e, ws, brand, "go.northpeak.example")

	// The defaults: off; once on, flagged and converting clicks and 5% of
	// the rest, up to 5,000 a month, flagged at 75.
	v := owner.must(200, "GET", base+"/ip-lookups", nil)
	if v["enabled"] != false || v["flagged"] != true || v["converting"] != true || v["sample_percent"] != float64(5) || v["monthly_cap"] != float64(5000) || v["threshold"] != float64(75) {
		t.Fatalf("defaults: %v", v)
	}
	owner.must(400, "PUT", base+"/ip-lookups", map[string]any{"enabled": true})
	claude := owner.must(201, "POST", "/v1/workspaces/"+ws+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})
	owner.must(400, "PUT", base+"/ip-lookups", map[string]any{"enabled": true, "integration_id": claude["id"]})
	iq := owner.must(201, "POST", "/v1/workspaces/"+ws+"/integrations", map[string]any{"connector": "ipqualityscore", "credentials": map[string]any{"api_key": "test-good"}})
	for _, bad := range []map[string]any{{"sample_percent": 101}, {"monthly_cap": -1}, {"threshold": 0}} {
		owner.must(400, "PUT", base+"/ip-lookups", bad)
	}
	// On: converting clicks off, all of the rest sampled, so every click is
	// looked up now.
	v = owner.must(200, "PUT", base+"/ip-lookups", map[string]any{"enabled": true, "integration_id": iq["id"], "converting": false, "sample_percent": 100})
	if v["enabled"] != true || v["integration_id"] != iq["id"] || v["sample_percent"] != float64(100) {
		t.Fatalf("on: %v", v)
	}

	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Push", "channel": "paid_ads", "source": "acme", "redirect_links": true})
	l := owner.must(201, "POST", base+"/tracking-campaigns/"+c["id"].(string)+"/links", map[string]any{"url": "https://northpeak.example/own"})
	code := regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(l["redirect_url"].(string))[1]
	click := func(addr string) string {
		t.Helper()
		req := httptest.NewRequest("GET", "/l/"+code, nil)
		req.Host = host
		req.RemoteAddr = addr
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		loc, _ := url.Parse(rec.Header().Get("Location"))
		return loc.Query().Get("ofclk")
	}
	key := click("203.0.113.20:51000")
	if err := e.srv.FlushClicks(ctx); err != nil {
		t.Fatal(err)
	}
	var ip, ua string
	var now bool
	if err := e.pool.QueryRow(ctx, `SELECT host(ip), user_agent, look_up FROM tracker_click_addresses WHERE click_key = $1 AND brand_id = $2`, key, brand).Scan(&ip, &ua, &now); err != nil ||
		ip != "203.0.113.20" || ua == "" || !now {
		t.Fatalf("kept address: %q %q %v %v", ip, ua, now, err)
	}

	// Sampling none and not converting: nothing is kept.
	owner.must(200, "PUT", base+"/ip-lookups", map[string]any{"sample_percent": 0, "flagged": false})
	none := click("203.0.113.21:51000")
	if err := e.srv.FlushClicks(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM tracker_click_addresses WHERE click_key = $1`, none).Scan(&n); err != nil || n != 0 {
		t.Fatalf("an unwanted click's address was kept: %d %v", n, err)
	}
	// Converting clicks: kept, to be looked up only if it converts.
	owner.must(200, "PUT", base+"/ip-lookups", map[string]any{"converting": true})
	later := click("203.0.113.22:51000")
	if err := e.srv.FlushClicks(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT look_up FROM tracker_click_addresses WHERE click_key = $1`, later).Scan(&now); err != nil || now {
		t.Fatalf("a converting-only click: %v %v", now, err)
	}

	// Off deletes the brand's kept addresses at once.
	owner.must(200, "PUT", base+"/ip-lookups", map[string]any{"enabled": false})
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM tracker_click_addresses WHERE brand_id = $1`, brand).Scan(&n); err != nil || n != 0 {
		t.Fatalf("addresses left after turning lookups off: %d %v", n, err)
	}
	// The 30-day prune deletes old addresses.
	owner.must(200, "PUT", base+"/ip-lookups", map[string]any{"enabled": true})
	old := click("203.0.113.23:51000")
	if err := e.srv.FlushClicks(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE tracker_click_addresses SET created_at = now() - interval '31 days' WHERE click_key = $1`, old); err != nil {
		t.Fatal(err)
	}
	if n, err := e.srv.PruneClickAddresses(ctx); err != nil || n != 1 {
		t.Fatalf("prune: %d %v", n, err)
	}
}
