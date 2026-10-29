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

// A push_to_crm step creates the person in the workspace's own HubSpot, or
// updates the contact with their email, with properties filled in, and
// keeps the contact's id for later steps.
func TestJourneyPushToCRM(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	var mu sync.Mutex
	var upserts []map[string]any
	known := map[string]bool{}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/account-info/v3/details":
			w.Write([]byte(`{"portalId": 42}`))
		case "/crm/v3/objects/contacts/batch/upsert":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			in := body["inputs"].([]any)[0].(map[string]any)
			props := in["properties"].(map[string]any)
			if props["nope"] != nil {
				w.WriteHeader(400)
				w.Write([]byte(`{"status": "error", "message": "Property \"nope\" does not exist"}`))
				return
			}
			mu.Lock()
			defer mu.Unlock()
			upserts = append(upserts, props)
			email := in["id"].(string)
			isNew := !known[email]
			known[email] = true
			json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"id": "c-" + email, "new": isNew}}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer hs.Close()
	e.integrations.Client.BaseURLs["hubspot"] = hs.URL

	// Without a CRM the step is listed but can't be met.
	for _, k := range owner.must(200, "GET", base+"/journeys/step-kinds", nil)["data"].([]any) {
		if k := k.(map[string]any); k["kind"] == "push_to_crm" && k["requires"].([]any)[0].(map[string]any)["connection"] != "crm" {
			t.Fatalf("kind: %v", k)
		}
	}
	for _, bad := range []map[string]any{
		{"id": "a", "kind": "push_to_crm", "properties": map[string]string{"Bad Name": "x"}},
		{"id": "a", "kind": "push_to_crm", "connection_id": "nope"},
		{"id": "a", "kind": "push_to_crm", "save_as": "Bad"},
	} {
		owner.must(400, "POST", base+"/journeys", map[string]any{"name": "Bad", "steps": []any{bad}})
	}
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "hubspot", "credentials": map[string]string{"api_key": "pat-test-not-a-real-token"}})

	j := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Trial", "trigger": map[string]any{"kind": "event", "event": "trial.started"},
		"steps": []any{
			map[string]any{"id": "push", "kind": "push_to_crm", "properties": map[string]string{"lifecyclestage": "lead", "plan": "{{event.plan}}"},
				"save_as": "crm", "next": "again"},
			map[string]any{"id": "again", "kind": "push_to_crm", "next": "bad"},
			map[string]any{"id": "bad", "kind": "push_to_crm", "properties": map[string]string{"nope": "x"}, "on_error": "continue", "next": "check"},
			map[string]any{"id": "check", "kind": "branch", "conditions": []any{map[string]any{"var": "crm.created", "op": "eq", "value": true}},
				"then": "tag", "else": ""},
			map[string]any{"id": "tag", "kind": "add_tag", "tag": "in_crm"},
		}})
	jid := j["id"].(string)
	owner.must(200, "POST", base+"/journeys/"+jid+"/activate", nil)

	ana, err := customers.EnsurePerson(ctx, e.pool, ws, "email", "ana@acme.example", true, "")
	if err != nil {
		t.Fatal(err)
	}
	acct := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme"})["id"].(string)
	owner.must(200, "PATCH", base+"/records/"+ana, map[string]any{"attributes": map[string]any{"first_name": "Ana", "companies": []string{acct}}})
	bo, err := customers.EnsurePerson(ctx, e.pool, ws, "email", "bo@acme.example", false, "") // not verified
	if err != nil {
		t.Fatal(err)
	}

	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	(&journeys.Service{Pool: e.pool, Log: log, Fields: e.srv, Store: e.integrations, Box: e.box}).Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	defer func() { cancel(); <-done }()

	finished := func(record string) (map[string]string, map[string]any) {
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
				vars, _ := run["vars"].(map[string]any)
				return out, vars
			}
			if time.Now().After(deadline) {
				t.Fatalf("the run didn't finish: %v", run)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	for _, who := range []string{ana, bo} {
		if _, err := customers.Record(ctx, e.pool, ws, customers.Event{RecordID: who, Name: "trial.started", Properties: map[string]any{"plan": "pro"}}); err != nil {
			t.Fatal(err)
		}
	}
	out, vars := finished(ana)
	if out["status"] != "completed" || out["push"] != "done: created in HubSpot" || out["again"] != "done: updated in HubSpot" ||
		out["bad"][:8] != "failed: " || out["check"] != "yes: " || out["tag"] != "done: " {
		t.Fatalf("ana: %v", out)
	}
	if crm := vars["crm"].(map[string]any); crm["id"] != "c-ana@acme.example" || crm["created"] != true {
		t.Fatalf("vars: %v", vars)
	}
	if out, _ := finished(bo); out["push"] != "skipped: no verified email address" {
		t.Fatalf("bo: %v", out)
	}
	mu.Lock()
	if len(upserts) != 2 || upserts[0]["firstname"] != "Ana" || upserts[0]["company"] != "Acme" || upserts[0]["plan"] != "pro" || upserts[0]["lifecyclestage"] != "lead" {
		t.Fatalf("upserts: %v", upserts)
	}
	mu.Unlock()

	var audited int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'crm.contact_pushed' AND target_id = $2`, ws, ana).Scan(&audited); err != nil || audited != 2 {
		t.Fatalf("audited %d, %v", audited, err)
	}
	if n := len(named(got(), "crm.contact_pushed")); n != 2 {
		t.Fatalf("events: %d", n)
	}
}
