package api_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/mailbox/mailtest"
	"github.com/outflareai/outflare/engine/internal/track"
)

// mailFake is Google's and Microsoft's token endpoints as the
// google_mail and outlook_mail connectors call them.
type mailFake struct {
	*httptest.Server
	mu sync.Mutex
	// refresh is how refresh grants are answered: "" works, "revoked"
	// refuses the grant, "down" is a provider outage.
	refresh string
}

func (f *mailFake) setRefresh(mode string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refresh = mode
}

func fakeMailSignIn(t *testing.T) *mailFake {
	fake := &mailFake{}
	fake.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm() //nolint:errcheck
		f := r.PostForm
		reply := func(status int, v map[string]any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(v) //nolint:errcheck
		}
		switch {
		case r.URL.Path == "/token" && f.Get("grant_type") == "authorization_code" && f.Get("code") == "g-code" && f.Get("client_id") == "gid":
			reply(200, map[string]any{"access_token": "g-access", "refresh_token": "g-refresh", "expires_in": 3600,
				"id_token": idToken(map[string]any{"email": "Li@GW.test", "email_verified": true, "name": "Li Wei"})})
		case r.URL.Path == "/token" && f.Get("grant_type") == "authorization_code" && f.Get("code") == "unverified":
			reply(200, map[string]any{"access_token": "x", "refresh_token": "y", "expires_in": 3600,
				"id_token": idToken(map[string]any{"email": "eve@gw.test", "email_verified": false})})
		case r.URL.Path == "/token" && f.Get("grant_type") == "refresh_token":
			fake.mu.Lock()
			mode := fake.refresh
			fake.mu.Unlock()
			switch {
			case mode == "down":
				reply(503, map[string]any{"error": "temporarily_unavailable"})
			case mode == "revoked" || f.Get("client_id") != "gid" || !strings.HasPrefix(f.Get("refresh_token"), "rt-"):
				reply(400, map[string]any{"error": "invalid_grant", "error_description": "Token has been expired or revoked."})
			default:
				reply(200, map[string]any{"access_token": "access-" + f.Get("refresh_token"), "expires_in": 3600})
			}
		case r.URL.Path == "/sa" && f.Get("grant_type") == "urn:ietf:params:oauth:grant-type:jwt-bearer":
			parts := strings.Split(f.Get("assertion"), ".")
			raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
			var c map[string]any
			json.Unmarshal(raw, &c) //nolint:errcheck
			if c["scope"] != "https://mail.google.com/" {
				reply(400, map[string]any{"error": "invalid_scope"})
				return
			}
			sub, _ := c["sub"].(string)
			reply(200, map[string]any{"access_token": "sa-" + sub, "expires_in": 3600})
		case r.URL.Path == "/ms/tenant-1/oauth2/v2.0/token" && f.Get("grant_type") == "client_credentials":
			if f.Get("client_secret") != "app-secret" || f.Get("scope") != "https://outlook.office365.com/.default" {
				reply(401, map[string]any{"error": "invalid_client"})
				return
			}
			reply(200, map[string]any{"access_token": "app-token", "expires_in": 3600})
		default:
			reply(400, map[string]any{"error": "unsupported_grant_type"})
		}
	}))
	t.Cleanup(fake.Close)
	return fake
}

