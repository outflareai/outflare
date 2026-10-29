package api_test

import (
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// The checklist says what would stop a draft measuring anything, and
// shipping shows the chosen version to everyone and ends the test.
func TestABTestsChecklistAndShip(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/flags", map[string]any{"key": "cta", "name": "Call to action",
		"variants": []map[string]any{{"key": "control", "weight": 50}, {"key": "bold", "weight": 50}}})
	ab := owner.must(201, "POST", base+"/ab-tests", map[string]any{"flag": "cta", "goals": []map[string]any{{"kind": "event", "name": "signup_started"}}})
	id := ab["id"].(string)

	statuses := func(c map[string]any) map[string]string {
		out := map[string]string{}
		for _, x := range c["checks"].([]any) {
			x := x.(map[string]any)
			out[x["id"].(string)] = x["status"].(string)
		}
		return out
	}
	// Nothing yet: the flag is off, no page shows it, nobody reaches the
	// goal, and there are no guardrails. It can still start.
	c := owner.must(200, "GET", base+"/ab-tests/"+id+"/checklist", nil)
	st := statuses(c)
	if c["ready"] != true || st["flag"] != "warn" || st["exposures"] != "warn" || st["goal"] != "warn" || st["guardrails"] != "warn" {
		t.Fatalf("empty checklist: %v", c)
	}
	// Pages show it and people sign up: those pass.
	if _, err := e.pool.Exec(t.Context(), `
		INSERT INTO customer_events (workspace_id, kind, name, anonymous_id, properties, context, occurred_at)
		SELECT $1::uuid, 'track', 'experiment_viewed', 'h-' || i, '{"flag": "cta", "variant": "control"}'::jsonb, '{}'::jsonb, now() - interval '1 day' FROM generate_series(1, 50) i
		UNION ALL
		SELECT $1::uuid, 'page', 'page.viewed', 'h-' || i, '{}'::jsonb, '{"page": {"path": "/"}}'::jsonb, now() - interval '2 days' FROM generate_series(1, 50) i
		UNION ALL
		SELECT $1::uuid, 'track', 'signup_started', 'h-' || i, '{}'::jsonb, '{}'::jsonb, now() - interval '1 day' FROM generate_series(1, 50) i WHERE i % 5 = 0`, ws); err != nil {
		t.Fatal(err)
	}
	if st := statuses(owner.must(200, "GET", base+"/ab-tests/"+id+"/checklist", nil)); st["exposures"] != "ok" || st["goal"] != "ok" {
		t.Fatalf("checklist with traffic: %v", st)
	}

	// Shipping needs a test that ran, and one of the flag's versions.
	owner.must(409, "POST", base+"/ab-tests/"+id+"/ship", map[string]any{"variant": "bold"})
	owner.must(200, "POST", base+"/ab-tests/"+id+"/start", nil)
	// A second test of the flag can't start while this one runs.
	other := owner.must(201, "POST", base+"/ab-tests/"+id+"/duplicate", nil)
	if c := owner.must(200, "GET", base+"/ab-tests/"+other["id"].(string)+"/checklist", nil); c["ready"] != false || statuses(c)["one_at_a_time"] != "fail" {
		t.Fatalf("second test checklist: %v", c)
	}
	owner.must(400, "POST", base+"/ab-tests/"+id+"/ship", map[string]any{"variant": "loud"})
	shipped := owner.must(200, "POST", base+"/ab-tests/"+id+"/ship", map[string]any{"variant": "bold"})
	if shipped["status"] != "done" || shipped["decision"] != "bold" {
		t.Fatalf("shipped: %v", shipped)
	}
	f := owner.must(200, "GET", base+"/flags/cta", nil)
	if f["status"] != "on" || f["rollout"] != 100.0 {
		t.Fatalf("flag after shipping: %v", f)
	}
	for _, v := range f["variants"].([]any) {
		v := v.(map[string]any)
		if want := map[string]float64{"bold": 100, "control": 0}[v["key"].(string)]; v["weight"] != want {
			t.Fatalf("flag variants after shipping: %v", f["variants"])
		}
	}
	// Shipping again from a finished test changes the flag only.
	if again := owner.must(200, "POST", base+"/ab-tests/"+id+"/ship", map[string]any{"variant": "control"}); again["decision"] != "control" {
		t.Fatalf("ship control: %v", again)
	}

	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
	}
	if names["ab_test.shipped"] != 2 || names["ab_test.ended"] != 1 || names["flag.updated"] < 3 {
		t.Errorf("events: %v", names)
	}
}
