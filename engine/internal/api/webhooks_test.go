package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	standardwebhooks "github.com/standard-webhooks/standard-webhooks/libraries/go"
	"go.yaml.in/yaml/v4"

	spec "github.com/outflareai/outflare/api"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/webhooks"
)

// receiver is a webhook endpoint that keeps what it is sent.
type receiver struct {
	*httptest.Server
	mu     sync.Mutex
	got    []http.Header
	bodies [][]byte
	status int
}

func newReceiver(t *testing.T) *receiver {
	rc := &receiver{status: 200}
	rc.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rc.mu.Lock()
		rc.got = append(rc.got, r.Header.Clone())
		rc.bodies = append(rc.bodies, b)
		status := rc.status
		rc.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(rc.Close)
	return rc
}

func (rc *receiver) count() int { rc.mu.Lock(); defer rc.mu.Unlock(); return len(rc.bodies) }

func webhookEnv(t *testing.T) (*env, *client, string, *webhooks.Service) {
	e := freshEnv(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	hooks := &webhooks.Service{Pool: e.pool, Box: e.box, Log: log, AllowPrivate: true}
	e.h = handler(t, api.NewServer(e.pool, log, api.Options{Box: e.box, Webhooks: hooks}))
	owner, ws := setupOwner(t, e.h)
	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	hooks.Register(q)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { q.Run(ctx, 2); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })
	return e, owner, ws, hooks
}

func TestWebhooksDeliverSignedEvents(t *testing.T) {
	_, owner, ws, _ := webhookEnv(t)
	rc := newReceiver(t)
	base := "/v1/workspaces/" + ws

	if n := len(owner.must(200, "GET", "/v1/webhook-events", nil)["data"].([]any)); n < 40 {
		t.Fatalf("event catalog has %d types", n)
	}
	if code, out := owner.do("POST", base+"/webhooks", map[string]any{"url": rc.URL, "events": []string{"lead.nope"}}); code != 400 {
		t.Fatalf("unknown event accepted: %d %v", code, out)
	}
	hook := owner.must(201, "POST", base+"/webhooks", map[string]any{"url": rc.URL + "/hook", "events": []string{"lead.*"}, "description": "CRM sync"})
	secret, _ := hook["secret"].(string)
	if !strings.HasPrefix(secret, "whsec_") {
		t.Fatalf("secret %q", secret)
	}
	id := hook["id"].(string)
	if _, ok := owner.must(200, "GET", base+"/webhooks", nil)["data"].([]any)[0].(map[string]any)["secret"]; ok {
		t.Fatal("list shows the secret")
	}
	if got := owner.must(200, "GET", base+"/webhooks/"+id+"/secret", nil)["secret"]; got != secret {
		t.Fatalf("secret endpoint %v", got)
	}

	// A lead change is an event the endpoint subscribed to; a sequence is not.
	lead := owner.must(201, "POST", base+"/leads", map[string]any{"email": "ana@getacme-co.example"})
	owner.must(201, "POST", base+"/sequences", map[string]any{"name": "Q4"})
	deadline := time.Now().Add(10 * time.Second)
	for rc.count() < 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if rc.count() != 1 {
		t.Fatalf("receiver got %d deliveries, want 1", rc.count())
	}
	wh, err := standardwebhooks.NewWebhook(secret)
	if err != nil {
		t.Fatal(err)
	}
	if err := wh.Verify(rc.bodies[0], rc.got[0]); err != nil {
		t.Fatalf("signature does not verify with the Standard Webhooks library: %v", err)
	}
	var ev struct {
		ID, Type    string
		WorkspaceID string `json:"workspace_id"`
		Target      struct{ Type, ID string }
		RequestID   *string `json:"request_id"`
		Data        map[string]any
	}
	json.Unmarshal(rc.bodies[0], &ev) //nolint:errcheck
	if ev.Type != "lead.created" || ev.WorkspaceID != ws || ev.Target.Type != "lead" || ev.Target.ID != lead["id"] ||
		ev.ID != rc.got[0].Get("webhook-id") || !strings.HasPrefix(ev.ID, "evt_") || ev.RequestID == nil {
		t.Fatalf("event %+v", ev)
	}

	ds := owner.must(200, "GET", base+"/webhooks/"+id+"/deliveries", nil)["data"].([]any)
	if len(ds) != 1 || ds[0].(map[string]any)["status"] != "delivered" || ds[0].(map[string]any)["last_status_code"] != 200.0 {
		t.Fatalf("deliveries %v", ds)
	}

	// A test is sent at once, and the answer carries the result.
	rc.mu.Lock()
	rc.status = 500
	rc.mu.Unlock()
	test := owner.must(200, "POST", base+"/webhooks/"+id+"/test", nil)
	if test["status"] != "failed" || test["last_status_code"] != 500.0 || test["event_type"] != "webhook.test" {
		t.Fatalf("test %v", test)
	}
	rc.mu.Lock()
	rc.status = 204
	rc.mu.Unlock()
	again := owner.must(200, "POST", base+"/webhooks/"+id+"/deliveries/"+jsonNum(test["id"])+"/redeliver", nil)
	if again["status"] != "delivered" || again["attempts"] != 2.0 || again["event_id"] != test["event_id"] {
		t.Fatalf("redeliver %v", again)
	}

	// After a rotation both secrets sign, so receivers can switch over.
	rotated := owner.must(200, "POST", base+"/webhooks/"+id+"/rotate-secret", nil)["secret"].(string)
	owner.must(200, "POST", base+"/webhooks/"+id+"/test", nil)
	last := rc.count() - 1
	for _, s := range []string{secret, rotated} {
		wh, _ := standardwebhooks.NewWebhook(s)
		if err := wh.Verify(rc.bodies[last], rc.got[last]); err != nil {
			t.Fatalf("after rotation, %s… does not verify: %v", s[:10], err)
		}
	}
	owner.must(200, "POST", base+"/webhooks/"+id+"/rotate-secret", map[string]any{"expire_now": true})
	owner.must(200, "POST", base+"/webhooks/"+id+"/test", nil)
	last = rc.count() - 1
	if err := wh.Verify(rc.bodies[last], rc.got[last]); err == nil {
		t.Fatal("expired secret still verifies")
	}

	// Turning it off stops deliveries; deleting removes it.
	if got := owner.must(200, "PATCH", base+"/webhooks/"+id, map[string]any{"enabled": false}); got["enabled"] != false || got["disabled_reason"] == nil {
		t.Fatalf("disable %v", got)
	}
	if got := owner.must(200, "PATCH", base+"/webhooks/"+id, map[string]any{"enabled": true}); got["disabled_reason"] != nil {
		t.Fatalf("enable %v", got)
	}
	owner.must(204, "DELETE", base+"/webhooks/"+id, nil)
	owner.must(404, "GET", base+"/webhooks/"+id, nil)
}

