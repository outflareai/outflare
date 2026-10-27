package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

func catalogEntryFor(t *testing.T, c *client, path, id string) map[string]any {
	t.Helper()
	for _, x := range c.must(200, "GET", path, nil)["data"].([]any) {
		if x.(map[string]any)["id"] == id {
			return x.(map[string]any)
		}
	}
	t.Fatalf("%s not in %s", id, path)
	return nil
}

// options reads auth_options as method -> option.
func options(e map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, o := range e["auth_options"].([]any) {
		out[o.(map[string]any)["method"].(string)] = o.(map[string]any)
	}
	return out
}

// A connector with OAuth and an API key offers both; the person picks,
// and the connection remembers which way it signed in.
func TestConnectorOffersOAuthAndAPIKey(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	anon := &client{t: t, h: e.h}

	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/" && r.Method == "POST":
			json.NewEncoder(w).Encode(map[string]any{"access_token": "hs_oauth", "refresh_token": "hs_r", "expires_in": 1800})
		case r.URL.Path == "/account-info/v3/details" && (r.Header.Get("Authorization") == "Bearer pat-good" || r.Header.Get("Authorization") == "Bearer hs_oauth"):
			json.NewEncoder(w).Encode(map[string]any{"portalId": 42, "companyCurrency": "USD"})
		default:
			w.WriteHeader(401)
		}
	}))
	t.Cleanup(hs.Close)
	e.integrations.Client.BaseURLs["hubspot"] = hs.URL
	e.integrations.Client.BaseURLs["hubspot.token"] = hs.URL

	// No HubSpot app on this server: the key is the way, OAuth says why not.
	entry := catalogEntryFor(t, anon, "/v1/connectors", "hubspot")
	opts := options(entry)
	if strings.Join(toStrings(entry["auth_methods"]), ",") != "oauth2,api_key" || entry["configured"] != true ||
		opts["oauth2"]["available"] != false || !strings.Contains(opts["oauth2"]["reason"].(string), "CONNECTOR_HUBSPOT_CLIENT_ID") ||
		opts["api_key"]["available"] != true || opts["api_key"]["recommended"] != true {
		t.Fatalf("catalog without an app: %v", entry)
	}
	for _, x := range anon.must(200, "GET", "/v1/connectors", nil)["data"].([]any) {
		if id := x.(map[string]any)["id"]; id == "hubspot_private_app" || id == "posthog_cloud" {
			t.Fatalf("%s should be folded into one connector", id)
		}
	}
	owner.must(400, "GET", base+"/integrations/oauth/hubspot/authorize?format=json", nil)

	key := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "hubspot", "credentials": map[string]string{"api_key": "pat-good"}})
	if key["auth_method"] != "api_key" || key["connector"] != "hubspot" {
		t.Fatalf("key connection: %v", key)
	}
	// The old id still works for API clients that use it.
	old := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "hubspot_private_app", "name": "Old id", "credentials": map[string]string{"api_key": "pat-good"}})
	if old["connector"] != "hubspot" || old["connector_name"] != "HubSpot" {
		t.Fatalf("old id: %v", old)
	}

	// With an app, OAuth becomes the recommended way.
	e.integrations.OAuthApps = map[string]connectors.OAuthApp{"hubspot": {ClientID: "cid", ClientSecret: "sec"}}
	opts = options(catalogEntryFor(t, owner, base+"/connectors", "hubspot"))
	if opts["oauth2"]["available"] != true || opts["oauth2"]["recommended"] != true || opts["api_key"]["recommended"] != false {
		t.Fatalf("catalog with an app: %v", opts)
	}
	link := owner.must(200, "GET", base+"/integrations/oauth/hubspot/authorize?format=json", nil)["url"].(string)
	u, _ := url.Parse(link)
	if u.Host != "app.hubspot.com" || u.Query().Get("client_id") != "cid" {
		t.Fatalf("authorize: %s", link)
	}
	cb := follow(t, owner, "/v1/integrations/oauth/callback?code=abc&state="+url.QueryEscape(u.Query().Get("state")))
	id := cb.Query().Get("integration")
	if got := owner.must(200, "GET", base+"/integrations/"+id, nil); got["auth_method"] != "oauth2" {
		t.Fatalf("oauth connection: %v", got)
	}
	owner.must(200, "POST", base+"/integrations/"+id+"/test", nil)

	// Pasting a key on a signed-in connection switches it to the key.
	if got := owner.must(200, "PATCH", base+"/integrations/"+id, map[string]any{"credentials": map[string]string{"api_key": "pat-good"}}); got["auth_method"] != "api_key" {
		t.Fatalf("after pasting a key: %v", got)
	}

	// OAuth-only connectors still refuse a pasted key, and are not
	// configured without their app.
	if entry := catalogEntryFor(t, anon, "/v1/connectors", "outlook_calendar"); entry["configured"] != false {
		t.Fatalf("outlook without an app: %v", entry)
	}
	if code, out := owner.do("POST", base+"/integrations", map[string]any{"connector": "outlook_calendar", "credentials": map[string]string{}}); code != 400 || !strings.Contains(out["error"].(map[string]any)["code"].(string), "use_oauth") {
		t.Fatalf("outlook key: %d %v", code, out)
	}

	// A connect link shows both ways to the person.
	req := owner.must(201, "POST", base+"/connect-requests", map[string]any{"connector": "hubspot"})
	_, secret, _ := strings.Cut(req["url"].(string), "#")
	info := owner.must(200, "POST", "/v1/connect/lookup", map[string]string{"token": secret})
	if len(info["connector"].(map[string]any)["auth_options"].([]any)) != 2 || info["requested_by"] != "person" || info["requested_by_email"] != "jack@example.com" {
		t.Fatalf("lookup: %v", info)
	}
	owner.must(201, "POST", "/v1/connect/complete", map[string]any{"token": secret, "credentials": map[string]string{"api_key": "pat-good"}})
}
