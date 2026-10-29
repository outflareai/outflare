package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/journeys"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/webhooks"
)

// received is one call the fake receiver got.
type received struct {
	path, query string
	header      http.Header
	body        []byte
}

func TestJourneyWebhooks(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// The receiver: ok answers JSON, flaky fails once, bad refuses.
	var mu sync.Mutex
	var calls []received
	flakyTries := 0
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, received{r.URL.Path, r.URL.RawQuery, r.Header.Clone(), body})
		mu.Unlock()
		switch {
		case strings.HasPrefix(r.URL.Path, "/ok/"):
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"customer_id": "cus_123", "stage": "customer"}`))
		case r.URL.Path == "/flaky":
			mu.Lock()
			flakyTries++
			n := flakyTries
			mu.Unlock()
			if n == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Write([]byte("fine"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer recv.Close()

	var posts []map[string]any
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth.test":
			w.Write([]byte(`{"ok": true, "team": "Acme", "team_id": "T1"}`))
		case "/chat.postMessage":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			posts = append(posts, body)
			mu.Unlock()
			w.Write([]byte(`{"ok": true}`))
		}
	}))
	defer slack.Close()
	e.integrations.Client.BaseURLs["slack"] = slack.URL
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "slack", "credentials": map[string]string{"api_key": "xoxb-test-not-a-real-token"}})

	// The step picker lists both kinds; Slack needs its connection.
	kinds := map[string]map[string]any{}
	for _, k := range owner.must(200, "GET", base+"/journeys/step-kinds", nil)["data"].([]any) {
		kinds[k.(map[string]any)["kind"].(string)] = k.(map[string]any)
	}
	if kinds["webhook"] == nil || len(kinds["slack"]["requires"].([]any)) != 1 {
		t.Fatalf("step kinds: %v %v", kinds["webhook"], kinds["slack"])
	}

	ana, err := customers.EnsurePerson(ctx, e.pool, ws, "email", "ana@acme.example", true, "")
	if err != nil {
		t.Fatal(err)
	}

	// Bad steps are refused.
	for _, bad := range []map[string]any{
		{"id": "a", "kind": "webhook", "url": "ftp://example.com"},
		{"id": "a", "kind": "webhook", "url": "https://{{event.host}}/x"},
		{"id": "a", "kind": "webhook", "url": "https://example.com", "headers": map[string]string{"Content-Length": "1"}},
		{"id": "a", "kind": "webhook", "url": "https://example.com", "auth": map[string]any{"type": "header"}},
		{"id": "a", "kind": "webhook", "url": "https://example.com", "method": "GET", "body": map[string]any{"a": 1}},
		{"id": "a", "kind": "webhook", "url": "https://example.com", "save_as": "Bad Name"},
		{"id": "a", "kind": "slack", "text": "hi"},
		{"id": "a", "kind": "branch", "conditions": []any{map[string]any{"var": "a b", "op": "exists"}}, "then": "", "else": ""},
	} {
		owner.must(400, "POST", base+"/journeys", map[string]any{"name": "Bad", "steps": []any{bad}})
	}

	steps := []any{
		map[string]any{"id": "hook", "kind": "webhook", "url": recv.URL + "/ok/{{person.id}}?plan={{event.plan}}",
			"headers": map[string]string{"X-Plan": "{{event.plan}}"},
			"body":    map[string]any{"email": "{{person.email}}", "seats": "{{event.seats}}", "note": "plan {{event.plan}}"},
			"auth":    map[string]any{"type": "header", "name": "Authorization"}, "save_as": "billing", "next": "check"},
		map[string]any{"id": "check", "kind": "branch", "conditions": []any{map[string]any{"var": "billing.body.customer_id", "op": "eq", "value": "cus_123"}},
			"then": "stage", "else": "out"},
		map[string]any{"id": "stage", "kind": "set_field", "field": "lifecycle_stage", "value": "{{vars.billing.body.stage}}", "next": "flaky"},
		map[string]any{"id": "flaky", "kind": "webhook", "method": "PUT", "url": recv.URL + "/flaky", "auth": map[string]any{"type": "query", "name": "api_key"}, "next": "bad"},
		map[string]any{"id": "bad", "kind": "webhook", "url": recv.URL + "/bad", "on_error": "continue", "next": "tell"},
		map[string]any{"id": "tell", "kind": "slack", "channel": "C1", "title": "Paid trial", "text": "{{person.email}} is {{vars.billing.body.customer_id}}"},
		map[string]any{"id": "out", "kind": "exit"},
	}
	j := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Trial", "trigger": map[string]any{"kind": "event", "event": "trial.started"}, "steps": steps})
	jid := j["id"].(string)

	// Keys: none set, so it can't go live; only steps that send one take
	// one, and the key is never shown.
	owner.must(400, "POST", base+"/journeys/"+jid+"/activate", nil)
	owner.must(400, "PUT", base+"/journeys/"+jid+"/step-secrets/bad", map[string]any{"value": "x"})
	owner.must(404, "PUT", base+"/journeys/"+jid+"/step-secrets/nope", map[string]any{"value": "x"})
	owner.must(400, "PUT", base+"/journeys/"+jid+"/step-secrets/hook", map[string]any{"value": "a\nb"})
	owner.must(200, "PUT", base+"/journeys/"+jid+"/step-secrets/hook", map[string]any{"value": "Bearer sk-test-not-a-real-key"})
	owner.must(200, "PUT", base+"/journeys/"+jid+"/step-secrets/flaky", map[string]any{"value": "key-test-not-real"})
	list := owner.must(200, "GET", base+"/journeys/"+jid+"/step-secrets", nil)["data"].([]any)
	if len(list) != 2 || strings.Contains(mustJSON(list), "sk-test") || strings.Contains(mustJSON(owner.must(200, "GET", base+"/journeys/"+jid, nil)), "sk-test") {
		t.Fatalf("keys shown: %v", list)
	}
	owner.must(200, "POST", base+"/journeys/"+jid+"/activate", nil)

	// A test run never calls out: call steps say so and move on, and no
	// call job is left behind.
	tr := owner.must(200, "POST", base+"/journeys/"+jid+"/test", map[string]any{"record_id": ana})
	tested := 0
	for _, st := range tr["steps"].([]any) {
		st := st.(map[string]any)
		if st["kind"] == "webhook" || st["kind"] == "slack" {
			tested++
			if st["outcome"] != "not_called" {
				t.Fatalf("test step = %v", st)
			}
		}
	}
	var callJobs int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'journey.call'`).Scan(&callJobs); err != nil {
		t.Fatal(err)
	}
	if tested == 0 || callJobs != 0 {
		t.Fatalf("test calls = %d, call jobs = %d: %v", tested, callJobs, tr)
	}

	// The signing secret: made on first sight, the same until rotated.
	sec := owner.must(200, "GET", base+"/journeys/signing-secret", nil)
	secret := sec["secret"].(string)
	if !strings.HasPrefix(secret, "whsec_") || owner.must(200, "GET", base+"/journeys/signing-secret", nil)["secret"] != secret {
		t.Fatalf("signing secret: %v", sec)
	}

	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	svc := &journeys.Service{Pool: e.pool, Log: log, Fields: e.srv, Store: e.integrations, Box: e.box, AllowPrivate: true, Client: recv.Client()}
	svc.Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	defer func() { cancel(); <-done }()

	if _, err := customers.Record(ctx, e.pool, ws, customers.Event{RecordID: ana, Name: "trial.started", Properties: map[string]any{"plan": "pro plus", "seats": 3}}); err != nil {
		t.Fatal(err)
	}
	var run map[string]any
	deadline := time.Now().Add(60 * time.Second)
	for {
		for _, r := range owner.must(200, "GET", base+"/journeys/"+jid+"/runs?record="+ana, nil)["data"].([]any) {
			run = r.(map[string]any)
		}
		if run != nil && run["status"] != "active" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the run didn't finish: %v", run)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if run["status"] != "completed" {
		t.Fatalf("run = %v", run)
	}
	run = owner.must(200, "GET", base+"/journeys/"+jid+"/runs/"+run["id"].(string), nil)
	billing := run["vars"].(map[string]any)["billing"].(map[string]any)
	if billing["status"] != float64(200) || billing["body"].(map[string]any)["customer_id"] != "cus_123" {
		t.Fatalf("vars = %v", run["vars"])
	}
	outcomes := map[string]string{}
	for _, h := range run["history"].([]any) {
		m := h.(map[string]any)
		outcomes[m["step"].(string)] = m["outcome"].(string) // the last one per step
	}
	if outcomes["hook"] != "done" || outcomes["check"] != "yes" || outcomes["stage"] != "done" || outcomes["flaky"] != "done" || outcomes["bad"] != "failed" || outcomes["tell"] != "done" {
		t.Fatalf("outcomes = %v", outcomes)
	}
	if st := owner.must(200, "GET", base+"/records/"+ana, nil)["attributes"].(map[string]any)["lifecycle_stage"]; st != "customer" {
		t.Errorf("lifecycle_stage = %v, want it from vars", st)
	}

	// What the receiver got: filled in, keyed, signed and idempotent.
	mu.Lock()
	defer mu.Unlock()
	var hook *received
	var flaky []received
	for i := range calls {
		switch c := calls[i]; {
		case strings.HasPrefix(c.path, "/ok/"):
			hook = &calls[i]
		case c.path == "/flaky":
			flaky = append(flaky, c)
		}
	}
	if hook == nil || hook.path != "/ok/"+ana || hook.query != "plan=pro+plus" || hook.header.Get("X-Plan") != "pro plus" ||
		hook.header.Get("Authorization") != "Bearer sk-test-not-a-real-key" {
		t.Fatalf("hook call: %+v", hook)
	}
	var body map[string]any
	if err := json.Unmarshal(hook.body, &body); err != nil || body["email"] != "ana@acme.example" || body["seats"] != float64(3) || body["note"] != "plan pro plus" {
		t.Fatalf("body = %s", hook.body)
	}
	id, ts := hook.header.Get("webhook-id"), hook.header.Get("webhook-timestamp")
	unix, _ := strconv.ParseInt(ts, 10, 64)
	want, _ := webhooks.Sign(secret, id, time.Unix(unix, 0), hook.body)
	if id == "" || hook.header.Get("Idempotency-Key") != id || hook.header.Get("webhook-signature") != want {
		t.Fatalf("signature: id %q, %q, want %q", id, hook.header.Get("webhook-signature"), want)
	}
	if len(flaky) != 2 || flaky[0].header.Get("webhook-id") != flaky[1].header.Get("webhook-id") || flaky[1].query != "api_key=key-test-not-real" {
		t.Fatalf("flaky calls: %+v", flaky)
	}
	if len(posts) != 1 || !strings.Contains(mustJSON(posts[0]), "ana@acme.example is cus_123") || posts[0]["channel"] != "C1" {
		t.Fatalf("slack: %v", posts)
	}

	// Rotating: the old secret signs a day longer, or not at all.
	rot := owner.must(200, "POST", base+"/journeys/signing-secret/rotate", nil)
	if rot["secret"] == secret || rot["previous_expires_at"] == nil {
		t.Fatalf("rotated: %v", rot)
	}
	if owner.must(200, "POST", base+"/journeys/signing-secret/rotate", map[string]any{"expire_now": true})["previous_expires_at"] != nil {
		t.Fatal("expire_now kept the old secret")
	}
	owner.must(204, "DELETE", base+"/journeys/"+jid+"/step-secrets/hook", nil)
	owner.must(404, "DELETE", base+"/journeys/"+jid+"/step-secrets/hook", nil)

	var called, failedCalls int
	names := map[string]bool{}
	for _, ev := range got() {
		names[ev.Name] = true
		if ev.Name == "journey_step.called" {
			if ev.Outcome == track.Success {
				called++
			} else {
				failedCalls++
			}
		}
	}
	if called != 3 || failedCalls != 2 {
		t.Errorf("journey_step.called: %d done, %d failed (the retried try and the refused one)", called, failedCalls)
	}
	for _, want := range []string{"journey.step_secret_set", "journey_signing_secret.rotated", "journey_signing_secret.revealed"} {
		if !names[want] {
			t.Errorf("no %s event", want)
		}
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
