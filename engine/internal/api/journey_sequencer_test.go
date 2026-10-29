package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/journeys"
	"github.com/outflareai/outflare/engine/internal/track"
)

// A push_to_sequencer step adds the person to the Smartlead campaign its
// sequence is linked to, through the workspace's own connection: once,
// with their company and the campaign's variables filled in, never for
// someone suppressed, and not for a sequence that sends through Outflare.
func TestJourneyPushToSequencer(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	var mu sync.Mutex
	var pushed []map[string]any
	in := map[string]bool{}
	sl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") != "sl-test-not-a-real-key" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/campaigns":
			w.Write([]byte(`[{"id": 42, "name": "Trial follow-up", "status": "ACTIVE"}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/campaigns/42/leads":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			lead := body["lead_list"].([]any)[0].(map[string]any)
			mu.Lock()
			defer mu.Unlock()
			pushed = append(pushed, body)
			if in[lead["email"].(string)] {
				w.Write([]byte(`{"ok": true, "upload_count": 0, "already_added_to_campaign": 1}`))
				return
			}
			in[lead["email"].(string)] = true
			w.Write([]byte(`{"ok": true, "upload_count": 1}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer sl.Close()
	e.integrations.Client.BaseURLs["smartlead"] = sl.URL
	integ := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "smartlead", "credentials": map[string]string{"api_key": "sl-test-not-a-real-key"}})["id"].(string)

	steps := []map[string]any{{"subject": "Hello", "body": "Hi"}}
	linked := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "In Smartlead", "steps": steps})["id"].(string)
	own := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "In Outflare", "steps": steps})["id"].(string)
	if _, err := e.pool.Exec(ctx, `INSERT INTO campaign_links (workspace_id, sequence_id, integration_id, external_id, external_name)
		VALUES ($1, $2, $3, '42', 'Trial follow-up')`, ws, linked, integ); err != nil {
		t.Fatal(err)
	}

	// The step picker lists it, needing a sequencer.
	for _, k := range owner.must(200, "GET", base+"/journeys/step-kinds", nil)["data"].([]any) {
		if k := k.(map[string]any); k["kind"] == "push_to_sequencer" && k["requires"].([]any)[0].(map[string]any)["connection"] != "sequencer" {
			t.Fatalf("kind: %v", k)
		}
	}
	tooMany := map[string]string{}
	for i := range 21 {
		tooMany[string(rune('a'+i))] = "x"
	}
	for _, bad := range []map[string]any{
		{"id": "a", "kind": "push_to_sequencer"},
		{"id": "a", "kind": "push_to_sequencer", "sequence_id": linked, "variables": map[string]string{"bad{name}": "x"}},
		{"id": "a", "kind": "push_to_sequencer", "sequence_id": linked, "variables": tooMany},
	} {
		owner.must(400, "POST", base+"/journeys", map[string]any{"name": "Bad", "steps": []any{bad}})
	}

	j := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Trial", "trigger": map[string]any{"kind": "event", "event": "trial.started"},
		"steps": []any{
			map[string]any{"id": "push", "kind": "push_to_sequencer", "sequence_id": linked, "variables": map[string]string{"plan": "{{event.plan}}"}, "next": "again"},
			map[string]any{"id": "again", "kind": "push_to_sequencer", "sequence_id": linked, "next": "own"},
			map[string]any{"id": "own", "kind": "push_to_sequencer", "sequence_id": own, "on_error": "continue"},
		}})
	jid := j["id"].(string)
	owner.must(200, "POST", base+"/journeys/"+jid+"/activate", nil)

	ana, err := customers.EnsurePerson(ctx, e.pool, ws, "email", "ana@acme.example", true, "")
	if err != nil {
		t.Fatal(err)
	}
	acct := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme"})["id"].(string)
	owner.must(200, "PATCH", base+"/records/"+ana, map[string]any{"attributes": map[string]any{"first_name": "Ana", "companies": []string{acct}}})
	bo, err := customers.EnsurePerson(ctx, e.pool, ws, "email", "bo@optout.example", true, "")
	if err != nil {
		t.Fatal(err)
	}
	owner.must(200, "POST", base+"/suppressions", map[string]any{"values": []string{"bo@optout.example"}, "reason": "manual"})
	// The sequence leaves out anyone tagged hands_off, wherever it sends.
	owner.must(200, "PATCH", base+"/sequences/"+linked, map[string]any{"exclude": map[string]any{"lists": []string{}, "tags": []string{"hands_off"}}})
	cy, err := customers.EnsurePerson(ctx, e.pool, ws, "email", "cy@acme.example", true, "")
	if err != nil {
		t.Fatal(err)
	}
	owner.must(200, "PATCH", base+"/records/"+cy, map[string]any{"attributes": map[string]any{"tags": []string{"hands_off"}}})

	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	(&journeys.Service{Pool: e.pool, Log: log, Fields: e.srv, Store: e.integrations, Box: e.box}).Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	defer func() { cancel(); <-done }()

	finished := func(record string) map[string]string {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for {
			var run map[string]any
			for _, r := range owner.must(200, "GET", base+"/journeys/"+jid+"/runs?record="+record, nil)["data"].([]any) {
				run = r.(map[string]any)
			}
			if run != nil && run["status"] != "active" {
				run = owner.must(200, "GET", base+"/journeys/"+jid+"/runs/"+run["id"].(string), nil)
				out := map[string]string{"status": run["status"].(string)}
				for _, h := range run["history"].([]any) {
					m := h.(map[string]any)
					d, _ := m["detail"].(string)
					out[m["step"].(string)] = m["outcome"].(string) + ": " + d
				}
				return out
			}
			if time.Now().After(deadline) {
				t.Fatalf("the run didn't finish: %v", run)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	for _, who := range []string{ana, bo, cy} {
		if _, err := customers.Record(ctx, e.pool, ws, customers.Event{RecordID: who, Name: "trial.started", Properties: map[string]any{"plan": "pro"}}); err != nil {
			t.Fatal(err)
		}
	}
	out := finished(ana)
	if out["status"] != "completed" || out["push"] != "done: added to Trial follow-up" || out["again"] != "skipped: already in Trial follow-up" ||
		out["own"] != "failed: the sequence sends through Outflare, not a sequencer; use an enroll_in_sequence step" {
		t.Fatalf("ana: %v", out)
	}
	if out := finished(bo); out["push"] != "skipped: suppressed" || out["again"] != "skipped: suppressed" {
		t.Fatalf("bo: %v", out)
	}
	if out := finished(cy); out["push"] != "skipped: excluded: tagged hands_off" {
		t.Fatalf("cy: %v", out)
	}

	mu.Lock()
	if len(pushed) != 2 {
		t.Fatalf("pushed %v", pushed)
	}
	lead := pushed[0]["lead_list"].([]any)[0].(map[string]any)
	if lead["email"] != "ana@acme.example" || lead["first_name"] != "Ana" || lead["company_name"] != "Acme" || lead["custom_fields"].(map[string]any)["plan"] != "pro" {
		t.Fatalf("lead %v", lead)
	}
	mu.Unlock()

	var audited int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'sequence.lead_pushed' AND target_id = $2
		AND data->>'record_id' = $3 AND data->>'connector' = 'smartlead'`, ws, linked, ana).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("audited %d, %v", audited, err)
	}
	ev := got()
	var called, skipped int
	for _, x := range named(ev, "journey_step.called") {
		if x.Props["kind"] == "push_to_sequencer" {
			called++
			if x.Props["result"] == "skipped" {
				skipped++
			}
		}
	}
	if called < 4 || skipped < 3 || len(named(ev, "sequence.lead_pushed")) != 1 {
		t.Fatalf("events: called %d, skipped %d, pushed %d", called, skipped, len(named(ev, "sequence.lead_pushed")))
	}
}
