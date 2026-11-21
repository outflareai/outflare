package api_test

import (
	"testing"
)

func TestEventCounts(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	var brand string
	if err := e.pool.QueryRow(t.Context(), `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	track := func(user, event string) map[string]any {
		return map[string]any{"type": "track", "userId": user, "event": event}
	}
	owner.must(200, "POST", "/v1/workspaces/"+ws+"/customer-events", map[string]any{"batch": []map[string]any{
		track("u1", "Signed Up"), track("u2", "Signed Up"), track("u1", "Order Completed"), track("u1", "Order Completed"), track("u1", "Order Completed"),
	}})
	// One old event, outside a 7-day range but inside 30 days.
	if _, err := e.pool.Exec(t.Context(), `UPDATE customer_events SET occurred_at = now() - interval '10 days'
		WHERE id = (SELECT id FROM customer_events WHERE brand_id = $1 AND name = 'order_completed' LIMIT 1)`, brand); err != nil {
		t.Fatal(err)
	}
	counts := func(q string) (map[string][2]float64, []string) {
		t.Helper()
		out := map[string][2]float64{}
		var order []string
		for _, c := range owner.must(200, "GET", base+"/catalog/event-counts"+q, nil)["data"].([]any) {
			c := c.(map[string]any)
			out[c["name"].(string)] = [2]float64{c["events"].(float64), c["people"].(float64)}
			order = append(order, c["name"].(string))
		}
		return out, order
	}
	got, order := counts("")
	if got["order_completed"] != [2]float64{3, 1} || got["signed_up"] != [2]float64{2, 2} || order[0] != "order_completed" {
		t.Fatalf("30 days: %v %v", got, order)
	}
	if got, _ := counts("?days=7"); got["order_completed"] != [2]float64{2, 1} {
		t.Fatalf("7 days leaves out the old one: %v", got)
	}
	for _, c := range owner.must(200, "GET", base+"/catalog/event-counts", nil)["data"].([]any) {
		if src := c.(map[string]any)["sources"].([]any); len(src) != 1 || src[0].(map[string]any)["source"] == "" {
			t.Fatalf("one source each: %v", c)
		}
	}
	owner.must(400, "GET", base+"/catalog/event-counts?days=0", nil)
	owner.must(400, "GET", base+"/catalog/event-counts?days=366", nil)
	owner.must(404, "GET", "/v1/brands/00000000-0000-4000-8000-000000000000/catalog/event-counts", nil)
}
