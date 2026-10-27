package api_test

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/outflareai/outflare/engine/internal/mailbox/mailtest"
	"github.com/outflareai/outflare/engine/internal/oauth"
)

// fakeProvider is Google's and Microsoft's token endpoints, enough to
// exercise every grant Outflare uses.
type fakeProvider struct {
	t   *testing.T
	key *rsa.PrivateKey // the service account's key

	mu       sync.Mutex
	refreshN int // refresh grants seen
	appCalls int
	// refresh is how refresh grants are answered: "" works, "revoked"
	// refuses the grant, "down" is a provider outage.
	refresh string
}

func (f *fakeProvider) setRefresh(mode string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refresh = mode
}

func newFakeProvider(t *testing.T) (*fakeProvider, *httptest.Server) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeProvider{t: t, key: key}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeProvider) serviceAccount() string {
	der, _ := x509.MarshalPKCS8PrivateKey(f.key)
	b, _ := json.Marshal(map[string]string{
		"type": "service_account", "client_email": "sender@proj.iam.gserviceaccount.com", "client_id": "1234",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"token_uri":   "https://oauth2.googleapis.com/token",
	})
	return string(b)
}

func idToken(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func (f *fakeProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.ParseForm() //nolint:errcheck
	form := r.PostForm
	reply := func(status int, v map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v) //nolint:errcheck
	}
	ok := func(v map[string]any) {
		v["token_type"], v["expires_in"] = "Bearer", 3600
		reply(200, v)
	}
	switch {
	case r.URL.Path == "/google/token" && form.Get("grant_type") == "urn:ietf:params:oauth:grant-type:jwt-bearer":
		parts := strings.Split(form.Get("assertion"), ".")
		if len(parts) != 3 {
			reply(400, map[string]any{"error": "invalid_grant"})
			return
		}
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if rsa.VerifyPKCS1v15(&f.key.PublicKey, crypto.SHA256, sum[:], sig) != nil {
			reply(400, map[string]any{"error": "invalid_grant", "error_description": "Invalid JWT Signature."})
			return
		}
		raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var c map[string]any
		json.Unmarshal(raw, &c) //nolint:errcheck
		if c["iss"] != "sender@proj.iam.gserviceaccount.com" || c["scope"] != oauth.GoogleMailScope || c["aud"] != "http://"+r.Host+"/google/token" {
			reply(400, map[string]any{"error": "invalid_grant", "error_description": "bad claims"})
			return
		}
		if strings.HasSuffix(c["sub"].(string), "@elsewhere.test") {
			reply(401, map[string]any{"error": "unauthorized_client", "error_description": "Client is unauthorized to retrieve access tokens using this method."})
			return
		}
		ok(map[string]any{"access_token": "sa-" + c["sub"].(string)})

	case r.URL.Path == "/google/token" && form.Get("grant_type") == "authorization_code" &&
		form.Get("redirect_uri") == "http://localhost:8080/v1/auth/google/callback":
		// Sign-in codes are "login:<sub>:<email>"; no refresh token.
		parts := strings.SplitN(form.Get("code"), ":", 3)
		if len(parts) != 3 || parts[0] != "login" || form.Get("client_secret") != "gsecret" {
			reply(400, map[string]any{"error": "invalid_grant"})
			return
		}
		ok(map[string]any{"access_token": "ignored",
			"id_token": idToken(map[string]any{"sub": parts[1], "email": parts[2], "email_verified": !strings.HasPrefix(parts[2], "unverified"), "name": "Signed In"})})

	case r.URL.Path == "/google/token" && form.Get("grant_type") == "authorization_code":
		if form.Get("code") != "good-code" || form.Get("client_id") != "gid" || form.Get("client_secret") != "gsecret" ||
			form.Get("redirect_uri") != "http://localhost:8080/v1/oauth/callback" {
			reply(400, map[string]any{"error": "invalid_grant"})
			return
		}
		ok(map[string]any{"access_token": "ignored", "refresh_token": "rt-1",
			"id_token": idToken(map[string]any{"email": "Li@GW.test", "email_verified": true, "name": "Li Wei"})})

	case r.URL.Path == "/google/token" && form.Get("grant_type") == "refresh_token":
		f.mu.Lock()
		f.refreshN++
		mode := f.refresh
		f.mu.Unlock()
		if mode == "down" {
			reply(503, map[string]any{"error": "temporarily_unavailable"})
			return
		}
		if form.Get("refresh_token") != "rt-1" || mode == "revoked" {
			reply(400, map[string]any{"error": "invalid_grant", "error_description": "Token has been expired or revoked."})
			return
		}
		ok(map[string]any{"access_token": "user-rt-1"})

	case r.URL.Path == "/ms/tenant-1/oauth2/v2.0/token" && form.Get("grant_type") == "client_credentials":
		f.mu.Lock()
		f.appCalls++
		f.mu.Unlock()
		if form.Get("client_id") != "app-1" || form.Get("client_secret") != "app-secret" || form.Get("scope") != oauth.MicrosoftAppScope {
			reply(401, map[string]any{"error": "invalid_client", "error_description": "AADSTS7000215: Invalid client secret provided.\r\nTrace ID: abc"})
			return
		}
		ok(map[string]any{"access_token": "app-token"})

	default:
		reply(400, map[string]any{"error": "unsupported_grant_type"})
	}
}

