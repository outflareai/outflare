package api_test

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// A random split's paths are compared with its holdout: who converted
// within the window, their revenue, and whether the difference is real.
func TestJourneyLift(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	exec := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	j := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Nurture", "steps": []map[string]any{
		{"id": "wait", "kind": "wait", "wait_seconds": 60, "next": "test"},
		{"id": "test", "kind": "split", "split_by": "random", "paths": []map[string]any{
			{"id": "nurture", "weight": 80, "next": "later"}, {"id": "control", "weight": 20, "holdout": true}}},
		{"id": "later", "kind": "wait", "wait_seconds": 60},
	}})
	jid, brand := j["id"].(string), j["brand_id"].(string)
	lift := "/v1/brands/" + brand + "/journeys/" + jid + "/lift"

	at := time.Now().Add(-10 * 24 * time.Hour)
	// people takes n people down a path, the first conv of them converting
	// with a payment of cents each, after days.
	people := func(path string, n, conv int, cents int, after time.Duration) {
		t.Helper()
		for i := 0; i < n; i++ {
			rec := exec(`INSERT INTO records (workspace_id, record_type, name) VALUES ($1, 'person', $2) RETURNING id::text`, ws, fmt.Sprintf("%s %d", path, i))
			run := exec(`INSERT INTO journey_runs (workspace_id, journey_id, record_id, status) VALUES ($1, $2, $3, 'completed') RETURNING id::text`, ws, jid, rec)
			exec(`INSERT INTO journey_run_steps (workspace_id, run_id, journey_id, step, kind, outcome, at) VALUES ($1, $2, $3, 'test', 'split', $4, $5) RETURNING id::text`,
				ws, run, jid, path, at)
			if i < conv {
				ev := exec(`INSERT INTO customer_events (workspace_id, record_id, kind, name, occurred_at) VALUES ($1, $2, 'track', 'payment.succeeded', $3) RETURNING id::text`, ws, rec, at.Add(after))
				exec(`INSERT INTO attribution_conversions (event_id, workspace_id, record_id, name, occurred_at, value_minor, currency, touches, matched_by)
					VALUES ($1, $2, $3, 'payment.succeeded', $4, $5, 'USD', 0, 'record') RETURNING event_id::text`, ev, ws, rec, at.Add(after), cents)
			}
		}
	}
	people("nurture", 200, 60, 1000, 36*time.Hour)
	people("control", 100, 10, 1000, 36*time.Hour)
	// Before the split, and long after it: neither counts.
	rec := exec(`SELECT jr.record_id::text FROM journey_runs jr JOIN journey_run_steps rs ON rs.run_id = jr.id WHERE rs.outcome = 'control' ORDER BY jr.id LIMIT 1 OFFSET 50`)
	for _, d := range []time.Duration{-time.Hour, 40 * 24 * time.Hour} {
		ev := exec(`INSERT INTO customer_events (workspace_id, record_id, kind, name, occurred_at) VALUES ($1, $2, 'track', 'signed_up', $3) RETURNING id::text`, ws, rec, at.Add(d))
		exec(`INSERT INTO attribution_conversions (event_id, workspace_id, record_id, name, occurred_at, touches, matched_by)
			VALUES ($1, $2, $3, 'signed_up', $4, 0, 'record') RETURNING event_id::text`, ev, ws, rec, at.Add(d))
	}

	got := owner.must(200, "GET", lift+"?step=test", nil)
	if got["control"] != "control" || got["days"] != 30.0 {
		t.Fatalf("lift = %v", got)
	}
	paths := got["paths"].([]any)
	n, c := paths[0].(map[string]any), paths[1].(map[string]any)
	usd := func(v any) float64 {
		for _, m := range v.([]any) {
			if m.(map[string]any)["currency"] == "USD" {
				return m.(map[string]any)["amount_cents"].(float64)
			}
		}
		return 0
	}
	// 30% against 10%: two times better, and clearly so; 60 payments
	// against the 20 that 200 control people would have made.
	if n["id"] != "nurture" || n["people"] != 200.0 || n["converted"] != 60.0 || n["rate"] != 0.3 || n["lift"] != 2.0 ||
		n["verdict"] != "better" || n["p_value"].(float64) >= 0.05 || usd(n["revenue"]) != 60000 || usd(n["incremental_revenue"]) != 40000 {
		t.Fatalf("nurture = %v", n)
	}
	if c["id"] != "control" || !c["holdout"].(bool) || c["people"] != 100.0 || c["converted"] != 10.0 || c["lift"] != nil || c["verdict"] != nil || c["incremental_revenue"] != nil {
		t.Fatalf("control = %v", c)
	}
	// A window that ends before anyone paid, and an event nobody did.
	for _, q := range []string{"&days=1", "&event=upgraded"} {
		p := owner.must(200, "GET", lift+"?step=test"+q, nil)["paths"].([]any)[0].(map[string]any)
		if p["converted"] != 0.0 || p["verdict"] != "too_early" {
			t.Fatalf("%s: %v", q, p)
		}
	}
	// Bad input.
	owner.must(400, "GET", lift+"?step=wait", nil)
	owner.must(400, "GET", lift+"?step=test&control=nope", nil)
	owner.must(400, "GET", lift+"?step=test&days=181", nil)
	owner.must(404, "GET", "/v1/brands/"+brand+"/journeys/00000000-0000-0000-0000-000000000000/lift?step=test", nil)
}
