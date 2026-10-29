package api_test

import (
	"fmt"
	"strings"
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

	// The last 28 days: 800 visitors, 10% of them signed up.
	if _, err := e.pool.Exec(t.Context(), `
		INSERT INTO customer_events (workspace_id, kind, name, anonymous_id, properties, context, occurred_at)
		SELECT $1::uuid, 'page', 'page.viewed', 'hist-' || i, '{}'::jsonb, jsonb_build_object('page', jsonb_build_object('path', CASE WHEN i % 2 = 0 THEN '/pricing' ELSE '/blog/post' END)), now() - interval '3 days'
		FROM generate_series(1, 800) i
		UNION ALL
		SELECT $1::uuid, 'track', 'signup_started', 'hist-' || i, '{}'::jsonb, '{}'::jsonb, now() - interval '2 days' FROM generate_series(1, 800) i WHERE i % 10 = 0`, ws); err != nil {
		t.Fatal(err)
	}
	// Planning: how long a test of the hero flag would take.
	owner.must(400, "POST", base+"/ab-tests/plan", map[string]any{"flag": "hero", "goal": map[string]any{"kind": "event", "name": "Signup"}})
	owner.must(400, "POST", base+"/ab-tests/plan", map[string]any{"flag": "hero", "goal": signup[0], "min_effect": 3})
	owner.must(400, "POST", base+"/ab-tests/plan", map[string]any{"flag": "nope", "goal": signup[0]})
	plan := owner.must(200, "POST", base+"/ab-tests/plan", map[string]any{"flag": "hero", "goal": signup[0]})
	tr, pl := plan["traffic"].(map[string]any), plan["plan"].(map[string]any)
	if tr["visitors"] != 800.0 || tr["converted"] != 80.0 || pl["baseline"] != 0.1 || pl["weekly_visitors"] != 200.0 || len(pl["rows"].([]any)) != 4 {
		t.Fatalf("plan: %v", plan)
	}
	// 100 visitors a week per version can't find +25% in 8 weeks.
	if pl["weeks"].(float64) <= 8 || plan["warning"] == "" || pl["detectable_8_weeks"].(float64) <= 0.25 {
		t.Fatalf("plan should warn: %v", plan)
	}
	onPricing := owner.must(200, "POST", base+"/ab-tests/plan", map[string]any{"flag": "hero", "goal": signup[0], "page": "/pricing", "min_effect": 0.5})
	if onPricing["traffic"].(map[string]any)["visitors"] != 400.0 || onPricing["plan"].(map[string]any)["min_effect"] != 0.5 {
		t.Fatalf("plan on /pricing: %v", onPricing)
	}
	nobody := owner.must(200, "POST", base+"/ab-tests/plan", map[string]any{"flag": "hero", "goal": map[string]any{"kind": "event", "name": "demo_booked"}})
	if !strings.Contains(nobody["warning"].(string), "No visitor reached this goal") {
		t.Fatalf("no conversions: %v", nobody)
	}

	// Starting turns the flag on, and records each goal's recent rate.
	ab = owner.must(200, "POST", base+"/ab-tests/"+id+"/start", nil)
	if ab["status"] != "running" || ab["started_at"] == nil {
		t.Fatalf("started: %v", ab)
	}
	if b, _ := ab["baselines"].([]any); len(b) != 2 || b[0] != 0.1 || b[1] != 0.0 {
		t.Fatalf("baselines: %v", ab["baselines"])
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

	// A breakdown by device: these visitors came through the API, with no
	// visit, so they are all "unknown".
	owner.must(400, "GET", base+"/ab-tests/"+id+"/breakdown?by=browser", nil)
	owner.must(409, "GET", base+"/ab-tests/"+again["id"].(string)+"/breakdown?by=device", nil)
	bd := owner.must(200, "GET", base+"/ab-tests/"+id+"/breakdown?by=device", nil)["breakdown"].(map[string]any)
	if segs := bd["segments"].([]any); len(segs) != 1 || segs[0].(map[string]any)["key"] != "unknown" || bd["corrected_for"] != 1.0 {
		t.Fatalf("breakdown: %v", bd)
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

// A test judged on revenue per visitor, with two versions against the
// control: results compare means, correct for the two versions, and mark
// the second goal exploratory; the planner plans in revenue too.
func TestABTestsRevenue(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/flags", map[string]any{"key": "checkout", "name": "Checkout",
		"variants": []map[string]any{{"key": "control", "weight": 34}, {"key": "bundle", "weight": 33}, {"key": "discount", "weight": 33}}})
	revenue := map[string]any{"kind": "event", "name": "order_completed", "metric": "value", "property": "revenue", "cap": 500}
	for _, g := range []map[string]any{
		{"kind": "event", "name": "order_completed", "metric": "value"},
		{"kind": "page", "path": "/thanks", "metric": "value"},
		{"kind": "deal", "metric": "count"},
		{"kind": "event", "name": "order_completed", "cap": 10},
	} {
		owner.must(400, "POST", base+"/ab-tests", map[string]any{"flag": "checkout", "goals": []any{g}})
	}
	ab := owner.must(201, "POST", base+"/ab-tests", map[string]any{"flag": "checkout",
		"goals": []any{revenue, map[string]any{"kind": "event", "name": "order_completed", "metric": "count"}}})
	id := ab["id"].(string)

	// Recent history for the planner: 1,000 visitors, 5% buying 100 each.
	if _, err := e.pool.Exec(t.Context(), `
		INSERT INTO customer_events (workspace_id, kind, name, anonymous_id, properties, context, occurred_at)
		SELECT $1::uuid, 'page', 'page.viewed', 'hist-' || i, '{}'::jsonb, '{"page": {"path": "/"}}'::jsonb, now() - interval '3 days' FROM generate_series(1, 1000) i
		UNION ALL
		SELECT $1::uuid, 'track', 'order_completed', 'hist-' || i, '{"revenue": 100}'::jsonb, '{}'::jsonb, now() - interval '2 days' FROM generate_series(1, 1000) i WHERE i % 20 = 0`, ws); err != nil {
		t.Fatal(err)
	}
	plan := owner.must(200, "POST", base+"/ab-tests/plan", map[string]any{"flag": "checkout", "goal": revenue})
	pl := plan["plan"].(map[string]any)
	if pl["metric"] != "value" || pl["baseline"] != 5.0 || pl["versions"] != 2.0 || plan["traffic"].(map[string]any)["sum"] != 5000.0 {
		t.Fatalf("revenue plan: %v", plan)
	}
	ab = owner.must(200, "POST", base+"/ab-tests/"+id+"/start", nil)
	if b := ab["baselines"].([]any); b[0] != 5.0 {
		t.Fatalf("baselines: %v", b)
	}

	// 3,000 visitors a version. Control and bundle: 5% buy 100. Discount:
	// 10% buy 100, and one buys 10,000, capped at 500.
	if _, err := e.pool.Exec(t.Context(), `
		WITH v AS (SELECT i, (ARRAY['control', 'bundle', 'discount'])[i % 3 + 1] AS variant FROM generate_series(1, 9000) i)
		INSERT INTO customer_events (workspace_id, kind, name, anonymous_id, properties, context, occurred_at)
		SELECT $1::uuid, 'track', 'experiment_viewed', 'v-' || i, jsonb_build_object('flag', 'checkout', 'variant', variant), '{}'::jsonb, now() FROM v
		UNION ALL
		SELECT $1::uuid, 'track', 'order_completed', 'v-' || i,
			jsonb_build_object('revenue', CASE WHEN i = 2 THEN 10000 ELSE 100 END), '{}'::jsonb, now() FROM v
		WHERE i % 60 < 3 OR (variant = 'discount' AND i % 60 < 6)`, ws); err != nil {
		t.Fatal(err)
	}
	res := owner.must(200, "GET", base+"/ab-tests/"+id+"/results", nil)
	r := res["result"].(map[string]any)
	if r["corrected_for"] != 2.0 {
		t.Fatalf("corrected_for: %v", r["corrected_for"])
	}
	goals := r["goals"].([]any)
	first, second := goals[0].(map[string]any), goals[1].(map[string]any)
	if first["metric"] != "value" || first["exploratory"] != false || second["exploratory"] != true || second["metric"] != "count" {
		t.Fatalf("goals: %v", goals)
	}
	byVariant := map[string]map[string]any{}
	for _, a := range first["arms"].([]any) {
		byVariant[a.(map[string]any)["variant"].(string)] = a.(map[string]any)
	}
	// 300 discount buyers: 299 × 100, and 500 for the capped one.
	if d := byVariant["discount"]; d["sum"] != 30400.0 || d["visitors"] != 3000.0 || d["conversions"] != 300.0 {
		t.Fatalf("discount arm: %v", d)
	}
	for _, c := range first["comparisons"].([]any) {
		c := c.(map[string]any)
		if p, raw := c["p_value"].(float64), c["raw_p_value"].(float64); p != min(1, raw*2) {
			t.Errorf("%s: p %v, raw %v", c["variant"], p, raw)
		}
	}
	if res["winner"] != "discount" {
		t.Fatalf("winner: %v", res["winner"])
	}
}
