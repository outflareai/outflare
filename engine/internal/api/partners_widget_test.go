package api_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
)

func TestPartnerWidget(t *testing.T) {
	e := freshEnvWith(t, func(o *api.Options) { o.PartnersDomain = "partners.test" })
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	prog := owner.must(201, "POST", base+"/partner-programs", map[string]any{
		"name": "Refer a friend", "kind": "referral", "landing_url": "https://northpeak.example/", "auto_accept": true,
	})
	// The portal stays closed: the widget doesn't need it.
	owner.must(200, "PUT", base+"/partner-programs/refer-a-friend/portal", map[string]any{"enabled": false, "subdomain": "northpeak"})

	// The backend opens the widget for its signed-in customer, who becomes a partner.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Backend", "scopes": []string{"partners.conversions"}})["token"].(string)
	backend := &client{t: t, h: e.h, bearer: tok}
	sess := backend.must(201, "POST", base+"/partner-programs/refer-a-friend/widget-sessions", map[string]any{"customer_id": "u-1", "name": "Dana"})
	if sess["status"] != "active" || sess["code"] == "" || !strings.HasPrefix(sess["embed_url"].(string), "https://northpeak.partners.test/embed#token=ofps_") {
		t.Fatalf("session = %v", sess)
	}
	// The same customer again is the same partner, with a new session.
	again := backend.must(201, "POST", base+"/partner-programs/refer-a-friend/widget-sessions", map[string]any{"customer_id": "u-1"})
	if again["partner_id"] != sess["partner_id"] || again["token"] == sess["token"] {
		t.Fatalf("again = %v", again)
	}
	backend.must(400, "POST", base+"/partner-programs/refer-a-friend/widget-sessions", map[string]any{})
	backend.must(404, "POST", base+"/partner-programs/nope/widget-sessions", map[string]any{"customer_id": "u-1"})

	// The page may be framed by another site; the rest of the portal may not.
	c := &portalClient{t: t, h: e.h, base: "https://northpeak.partners.test/"}
	_, _, rec := c.do("GET", "embed", nil)
	if rec.Code != 200 || rec.Header().Get("X-Frame-Options") != "" || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors https:") {
		t.Fatalf("embed = %d %v", rec.Code, rec.Header())
	}
	_, _, rec = c.do("GET", "", nil)
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("portal page may be framed: %v", rec.Header())
	}
	// The closed portal itself stays closed.
	if code, _, _ := c.do("GET", "api/program", nil); code != 404 {
		t.Fatalf("closed portal's program = %d", code)
	}

	// The widget reads with its session as a bearer token, with no cookie.
	me := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "https://northpeak.partners.test/api/me", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	if rec := me(sess["token"].(string)); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"code":"`+sess["code"].(string)+`"`) {
		t.Fatalf("me = %d %s", rec.Code, rec.Body)
	}
	// Anything else finds the portal closed.
	if rec := me("ofps_not_a_real_session"); rec.Code != 404 {
		t.Fatalf("bad session = %d", rec.Code)
	}
	if rec := me(tok); rec.Code != 404 {
		t.Fatalf("an API token opened the portal: %d", rec.Code)
	}

	// Another program's session doesn't open this one.
	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate", "landing_url": "https://northpeak.example/", "auto_accept": true})
	other := backend.must(201, "POST", base+"/partner-programs/affiliates/widget-sessions", map[string]any{"customer_id": "u-1"})
	if rec := me(other["token"].(string)); rec.Code != 404 {
		t.Fatalf("other program's session = %d", rec.Code)
	}
	_ = prog
}
