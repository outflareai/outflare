package api_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/oauth"
)

// Sales "Connect Gmail" returns to the sign-in callback, the one redirect URI
// the Google OAuth client registers, and that callback hands it to Sales.
func TestSalesGoogleConsentUsesSignInCallback(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana", "identities": []map[string]any{{"namespace": "email", "value": "ana@customer.example"}}})
	brand := ana["brand_id"].(string)
	sales := "/v1/brands/" + brand + "/sales"
	inv := owner.must(201, "POST", base+"/invites", map[string]any{"email": "rep@northpeak.example", "role": "operator"})
	rep := &client{t: t, h: e.h}
	rep.must(200, "POST", "/v1/invites/accept", map[string]any{"token": inv["token"], "name": "Rep", "password": "not-real-test-password"})
	repID := rep.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	owner.must(200, "PUT", sales+"/team/"+repID, map[string]any{"role": "sdr"})
	e.oauth.Google = oauth.Client{ID: "test-sales-client", Secret: "test-client-secret-not-real"}
	e.oauth.Endpoints.GoogleAuth = "https://accounts.google.test/authorize"

	start := func() *url.URL {
		t.Helper()
		u, err := url.Parse(rep.must(200, "POST", sales+"/mail/authorize", nil)["url"].(string))
		if err != nil {
			t.Fatal(err)
		}
		if got := u.Query().Get("redirect_uri"); !strings.HasSuffix(got, "/v1/auth/google/callback") {
			t.Fatal("Sales consent uses an unregistered redirect URI", got)
		}
		if !strings.Contains(u.Query().Get("scope"), oauth.GoogleSendMailScope) {
			t.Fatal("Sales consent must ask to send", u.Query().Get("scope"))
		}
		return u
	}
	back := func(c *client, state string) *url.URL {
		t.Helper()
		return follow(t, c, "/v1/auth/google/callback?error=access_denied&state="+url.QueryEscape(state))
	}

	// Declining on Google's screen lands back on My email, not on sign-in.
	u := start()
	if loc := back(rep, u.Query().Get("state")); loc.Query().Get("sales_google") != "denied" || !strings.Contains(loc.Path, "sales") {
		t.Fatal("declined consent did not return to Sales", loc)
	}
	// A consent finishes once, only for the person who started it.
	u = start()
	owner.must(400, "GET", "/v1/auth/google/callback?error=access_denied&state="+url.QueryEscape(u.Query().Get("state")), nil)
	back(rep, u.Query().Get("state"))
	rep.must(400, "GET", "/v1/auth/google/callback?error=access_denied&state="+url.QueryEscape(u.Query().Get("state")), nil)
}
