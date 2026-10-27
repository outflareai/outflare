package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/catalog"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/mailbox/mailtest"
	"github.com/outflareai/outflare/engine/internal/vendorsync"
)

// Push: Outflare adds itself at Cheap Inboxes as a self-hosted EmailBison,
// and Cheap Inboxes sends mailboxes in through the EmailBison-compatible API.
func TestCheapInboxesPush(t *testing.T) {
	e := freshEnv(t)
	mail := mailtest.Start(t, map[string]string{"ms@acme.test": "ms-password"})
	e.mail.Dialer.TLSConfig = mail.ClientTLS
	fake := &fakeCheapInboxes{
		passwords:   map[string]string{"mb-ms": "ms-password", "mb-two": "two-password"},
		appPassword: map[string]string{}, appPwValue: map[string]string{},
		hooks: map[string]string{}, secret: "whsec-test", integrations: map[string]map[string]any{},
		smtpHost: mail.Host, smtpPort: mail.SMTPPort, imapPort: mail.IMAPPort,
		mailboxes: []map[string]any{
			{"id": "mb-ms", "full_email": "ms@acme.test", "first_name": "Mia", "last_name": "Stone", "status": "active", "provider": "microsoft"},
			{"id": "mb-two", "full_email": "two@acme.test", "first_name": "Tia", "last_name": "Wo", "status": "active", "provider": "microsoft"},
			{"id": "mb-new", "full_email": "new@acme.test", "first_name": "Nia", "last_name": "Ew", "status": "provisioning", "provider": "microsoft"},
		},
	}
	ci := httptest.NewServer(fake)
	t.Cleanup(ci.Close)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := &connectors.Store{Pool: e.pool, Box: e.box, Registry: catalog.New(),
		Client: &connectors.Client{BaseURLs: map[string]string{"cheapinboxes": ci.URL}}}
	vendors := &vendorsync.Service{Pool: e.pool, Box: e.box, Store: store, Mail: e.mail, Log: log,
		PublicURL: "https://outflare.example.com/", AppPasswordPoll: 50 * time.Millisecond, SyncEvery: time.Hour}
	store.OnConnect = append(store.OnConnect, vendors.Connected)
	store.OnDisconnect = append(store.OnDisconnect, vendors.Disconnect)
	h := handler(t, api.NewServer(e.pool, log, api.Options{Mailboxes: e.mail, Sending: e.send, Inbox: e.sync, OAuth: e.oauth,
		Box: e.box, Integrations: store, Vendors: vendors}))
	fake.outflare = h

	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	e.mail.Register(q)
	vendors.Register(q)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { q.Run(ctx, 4); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })

	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws
	integ := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "cheapinboxes", "credentials": map[string]string{"api_key": "ci_live_test"}})
	path := base + "/integrations/" + integ["id"].(string)
	waitFor(t, owner, path+"/mailboxes", func(m map[string]any) bool { return m["synced"] == true })

	push := func() map[string]any {
		return owner.must(200, "GET", path+"/mailbox-sync", nil)["push"].(map[string]any)
	}
	p := push()
	if p["available"] != true || p["state"] != "off" || p["base_url"] != "https://outflare.example.com/emailbison/"+ws {
		t.Fatalf("push before: %v", p)
	}

	// Pushing needs push on; turning it on needs integrations.manage too.
	if code, out := owner.do("POST", path+"/mailboxes/push", map[string]any{"all": true}); code != 409 || errCode(out) != "push_not_on" {
		t.Fatalf("push while off: %d %v", code, out)
	}
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "mailboxes only", "scopes": []string{"mailboxes.read", "mailboxes.manage"}})
	narrow := &client{t: t, h: h, bearer: tok["token"].(string)}
	if code, _ := narrow.do("PATCH", path+"/mailbox-sync", map[string]any{"push": true}); code != 403 {
		t.Fatalf("push without integrations.manage: %d", code)
	}

	// A refusal at the supplier is shown, and its token is revoked.
	fake.mu.Lock()
	fake.failCreate = true
	fake.mu.Unlock()
	if st := owner.must(200, "PATCH", path+"/mailbox-sync", map[string]any{"push": true}); st["push"].(map[string]any)["state"] != "connecting" {
		t.Fatalf("connecting: %v", st)
	}
	waitUntil(t, func() bool { return push()["state"] == "failed" })
	if p := push(); !strings.Contains(p["error"].(string), "Could not connect") {
		t.Fatalf("failed: %v", p)
	}
	fake.mu.Lock()
	fake.failCreate = false
	fake.mu.Unlock()

	// On: the supplier holds a token that answers the EmailBison API.
	owner.must(200, "PATCH", path+"/mailbox-sync", map[string]any{"push": true})
	waitUntil(t, func() bool { return push()["state"] == "on" })
	owner.must(200, "PATCH", path+"/mailbox-sync", map[string]any{"push": true}) // already on: nothing new
	fake.mu.Lock()
	if len(fake.integrations) != 1 {
		t.Fatalf("connections: %v", fake.integrations)
	}
	conn := fake.integrations["int_1"]
	login := conn["sequencer_login"].(map[string]any)
	fake.mu.Unlock()
	if !strings.HasPrefix(conn["api_key"].(string), "oft_") || conn["base_url"] != "https://outflare.example.com/emailbison/"+ws || login["email"] != "jack@example.com" {
		t.Fatalf("connection: %v", conn)
	}
	toks := owner.must(200, "GET", base+"/api-tokens", nil)["data"].([]any)
	var pushTokens int
	for _, tk := range toks {
		if tm := tk.(map[string]any); strings.HasPrefix(tm["name"].(string), "Push from") {
			pushTokens++
		}
	}
	if pushTokens != 1 {
		t.Fatalf("live push tokens: %d in %v", pushTokens, toks)
	}

	// Push every active mailbox not yet in Outflare: the provisioning one waits.
	res := owner.must(202, "POST", path+"/mailboxes/push", map[string]any{"all": true})
	if res["mailboxes"] != 2.0 {
		t.Fatalf("push: %v", res)
	}
	waitUntil(t, func() bool { return push()["last_result"] != nil })
	if r := push()["last_result"].(map[string]any); r["synced"] != 2.0 || r["failed"] != 0.0 {
		t.Fatalf("pushed: %v", r)
	}
	fake.mu.Lock()
	linked := strings.Join(fake.integrations["int_1"]["linked"].([]string), ",")
	fake.mu.Unlock()
	if linked != "mb-ms,mb-two" {
		t.Fatalf("linked: %s", linked)
	}
	var ms map[string]any
	for _, m := range owner.must(200, "GET", base+"/mailboxes", nil)["data"].([]any) {
		if m.(map[string]any)["email"] == "ms@acme.test" {
			ms = m.(map[string]any)
		}
	}
	if ms == nil || ms["source"] != "emailbison_api" {
		t.Fatalf("pushed mailbox: %v", ms)
	}
	got := waitFor(t, owner, base+"/mailboxes/"+ms["id"].(string), func(m map[string]any) bool { return m["status"] != "verifying" })
	if got["status"] != "active" {
		t.Fatalf("verified: %v", got)
	}

	// Off: the token stops working and the connection is removed there.
	fake.mu.Lock()
	key := conn
	fake.mu.Unlock()
	owner.must(200, "PATCH", path+"/mailbox-sync", map[string]any{"push": false})
	if p := push(); p["state"] != "off" {
		t.Fatalf("off: %v", p)
	}
	if code, _ := fake.callOutflare(key, "GET", "/api/users", nil); code != 401 {
		t.Fatalf("revoked token: %d", code)
	}
	waitUntil(t, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.integrations) == 0
	})

	// Disconnecting the account removes a live push connection too.
	owner.must(200, "PATCH", path+"/mailbox-sync", map[string]any{"push": true})
	waitUntil(t, func() bool { return push()["state"] == "on" })
	owner.must(204, "DELETE", path, nil)
	fake.mu.Lock()
	left := len(fake.integrations)
	fake.mu.Unlock()
	if left != 0 {
		t.Fatalf("connections after disconnect: %d", left)
	}
}