// Mailbox sign-ins are connections: signing in through the connector OAuth
// flow adds the mailbox, admin connections add mailboxes by address, and
// either signs the mailbox in to SMTP and IMAP with XOAUTH2.
func TestMailSignInIsAConnection(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	fake := fakeMailSignIn(t)
	e.integrations.Client.BaseURLs["google_mail.token"] = fake.URL + "/token"
	e.integrations.Client.BaseURLs["google.token"] = fake.URL + "/sa"
	e.integrations.Client.BaseURLs["outlook_mail.login"] = fake.URL + "/ms"
	e.integrations.OAuthApps = map[string]connectors.OAuthApp{"google_mail": {ClientID: "gid", ClientSecret: "gsecret"}}
	mail := mailtest.Start(t, map[string]string{"li@gw.test": "unused", "ana@acme.test": "unused", "cy@tenant.test": "unused"})
	e.mail.Dialer.TLSConfig, e.mail.Dialer.Route = mail.ClientTLS, mail.Route
	mail.AllowToken("li@gw.test", "g-access")
	mail.AllowToken("ana@acme.test", "sa-ana@acme.test")
	mail.AllowToken("cy@tenant.test", "app-token")
	e.runJobs(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	// Both are in the catalog with both ways to connect.
	g := options(catalogEntryFor(t, owner, base+"/connectors", "google_mail"))
	o := options(catalogEntryFor(t, owner, base+"/connectors", "outlook_mail"))
	if g["oauth2"]["recommended"] != true || g["api_key"]["available"] != true || o["oauth2"]["available"] != false || o["api_key"]["recommended"] != true {
		t.Fatalf("catalog: %v %v", g, o)
	}

	// One person signs in to their mailbox.
	link := owner.must(200, "GET", base+"/integrations/oauth/google_mail/authorize?format=json&pool=Founders", nil)["url"].(string)
	u, _ := url.Parse(link)
	if q := u.Query(); u.Host != "accounts.google.com" || !strings.Contains(q.Get("scope"), "https://mail.google.com/") || q.Get("access_type") != "offline" {
		t.Fatalf("authorize: %s", link)
	}
	state := url.QueryEscape(u.Query().Get("state"))
	cb := follow(t, owner, "/v1/integrations/oauth/callback?code=g-code&state="+state)
	id := cb.Query().Get("integration")
	if id == "" || cb.Query().Get("mailbox") != "li@gw.test" || !strings.HasSuffix(cb.Path, "/mailboxes") {
		t.Fatalf("callback: %s", cb)
	}
	ev := events()
	if c := named(ev, "integration.connected"); len(c) != 1 || c[0].Props["target_type"] != "integration" {
		t.Fatalf("integration.connected: %+v", c)
	}
	if len(named(ev, "mailbox.added")) != 1 {
		t.Fatalf("mailbox.added: %+v", named(ev, "mailbox.added"))
	}
	conn := owner.must(200, "GET", base+"/integrations/"+id, nil)
	if conn["connector"] != "google_mail" || conn["auth_method"] != "oauth2" || conn["account"].(map[string]any)["email"] != "li@gw.test" {
		t.Fatalf("connection: %v", conn)
	}
	byEmail := func() map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, m := range owner.must(200, "GET", base+"/mailboxes", nil)["data"].([]any) {
			out[m.(map[string]any)["email"].(string)] = m.(map[string]any)
		}
		return out
	}
	li := byEmail()["li@gw.test"]
	if li["auth_method"] != "oauth" || li["provider"] != "google" || li["pool_name"] != "Founders" || li["display_name"] != "Li Wei" {
		t.Fatalf("li: %v", li)
	}
	var linked string
	if err := e.pool.QueryRow(t.Context(), `SELECT integration_id::text FROM mailboxes WHERE id = $1`, li["id"]).Scan(&linked); err != nil || linked != id {
		t.Fatalf("mailbox integration: %q %v", linked, err)
	}
	got := waitFor(t, owner, base+"/mailboxes/"+li["id"].(string), func(m map[string]any) bool { return m["status"] != "verifying" })
	if got["status"] != "active" || got["smtp_ok"] != true || got["imap_ok"] != true {
		t.Fatalf("li verified: %v", got)
	}
	owner.must(200, "POST", base+"/mailboxes/"+li["id"].(string)+"/test-send", map[string]any{"to": "ro@one-io.example"})
	if msgs := mail.Messages(); len(msgs) != 1 || msgs[0].From != "li@gw.test" {
		t.Fatalf("sent: %v", msgs)
	}

	// Signing in again to the same mailbox keeps one connection.
	link = owner.must(200, "GET", base+"/integrations/oauth/google_mail/authorize?format=json", nil)["url"].(string)
	u, _ = url.Parse(link)
	if again := follow(t, owner, "/v1/integrations/oauth/callback?code=g-code&state="+url.QueryEscape(u.Query().Get("state"))); again.Query().Get("integration") != id {
		t.Fatalf("second sign-in: %s", again)
	}
	if n := len(owner.must(200, "GET", base+"/integrations?connector=google_mail", nil)["data"].([]any)); n != 1 {
		t.Fatalf("connections after signing in twice: %d", n)
	}
	// Google must have verified the address.
	link = owner.must(200, "GET", base+"/integrations/oauth/google_mail/authorize?format=json", nil)["url"].(string)
	u, _ = url.Parse(link)
	if bad := follow(t, owner, "/v1/integrations/oauth/callback?code=unverified&state="+url.QueryEscape(u.Query().Get("state"))); !strings.Contains(bad.Query().Get("connect_error"), "verified") {
		t.Fatalf("unverified: %s", bad)
	}

	// A sign-in connects only its own mailbox.
	owner.must(400, "POST", base+"/integrations/"+id+"/mailboxes", map[string]any{"mailboxes": []map[string]string{{"email": "x@gw.test"}}})
	// A connection mailboxes use cannot be removed.
	owner.must(409, "DELETE", base+"/integrations/"+id, nil)

	// Admin connections: a Google service account and a Microsoft Entra app.
	sa := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "google_mail", "credentials": map[string]string{"service_account_json": newServiceAccount(t)}})
	owner.must(422, "POST", base+"/integrations", map[string]any{"connector": "outlook_mail", "credentials": map[string]string{"tenant_id": "tenant-1", "client_id": "app-1", "client_secret": "wrong"}})
	app := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "outlook_mail", "credentials": map[string]string{"tenant_id": "tenant-1", "client_id": "app-1", "client_secret": "app-secret"}})
	res := owner.must(200, "POST", base+"/integrations/"+sa["id"].(string)+"/mailboxes", map[string]any{
		"mailboxes": []map[string]string{{"email": "Ana@acme.test", "display_name": "Ana"}, {"email": "nope"}}, "pool": "Google"})
	if res["created"] != 1.0 || len(res["errors"].([]any)) != 1 {
		t.Fatalf("service account add: %v", res)
	}
	owner.must(200, "POST", base+"/integrations/"+app["id"].(string)+"/mailboxes", map[string]any{"mailboxes": []map[string]string{{"email": "cy@tenant.test"}}})
	all := byEmail()
	if c := all["cy@tenant.test"]; c["provider"] != "microsoft" || c["smtp_host"] != "smtp.office365.com" || c["auth_method"] != "oauth" {
		t.Fatalf("cy: %v", c)
	}
	for _, email := range []string{"ana@acme.test", "cy@tenant.test"} {
		got := waitFor(t, owner, base+"/mailboxes/"+all[email]["id"].(string), func(m map[string]any) bool { return m["status"] != "verifying" })
		if got["status"] != "active" || got["smtp_ok"] != true || got["imap_ok"] != true {
			t.Fatalf("%s: %v", email, got)
		}
	}
	// They show in the one list of connections.
	kinds := map[string]int{}
	for _, c := range owner.must(200, "GET", base+"/connections?category=mailbox_provider", nil)["data"].([]any) {
		kinds[c.(map[string]any)["kind"].(string)]++
	}
	if kinds["integration"] != 3 {
		t.Fatalf("connections: %v", kinds)
	}
	owner.must(204, "DELETE", base+"/mailboxes/"+all["cy@tenant.test"]["id"].(string), nil)
	owner.must(204, "DELETE", base+"/integrations/"+app["id"].(string), nil)

	// Signing in adds a mailbox, so it needs mailboxes.manage too.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "connector", "scopes": []string{"integrations.manage"}})
	agent := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	agent.must(403, "GET", base+"/integrations/oauth/google_mail/authorize?format=json", nil)
	agent.must(403, "POST", base+"/integrations/"+sa["id"].(string)+"/mailboxes", map[string]any{"mailboxes": []map[string]string{{"email": "bo@acme.test"}}})
	// Nor can it ask someone else to sign in a mailbox through a link.
	agent.must(403, "POST", base+"/connect-requests", map[string]any{"connector": "google_mail"})
	owner.must(201, "POST", base+"/connect-requests", map[string]any{"connector": "google_mail"})
}

// newServiceAccount is a Google service account key file with a fresh key.
func newServiceAccount(t *testing.T) string {
	f, _ := newFakeProvider(t)
	return f.serviceAccount()
}
