package api_test

import (
	"testing"
	"time"
)

// TestPartnerClickBlocks: with block_ad_clicks, a ref visit from a paid
// ad click doesn't credit the partner; with block_social_clicks, neither
// does one from social media. Other ref visits still do.
func TestPartnerClickBlocks(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	key := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "northpeak.example", "origins": []string{"https://northpeak.example"}})["key"].(string)
	prog := owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate", "landing_url": "https://northpeak.example/"})
	if prog["block_ad_clicks"] != false || prog["block_social_clicks"] != false {
		t.Fatalf("defaults = %v %v", prog["block_ad_clicks"], prog["block_social_clicks"])
	}
	ana := owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "ana@acme.example"})
	conv := base + "/partner-conversions"

	n := 0
	visit := func(clickIDs map[string]any, referrer string) string {
		t.Helper()
		n++
		anon := "visitor-" + string(rune('a'+n)) + "-1"
		props := map[string]any{"ref": ana["code"]}
		if clickIDs != nil {
			props["click_ids"] = clickIDs
		}
		ev := map[string]any{"type": "page", "anonymousId": anon, "messageId": "m-" + anon, "timestamp": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
			"properties": props, "context": map[string]any{"page": map[string]any{"url": "https://northpeak.example/", "path": "/", "referrer": referrer}}}
		if c, out := sendSite(t, e.h, "https://northpeak.example", key, ev); c != 200 || out["accepted"] != 1.0 {
			t.Fatalf("send: %d %v", c, out)
		}
		return anon
	}
	signup := func(anon string) map[string]any {
		t.Helper()
		code, out := owner.do("POST", conv, map[string]any{"type": "signup", "customer_id": "u-" + anon, "anonymous_id": anon})
		if code != 200 && code != 201 {
			t.Fatalf("signup = %d %v", code, out)
		}
		return out
	}

	// Off: a ref visit from a Google ad credits Ana.
	if res := signup(visit(map[string]any{"gclid": "g-1"}, "")); res["partner_id"] != ana["id"] {
		t.Fatalf("ad visit, no block = %v", res)
	}
	owner.must(200, "PATCH", base+"/partner-programs/affiliates", map[string]any{"block_ad_clicks": true, "block_social_clicks": true})
	if res := signup(visit(map[string]any{"gclid": "g-2"}, "")); res["reason"] != "no_partner" {
		t.Fatalf("ad visit, blocked = %v", res)
	}
	if res := signup(visit(map[string]any{"fbclid": "f-1"}, "")); res["reason"] != "no_partner" {
		t.Fatalf("fbclid visit, blocked = %v", res)
	}
	if res := signup(visit(nil, "https://www.facebook.com/")); res["reason"] != "no_partner" {
		t.Fatalf("facebook referrer, blocked = %v", res)
	}
	// A visit from elsewhere still credits Ana.
	if res := signup(visit(nil, "https://blog.example/review")); res["partner_id"] != ana["id"] {
		t.Fatalf("plain visit = %v", res)
	}
}