func (e *env) useFakeProvider(t *testing.T) *fakeProvider {
	f, srv := newFakeProvider(t)
	e.oauth.Endpoints = oauth.Endpoints{GoogleAuth: srv.URL + "/google/auth", GoogleToken: srv.URL + "/google/token", MicrosoftLogin: srv.URL + "/ms"}
	e.oauth.Google = oauth.Client{ID: "gid", Secret: "gsecret"}
	return f
}

// follow sends a GET with c's session and returns the redirect target.
func follow(t *testing.T, c *client, path string) *url.URL {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
	}
	u, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestOAuthConnectionsSignInWithXOAUTH2(t *testing.T) {
	e := freshEnv(t)
	f := e.useFakeProvider(t)
	srv := mailtest.Start(t, map[string]string{"ana@acme.test": "unused-1", "bo@acme.test": "unused-2", "cy@tenant.test": "unused-3", "ghost@elsewhere.test": "x"})
	e.mail.Dialer.TLSConfig, e.mail.Dialer.Route = srv.ClientTLS, srv.Route
	srv.AllowToken("ana@acme.test", "sa-ana@acme.test")
	srv.AllowToken("bo@acme.test", "sa-bo@acme.test")
	srv.AllowToken("cy@tenant.test", "app-token")
	e.runJobs(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	// Google: a service account with domain-wide delegation.
	owner.must(400, "POST", base+"/oauth-connections", map[string]any{"provider": "google", "service_account": map[string]any{"type": "authorized_user"}})
	g := owner.must(201, "POST", base+"/oauth-connections", map[string]any{"provider": "google", "service_account": f.serviceAccount()})
	if g["name"] != "sender@proj.iam.gserviceaccount.com" || g["details"].(map[string]any)["client_email"] != g["name"] {
		t.Fatalf("google connection: %v", g)
	}
	// Microsoft: the secret is checked before it is saved.
	out := owner.must(400, "POST", base+"/oauth-connections", map[string]any{"provider": "microsoft", "tenant_id": "tenant-1", "client_id": "app-1", "client_secret": "wrong"})
	if msg := out["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "AADSTS7000215") || strings.Contains(msg, "Trace") {
		t.Fatalf("microsoft error: %s", msg)
	}
	m := owner.must(201, "POST", base+"/oauth-connections", map[string]any{"provider": "microsoft", "name": "Tenant", "tenant_id": "tenant-1", "client_id": "app-1", "client_secret": "app-secret"})

	code, body := plainAuth(owner, "GET", base+"/oauth-connections")
	if code != 200 || strings.Contains(body, "PRIVATE KEY") || strings.Contains(body, "app-secret") {
		t.Fatalf("list leaks secrets: %d %s", code, body)
	}

	// Bulk add: no passwords, hosts from the provider.
	res := owner.must(200, "POST", base+"/oauth-connections/"+g["id"].(string)+"/mailboxes", map[string]any{
		"mailboxes": []map[string]string{{"email": "ana@acme.test", "display_name": "Ana"}, {"email": "Bo@acme.test"}, {"email": "ghost@elsewhere.test"}, {"email": "nope"}},
		"pool":      "Google",
	})
	if res["created"] != 3.0 || len(res["errors"].([]any)) != 1 {
		t.Fatalf("bulk add: %v", res)
	}
	owner.must(200, "POST", base+"/oauth-connections/"+m["id"].(string)+"/mailboxes", map[string]any{"mailboxes": []map[string]string{{"email": "cy@tenant.test"}}})

	byEmail := map[string]map[string]any{}
	for _, mb := range owner.must(200, "GET", base+"/mailboxes", nil)["data"].([]any) {
		byEmail[mb.(map[string]any)["email"].(string)] = mb.(map[string]any)
	}
	if a := byEmail["ana@acme.test"]; a["auth_method"] != "oauth" || a["provider"] != "google" || a["smtp_host"] != "smtp.gmail.com" || a["pool_name"] != "Google" || a["display_name"] != "Ana" {
		t.Fatalf("ana: %v", a)
	}
	if c := byEmail["cy@tenant.test"]; c["provider"] != "microsoft" || c["smtp_host"] != "smtp.office365.com" {
		t.Fatalf("cy: %v", c)
	}
	for _, email := range []string{"ana@acme.test", "bo@acme.test", "cy@tenant.test"} {
		got := waitFor(t, owner, base+"/mailboxes/"+byEmail[email]["id"].(string), func(m map[string]any) bool { return m["status"] != "verifying" })
		if got["status"] != "active" || got["smtp_ok"] != true || got["imap_ok"] != true {
			t.Fatalf("%s: %v", email, got)
		}
	}
	// Delegation not granted for that domain: both checks fail with the
	// provider's reason.
	got := waitFor(t, owner, base+"/mailboxes/"+byEmail["ghost@elsewhere.test"]["id"].(string), func(m map[string]any) bool { return m["status"] != "verifying" })
	if got["status"] != "error" || got["imap_ok"] != false || !strings.Contains(got["status_reason"].(string), "unauthorized_client") {
		t.Fatalf("ghost: %v", got)
	}
	f.mu.Lock()
	appCalls := f.appCalls
	f.mu.Unlock()
	if appCalls != 2 { // the wrong secret, then the right one; the token is cached after that
		t.Fatalf("app token requests: %d", appCalls)
	}

	// Sending signs in with the token too.
	owner.must(200, "POST", base+"/mailboxes/"+byEmail["ana@acme.test"]["id"].(string)+"/test-send", map[string]any{"to": "li@one-io.example"})
	if msgs := srv.Messages(); len(msgs) != 1 || msgs[0].From != "ana@acme.test" {
		t.Fatalf("sent: %v", msgs)
	}

	// A connection in use cannot be removed.
	owner.must(409, "DELETE", base+"/oauth-connections/"+m["id"].(string), nil)
	owner.must(204, "DELETE", base+"/mailboxes/"+byEmail["cy@tenant.test"]["id"].(string), nil)
	owner.must(204, "DELETE", base+"/oauth-connections/"+m["id"].(string), nil)
	owner.must(404, "POST", base+"/oauth-connections/"+m["id"].(string)+"/mailboxes", map[string]any{"mailboxes": []map[string]string{{"email": "x@tenant.test"}}})

	// Read-only callers see connections but cannot add them.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "reader", "scopes": []string{"mailboxes.read"}})
	reader := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	reader.must(200, "GET", base+"/oauth-connections", nil)
	reader.must(403, "POST", base+"/oauth-connections", map[string]any{"provider": "google", "service_account": f.serviceAccount()})
}

func TestOAuthConsentScreen(t *testing.T) {
	e := freshEnv(t)
	f := e.useFakeProvider(t)
	srv := mailtest.Start(t, map[string]string{"li@gw.test": "unused"})
	e.mail.Dialer.TLSConfig, e.mail.Dialer.Route = srv.ClientTLS, srv.Route
	srv.AllowToken("li@gw.test", "user-rt-1")
	e.runJobs(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	meta := owner.must(200, "GET", "/v1/meta", nil)
	if p := meta["oauth_providers"].([]any); len(p) != 1 || p[0] != "google" {
		t.Fatalf("providers: %v", p)
	}
	owner.must(400, "GET", base+"/oauth/microsoft/authorize", nil) // not configured

	// The consent URL carries a signed state.
	to := follow(t, owner, base+"/oauth/google/authorize?pool=Warm")
	q := to.Query()
	if to.Path != "/google/auth" || q.Get("client_id") != "gid" || q.Get("access_type") != "offline" ||
		q.Get("redirect_uri") != "http://localhost:8080/v1/oauth/callback" || !strings.Contains(q.Get("scope"), "https://mail.google.com/") {
		t.Fatalf("auth url: %s", to)
	}
	state := q.Get("state")

	// A forged or foreign state is refused.
	back := follow(t, owner, "/v1/oauth/callback?code=good-code&state="+url.QueryEscape(state+"x"))
	if !strings.Contains(back.Query().Get("oauth_error"), "not valid") {
		t.Fatalf("forged: %s", back)
	}
	stranger := &client{t: t, h: e.h}
	if back = follow(t, stranger, "/v1/oauth/callback?code=good-code&state="+url.QueryEscape(state)); back.Query().Get("oauth_error") == "" {
		t.Fatalf("signed out: %s", back)
	}
	if back = follow(t, owner, "/v1/oauth/callback?error=access_denied&state="+url.QueryEscape(state)); back.Query().Get("oauth_error") != "access was not granted" {
		t.Fatalf("denied: %s", back)
	}

	back = follow(t, owner, "/v1/oauth/callback?code=good-code&state="+url.QueryEscape(state))
	if back.Query().Get("connected") != "li@gw.test" || back.Query().Get("ws") != ws || back.Path != "/"+owner.must(200, "GET", base, nil)["slug"].(string)+"/mailboxes" {
		t.Fatalf("callback: %s", back)
	}
	list := owner.must(200, "GET", base+"/mailboxes", nil)["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("mailboxes: %v", list)
	}
	li := list[0].(map[string]any)
	if li["email"] != "li@gw.test" || li["auth_method"] != "oauth" || li["display_name"] != "Li Wei" || li["pool_name"] != "Warm" {
		t.Fatalf("li: %v", li)
	}
	got := waitFor(t, owner, base+"/mailboxes/"+li["id"].(string), func(m map[string]any) bool { return m["status"] != "verifying" })
	if got["status"] != "active" {
		t.Fatalf("verify: %v", got)
	}
	f.mu.Lock()
	n := f.refreshN
	f.mu.Unlock()
	if n != 1 {
		t.Fatalf("refresh grants: %d, want 1 (the token is cached)", n)
	}

	// API tokens cannot start a consent screen.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "bot", "scopes": []string{"mailboxes.manage"}})
	(&client{t: t, h: e.h, bearer: tok["token"].(string)}).must(403, "GET", base+"/oauth/google/authorize", nil)
}

// plainAuth is a GET with c's credentials that returns the raw body.
func plainAuth(c *client, method, path string) (int, string) {
	req := httptest.NewRequest(method, path, nil)
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}
