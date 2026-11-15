package api_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/journeys"
)

// The outflare_action step: a journey calls the API as one of the
// workspace's agents, and the agent's grants decide what happens.
func TestJourneyOutflareAction(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	wsBase := "/v1/workspaces/" + ws

	agent := owner.must(201, "POST", wsBase+"/api-tokens", map[string]any{"name": "Flow agent", "scopes": []string{"customers.manage", "customers.read"}, "action_gate": true})["api_token"].(map[string]any)["id"].(string)
	plain := owner.must(201, "POST", wsBase+"/api-tokens", map[string]any{"name": "Script", "scopes": []string{"customers.manage"}})["api_token"].(map[string]any)["id"].(string)
	// It may add CRM activities on its own; anything else waits.
	owner.must(201, "POST", wsBase+"/action-grants", map[string]any{"token_id": agent, "path": "crm/activities", "mode": "run"})

	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme"})["id"].(string)
	step := func(more map[string]any) []map[string]any {
		st := map[string]any{"id": "act", "kind": "outflare_action", "agent_id": agent, "path": "crm/activities",
			"body": map[string]any{"record_id": "{{person.id}}", "kind": "note", "title": "Check in this week"}}
		for k, v := range more {
			st[k] = v
		}
		return []map[string]any{st}
	}
	create := func(status int, name string, steps []map[string]any) map[string]any {
		t.Helper()
		return owner.must(status, "POST", base+"/journeys", map[string]any{"name": name, "record_type": "account", "trigger": map[string]any{"kind": "manual"}, "steps": steps})
	}
	// What a step can't call or be.
	create(400, "Bad", step(map[string]any{"path": "/v1/brands/x"}))
	create(400, "Bad", step(map[string]any{"path": "actions/x/approve"}))
	create(400, "Bad", step(map[string]any{"path": "records?x=1"}))
	create(400, "Bad", step(map[string]any{"method": "GET"}))
	create(400, "Bad", step(map[string]any{"agent_id": "nope"}))
	create(400, "Bad", step(map[string]any{"body": "text"}))
	// A token that doesn't ask for approval can't be the agent.
	create(400, "Not gated", step(map[string]any{"agent_id": plain}))

	runs := create(201, "Note it", step(nil))["id"].(string)
	held := create(201, "Tag it", step(map[string]any{"method": "PATCH", "path": "records/{{person.id}}", "body": map[string]any{"attributes": map[string]any{"tier": "gold"}}}))["id"].(string)
	for _, id := range []string{runs, held} {
		owner.must(200, "POST", base+"/journeys/"+id+"/activate", nil)
	}

	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	svc := &journeys.Service{Pool: e.pool, Log: log, Fields: e.srv, Agents: e.srv}
	svc.Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	defer func() { cancel(); <-done }()
	settle := func() {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for {
			var n int
			_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE workspace_id = $1 AND status IN ('queued', 'running') AND run_at <= now()`, ws).Scan(&n)
			if n == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("waited 60s for jobs")
			}
			time.Sleep(30 * time.Millisecond)
		}
	}
	for _, id := range []string{runs, held} {
		owner.must(201, "POST", base+"/journeys/"+id+"/runs", map[string]any{"record_id": acme})
		settle()
		got := owner.must(200, "GET", base+"/journeys/"+id+"/runs?record="+acme, nil)["data"].([]any)
		if len(got) != 1 || got[0].(map[string]any)["status"] != "completed" {
			t.Fatalf("the run of %s: %v", id, got)
		}
	}

	// The granted call ran, as the agent.
	acts := owner.must(200, "GET", base+"/crm/activities?record_id="+acme, nil)["data"].([]any)
	if len(acts) != 1 || acts[0].(map[string]any)["title"] != "Check in this week" {
		t.Fatalf("activities: %v", acts)
	}
	// The other waits for a person, and nothing changed.
	waiting := owner.must(200, "GET", base+"/actions?status=held", nil)["data"].([]any)
	if len(waiting) != 1 || waiting[0].(map[string]any)["method"] != "PATCH" {
		t.Fatalf("held actions: %v", waiting)
	}
	if tier := owner.must(200, "GET", base+"/records/"+acme, nil)["attributes"].(map[string]any)["tier"]; tier != nil {
		t.Fatalf("a held change ran: tier = %v", tier)
	}
}
