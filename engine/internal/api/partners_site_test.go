package api_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPartnerSignupWays: the three ways to record a sign-up. The website's
// outflare.signup() makes the lead from the visitor's own ref visit but
// pays nothing unless the program trusts it; the server credits by the
// visitor's anonymous id too.
func TestPartnerSignupWays(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	key := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "northpeak.example", "origins": []string{"https://northpeak.example"}})["key"].(string)
	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate", "landing_url": "https://northpeak.example/"})
	owner.must(200, "PUT", base+"/partner-programs/affiliates/rules", map[string]any{"rules": []any{
		map[string]any{"trigger": "signup", "side": "partner", "kind": "fixed", "amount_cents": 500},
	}})
	ana := owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "ana@acme.example"})
	visit := func(anon string) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `INSERT INTO site_sessions (workspace_id, anonymous_id, started_at, last_seen_at, ref, landing_url)
			VALUES ($1, $2, now() - interval '1 day', now() - interval '1 day', $3, 'https://northpeak.example/')`, ws, anon, ana["code"]); err != nil {
			t.Fatal(err)
		}
	}
	signup := func(origin, anon, email string) int {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"key": key, "anonymousId": anon, "email": email})
		req := httptest.NewRequest("POST", "/v1/site/signup", strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "text/plain")
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec.Code
	}
	rewards := func() (n int, cents int64) {
		e.pool.QueryRow(ctx, `SELECT count(*), coalesce(sum(amount_cents), 0) FROM partner_rewards WHERE workspace_id = $1`, ws).Scan(&n, &cents) //nolint:errcheck
		return
	}
	referrals := func() (n int) {
		e.pool.QueryRow(ctx, `SELECT count(*) FROM partner_referrals WHERE workspace_id = $1 AND membership_id = $2`, ws, ana["id"]).Scan(&n) //nolint:errcheck
		return
	}

	// 1. The browser alone: the lead shows, nothing is paid.
	visit("visitor-one-1")
	if code := signup("https://elsewhere.example", "visitor-one-1", "cara@example.com"); code != 403 {
		t.Fatalf("another origin = %d", code)
	}
	if code := signup("https://northpeak.example", "visitor-one-1", "not an address"); code != 400 {
		t.Fatalf("bad email = %d", code)
	}
	if code := signup("https://northpeak.example", "visitor-one-1", "cara@example.com"); code != 200 {
		t.Fatalf("signup = %d", code)
	}
	if n, _ := rewards(); referrals() != 1 || n != 0 {
		t.Fatalf("browser signup: referrals %d, rewards %d", referrals(), n)
	}
	var source string
	e.pool.QueryRow(ctx, `SELECT source FROM partner_conversions WHERE workspace_id = $1`, ws).Scan(&source) //nolint:errcheck
	if source != "browser" {
		t.Fatalf("source = %q", source)
	}
	// A visitor with no partner visit is answered the same, and credits nobody.
	if code := signup("https://northpeak.example", "visitor-none", "dan@example.com"); code != 200 || referrals() != 1 {
		t.Fatalf("no visit: %d, referrals %d", code, referrals())
	}

	// The program trusts browser sign-ups: the next one pays.
	prog := owner.must(200, "PATCH", base+"/partner-programs/affiliates", map[string]any{"pay_browser_signups": true})
	if prog["pay_browser_signups"] != true {
		t.Fatalf("program = %v", prog["pay_browser_signups"])
	}
	visit("visitor-two-2")
	signup("https://northpeak.example", "visitor-two-2", "eve@example.com")
	if n, cents := rewards(); n != 1 || cents != 500 {
		t.Fatalf("trusted browser signup: %d rewards, %d cents", n, cents)
	}

	// 2. The server alone, with the visitor's anonymous id: credited and paid.
	visit("visitor-three-3")
	res := owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "signup", "customer_id": "u-3", "anonymous_id": "visitor-three-3"})
	if res["credited"] != true || res["partner_id"] != ana["id"] {
		t.Fatalf("server signup = %v", res)
	}
	if n, _ := rewards(); n != 2 {
		t.Fatalf("rewards after server signup = %d", n)
	}
	owner.must(400, "POST", base+"/partner-conversions", map[string]any{"type": "signup", "customer_id": "u-4", "anonymous_id": "bad id!"})
}
