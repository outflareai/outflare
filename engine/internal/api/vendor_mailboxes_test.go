package api_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/catalog"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/mailbox/mailtest"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/vendorsync"
)

// fakeCheapInboxes is a stand-in for api.cheapinboxes.com, shaped like the
// live API (camelCase credentials, limit/offset pagination).
type fakeCheapInboxes struct {
	mu          sync.Mutex
	mailboxes   []map[string]any
	passwords   map[string]string
	appPassword map[string]string // id → app password state: none, running, ready
	appPwValue  map[string]string
	appRequests int
	appFailures int               // app password requests to answer with a 503 first
	hooks       map[string]string // id → url
	secret      string
	deleted     []string
	smtpHost    string
	smtpPort    int
	imapPort    int

	// Push: sequencer connections, and Outflare's handler, which the fake
	// calls the way Cheap Inboxes would call a self-hosted EmailBison.
	outflare     http.Handler
	integrations map[string]map[string]any
	failCreate   bool
}

// callOutflare sends one EmailBison API call to the connection's base URL.
func (f *fakeCheapInboxes) callOutflare(conn map[string]any, method, path string, body any) (int, []byte) {
	u := strings.TrimPrefix(conn["base_url"].(string), "https://outflare.example.com")
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req := httptest.NewRequest(method, u+path, rd)
	req.Header.Set("Authorization", "Bearer "+conn["api_key"].(string))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.outflare.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func (f *fakeCheapInboxes) find(id string) map[string]any {
	for _, m := range f.mailboxes {
		if m["id"] == id {
			return m
		}
	}
	return nil
}

func (f *fakeCheapInboxes) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer ci_live_test" {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":{"code":"UNAUTHORIZED","message":"bad key"}}`)
		return
	}
	reply := func(v any) { json.NewEncoder(w).Encode(v) } //nolint:errcheck
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.URL.Path == "/v1/org":
		reply(map[string]any{"organization": map[string]any{"id": "org1", "name": "Acme", "status": "active"}, "role": "owner"})
	case r.URL.Path == "/v1/mailboxes":
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		limit = min(limit, 2) // force paging
		end := min(offset+limit, len(f.mailboxes))
		page := f.mailboxes[min(offset, end):end]
		reply(map[string]any{"mailboxes": page, "pagination": map[string]any{"total": len(f.mailboxes), "limit": limit, "offset": offset, "has_more": end < len(f.mailboxes)}})
	case len(parts) == 3 && parts[1] == "mailboxes" && r.Method == "GET":
		m := f.find(parts[2])
		if m == nil {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":{"code":"NOT_FOUND","message":"Mailbox not found"}}`)
			return
		}
		reply(map[string]any{"mailbox": m})
	case len(parts) == 4 && parts[3] == "credentials":
		m := f.find(parts[2])
		if m == nil || m["status"] != "active" {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"code":"VALIDATION_ERROR","message":"Mailbox is not active"}}`)
			return
		}
		var app any
		if f.appPassword[parts[2]] == "ready" {
			app = f.appPwValue[parts[2]]
		}
		reply(map[string]any{"credentials": map[string]any{"email": m["full_email"], "password": f.passwords[parts[2]], "appPassword": app,
			"smtpHost": f.smtpHost, "smtpPort": f.smtpPort, "imapHost": f.smtpHost, "imapPort": f.imapPort}})
	case len(parts) == 4 && parts[3] == "app-password" && r.Method == "POST" && f.appFailures > 0:
		f.appFailures--
		w.WriteHeader(503)
		reply(map[string]any{"error": "try again later"})
	case len(parts) == 4 && parts[3] == "app-password" && r.Method == "POST":
		f.appRequests++
		f.appPassword[parts[2]] = "running"
		w.WriteHeader(202)
		reply(map[string]any{"success": true, "status": "queued"})
	case len(parts) == 4 && parts[3] == "app-password":
		st := f.appPassword[parts[2]]
		if st == "running" { // ready on the second look
			f.appPassword[parts[2]] = "ready"
		}
		var pw any
		if st == "ready" {
			pw = f.appPwValue[parts[2]]
		}
		reply(map[string]any{"status": orNone(st), "appPassword": pw})
	case r.URL.Path == "/v1/webhooks" && r.Method == "POST":
		var body struct {
			URL string `json:"url"`
		}
		json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
		id := fmt.Sprintf("wh_%d", len(f.hooks)+1)
		f.hooks[id] = body.URL
		w.WriteHeader(201)
		reply(map[string]any{"id": id, "url": body.URL, "secret": f.secret})
	case len(parts) == 3 && parts[1] == "webhooks" && r.Method == "DELETE":
		f.deleted = append(f.deleted, parts[2])
		delete(f.hooks, parts[2])
		w.WriteHeader(204)
	case r.URL.Path == "/v1/integrations" && r.Method == "POST":
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
		login, _ := body["sequencer_login"].(map[string]any)
		if f.failCreate || body["provider"] != "emailbison" || login["email"] == "" || login["password"] == "" {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"code":"VALIDATION_ERROR","message":"Could not connect to EmailBison"}}`)
			return
		}
		// Like the real one, check the key against the sequencer first.
		if code, _ := f.callOutflare(body, "GET", "/api/users", nil); code != 200 {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"code":"VALIDATION_ERROR","message":"EmailBison rejected the API key"}}`)
			return
		}
		id := fmt.Sprintf("int_%d", len(f.integrations)+1)
		body["linked"] = []string{}
		f.integrations[id] = body
		reply(map[string]any{"integration": map[string]any{"id": id, "provider": "emailbison", "status": "active"}})
	case len(parts) == 4 && parts[1] == "integrations" && parts[3] == "link-mailboxes":
		conn := f.integrations[parts[2]]
		var body struct {
			IDs []string `json:"mailbox_ids"`
		}
		json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
		conn["linked"] = append(conn["linked"].([]string), body.IDs...)
		reply(map[string]any{"linked": len(body.IDs)})
	case len(parts) == 4 && parts[1] == "integrations" && parts[3] == "sync":
		conn := f.integrations[parts[2]]
		var body struct {
			IDs []string `json:"mailbox_ids"`
		}
		json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
		res := map[string]int{"synced": 0, "failed": 0, "skipped": 0}
		for _, id := range body.IDs {
			m := f.find(id)
			if m == nil || m["status"] != "active" {
				res["skipped"]++
				continue
			}
			code, _ := f.callOutflare(conn, "POST", "/api/sender-emails/imap-smtp", map[string]any{
				"email": m["full_email"], "name": fmt.Sprint(m["first_name"], " ", m["last_name"]), "password": f.passwords[id],
				"smtp_server": f.smtpHost, "smtp_port": f.smtpPort, "imap_server": f.smtpHost, "imap_port": f.imapPort})
			if code == 200 {
				res["synced"]++
			} else {
				res["failed"]++
			}
		}
		reply(res)
	case len(parts) == 3 && parts[1] == "integrations" && r.Method == "DELETE":
		if f.integrations[parts[2]] == nil {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":{"code":"NOT_FOUND","message":"Integration not found"}}`)
			return
		}
		delete(f.integrations, parts[2])
		reply(map[string]any{"success": true})
	default:
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error":{"code":"NOT_FOUND","message":"no route"}}`)
	}
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func (f *fakeCheapInboxes) set(id, key string, v any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.find(id)[key] = v
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestCheapInboxesImportAndSync(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	mail := mailtest.Start(t, map[string]string{
		"ms@acme.test": "ms-password", "gw@acme.test": "gw-app-password", "new@acme.test": "new-password",
	})
	e.mail.Dialer.TLSConfig = mail.ClientTLS

	fake := &fakeCheapInboxes{
		passwords:   map[string]string{"mb-ms": "ms-password", "mb-gw": "plain-google-password", "mb-new": "new-password"},
		appPassword: map[string]string{}, appPwValue: map[string]string{"mb-gw": "gw-app-password"},
		hooks: map[string]string{}, secret: "whsec-test", appFailures: 2,
		smtpHost: mail.Host, smtpPort: mail.SMTPPort, imapPort: mail.IMAPPort,
		mailboxes: []map[string]any{
			{"id": "mb-ms", "full_email": "ms@acme.test", "first_name": "Mia", "last_name": "Stone", "status": "active",
				"provider": "microsoft", "daily_limit": 40, "tags": []string{"sales"}, "emailbison_id": "350"},
			{"id": "mb-gw", "full_email": "GW@acme.test", "first_name": "Gus", "last_name": "Webb", "status": "active",
				"provider": "google", "daily_limit": 50, "app_password_status": "none"},
			{"id": "mb-new", "full_email": "new@acme.test", "first_name": "Nia", "last_name": "Ew", "status": "provisioning",
				"provider": "microsoft"},
		},
	}
	ci := httptest.NewServer(fake)
	t.Cleanup(ci.Close)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := &connectors.Store{Pool: e.pool, Box: e.box, Registry: catalog.New(),
		Client: &connectors.Client{BaseURLs: map[string]string{"cheapinboxes": ci.URL}}}
	vendors := &vendorsync.Service{Pool: e.pool, Box: e.box, Store: store, Mail: e.mail, Log: log,
		PublicURL: "https://outflare.example.com", AppPasswordPoll: 50 * time.Millisecond, SyncEvery: time.Hour}
	store.OnConnect = append(store.OnConnect, vendors.Connected)
	store.OnDisconnect = append(store.OnDisconnect, vendors.Disconnect)
	h := handler(t, api.NewServer(e.pool, log, api.Options{Mailboxes: e.mail, Sending: e.send, Inbox: e.sync, OAuth: e.oauth,
		Box: e.box, Integrations: store, Vendors: vendors}))

	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	e.mail.Register(q)
	vendors.Register(q)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 2)
	go func() { q.Run(ctx, 4); done <- struct{}{} }() //nolint:errcheck
	go func() { vendors.Run(ctx, 50*time.Millisecond); done <- struct{}{} }()
	t.Cleanup(func() { cancel(); <-done; <-done })

	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws

	// A wrong key is refused before anything is saved.
	if code, out := owner.do("POST", base+"/integrations", map[string]any{"connector": "cheapinboxes", "credentials": map[string]string{"api_key": "ci_live_wrong"}}); code != 422 {
		t.Fatalf("wrong key: %d %v", code, out)
	}
	integ := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "cheapinboxes", "credentials": map[string]string{"api_key": "ci_live_test"}})
	id := integ["id"].(string)
	path := base + "/integrations/" + id

	// The first sync runs on connect and pages through all three.
	list := waitFor(t, owner, path+"/mailboxes", func(m map[string]any) bool { return m["synced"] == true })
	counts := list["counts"].(map[string]any)
	if counts["total"] != 3.0 || counts["by_status"].(map[string]any)["provisioning"] != 1.0 || counts["connected_to"].(map[string]any)["emailbison"] != 1.0 {
		t.Fatalf("after sync: %v", list)
	}
	for _, m := range list["mailboxes"].([]any) {
		if m.(map[string]any)["email"] == "gw@acme.test" && m.(map[string]any)["import_state"] != "available" {
			t.Fatalf("lower-cased and not imported: %v", m)
		}
	}
	// A webhook was registered at the public URL.
	waitUntil(t, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.hooks["wh_1"] == "https://outflare.example.com/v1/integrations/"+id+"/webhook"
	})

	// Import everything.
	res := owner.must(202, "POST", path+"/mailboxes/import", map[string]any{"all": true})
	if res["queued"] != 2.0 || res["waiting"] != 1.0 {
		t.Fatalf("import: %v", res)
	}
	stateOf := func(email string) map[string]any {
		l := owner.must(200, "GET", path+"/mailboxes?q="+email, nil)["mailboxes"].([]any)
		if len(l) != 1 {
			t.Fatalf("find %s: %v", email, l)
		}
		return l[0].(map[string]any)
	}
	waitUntil(t, func() bool { return stateOf("ms@acme.test")["import_state"] == "imported" })
	// Google waits for an app password, which the pacer requests and collects.
	waitUntil(t, func() bool { return stateOf("gw@acme.test")["import_state"] == "imported" })
	fake.mu.Lock()
	requests := fake.appRequests
	fake.mu.Unlock()
	if requests != 1 {
		t.Fatalf("app password requests: %d", requests)
	}
	// The supplier's two 503s were each left to the next round, not retried
	// until the job died.
	if failed := named(events(), "vendor.app_password_request_failed"); len(failed) != 2 || failed[0].ErrorCode != "connector_unavailable" {
		t.Fatalf("request failures: %+v", failed)
	}
	for _, e := range named(events(), "job.dead") {
		if e.Props["kind"] == vendorsync.JobAppPasswords {
			t.Fatalf("app password round died: %+v", e)
		}
	}
	ms, gw := stateOf("ms@acme.test"), stateOf("gw@acme.test")
	msPath := base + "/mailboxes/" + ms["mailbox_id"].(string)
	got := waitFor(t, owner, msPath, func(m map[string]any) bool { return m["status"] != "verifying" })
	if got["status"] != "active" || got["provider"] != "microsoft" || got["daily_limit"] != 40.0 || got["display_name"] != "Mia Stone" || got["source"] != "cheapinboxes" {
		t.Fatalf("microsoft mailbox: %v", got)
	}
	// Signed in with the app password, not the plain one.
	got = waitFor(t, owner, base+"/mailboxes/"+gw["mailbox_id"].(string), func(m map[string]any) bool { return m["status"] != "verifying" })
	if got["status"] != "active" {
		t.Fatalf("google mailbox: %v", got)
	}

	// The provisioning one waits, then imports when a webhook says it is active.
	if s := stateOf("new@acme.test"); s["import_state"] != "waiting" {
		t.Fatalf("new: %v", s)
	}
	fake.set("mb-new", "status", "active")
	body := []byte(`{"id":"evt_1","event":"mailbox.active","payload":{"mailbox_id":"mb-new"},"timestamp":"2026-10-23T12:00:00Z"}`)
	hook := &client{t: t, h: h}
	post := func(sig string) int {
		req := httptest.NewRequest("POST", "/v1/integrations/"+id+"/webhook", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Webhook-Signature", sig)
		rec := httptest.NewRecorder()
		hook.h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post(sign("wrong", body)); code != 401 {
		t.Fatalf("bad signature: %d", code)
	}
	if code := post(sign(fake.secret, body)); code != 200 {
		t.Fatalf("webhook: %d", code)
	}
	if code := post(sign(fake.secret, body)); code != 200 { // a retry is accepted and ignored
		t.Fatalf("duplicate webhook: %d", code)
	}
	waitUntil(t, func() bool { return stateOf("new@acme.test")["import_state"] == "imported" })

	// Cancelled at the supplier: the next sync pauses the Outflare mailbox.
	fake.set("mb-ms", "status", "cancelled")
	owner.must(202, "POST", path+"/mailboxes/sync", nil)
	got = waitFor(t, owner, msPath, func(m map[string]any) bool { return m["status"] == "paused" })
	if !strings.Contains(fmt.Sprint(got["status_reason"]), "Cancelled") {
		t.Fatalf("paused: %v", got)
	}

	// Settings.
	st := owner.must(200, "PATCH", path+"/mailbox-sync", map[string]any{"auto_import": true, "daily_limit": 25})
	if st["auto_import"] != true || st["daily_limit"] != 25.0 || st["webhook"] != true {
		t.Fatalf("settings: %v", st)
	}
	if code, _ := owner.do("PATCH", path+"/mailbox-sync", map[string]any{"daily_limit": 900}); code != 400 {
		t.Fatalf("limit out of range: %d", code)
	}

	// Disconnecting removes the supplier's webhook.
	owner.must(204, "DELETE", path, nil)
	fake.mu.Lock()
	deleted := strings.Join(fake.deleted, ",")
	fake.mu.Unlock()
	if deleted != "wh_1" {
		t.Fatalf("webhooks deleted: %q", deleted)
	}
	if code, _ := owner.do("GET", path+"/mailboxes", nil); code != 404 {
		t.Fatalf("after disconnect: %d", code)
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(25 * time.Millisecond)
	}
}
