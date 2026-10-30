package api_test

import (
	"testing"
	"time"
)

// A multivariate test measures every combination of its flags' versions,
// each factor's own effect, and the best combination.
func TestABTestMultivariate(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	split := []map[string]any{{"key": "control", "weight": 50}, {"key": "new", "weight": 50}}
	owner.must(201, "POST", base+"/flags", map[string]any{"key": "hero", "status": "on", "variants": split, "exclusion_group": "top"})
	owner.must(201, "POST", base+"/flags", map[string]any{"key": "cta", "variants": split})
	owner.must(201, "POST", base+"/flags", map[string]any{"key": "side", "status": "on", "variants": split, "exclusion_group": "top"})
	owner.must(201, "POST", base+"/flags", map[string]any{"key": "plain"})
	goal := []map[string]any{{"kind": "event", "name": "signup_started"}}

	// Factors must be other flags with two versions, outside the test
	// flag's exclusion group, and at most two.
	for _, bad := range [][]map[string]any{
		{{"flag": "hero"}}, {{"flag": "side"}}, {{"flag": "plain"}}, {{"flag": "cta", "control": "nope"}},
		{{"flag": "cta"}, {"flag": "cta"}}, {{"flag": "cta"}, {"flag": "plain"}, {"flag": "side"}},
	} {
		owner.must(400, "POST", base+"/ab-tests", map[string]any{"flag": "hero", "goals": goal, "factors": bad})
	}
	ab := owner.must(201, "POST", base+"/ab-tests", map[string]any{"flag": "hero", "goals": goal, "factors": []map[string]any{{"flag": "cta"}}})
	id := ab["id"].(string)
	if f := ab["factors"].([]any); len(f) != 1 || f[0].(map[string]any)["flag_key"] != "cta" || f[0].(map[string]any)["control"] != "control" {
		t.Fatalf("factors: %v", ab["factors"])
	}
	owner.must(409, "GET", base+"/ab-tests/"+id+"/factorial", nil)

	// Starting turns the factor's flag on, and holds it.
	owner.must(200, "POST", base+"/ab-tests/"+id+"/start", nil)
	if owner.must(200, "GET", base+"/flags/cta", nil)["status"] != "on" {
		t.Fatal("the factor's flag wasn't turned on")
	}
	owner.must(409, "PATCH", base+"/ab-tests/"+id, map[string]any{"factors": []map[string]any{}})
	other := owner.must(201, "POST", base+"/ab-tests", map[string]any{"flag": "cta", "goals": goal})["id"].(string)
	owner.must(409, "POST", base+"/ab-tests/"+other+"/start", nil)

	// 2,400 visitors over the four cells: each change helps, both together
	// most (10%, 20%, 15%, 35%).
	if _, err := e.pool.Exec(t.Context(), `
		WITH v AS (SELECT i, CASE WHEN i % 2 = 0 THEN 'control' ELSE 'new' END AS h, CASE WHEN (i / 2) % 2 = 0 THEN 'control' ELSE 'new' END AS c
			FROM generate_series(1, 2400) i)
		INSERT INTO customer_events (workspace_id, kind, name, anonymous_id, properties, context, occurred_at)
		SELECT $1::uuid, 'track', 'experiment_viewed', 'm-' || i, jsonb_build_object('flag', 'hero', 'variant', h), '{}'::jsonb, now() FROM v
		UNION ALL
		SELECT $1::uuid, 'track', 'experiment_viewed', 'm-' || i, jsonb_build_object('flag', 'cta', 'variant', c), '{}'::jsonb, now() FROM v
		UNION ALL
		SELECT $1::uuid, 'track', 'signup_started', 'm-' || i, '{}'::jsonb, '{}'::jsonb, now() + interval '1 second' FROM v
		WHERE (i / 4) % 20 < CASE WHEN h = 'new' AND c = 'new' THEN 7 WHEN h = 'new' THEN 4 WHEN c = 'new' THEN 3 ELSE 2 END`, ws); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	r := owner.must(200, "GET", base+"/ab-tests/"+id+"/factorial", nil)["result"].(map[string]any)
	cells := r["cells"].([]any)
	if len(cells) != 4 || r["corrected_for"] != 3.0 {
		t.Fatalf("cells: %v", cells)
	}
	best := r["best"].(map[string]any)["versions"].(map[string]any)
	if best["hero"] != "new" || best["cta"] != "new" {
		t.Fatalf("best: %v", r["best"])
	}
	for _, m := range r["main_effects"].([]any) {
		m := m.(map[string]any)
		c := m["comparisons"].([]any)[0].(map[string]any)
		if arms := m["arms"].([]any); len(arms) != 2 || arms[0].(map[string]any)["visitors"] != 1200.0 || c["chance_to_beat"].(float64) < 0.99 {
			t.Fatalf("main effect of %s: %v", m["flag_key"], m)
		}
	}
	if in := r["interactions"].([]any); len(in) != 1 || len(in[0].(map[string]any)["effects"].([]any)) != 2 {
		t.Fatalf("interactions: %v", in)
	}
}
