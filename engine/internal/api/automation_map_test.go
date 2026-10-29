package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/customers"
)

func TestAutomationMap(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	// A stalled-trial rule, a rule whose event nobody uses, a static list,
	// and two journeys that enter each other.
	owner.must(201, "POST", base+"/rules", map[string]any{"name": "Stalled after sign-up", "emit_event": "trial.stalled",
		"trigger": map[string]any{"kind": "absence", "start_event": "signed_up", "event": "project_created", "within_seconds": 259200}})
	orphan := owner.must(201, "POST", base+"/rules", map[string]any{"name": "Big order", "emit_event": "order.big",
		"trigger": map[string]any{"kind": "event", "event": "order_completed"}})["id"].(string)
	list := owner.must(201, "POST", base+"/smart-lists", map[string]any{"name": "Stalled trials", "kind": "static"})["id"].(string)
	helper := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Nudge again", "steps": []map[string]any{{"id": "done", "kind": "exit"}}})["id"].(string)
	main := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Rescue stalled trials", "trigger": map[string]any{"kind": "event", "event": "trial.stalled"},
		"steps": []map[string]any{
			{"id": "tag", "kind": "add_tag", "tag": "stalled", "next": "list"},
			{"id": "list", "kind": "add_to_list", "list_id": list, "next": "again"},
			{"id": "again", "kind": "enter_journey", "journey_id": helper},
		}})["id"].(string)
	owner.must(200, "PATCH", base+"/journeys/"+helper, map[string]any{"steps": []map[string]any{{"id": "back", "kind": "enter_journey", "journey_id": main}}})
	owner.must(200, "POST", base+"/journeys/"+main+"/activate", nil)

	// Some history: a sign-up this week, and one person emailed by both
	// journeys within the hour this week, on runs they entered long ago
	// (a journey people entered lately isn't one that never starts).
	if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, kind, name, occurred_at) VALUES ($1, 'track', 'signed_up', now())`, ws); err != nil {
		t.Fatal(err)
	}
	rec, err := customers.EnsurePerson(ctx, e.pool, ws, "email", "ana@acme.example", true, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range []string{main, helper} {
		if _, err := e.pool.Exec(ctx, `WITH run AS (INSERT INTO journey_runs (workspace_id, journey_id, record_id, status, entered_at) VALUES ($1, $2, $3, 'completed', now() - interval '40 days') RETURNING id)
			INSERT INTO journey_run_steps (workspace_id, run_id, journey_id, step, kind, outcome) SELECT $1, id, $2, 'mail', 'email', 'sent' FROM run`, ws, j, rec); err != nil {
			t.Fatal(err)
		}
	}

	m := owner.must(200, "GET", base+"/automation-map", nil)
	nodes := map[string]map[string]any{}
	for _, n := range m["nodes"].([]any) {
		n := n.(map[string]any)
		nodes[n["id"].(string)] = n
	}
	for _, id := range []string{"event:signed_up", "event:project_created", "event:trial.stalled", "event:order_completed", "event:order.big",
		"journey:" + main, "journey:" + helper, "list:" + list, "tag:stalled", "rule:" + orphan} {
		if nodes[id] == nil {
			t.Fatalf("no node %s: %v", id, nodes)
		}
	}
	if nodes["event:signed_up"]["volume"] != float64(1) || nodes["event:order_completed"]["volume"] != float64(0) ||
		nodes["journey:"+main]["volume"] != float64(0) || nodes["list:"+list]["volume"] != nil {
		t.Fatalf("volumes: %v", nodes)
	}
	// A link, where "rule:" stands for any rule.
	linked := func(from, kind, to string) bool {
		for _, ed := range m["edges"].([]any) {
			ed := ed.(map[string]any)
			if ed["kind"] == kind && strings.HasPrefix(ed["from"].(string), from) && strings.HasPrefix(ed["to"].(string), to) {
				return true
			}
		}
		return false
	}
	for _, want := range [][3]string{
		{"event:signed_up", "starts", "rule:"}, {"event:project_created", "waits_for", "rule:"}, {"rule:", "emits", "event:trial.stalled"},
		{"event:trial.stalled", "starts", "journey:" + main}, {"journey:" + main, "tags", "tag:stalled"}, {"journey:" + main, "adds_to", "list:" + list},
		{"journey:" + main, "enters", "journey:" + helper}, {"journey:" + helper, "enters", "journey:" + main},
	} {
		if !linked(want[0], want[1], want[2]) {
			t.Errorf("no link %v in %v", want, m["edges"])
		}
	}
	flags := map[string]string{}
	for _, f := range m["flags"].([]any) {
		f := f.(map[string]any)
		flags[f["kind"].(string)] += f["message"].(string) + " "
	}
	switch {
	case !strings.Contains(flags["loop"], "Rescue stalled trials") || !strings.Contains(flags["loop"], "Nudge again"):
		t.Errorf("loop: %v", flags)
	case !strings.Contains(flags["unheard"], "order.big"):
		t.Errorf("unheard: %v", flags)
	case !strings.Contains(flags["never_fires"], "trial.stalled"):
		t.Errorf("never fires: %v", flags)
	case !strings.Contains(flags["overlap"], "1 person"):
		t.Errorf("overlap: %v", flags)
	}

	// The map needs customers.read too.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Emails only", "scopes": []string{"emails.read"}})["token"].(string)
	(&client{t: t, h: e.h, bearer: tok}).must(403, "GET", base+"/automation-map", nil)
}