// Without a public https address there is nowhere for the supplier to push.
func TestCheapInboxesPushNeedsPublicURL(t *testing.T) {
	e := freshEnv(t)
	fake := &fakeCheapInboxes{passwords: map[string]string{}, appPassword: map[string]string{}, appPwValue: map[string]string{},
		hooks: map[string]string{}, integrations: map[string]map[string]any{}}
	ci := httptest.NewServer(fake)
	t.Cleanup(ci.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := &connectors.Store{Pool: e.pool, Box: e.box, Registry: catalog.New(),
		Client: &connectors.Client{BaseURLs: map[string]string{"cheapinboxes": ci.URL}}}
	vendors := &vendorsync.Service{Pool: e.pool, Box: e.box, Store: store, Mail: e.mail, Log: log, PublicURL: "http://localhost:8080"}
	store.OnConnect = append(store.OnConnect, vendors.Connected)
	h := handler(t, api.NewServer(e.pool, log, api.Options{Mailboxes: e.mail, Sending: e.send, Inbox: e.sync, OAuth: e.oauth,
		Box: e.box, Integrations: store, Vendors: vendors}))
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws
	integ := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "cheapinboxes", "credentials": map[string]string{"api_key": "ci_live_test"}})
	path := base + "/integrations/" + integ["id"].(string)
	if p := owner.must(200, "GET", path+"/mailbox-sync", nil)["push"].(map[string]any); p["available"] != false || p["base_url"] != nil {
		t.Fatalf("push: %v", p)
	}
	if code, out := owner.do("PATCH", path+"/mailbox-sync", map[string]any{"push": true}); code != 422 || errCode(out) != "push_unavailable" {
		t.Fatalf("push without public URL: %d %v", code, out)
	}
}

func errCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}