func TestWebhooksAccess(t *testing.T) {
	_, owner, ws, _ := webhookEnv(t)
	base := "/v1/workspaces/" + ws
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "reader", "scopes": []string{"webhooks.read"}})["token"].(string)
	reader := &client{t: t, h: owner.h, bearer: tok}
	reader.must(200, "GET", base+"/webhooks", nil)
	reader.must(403, "POST", base+"/webhooks", map[string]any{"url": "https://example.com/h"})
	hook := owner.must(201, "POST", base+"/webhooks", map[string]any{"url": "https://example.com/h"})
	reader.must(403, "GET", base+"/webhooks/"+hook["id"].(string)+"/secret", nil)
	if hook["events"].([]any)[0] != "*" {
		t.Fatalf("default events %v", hook["events"])
	}
}

func TestWebhookURLChecks(t *testing.T) {
	for _, tc := range []struct {
		url     string
		private bool
		ok      bool
	}{
		{"https://hooks.example.com/x", false, true},
		{"http://hooks.example.com/x", false, false},
		{"https://127.0.0.1/x", false, false},
		{"https://10.0.0.5/x", false, false},
		{"https://[::1]/x", false, false},
		{"https://169.254.169.254/latest/meta-data", false, false},
		{"https://localhost/x", false, false},
		{"https://db.internal/x", false, false},
		{"https://user:pass@hooks.example.com/x", false, false},
		{"ftp://hooks.example.com/x", false, false},
		{"http://10.0.0.5:8080/x", true, true},
	} {
		if err := webhooks.CheckURL(tc.url, tc.private); (err == nil) != tc.ok {
			t.Errorf("CheckURL(%q, private=%v) = %v", tc.url, tc.private, err)
		}
	}
}

