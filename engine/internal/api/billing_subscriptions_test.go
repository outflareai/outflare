package api_test

import (
	"context"
	"testing"
	"time"
)

// A business that bills with its own system sends its subscriptions: each
// is saved once per source_id, its changes are on the person's timeline,
// and monthly revenue, status and plan on the record follow it.
func TestBillingSubscriptionsFromOwnBilling(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana"})["id"].(string)
	owner.must(201, "POST", base+"/products", map[string]any{"name": "Residential", "key": "resi"})
	count := func(name string) int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM customer_events WHERE workspace_id = $1 AND record_id = $2 AND name = $3`, ws, ana, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	attrs := func() map[string]any {
		t.Helper()
		return owner.must(200, "GET", base+"/records/"+ana, nil)["attributes"].(map[string]any)
	}

	for _, bad := range []map[string]any{
		{"amount_minor": 4900, "currency": "USD", "record_id": ana},
		{"source_id": "sub_1", "amount_minor": 4900, "currency": "USD"},
		{"source_id": "sub_1", "amount_minor": 4900, "currency": "USD", "record_id": ana, "status": "paused"},
		{"source_id": "sub_1", "amount_minor": 4900, "currency": "USD", "record_id": ana, "interval": "quarter"},
		{"source_id": "sub_1", "amount_minor": -1, "currency": "USD", "record_id": ana},
		{"source_id": "sub_1", "amount_minor": 4900, "currency": "dollars", "record_id": ana},
		{"source_id": "sub_1", "amount_minor": 4900, "currency": "USD", "record_id": ana, "product": "nope"},
		{"source_id": "sub_1", "amount_minor": 4900, "currency": "USD", "user_id": "nobody-knows-me"},
	} {
		owner.must(400, "POST", base+"/billing-subscriptions", bad)
	}

	// History: a yearly plan that began three months ago.
	began := time.Now().Add(-90 * 24 * time.Hour).UTC().Truncate(time.Second)
	sub := owner.must(201, "POST", base+"/billing-subscriptions", map[string]any{"source_id": "sub_1", "record_id": ana, "plan": "Pro",
		"product": "resi", "price_id": "pro_yearly", "interval": "year", "amount_minor": 58800, "currency": "usd", "started_at": began})
	if sub["monthly_minor"] != 4900.0 || sub["status"] != "active" || sub["provider"] != "api" || sub["product"] != "resi" || sub["currency"] != "USD" {
		t.Fatalf("saved: %v", sub)
	}
	if count("subscription.started") != 1 {
		t.Fatalf("started events: %d", count("subscription.started"))
	}
	if a := attrs(); a["billing_status"] != "active" || a["billing_plan"] != "Pro" {
		t.Fatalf("record after start: %v", a)
	}

	// Sent again unchanged: the same one, and nothing new on the timeline.
	same := owner.must(200, "POST", base+"/billing-subscriptions", map[string]any{"source_id": "sub_1", "record_id": ana, "plan": "Pro",
		"product": "resi", "price_id": "pro_yearly", "interval": "year", "amount_minor": 58800, "currency": "USD", "started_at": began})
	if same["id"] != sub["id"] || count("subscription.started") != 1 || count("subscription.changed") != 0 {
		t.Fatalf("resent: %v, %d changed", same, count("subscription.changed"))
	}

	// An upgrade to a monthly plan is a change.
	up := owner.must(200, "POST", base+"/billing-subscriptions", map[string]any{"source_id": "sub_1", "record_id": ana, "plan": "Scale",
		"price_id": "scale_monthly", "amount_minor": 9900, "currency": "USD"})
	if up["monthly_minor"] != 9900.0 || up["plan"] != "Scale" || count("subscription.changed") != 1 {
		t.Fatalf("upgrade: %v, %d changed", up, count("subscription.changed"))
	}
	if a := attrs(); a["billing_plan"] != "Scale" {
		t.Fatalf("record after upgrade: %v", a)
	}

	// Canceled: once, and the record says so.
	for range 2 {
		owner.must(200, "POST", base+"/billing-subscriptions", map[string]any{"source_id": "sub_1", "record_id": ana, "plan": "Scale",
			"price_id": "scale_monthly", "amount_minor": 9900, "currency": "USD", "status": "canceled"})
	}
	if count("subscription.canceled") != 1 || count("subscription.changed") != 1 {
		t.Fatalf("canceled: %d canceled, %d changed", count("subscription.canceled"), count("subscription.changed"))
	}
	if a := attrs(); a["billing_status"] != "canceled" {
		t.Fatalf("record after cancel: %v", a)
	}

	list := owner.must(200, "GET", base+"/billing-subscriptions?record="+ana, nil)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["status"] != "canceled" || list[0].(map[string]any)["canceled_at"] == nil {
		t.Fatalf("list: %v", list)
	}
	if got := owner.must(200, "GET", base+"/billing-subscriptions?status=active", nil)["data"].([]any); len(got) != 0 {
		t.Fatalf("active list: %v", got)
	}
	owner.must(400, "GET", base+"/billing-subscriptions?status=paused", nil)
}
