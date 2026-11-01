package api_test

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestPartnerPortalAnalytics: a portal can load the workspace's own
// website script with one of its site keys, plus Google Analytics held
// back by that script, and the key takes the portal's events without the
// portal's address in its origins.
func TestPartnerPortalAnalytics(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	sk := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "northpeak.example", "origins": []string{"https://northpeak.example"}})
	keyID, key := sk["id"].(string), sk["key"].(string)
	id := owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate"})["id"].(string)
	portal := base + "/partner-programs/" + id + "/portal"

	page := func() (string, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, httptest.NewRequest("GET", "/p/"+id+"/", nil))
		if rec.Code != 200 {
			t.Fatalf("portal page = %d", rec.Code)
		}
		return rec.Body.String(), rec.Header().Get("Content-Security-Policy")
	}
	if body, _ := page(); strings.Contains(body, "site.js") {
		t.Fatal("the script is on a portal without analytics")
	}

	owner.must(400, "PUT", portal, map[string]any{"enabled": true, "settings": map[string]any{"ga_measurement_id": "G-ABC123XYZ"}})
	owner.must(400, "PUT", portal, map[string]any{"settings": map[string]any{"site_key_id": "nope"}})
	owner.must(400, "PUT", portal, map[string]any{"settings": map[string]any{"site_key_id": "00000000-0000-4000-8000-000000000000"}})
	owner.must(400, "PUT", portal, map[string]any{"settings": map[string]any{"site_key_id": keyID, "ga_measurement_id": "UA-1234-1"}})
	saved := owner.must(200, "PUT", portal, map[string]any{"enabled": true, "settings": map[string]any{"site_key_id": keyID, "ga_measurement_id": "g-abc123xyz"}})
	if pt := saved["portal"].(map[string]any); pt["site_key_id"] != keyID || pt["ga_measurement_id"] != "G-ABC123XYZ" {
		t.Fatalf("portal = %v", pt)
	}

	body, csp := page()
	if !strings.Contains(body, `/site.js" data-key="`+key+`"`) {
		t.Errorf("no site script in the page")
	}
	if !strings.Contains(body, `type="text/plain" data-category="analytics" async src="https://www.googletagmanager.com/gtag/js?id=G-ABC123XYZ"`) ||
		!strings.Contains(body, `gtag("config","G-ABC123XYZ")`) {
		t.Errorf("Google Analytics isn't held back for consent: %s", body)
	}
	if !strings.Contains(csp, "script-src 'self' 'unsafe-inline' http://localhost:8080 https://www.googletagmanager.com") || !strings.Contains(csp, "connect-src 'self' http://localhost:8080 https://*.google-analytics.com") {
		t.Errorf("CSP = %s", csp)
	}

	// The key takes the portal's events (here at /p/ on this server), and
	// still refuses other sites.
	ev := map[string]any{"type": "page", "anonymousId": "portal-visitor-1", "messageId": "m-portal-1", "timestamp": time.Now().UTC().Format(time.RFC3339),
		"context": map[string]any{"page": map[string]any{"url": "http://localhost:8080/p/" + id + "/", "path": "/p/" + id + "/"}}}
	if c, out := sendSite(t, e.h, "http://localhost:8080", key, ev); c != 200 || out["accepted"] != 1.0 {
		t.Fatalf("portal event = %d %v", c, out)
	}
	if c, _ := sendSite(t, e.h, "https://elsewhere.example", key, ev); c != 403 {
		t.Fatalf("another site = %d", c)
	}
	// Off again: the page has no script, and the key goes back to its origins.
	owner.must(200, "PUT", portal, map[string]any{"settings": map[string]any{"site_key_id": ""}})
	if body, _ := page(); strings.Contains(body, "site.js") {
		t.Error("the script stayed after analytics were turned off")
	}
	if c, _ := sendSite(t, e.h, "http://localhost:8080", key, ev); c != 403 {
		t.Errorf("the portal's origin after turning it off = %d", c)
	}
}
