package api_test

import (
	"fmt"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestABTests(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	owner.must(201, "POST", base+"/flags", map[string]any{"key": "hero", "name": "Hero headline",
		"variants": []map[string]any{{"key": "control", "weight": 50}, {"key": "faster", "weight": 50}}})
	owner.must(201, "POST", base+"/flags", map[string]any{"key": "chat-widget", "status": "on"})
	signup := []map[string]any{{"kind": "event", "name": "signup_started"}}

	// Bad tests are refused with a reason.
	for _, body := range []map[string]any{
		{"goals": signup},
		{"flag": "nope", "goals": signup},
		{"flag": "hero"},
		{"flag": "hero", "goals": []map[string]any{{"kind": "event", "name": "Signup Started"}}},
		{"flag": "hero", "goals": []map[string]any{{"kind": "page", "path": "thanks"}}},
		{"flag": "hero", "goals": signup, "control": "slower"},
		{"flag": "hero", "goals": signup, "min_effect": 2},
	} {
		owner.must(400, "POST", base+"/ab-tests", body)
	}

	// A draft of the hero flag: control by default, name from the flag.
	ab := owner.must(201, "POST", base+"/ab-tests", map[string]any{"flag": "hero", "hypothesis": "A shorter headline gets more signups",
		"goals": []map[string]any{{"kind": "event", "name": "signup_started"}, {"kind": "page", "path": "/thanks*"}}})
	id := ab["id"].(string)
	if ab["status"] != "draft" || ab["control"] != "control" || ab["name"] != "Hero headline" || ab["flag_key"] != "hero" || ab["started_at"] != nil || ab["min_effect"] != 0.25 {
		t.Fatalf("draft: %v", ab)
	}
	owner.must(409, "GET", base+"/ab-tests/"+id+"/results", nil)
	owner.must(200, "PATCH", base+"/ab-tests/"+id, map[string]any{"name": "Shorter hero"})
	if ab := owner.must(200, "PATCH", base+"/ab-tests/"+id, map[string]any{"min_effect": 0.5}); ab["min_effect"] != 0.5 {
		t.Fatalf("min_effect: %v", ab)
	}
	owner.must(400, "PATCH", base+"/ab-tests/"+id, map[string]any{"flag": "chat-widget"})
	owner.must(404, "GET", base+"/ab-tests/00000000-0000-0000-0000-000000000000", nil)
	owner.must(404, "GET", base+"/ab-tests/hero", nil)

	// A switch has one version: nothing to compare.
	sw := owner.must(201, "POST", base+"/ab-tests", map[string]any{"flag": "chat-widget", "goals": signup})
	owner.must(409, "POST", base+"/ab-tests/"+sw["id"].(string)+"/start", nil)

	// Starting turns the flag on.
	ab = owner.must(200, "POST", base+"/ab-tests/"+id+"/start", nil)
	if ab["status"] != "running" || ab["started_at"] == nil {
		t.Fatalf("started: %v", ab)
	}
	if f := owner.must(200, "GET", base+"/flags/hero", nil); f["status"] != "on" {
		t.Fatalf("flag not turned on: %v", f)
	}
	owner.must(409, "POST", base+"/ab-tests/"+id+"/start", nil)
	owner.must(409, "PATCH", base+"/ab-tests/"+id, map[string]any{"goals": signup})
	owner.must(409, "PATCH", base+"/ab-tests/"+id, map[string]any{"min_effect": 0.1})
	// One running test per flag.
	again := owner.must(201, "POST", base+"/ab-tests", map[string]any{"flag": "hero", "goals": signup})
	owner.must(409, "POST", base+"/ab-tests/"+again["id"].(string)+"/start", nil)

	// Visitors see a version, some sign up after.
	var batch []map[string]any
	for i := range 40 {
		anon, v := fmt.Sprintf("visitor-%03d", i), "control"
		if i%2 == 1 {
			v = "faster"
		}
		batch = append(batch, map[string]any{"type": "track", "event": "Experiment Viewed", "anonymousId": anon, "messageId": anon + "-seen",
			"properties": map[string]any{"flag": "hero", "variant": v}})
		if v == "faster" || i%10 == 0 {
			batch = append(batch, map[string]any{"type": "track", "event": "Signup Started", "anonymousId": anon, "messageId": anon + "-signup"})
		}
	}
	owner.must(200, "POST", base+"/customer-events", map[string]any{"batch": batch})
	// And many more, enough for the always-valid p-value: 10% sign up
	// after the control, 30% after the faster headline.
	if _, err := e.pool.Exec(t.Context(), `
		WITH v AS (SELECT i, CASE WHEN i % 2 = 0 THEN 'control' ELSE 'faster' END AS variant FROM generate_series(1, 2000) i)
		INSERT INTO customer_events (workspace_id, kind, name, anonymous_id, properties, context, occurred_at)
		SELECT $1::uuid, 'track', 'experiment_viewed', 'bulk-' || i, jsonb_build_object('flag', 'hero', 'variant', variant), '{}'::jsonb, now() FROM v
		UNION ALL
		SELECT $1::uuid, 'track', 'signup_started', 'bulk-' || i, '{}'::jsonb, '{}'::jsonb, now() FROM v
		WHERE (variant = 'control' AND i % 20 = 0) OR (variant = 'faster' AND i % 10 IN (1, 3, 5) AND i % 20 <> 0)`, ws); err != nil {
		t.Fatal(err)
	}

	res := owner.must(200, "GET", base+"/ab-tests/"+id+"/results", nil)
	r := res["result"].(map[string]any)
	if r["min_effect"] != 0.5 {
		t.Fatalf("results not tuned to the test: %v", r["min_effect"])
	}
	arms := r["arms"].([]any)
	if res["status"] != "running" || len(arms) != 2 || arms[0].(map[string]any)["variant"] != "control" || arms[0].(map[string]any)["visitors"] != 1020.0 {
		t.Fatalf("results: %v", res)
	}
	first := r["goals"].([]any)[0].(map[string]any)
	if conv := first["arms"].([]any)[1].(map[string]any)["conversions"]; conv != 620.0 {
		t.Fatalf("conversions: %v", first)
	}
	if res["winner"] != "faster" {
		t.Fatalf("winner: %v", res)
	}

	// Ending keeps the results and the choice; the flag stays as it is.
	owner.must(400, "POST", base+"/ab-tests/"+id+"/end", map[string]any{"decision": "slower"})
	owner.must(400, "POST", base+"/ab-tests/"+id+"/end", map[string]any{})
	ab = owner.must(200, "POST", base+"/ab-tests/"+id+"/end", map[string]any{"decision": "faster"})
	if ab["status"] != "done" || ab["decision"] != "faster" || ab["ended_at"] == nil {
		t.Fatalf("ended: %v", ab)
	}
	owner.must(409, "POST", base+"/ab-tests/"+id+"/end", map[string]any{"decision": ""})
	owner.must(200, "POST", base+"/customer-events", map[string]any{"type": "track", "event": "Experiment Viewed", "anonymousId": "visitor-late",
		"messageId": "late", "properties": map[string]any{"flag": "hero", "variant": "control"}})
	kept := owner.must(200, "GET", base+"/ab-tests/"+id+"/results", nil)
	if kept["status"] != "done" || kept["winner"] != "faster" || kept["result"].(map[string]any)["arms"].([]any)[0].(map[string]any)["visitors"] != 1020.0 {
		t.Fatalf("kept results: %v", kept)
	}

	if list := owner.must(200, "GET", base+"/ab-tests?status=done", nil)["data"].([]any); len(list) != 1 {
		t.Fatalf("done: %v", list)
	}
	if list := owner.must(200, "GET", base+"/ab-tests?flag=hero", nil)["data"].([]any); len(list) != 2 {
		t.Fatalf("by flag: %v", list)
	}
	owner.must(400, "GET", base+"/ab-tests?status=paused", nil)

	names, ended := map[string]int{}, map[string]any{}
	for _, ev := range got() {
		names[ev.Name]++
		if ev.Name == "ab_test.ended" {
			ended = ev.Props
		}
	}
	for _, name := range []string{"ab_test.created", "ab_test.updated", "ab_test.started", "ab_test.ended"} {
		if names[name] == 0 {
			t.Errorf("no %s event", name)
		}
	}
	if ended["chose_winner"] != true || ended["visitors"] != int64(2040) {
		t.Errorf("ended event: %v", ended)
	}
}
