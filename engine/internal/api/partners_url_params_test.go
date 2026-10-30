package api_test

import (
	"testing"
	"time"
)

// TestPartnerURLParams: a program's extra link parameters (url_params)
// carry a partner's code on the website like ref, so a visit through an
// ?aff= link made on another platform credits the partner.
func TestPartnerURLParams(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	key := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "northpeak.example", "origins": []string{"https://northpeak.example"}})["key"].(string)
	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate", "landing_url": "https://northpeak.example/"})
	for _, bad := range [][]string{{"ref"}, {"utm_source"}, {"of_x"}, {"Has Space"}, {"gclid"}, {"a", "b", "c", "d", "e", "f"}} {
		owner.must(400, "PATCH", base+"/partner-programs/affiliates", map[string]any{"url_params": bad})
	}
	prog := owner.must(200, "PATCH", base+"/partner-programs/affiliates", map[string]any{"url_params": []string{"aff", "fpr", "aff"}})
	if l := prog["url_params"].([]any); len(l) != 2 || l[0] != "aff" {
		t.Fatalf("url_params = %v", l)
	}
	ana := owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "ana@acme.example"})
	code := ana["code"].(string)

	land := func(anon, search string) {
		t.Helper()
		ev := map[string]any{"type": "page", "anonymousId": anon, "messageId": "m-" + anon, "timestamp": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
			"context": map[string]any{"page": map[string]any{"url": "https://northpeak.example/" + search, "path": "/", "search": search}}}
		if c, out := sendSite(t, e.h, "https://northpeak.example", key, ev); c != 200 || out["accepted"] != 1.0 {
			t.Fatalf("send: %d %v", c, out)
		}
	}
	conv := base + "/partner-conversions"
	// A visit through ?aff= credits Ana.
	land("visitor-aff-1", "?aff="+code+"&plan=pro")
	if res := owner.must(201, "POST", conv, map[string]any{"type": "signup", "customer_id": "u-1", "anonymous_id": "visitor-aff-1"}); res["partner_id"] != ana["id"] {
		t.Fatalf("aff visit = %v", res)
	}
	// A parameter no program names credits nobody.
	land("visitor-other-2", "?coupon="+code)
	if res := owner.must(200, "POST", conv, map[string]any{"type": "signup", "customer_id": "u-2", "anonymous_id": "visitor-other-2"}); res["reason"] != "no_partner" {
		t.Fatalf("other parameter = %v", res)
	}
}
