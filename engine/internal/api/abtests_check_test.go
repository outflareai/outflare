package api_test

import (
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

// Guardrails, freezing, duplicating, and the background check's alerts: a
// version that wins on signups but triples the errors is the winner, is
// flagged on the guardrail, and both are raised once as alerts.
func TestABTestsGuardrailsAndChecks(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/flags", map[string]any{"key": "signup", "name": "Signup form",
		"variants": []map[string]any{{"key": "control", "weight": 50}, {"key": "short", "weight": 50}}})
	signup := []map[string]any{{"kind": "event", "name": "signup_started"}}
	errorsUp := []map[string]any{{"kind": "event", "name": "error_shown", "better": "lower"}}
	owner.must(400, "POST", base+"/ab-tests", map[string]any{"flag": "signup", "goals": signup, "guardrails": []map[string]any{{"kind": "event", "name": "error_shown", "better": "down"}}})
	owner.must(400, "POST", base+"/ab-tests", map[string]any{"flag": "signup", "goals": []map[string]any{{"kind": "event", "name": "signup_started", "better": "lower"}}})
	ab := owner.must(201, "POST", base+"/ab-tests", map[string]any{"flag": "signup", "goals": signup, "guardrails": errorsUp})
	id := ab["id"].(string)
	if g := ab["guardrails"].([]any); len(g) != 1 || ab["frozen_at"] != nil {
		t.Fatalf("created: %v", ab)
	}
	owner.must(409, "POST", base+"/ab-tests/"+id+"/freeze", map[string]any{"frozen": true})
	owner.must(200, "POST", base+"/ab-tests/"+id+"/start", nil)
	owner.must(409, "PATCH", base+"/ab-tests/"+id, map[string]any{"guardrails": []any{}})

	// 2,000 a version: short gets twice the signups and four times the
	// errors.
	if _, err := e.pool.Exec(t.Context(), `
		WITH v AS (SELECT i, CASE WHEN i % 2 = 0 THEN 'control' ELSE 'short' END AS variant FROM generate_series(1, 4000) i)
		INSERT INTO customer_events (workspace_id, kind, name, anonymous_id, properties, context, occurred_at)
		SELECT $1::uuid, 'track', 'experiment_viewed', 'v-' || i, jsonb_build_object('flag', 'signup', 'variant', variant), '{}'::jsonb, now() FROM v
		UNION ALL
		SELECT $1::uuid, 'track', 'signup_started', 'v-' || i, '{}'::jsonb, '{}'::jsonb, now() FROM v
		WHERE (variant = 'control' AND i % 20 = 0) OR (variant = 'short' AND i % 10 IN (1, 3))
		UNION ALL
		SELECT $1::uuid, 'track', 'error_shown', 'v-' || i, '{}'::jsonb, '{}'::jsonb, now() FROM v
		WHERE (variant = 'control' AND i % 20 = 0) OR (variant = 'short' AND i % 5 = 1)`, ws); err != nil {
		t.Fatal(err)
	}
	res := owner.must(200, "GET", base+"/ab-tests/"+id+"/results", nil)
	r := res["result"].(map[string]any)
	g := r["guardrails"].([]any)
	if len(g) != 1 || res["winner"] != "short" || r["crossovers"] != 0.0 {
		t.Fatalf("results: %v", res)
	}
	if h := g[0].(map[string]any)["harmed"].([]any); len(h) != 1 || h[0] != "short" {
		t.Fatalf("harmed: %v", g[0])
	}

	// The check is due: it raises "ready" and "guardrail hurt", once each.
	check := func() {
		t.Helper()
		if _, err := e.pool.Exec(t.Context(), `UPDATE ab_tests SET next_check_at = now() - interval '1 minute' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
		if n, err := e.srv.QueueDueABTestChecks(t.Context()); err != nil || n != 1 {
			t.Fatalf("queued %d checks: %v", n, err)
		}
	}
	check()
	e.runServerJobs(t)
	kinds := func() map[string]int {
		out := map[string]int{}
		rows, err := e.pool.Query(t.Context(), `SELECT kind FROM alerts WHERE workspace_id = $1 AND target_id = $2`, ws, id)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				t.Fatal(err)
			}
			out[k]++
		}
		return out
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(kinds()) < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	check()
	time.Sleep(500 * time.Millisecond)
	if k := kinds(); k["ab_test.ready"] != 1 || k["ab_test.guardrail_hurt"] != 1 || len(k) != 2 {
		t.Fatalf("alerts: %v", k)
	}

	// Freezing keeps new visitors out; unfreezing lets them in again.
	owner.must(400, "POST", base+"/ab-tests/"+id+"/freeze", map[string]any{})
	if ab := owner.must(200, "POST", base+"/ab-tests/"+id+"/freeze", map[string]any{"frozen": true}); ab["frozen_at"] == nil {
		t.Fatalf("frozen: %v", ab)
	}
	owner.must(200, "POST", base+"/ab-tests/"+id+"/freeze", map[string]any{"frozen": true})
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO customer_events (workspace_id, kind, name, anonymous_id, properties, context, occurred_at)
		VALUES ($1, 'track', 'experiment_viewed', 'late', '{"flag": "signup", "variant": "control"}', '{}', now() + interval '1 second')`, ws); err != nil {
		t.Fatal(err)
	}
	visitors := func() float64 {
		return owner.must(200, "GET", base+"/ab-tests/"+id+"/results", nil)["result"].(map[string]any)["arms"].([]any)[0].(map[string]any)["visitors"].(float64)
	}
	if v := visitors(); v != 2000 {
		t.Fatalf("frozen test let a new visitor in: %v", v)
	}
	if ab := owner.must(200, "POST", base+"/ab-tests/"+id+"/freeze", map[string]any{"frozen": false}); ab["frozen_at"] != nil {
		t.Fatalf("unfrozen: %v", ab)
	}
	time.Sleep(1100 * time.Millisecond)
	if v := visitors(); v != 2001 {
		t.Fatalf("unfrozen test: %v visitors", v)
	}

	// Duplicating makes a draft with the same setup.
	dup := owner.must(201, "POST", base+"/ab-tests/"+id+"/duplicate", nil)
	if dup["status"] != "draft" || dup["name"] != "Signup form (copy)" || len(dup["guardrails"].([]any)) != 1 || dup["id"] == id {
		t.Fatalf("duplicate: %v", dup)
	}
	owner.must(404, "POST", base+"/ab-tests/00000000-0000-0000-0000-000000000000/duplicate", nil)

	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
	}
	for _, n := range []string{"ab_test.frozen", "ab_test.unfrozen", "alert.raised"} {
		if names[n] == 0 {
			t.Errorf("no %s event (%v)", n, names)
		}
	}
	if names["ab_test.frozen"] != 1 {
		t.Errorf("freezing twice recorded %d events", names["ab_test.frozen"])
	}
}
