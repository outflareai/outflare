package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/catalog"
	"github.com/outflareai/outflare/engine/internal/inbound"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// fakeGeonodeHooks is Geonode's webhook API: one subscription per event
// type, each with its own signing secret.
type fakeGeonodeHooks struct {
	mu      sync.Mutex
	hooks   map[string]fakeHook // id → subscription
	deleted []string
	next    int
	down    bool // answer webhook requests with a 503
}

type fakeHook struct{ url, event, secret string }

func (f *fakeGeonodeHooks) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("X-Api-Key") != "gn-key" {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"code": "UNAUTHORIZED", "message": "bad key"}`)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/v1/usage/concurrency":
		io.WriteString(w, `{"work_concurrency_in_use": 0, "work_concurrency_limit": 5}`)
	case r.URL.Path == "/v1/webhooks" && r.Method == "POST":
		if f.down {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, `{"code": "SERVICE_UNAVAILABLE", "message": "try later", "retryable": true}`)
			return
		}
		var req struct {
			URL       string `json:"url"`
			EventType string `json:"event_type"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		f.next++
		id := "00000000-0000-4000-8000-00000000000" + string(rune('0'+f.next))
		secret := "whsec_test_" + req.EventType
		f.hooks[id] = fakeHook{req.URL, req.EventType, secret}
		json.NewEncoder(w).Encode(map[string]any{"id": id, "url": req.URL, "event_type": req.EventType, "secret": secret, "is_active": true,
			"description": "Outflare", "created_at": "2026-10-27T00:00:00Z", "updated_at": "2026-10-27T00:00:00Z"})
	case strings.HasPrefix(r.URL.Path, "/v1/webhooks/") && r.Method == "DELETE":
		id := strings.TrimPrefix(r.URL.Path, "/v1/webhooks/")
		delete(f.hooks, id)
		f.deleted = append(f.deleted, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"code": "NOT_FOUND", "message": "no route"}`)
	}
}

// subscribed returns the address and secret for one event type.
func (f *fakeGeonodeHooks) subscribed(event string) (url, secret string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, h := range f.hooks {
		if h.event == event {
			url, secret = h.url, h.secret
		}
	}
	return url, secret, len(f.hooks)
}

func TestConnectionHooksGeonode(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	fake := &fakeGeonodeHooks{hooks: map[string]fakeHook{}}
	gn := httptest.NewServer(fake)
	t.Cleanup(gn.Close)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := &connectors.Store{Pool: e.pool, Box: e.box, Registry: catalog.New(),
		Client: &connectors.Client{BaseURLs: map[string]string{"geonode": gn.URL}}}
	live := &inbound.Service{Pool: e.pool, Box: e.box, Store: store, PublicURL: "https://outflare.example.com", Log: log}
	store.OnConnect = append(store.OnConnect, live.Connected)
	store.OnDisconnect = append(store.OnDisconnect, live.Disconnect)
	h := handler(t, api.NewServer(e.pool, log, api.Options{Mailboxes: e.mail, Sending: e.send, Inbox: e.sync, OAuth: e.oauth,
		Box: e.box, Integrations: store, Inbound: live}))

	// Batches are used; crawls fail once, then work.
	var mu sync.Mutex
	handled := map[string]int{}
	live.On("geonode", func(_ context.Context, ev inbound.Event) error {
		mu.Lock()
		defer mu.Unlock()
		handled[ev.Type]++
		switch {
		case ev.Type == "extract_completed":
			return inbound.ErrIgnore
		case ev.Type == "crawl_completed" && handled[ev.Type] == 1:
			return jobs.ErrPermanent
		}
		if !strings.Contains(string(ev.Payload), `"job_id"`) {
			return errors.New("no payload")
		}
		return nil
	})

	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	live.Register(q)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { q.Run(ctx, 2); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })

	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws
	integ := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "geonode", "credentials": map[string]string{"api_key": "gn-key"}})
	id := integ["id"].(string)
	hookPath := base + "/integrations/" + id + "/hook"

	// Connecting registered the address for all three event types.
	view := waitFor(t, owner, hookPath, func(m map[string]any) bool { return m["status"] == "waiting" })
	addr, _ := view["url"].(string)
	if !strings.HasPrefix(addr, "https://outflare.example.com/v1/hooks/ofh_") || view["setup"] != "auto" || view["signed"] != true {
		t.Fatalf("hook: %v", view)
	}
	url, secret, n := fake.subscribed("batch_completed")
	if url != addr || n != 3 {
		t.Fatalf("registered %q (%d), want %q", url, n, addr)
	}
	path := strings.TrimPrefix(addr, "https://outflare.example.com")

	post := func(p, sig string, body string) (int, map[string]any) {
		req := httptest.NewRequest("POST", p, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Webhook-Signature", sig)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	batch := `{"event":"batch_completed","webhook_id":"w1","delivery_id":"d1","timestamp":"2026-10-27T12:00:00Z","data":{"job_id":"job-1","status":"completed","total_urls":40,"completed_urls":39,"failed_urls":1,"results":[{"url":"https://example.com","html":"<p>page text</p>"}]}}`

	// Wrong signatures and unknown addresses are refused.
	if code, _ := post(path, sign("wrong", []byte(batch)), batch); code != 401 {
		t.Fatalf("bad signature: %d", code)
	}
	if code, _ := post("/v1/hooks/ofh_not_a_real_address", sign(secret, []byte(batch)), batch); code != 404 {
		t.Fatalf("unknown address: %d", code)
	}
	// Signed with the batch subscription's secret: accepted, then a retry
	// of the same news is ignored.
	if code, out := post(path, sign(secret, []byte(batch)), batch); code != 200 || out["received"] != 1.0 {
		t.Fatalf("batch: %d %v", code, out)
	}
	retry := strings.Replace(batch, `"d1"`, `"d2"`, 1)
	if code, out := post(path, "sha256="+sign(secret, []byte(retry)), retry); code != 200 || out["duplicates"] != 1.0 {
		t.Fatalf("retry: %d %v", code, out)
	}
	_, crawlSecret, _ := fake.subscribed("crawl_completed")
	crawlBody := `{"event":"crawl_completed","delivery_id":"d3","data":{"job_id":"job-2","status":"completed","pages_crawled":12}}`
	if code, _ := post(path, sign(crawlSecret, []byte(crawlBody)), crawlBody); code != 200 {
		t.Fatalf("crawl: %d", code)
	}
	_, extractSecret, _ := fake.subscribed("extract_completed")
	extractBody := `{"event":"extract_completed","delivery_id":"d4","data":{"job_id":"job-3","status":"completed"}}`
	if code, _ := post(path, sign(extractSecret, []byte(extractBody)), extractBody); code != 200 {
		t.Fatalf("extract: %d", code)
	}

	// The log: batch handled with its counts (never the page text), the
	// extract ignored, the crawl failed.
	statusOf := func(typ string) map[string]any {
		for _, ev := range owner.must(200, "GET", base+"/hook-events?integration_id="+id, nil)["data"].([]any) {
			if m := ev.(map[string]any); m["type"] == typ {
				return m
			}
		}
		return nil
	}
	waitUntil(t, func() bool {
		b, c, x := statusOf("batch_completed"), statusOf("crawl_completed"), statusOf("extract_completed")
		return b != nil && b["status"] == "handled" && c != nil && c["status"] == "failed" && x != nil && x["status"] == "ignored"
	})
	b := statusOf("batch_completed")
	sum := b["summary"].(map[string]any)
	if sum["job_id"] != "job-1" || sum["completed_urls"] != 39.0 || b["duplicates"] != 1.0 || b["replayable"] != true {
		t.Fatalf("batch event: %v", b)
	}
	if raw, _ := json.Marshal(b); strings.Contains(string(raw), "page text") {
		t.Fatalf("the log shows page text: %s", raw)
	}
	waitFor(t, owner, hookPath, func(m map[string]any) bool { return m["status"] == "receiving" && m["last_event_at"] != nil })

	// Replaying the failed crawl handles it.
	crawl := statusOf("crawl_completed")
	if crawl["error_code"] != "internal" {
		t.Fatalf("crawl event: %v", crawl)
	}
	owner.must(202, "POST", base+"/hook-events/"+crawl["id"].(string)+"/replay", nil)
	waitUntil(t, func() bool { return statusOf("crawl_completed")["status"] == "handled" })
	failedOnly := owner.must(200, "GET", base+"/hook-events?status=failed", nil)["data"].([]any)
	if len(failedOnly) != 0 {
		t.Fatalf("failed after replay: %v", failedOnly)
	}

	// A new address: the old one stops, and Geonode gets the new one.
	owner.must(200, "POST", hookPath+"/rotate", nil)
	view = waitFor(t, owner, hookPath, func(m map[string]any) bool { return m["status"] == "receiving" })
	if view["url"] == addr || view["rotated_at"] == nil {
		t.Fatalf("rotated: %v", view)
	}
	if code, _ := post(path, sign(secret, []byte(batch)), batch); code != 404 {
		t.Fatalf("old address: %d", code)
	}
	waitUntil(t, func() bool { u, _, n := fake.subscribed("batch_completed"); return u == view["url"] && n == 3 })

	// Turned off: the address stops and Geonode's subscriptions go.
	owner.must(204, "DELETE", hookPath, nil)
	if v := owner.must(200, "GET", hookPath, nil); v["status"] != "off" || v["url"] != nil {
		t.Fatalf("off: %v", v)
	}
	waitUntil(t, func() bool { _, _, n := fake.subscribed("batch_completed"); return n == 0 })
	if code, _ := owner.do("DELETE", hookPath, nil); code != 409 {
		t.Fatalf("off twice: %d", code)
	}

	// On again by hand, then disconnecting removes its subscriptions.
	owner.must(200, "POST", hookPath, nil)
	waitFor(t, owner, hookPath, func(m map[string]any) bool { return m["status"] == "waiting" })
	owner.must(204, "DELETE", base+"/integrations/"+id, nil)
	if _, _, n := fake.subscribed("batch_completed"); n != 0 {
		t.Fatalf("subscriptions left after disconnect: %d", n)
	}

	// Events: received, the refused signature, and each change audited.
	if got := named(events(), "connection_hook.received"); len(got) < 5 {
		t.Fatalf("received events: %d", len(got))
	}
	bad := false
	for _, ev := range named(events(), "connection_hook.received") {
		if ev.ErrorCode == "bad_signature" && ev.Outcome == track.Failure {
			bad = true
		}
	}
	if !bad {
		t.Fatal("no event for the bad signature")
	}
	for _, name := range []string{"connection_hook.registered", "connection_hook.event_handled", "connection_hook.event_failed", "connection_hook.rotated", "connection_hook.removed", "connection_hook.set_up", "connection_hook.event_replayed"} {
		if len(named(events(), name)) == 0 {
			t.Fatalf("no %s event", name)
		}
	}
}

func TestConnectionHooksRegisterFails(t *testing.T) {
	e := freshEnv(t)
	fake := &fakeGeonodeHooks{hooks: map[string]fakeHook{}, down: true}
	gn := httptest.NewServer(fake)
	t.Cleanup(gn.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := &connectors.Store{Pool: e.pool, Box: e.box, Registry: catalog.New(),
		Client: &connectors.Client{BaseURLs: map[string]string{"geonode": gn.URL}}}
	live := &inbound.Service{Pool: e.pool, Box: e.box, Store: store, PublicURL: "https://outflare.example.com", Log: log}
	store.OnConnect = append(store.OnConnect, live.Connected)
	h := handler(t, api.NewServer(e.pool, log, api.Options{Mailboxes: e.mail, Sending: e.send, Inbox: e.sync, OAuth: e.oauth,
		Box: e.box, Integrations: store, Inbound: live}))
	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	live.Register(q)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { q.Run(ctx, 2); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })

	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws
	id := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "geonode", "credentials": map[string]string{"api_key": "gn-key"}})["id"].(string)
	// Geonode stays down: after its retries the connection says why.
	if _, err := e.pool.Exec(ctx, `UPDATE jobs SET run_at = now() WHERE kind = 'hooks.register'`); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		_, _ = e.pool.Exec(ctx, `UPDATE jobs SET run_at = now() WHERE kind = 'hooks.register' AND status = 'queued'`)
		code, v := owner.do("GET", base+"/integrations/"+id+"/hook", nil)
		return code == 200 && v["status"] == "failed" && v["last_error"] != nil
	})
	// Back up: turning it on again registers.
	fake.mu.Lock()
	fake.down = false
	fake.mu.Unlock()
	owner.must(200, "POST", base+"/integrations/"+id+"/hook", nil)
	waitFor(t, owner, base+"/integrations/"+id+"/hook", func(m map[string]any) bool { return m["status"] == "waiting" })

}

func TestConnectionHooksUnreachable(t *testing.T) {
	e := freshEnv(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	live := &inbound.Service{Pool: e.pool, Box: e.box, Store: e.integrations, PublicURL: "http://localhost:8080", Log: log}
	e.integrations.OnConnect = append(e.integrations.OnConnect, live.Connected)
	h := handler(t, api.NewServer(e.pool, log, api.Options{Mailboxes: e.mail, Sending: e.send, Inbox: e.sync, OAuth: e.oauth,
		Box: e.box, Integrations: e.integrations, Inbound: live}))
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws
	id := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "geonode", "credentials": map[string]string{"api_key": "gn-key"}})["id"].(string)
	v := owner.must(200, "GET", base+"/integrations/"+id+"/hook", nil)
	if v["status"] != "off" || v["reachable"] != false || v["supports"].(map[string]any)["auto"] != true {
		t.Fatalf("hook: %v", v)
	}
	if code, out := owner.do("POST", base+"/integrations/"+id+"/hook", nil); code != 409 {
		t.Fatalf("set up unreachable: %d %v", code, out)
	}
	// A connection that can't send webhooks has no live updates.
	other := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})
	if code, _ := owner.do("GET", base+"/integrations/"+other["id"].(string)+"/hook", nil); code != 404 {
		t.Fatalf("plausible hook: %d", code)
	}
}
