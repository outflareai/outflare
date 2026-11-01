package api_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestPartnerCountryBlock: a program's blocked countries stop website
// visits from them (by the visitor's address, never the time zone) and new
// customers whose conversion call names one from crediting the partner.
func TestPartnerCountryBlock(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	key := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "northpeak.example", "origins": []string{"https://northpeak.example"}})["key"].(string)
	prog := owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate", "landing_url": "https://northpeak.example/"})
	if c := prog["blocked_countries"].([]any); len(c) != 0 {
		t.Fatalf("default = %v", c)
	}
	owner.must(400, "PATCH", base+"/partner-programs/affiliates", map[string]any{"blocked_countries": []string{"USA"}})
	prog = owner.must(200, "PATCH", base+"/partner-programs/affiliates", map[string]any{"blocked_countries": []string{"in", "br", "IN"}})
	if c := prog["blocked_countries"].([]any); len(c) != 2 || c[0] != "BR" || c[1] != "IN" {
		t.Fatalf("blocked = %v", c)
	}
	ana := owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "ana@acme.example"})
	conv := base + "/partner-conversions"

	n := 0
	// visit sends a page view with Ana's ref from address ip, in time zone tz.
	visit := func(ip, tz string) string {
		t.Helper()
		n++
		anon := "visitor-" + string(rune('a'+n)) + "-1"
		ev := map[string]any{"type": "page", "anonymousId": anon, "messageId": "m-" + anon, "timestamp": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
			"properties": map[string]any{"ref": ana["code"]},
			"context":    map[string]any{"timezone": tz, "page": map[string]any{"url": "https://northpeak.example/", "path": "/"}}}
		b, _ := json.Marshal(map[string]any{"key": key, "batch": []any{ev}})
		req := httptest.NewRequest("POST", "/v1/site/events", strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
		req.Header.Set("User-Agent", desktopUA)
		req.Header.Set("Origin", "https://northpeak.example")
		req.RemoteAddr = ip + ":51000"
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("send: %d %s", rec.Code, rec.Body)
		}
		return anon
	}
	visitCountry := func(anon string) (country, source string) {
		t.Helper()
		if err := e.pool.QueryRow(ctx, `SELECT country, country_source FROM site_sessions WHERE workspace_id = $1 AND anonymous_id = $2`, ws, anon).
			Scan(&country, &source); err != nil {
			t.Fatal(err)
		}
		return country, source
	}
	signup := func(body map[string]any) map[string]any {
		t.Helper()
		code, out := owner.do("POST", conv, body)
		if code != 200 && code != 201 {
			t.Fatalf("signup = %d %v", code, out)
		}
		return out
	}

	// 8.8.8.8 is in the US: allowed while only BR and IN are blocked.
	us := visit("8.8.8.8", "Europe/Berlin")
	if c, src := visitCountry(us); c != "US" || src != "ip" {
		t.Fatalf("visit country = %q %q", c, src)
	}
	if res := signup(map[string]any{"type": "signup", "customer_id": "u-1", "anonymous_id": us}); res["partner_id"] != ana["id"] {
		t.Fatalf("US visit = %v", res)
	}

	owner.must(200, "PATCH", base+"/partner-programs/affiliates", map[string]any{"blocked_countries": []string{"US"}})
	if res := signup(map[string]any{"type": "signup", "customer_id": "u-2", "anonymous_id": visit("8.8.8.8", "Europe/Berlin")}); res["reason"] != "no_partner" {
		t.Fatalf("blocked US visit = %v", res)
	}
	// An address with no country falls back to the time zone, which is
	// never trusted to block.
	tzOnly := visit("192.0.2.10", "America/New_York")
	if c, src := visitCountry(tzOnly); c != "US" || src != "timezone" {
		t.Fatalf("time zone visit = %q %q", c, src)
	}
	if res := signup(map[string]any{"type": "signup", "customer_id": "u-3", "anonymous_id": tzOnly}); res["partner_id"] != ana["id"] {
		t.Fatalf("time zone visit = %v", res)
	}

	// A conversion that names the partner and says the customer's country.
	owner.must(400, "POST", conv, map[string]any{"type": "signup", "customer_id": "u-4", "ref": ana["code"], "country": "United States"})
	if res := signup(map[string]any{"type": "signup", "customer_id": "u-4", "ref": ana["code"], "country": "us"}); res["reason"] != "country_blocked" || res["credited"] != false {
		t.Fatalf("blocked country = %v", res)
	}
	if res := signup(map[string]any{"type": "signup", "customer_id": "u-5", "ref": ana["code"], "country": "DE"}); res["partner_id"] != ana["id"] {
		t.Fatalf("allowed country = %v", res)
	}
}
