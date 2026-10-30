package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestPlaybookAutomations(t *testing.T) {
	got := track.Capture(t)
	ctx := context.Background()
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	auto := base + "/playbook-automations"

	out := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Agent", "scopes": []string{"missions.read", "missions.run", "missions.approve"}})
	agent := &client{t: t, h: e.h, bearer: out["token"].(string)}
	tokenID := out["api_token"].(map[string]any)["id"].(string)

	// The playbook suggests a monthly schedule; an agent's automation runs
	// its missions itself, and waits for a person's approval.
	inputs := map[string]any{"site": "northpeak.example"}
	sched := agent.must(201, "POST", auto, map[string]any{"playbook_id": "link-reclaim-lost-links", "inputs": inputs})
	if sched["kind"] != "schedule" || sched["cadence"] != "monthly" || sched["status"] != "draft" || sched["runner"] != "external" ||
		sched["token_id"] != tokenID || sched["mode"] != "approve_writes" || sched["name"] != "Win back the links you lost" || sched["next_run_at"] != nil {
		t.Fatalf("schedule: %v", sched)
	}
	sid := sched["id"].(string)

	// Checked: the playbook, its inputs, the kind and the event.
	agent.must(400, "POST", auto, map[string]any{"playbook_id": "nope"})
	agent.must(400, "POST", auto, map[string]any{"playbook_id": "link-reclaim-lost-links"})
	agent.must(400, "POST", auto, map[string]any{"playbook_id": "link-reclaim-lost-links", "inputs": inputs, "kind": "event", "event_type": "not.an_event"})
	agent.must(400, "POST", auto, map[string]any{"playbook_id": "link-reclaim-lost-links", "inputs": inputs, "kind": "schedule", "cadence": "hourly"})
	owner.must(400, "POST", auto, map[string]any{"playbook_id": "link-reclaim-lost-links", "inputs": inputs, "runner": "external"})

	// Only a person turns it on: each run carries their approval.
	res := agent.must(403, "POST", auto+"/"+sid+"/approve", nil)
	if !strings.Contains(res["error"].(map[string]any)["message"].(string), "only a person") {
		t.Fatalf("agent approve: %v", res)
	}
	agent.must(409, "POST", auto+"/"+sid+"/run", nil)
	sched = owner.must(200, "POST", auto+"/"+sid+"/approve", nil)
	if sched["status"] != "active" || sched["next_run_at"] == nil || sched["approved_by"] == nil {
		t.Fatalf("approved: %v", sched)
	}
	owner.must(409, "POST", auto+"/"+sid+"/approve", nil)

	// Its time comes: a running mission from the playbook, with its mode
	// and runner, approved by the automation.
	due := func(id string) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `UPDATE playbook_automations SET next_run_at = now() - interval '1 minute' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
	due(sid)
	if n, err := e.srv.RunDueAutomations(ctx); err != nil || n != 1 {
		t.Fatalf("scheduled run: %d, %v", n, err)
	}
	runs := owner.must(200, "GET", auto+"/"+sid+"/runs", nil)["data"].([]any)
	if len(runs) != 1 {
		t.Fatalf("runs: %v", runs)
	}
	m := runs[0].(map[string]any)
	if m["status"] != "running" || m["mode"] != "approve_writes" || m["runner"] != "external" || m["token_id"] != tokenID ||
		m["automation_id"] != sid || m["playbook_id"] != "link-reclaim-lost-links" || m["approved_at"] == nil {
		t.Fatalf("run: %v", m)
	}
	if ins := m["instructions"].(string); !strings.Contains(ins, "northpeak.example") || !strings.Contains(ins, "which runs monthly") {
		t.Fatalf("instructions: %s", ins)
	}
	sched = owner.must(200, "GET", auto+"/"+sid, nil)
	next, _ := time.Parse(time.RFC3339, sched["next_run_at"].(string))
	if sched["runs"] != float64(1) || sched["last_mission_id"] != m["id"] || !next.After(time.Now().Add(24*time.Hour)) {
		t.Fatalf("after the run: %v", sched)
	}
	// Nothing is due now.
	if n, _ := e.srv.RunDueAutomations(ctx); n != 0 {
		t.Fatalf("ran again: %d", n)
	}

	// A run now, by hand, inside the same limits.
	m = agent.must(201, "POST", auto+"/"+sid+"/run", nil)
	if m["automation_id"] != sid || m["status"] != "running" {
		t.Fatalf("run now: %v", m)
	}
	if !strings.Contains(m["instructions"].(string), "previous run was at") {
		t.Fatalf("run now instructions: %s", m["instructions"])
	}

	// Renaming keeps the approval; changing what was approved doesn't.
	sched = agent.must(200, "PATCH", auto+"/"+sid, map[string]any{"name": "Reclaim monthly"})
	if sched["status"] != "active" || sched["name"] != "Reclaim monthly" {
		t.Fatalf("renamed: %v", sched)
	}
	sched = agent.must(200, "PATCH", auto+"/"+sid, map[string]any{"mode": "autonomous"})
	if sched["status"] != "draft" || sched["approved_by"] != nil || sched["next_run_at"] != nil {
		t.Fatalf("changed: %v", sched)
	}
	agent.must(400, "PATCH", auto+"/"+sid, map[string]any{"playbook_id": "link-resource-pages"})

	// An event trigger, a run per event, at most two a day.
	ev := owner.must(201, "POST", auto, map[string]any{"playbook_id": "link-reclaim-lost-links", "inputs": inputs, "kind": "event",
		"gather": "each", "daily_cap": 2, "runner": "external", "token_id": tokenID})
	if ev["event_type"] != "backlink.lost" || ev["cadence"] != nil {
		t.Fatalf("event automation: %v", ev)
	}
	eid := ev["id"].(string)
	lost := func() {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `INSERT INTO audit_events (workspace_id, actor_type, action, target_type, target_id, data)
			VALUES ($1, 'system', 'backlink.lost', 'backlink', gen_random_uuid()::text, '{}')`, ws); err != nil {
			t.Fatal(err)
		}
	}
	lost() // before approval: never runs
	owner.must(200, "POST", auto+"/"+eid+"/approve", nil)
	lost()
	lost()
	lost()
	due(eid)
	if n, err := e.srv.RunDueAutomations(ctx); err != nil || n != 2 {
		t.Fatalf("event runs: %d, %v", n, err)
	}
	ev = owner.must(200, "GET", auto+"/"+eid, nil)
	next, _ = time.Parse(time.RFC3339, ev["next_run_at"].(string))
	if ev["runs"] != float64(2) || next.Before(time.Now()) || next.UTC().Hour() != 0 {
		t.Fatalf("capped for today: %v", ev)
	}
	runs = owner.must(200, "GET", auto+"/"+eid+"/runs", nil)["data"].([]any)
	if len(runs) != 2 || !strings.Contains(runs[0].(map[string]any)["instructions"].(string), "for 1 backlink.lost event(s)") {
		t.Fatalf("event run: %v", runs)
	}

	// Gathered daily: the waiting event and a new one in one run.
	agent.must(200, "PATCH", auto+"/"+eid, map[string]any{"gather": "daily"})
	if _, err := e.pool.Exec(ctx, `UPDATE playbook_automations SET status = 'held', held_reason = 'test' WHERE id = $1`, eid); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "POST", auto+"/"+eid+"/approve", nil) // a held one keeps its events
	lost()
	due(eid)
	if n, err := e.srv.RunDueAutomations(ctx); err != nil || n != 1 {
		t.Fatalf("gathered run: %d, %v", n, err)
	}
	runs = owner.must(200, "GET", auto+"/"+eid+"/runs", nil)["data"].([]any)
	if ins := runs[0].(map[string]any)["instructions"].(string); !strings.Contains(ins, "for 2 backlink.lost event(s)") {
		t.Fatalf("gathered: %s", ins)
	}
	// Nothing new: no run.
	due(eid)
	if n, _ := e.srv.RunDueAutomations(ctx); n != 0 {
		t.Fatalf("ran with no events: %d", n)
	}

	// The monthly limit holds it until a person looks.
	agent.must(200, "PATCH", auto+"/"+eid, map[string]any{"max_spend_cents_per_month": 0})
	owner.must(200, "POST", auto+"/"+eid+"/approve", nil)
	lost()
	due(eid)
	if n, err := e.srv.RunDueAutomations(ctx); err != nil || n != 0 {
		t.Fatalf("over the limit: %d, %v", n, err)
	}
	ev = owner.must(200, "GET", auto+"/"+eid, nil)
	if ev["status"] != "held" || !strings.Contains(ev["held_reason"].(string), "monthly limit") || ev["next_run_at"] != nil {
		t.Fatalf("held: %v", ev)
	}

	// Its approver leaving holds it too.
	sched = owner.must(200, "POST", auto+"/"+sid+"/approve", nil)
	if _, err := e.pool.Exec(ctx, `UPDATE memberships SET role = 'viewer' WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	due(sid)
	if n, err := e.srv.RunDueAutomations(ctx); err != nil || n != 0 {
		t.Fatalf("approver gone: %d, %v", n, err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE memberships SET role = 'owner' WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	sched = owner.must(200, "GET", auto+"/"+sid, nil)
	if sched["status"] != "held" || !strings.Contains(sched["held_reason"].(string), "can no longer run and approve missions") {
		t.Fatalf("approver gone: %v", sched)
	}

	// Pause, list, delete; the runs stay as missions.
	owner.must(200, "POST", auto+"/"+sid+"/approve", nil)
	if p := agent.must(200, "POST", auto+"/"+sid+"/pause", nil); p["status"] != "paused" {
		t.Fatalf("paused: %v", p)
	}
	agent.must(409, "POST", auto+"/"+sid+"/pause", nil)
	if l := owner.must(200, "GET", auto+"?playbook_id=link-reclaim-lost-links", nil)["data"].([]any); len(l) != 2 {
		t.Fatalf("list: %v", l)
	}
	if l := owner.must(200, "GET", auto+"?playbook_id=link-resource-pages", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("filtered list: %v", l)
	}
	agent.must(204, "DELETE", auto+"/"+sid, nil)
	agent.must(404, "GET", auto+"/"+sid, nil)
	owner.must(200, "GET", base+"/missions/"+m["id"].(string), nil)

	evs := got()
	for name, want := range map[string]int{"playbook_automation.created": 2, "playbook_automation.ran": 5, "playbook.started": 5, "playbook_automation.held": 2, "playbook_automation.deleted": 1} {
		if n := len(named(evs, name)); n != want {
			t.Errorf("%s: %d events, want %d", name, n, want)
		}
	}
	if r := named(evs, "playbook_automation.ran"); len(r) > 0 && (r[0].Props["trigger"] != "schedule" || r[0].Props["playbook_id"] != "link-reclaim-lost-links") {
		t.Errorf("ran props: %v", r[0].Props)
	}
}