func jsonNum(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// The document lists every event the catalog delivers, both in the
// WebhookEvent type enum and as an OpenAPI webhook, so generated clients
// know them.
func TestWebhookCatalogMatchesTheDocument(t *testing.T) {
	var doc struct {
		Webhooks   map[string]any `yaml:"webhooks"`
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Enum []string `yaml:"enum"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(spec.OpenAPI, &doc); err != nil {
		t.Fatal(err)
	}
	enum := doc.Components.Schemas["WebhookEvent"].Properties["type"].Enum
	var want []string
	for _, e := range webhooks.Catalog {
		want = append(want, e.Type)
		if _, ok := doc.Webhooks[e.Type]; !ok {
			t.Errorf("webhooks: has no %s", e.Type)
		}
	}
	if !slices.Equal(enum, want) || len(doc.Webhooks) != len(want) {
		t.Errorf("WebhookEvent.type enum %v\nwant (webhooks.Catalog) %v", enum, want)
	}
}

func TestWhoamiTellsATokenItsWorkspace(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	if me := owner.must(200, "GET", "/v1/whoami", nil); me["token"] != nil || me["user"].(map[string]any)["email"] != "jack@example.com" {
		t.Fatalf("session whoami %v", me)
	}
	secret := owner.must(201, "POST", "/v1/workspaces/"+ws+"/api-tokens", map[string]any{"name": "cli", "scopes": []string{"leads.read"}})["token"].(string)
	bot := &client{t: t, h: h, bearer: secret}
	tok := bot.must(200, "GET", "/v1/whoami", nil)["token"].(map[string]any)
	if tok["workspace_id"] != ws || tok["name"] != "cli" || tok["kind"] != "workspace" {
		t.Fatalf("token whoami %v", tok)
	}
	if w := tok["workspace"].(map[string]any); w["slug"] != "acme-outbound" || w["name"] != "Acme Outbound" {
		t.Fatalf("token workspace %v", w)
	}

	// A manage scope includes reading its area, and nothing else.
	writer := &client{t: t, h: h, bearer: owner.must(201, "POST", "/v1/workspaces/"+ws+"/api-tokens", map[string]any{"name": "writer", "scopes": []string{"leads.manage"}})["token"].(string)}
	writer.must(201, "POST", "/v1/workspaces/"+ws+"/leads", map[string]any{"email": "ana@getacme-co.example"})
	writer.must(200, "GET", "/v1/workspaces/"+ws+"/leads", nil)
	writer.must(403, "GET", "/v1/workspaces/"+ws+"/mailboxes", nil)
	(&client{t: t, h: h}).must(401, "GET", "/v1/whoami", nil)
}

func TestEventsFeedPollsForward(t *testing.T) {
	_, owner, ws, _ := webhookEnv(t)
	base := "/v1/workspaces/" + ws
	first := owner.must(200, "GET", base+"/events", nil)
	cursor := first["next_after"].(string)
	owner.must(201, "POST", base+"/leads", map[string]any{"email": "ana@getacme-co.example"})
	owner.must(201, "POST", base+"/sequences", map[string]any{"name": "Q4"})

	page := owner.must(200, "GET", base+"/events?types=lead.*&after="+cursor, nil)
	data := page["data"].([]any)
	if len(data) != 1 || data[0].(map[string]any)["type"] != "lead.created" || page["next_after"] == cursor {
		t.Fatalf("feed %v", page)
	}
	// The lead's new person record is an event too.
	all := owner.must(200, "GET", base+"/events?after="+cursor, nil)["data"].([]any)
	if len(all) != 3 || all[0].(map[string]any)["type"] != "record.created" {
		t.Fatalf("all types: %v", all)
	}
	again := owner.must(200, "GET", base+"/events?types=lead.*&after="+page["next_after"].(string), nil)
	if len(again["data"].([]any)) != 0 || again["next_after"] != page["next_after"] {
		t.Fatalf("nothing new keeps the cursor: %v", again)
	}
	owner.must(400, "GET", base+"/events?types=nope", nil)

	latest := owner.must(200, "GET", base+"/events?after=latest", nil)
	if len(latest["data"].([]any)) != 0 || latest["next_after"] != all[2].(map[string]any)["id"] {
		t.Fatalf("latest %v", latest)
	}
}
