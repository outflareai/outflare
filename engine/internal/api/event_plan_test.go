package api_test

import (
	"testing"
)

func TestEventPlanProblems(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	var brand string
	if err := e.pool.QueryRow(t.Context(), `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	send := func(events ...map[string]any) {
		t.Helper()
		if b := owner.must(200, "POST", "/v1/workspaces/"+ws+"/customer-events", map[string]any{"batch": events}); b["accepted"] != float64(len(events)) {
			t.Fatalf("send: %v", b)
		}
	}
	problems := func() map[string]float64 {
		t.Helper()
		out := map[string]float64{}
		for _, p := range owner.must(200, "GET", base+"/catalog/event-problems", nil)["data"].([]any) {
			p := p.(map[string]any)
			out[p["event_name"].(string)+" "+p["kind"].(string)+" "+p["property"].(string)] = p["events"].(float64)
		}
		return out
	}
	order := func(props map[string]any) map[string]any {
		return map[string]any{"type": "track", "userId": "u1", "event": "Order Completed", "properties": props}
	}

	// Without a plan nothing is a problem.
	send(order(map[string]any{"plan": "pro", "coupon": "X"}))
	if got := problems(); len(got) != 0 {
		t.Fatalf("an event with no listed properties has no plan: %v", got)
	}

	// The plan: plan and value are required, note is optional.
	def := owner.must(200, "PATCH", base+"/catalog/events/order_completed", map[string]any{"properties": []map[string]any{
		{"key": "plan", "value_type": "text", "required": true},
		{"key": "value", "value_type": "number", "required": true},
		{"key": "note", "value_type": "text"},
	}})
	if props := def["properties"].([]any); props[0].(map[string]any)["required"] != true || props[2].(map[string]any)["required"] != false {
		t.Fatalf("required is kept: %v", def)
	}
	send(
		order(map[string]any{"plan": "pro", "value": 49}),
		order(map[string]any{"plan": "pro", "coupon": "X"}),
		order(map[string]any{"plan": nil, "value": 20, "coupon": "Y", "utm": "a"}),
		map[string]any{"type": "track", "userId": "u1", "event": "Signed Up", "properties": map[string]any{"anything": 1}},
	)
	want := map[string]float64{
		"order_completed missing_property plan":     1,
		"order_completed missing_property value":    1,
		"order_completed unplanned_property coupon": 2,
		"order_completed unplanned_property utm":    1,
	}
	got := problems()
	if len(got) != len(want) {
		t.Fatalf("problems: %v", got)
	}
	for k, n := range want {
		if got[k] != n {
			t.Fatalf("%s: got %v, want %v (%v)", k, got[k], n, got)
		}
	}
	// A second batch adds to the day's counts.
	send(order(map[string]any{"value": 5}))
	if got := problems(); got["order_completed missing_property plan"] != 2 {
		t.Fatalf("counts add up: %v", got)
	}

	// Listing coupon and no longer requiring value clears those problems.
	owner.must(200, "PATCH", base+"/catalog/events/order_completed", map[string]any{"properties": []map[string]any{
		{"key": "plan", "value_type": "text", "required": true},
		{"key": "value", "value_type": "number"},
		{"key": "coupon", "value_type": "text"},
	}})
	got = problems()
	if len(got) != 2 || got["order_completed missing_property plan"] != 2 || got["order_completed unplanned_property utm"] != 1 {
		t.Fatalf("after changing the plan: %v", got)
	}

	owner.must(400, "GET", base+"/catalog/event-problems?days=0", nil)
	owner.must(404, "GET", "/v1/brands/00000000-0000-4000-8000-000000000000/catalog/event-problems", nil)
}
