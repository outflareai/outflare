package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/catalog"
)

// Cal.com signs in with its own OAuth app; the key stays for self-hosted.
func TestCalComSignsIn(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/" && r.Method == "POST":
			r.ParseForm()
			if r.Form.Get("client_secret") != "cal-secret" || r.Form.Get("code") != "abc" {
				w.WriteHeader(400)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "cal_oauth", "refresh_token": "cal_r", "expires_in": 3600})
		case r.URL.Path == "/me" && r.Header.Get("Authorization") == "Bearer cal_oauth":
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"id": 7, "email": "ana@acme.example", "name": "Ana", "username": "ana"}})
		default:
			w.WriteHeader(401)
		}
	}))
	t.Cleanup(srv.Close)
	e.integrations.Client.BaseURLs["cal_com"] = srv.URL
	e.integrations.Client.BaseURLs["cal_com.token"] = srv.URL
	e.integrations.OAuthApps = map[string]connectors.OAuthApp{"cal_com": {ClientID: "cal-id", ClientSecret: "cal-secret"}}

	opts := options(catalogEntryFor(t, owner, base+"/connectors", "cal_com"))
	if opts["oauth2"]["recommended"] != true || opts["api_key"]["available"] != true {
		t.Fatalf("cal.com options: %v", opts)
	}
	link := owner.must(200, "GET", base+"/integrations/oauth/cal_com/authorize?format=json", nil)["url"].(string)
	u, _ := url.Parse(link)
	if u.Host != "app.cal.com" || !strings.Contains(u.Query().Get("scope"), "BOOKING_READ") {
		t.Fatalf("authorize: %s", link)
	}
	cb := follow(t, owner, "/v1/integrations/oauth/callback?code=abc&state="+url.QueryEscape(u.Query().Get("state")))
	id := cb.Query().Get("integration")
	if id == "" {
		t.Fatalf("callback: %s", cb)
	}
	owner.must(200, "POST", base+"/integrations/"+id+"/test", nil)
}

// Stripe signs in through the server's Stripe App: the install link set on
// the server, and a token call that carries only the app's secret key.
func TestStripeSignsInThroughItsApp(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	var form url.Values
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		r.ParseForm()
		form = r.PostForm
		if !ok || user != "sk_test_app" || pass != "" || r.PostForm.Get("code") != "st_code" {
			w.WriteHeader(401)
			return
		}
		// Stripe's answer has no expires_in.
		json.NewEncoder(w).Encode(map[string]any{"access_token": "rk_test_key", "refresh_token": "st_r", "token_type": "bearer", "account_id": "acct_1"})
	}))
	t.Cleanup(tok.Close)
	e.integrations.Client.BaseURLs["stripe"] = fakeStripe(t).URL
	e.integrations.Client.BaseURLs["stripe.token"] = tok.URL
	install := "https://marketplace.stripe.com/oauth/v2/chnlink_test/authorize?client_id=ca_app&redirect_uri=" + url.QueryEscape("https://app.example/v1/integrations/oauth/callback")
	e.integrations.OAuthApps = map[string]connectors.OAuthApp{"stripe": {ClientID: "ca_app", ClientSecret: "sk_test_app", AuthURL: install}}

	link := owner.must(200, "GET", base+"/integrations/oauth/stripe/authorize?format=json", nil)["url"].(string)
	u, _ := url.Parse(link)
	if u.Path != "/oauth/v2/chnlink_test/authorize" || len(u.Query()["client_id"]) != 1 || u.Query().Get("state") == "" ||
		u.Query().Get("redirect_uri") != "https://app.example/v1/integrations/oauth/callback" {
		t.Fatalf("authorize: %s", link)
	}
	cb := follow(t, owner, "/v1/integrations/oauth/callback?code=st_code&state="+url.QueryEscape(u.Query().Get("state")))
	id := cb.Query().Get("integration")
	if id == "" {
		t.Fatalf("callback: %s", cb)
	}
	if len(form) != 2 || form.Get("grant_type") != "authorization_code" {
		t.Fatalf("Stripe takes only the grant and code: %v", form)
	}
	if got := owner.must(200, "GET", base+"/integrations/"+id, nil); got["auth_method"] != "oauth2" {
		t.Fatalf("stripe connection: %v", got)
	}
	owner.must(200, "POST", base+"/integrations/"+id+"/test", nil)
}

// OpenRouter's sign-in hands back a key on the person's own account, so it
// works on every server without an app, and the key is used like a pasted one.
func TestOpenRouterSignInMakesAKey(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	var bodies []map[string]any
	judge := ""
	or := httptest.NewServer(fakeOpenRouter{bodies: &bodies, judge: &judge})
	t.Cleanup(or.Close)
	var sent map[string]string
	keys := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&sent)
		if sent["code"] != "or_code" || sent["code_verifier"] == "" {
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"key": "sk-or-good"})
	}))
	t.Cleanup(keys.Close)
	e.integrations.Client.BaseURLs["openrouter"] = or.URL
	e.integrations.Client.BaseURLs["openrouter.token"] = keys.URL
	// The server's own apps, as start-up reads them: OpenRouter needs none.
	e.integrations.OAuthApps = connectors.OAuthAppsFromEnv(catalog.New())

	opts := options(catalogEntryFor(t, owner, base+"/connectors", "openrouter"))
	if opts["oauth2"]["available"] != true || opts["oauth2"]["recommended"] != true {
		t.Fatalf("openrouter options: %v", opts)
	}
	link := owner.must(200, "GET", base+"/integrations/oauth/openrouter/authorize?format=json", nil)["url"].(string)
	u, _ := url.Parse(link)
	cbURL, err := url.Parse(u.Query().Get("callback_url"))
	if u.Host != "openrouter.ai" || err != nil || !strings.HasSuffix(cbURL.Path, "/v1/integrations/oauth/callback") ||
		cbURL.Query().Get("state") == "" || u.Query().Get("code_challenge") == "" {
		t.Fatalf("authorize: %s", link)
	}
	cb := follow(t, owner, "/v1/integrations/oauth/callback?state="+url.QueryEscape(cbURL.Query().Get("state"))+"&code=or_code")
	id := cb.Query().Get("integration")
	if id == "" {
		t.Fatalf("callback: %s", cb)
	}
	if got := owner.must(200, "GET", base+"/integrations/"+id, nil); got["auth_method"] != "oauth2" {
		t.Fatalf("openrouter connection: %v", got)
	}
	// The key works wherever a pasted key does.
	owner.must(200, "POST", base+"/integrations/"+id+"/test", nil)
	owner.must(200, "PUT", base+"/ai-settings", map[string]any{"integration_id": id, "model": "openai/gpt-5"})
}
