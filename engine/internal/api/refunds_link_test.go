package api_test

import (
	"context"
	"testing"
)

// TestRefundsFindTheirPayment: an Order Refunded event, or a refund sent to
// the API, finds its payment by any id the developer has for it, and a
// refund whose payment isn't in yet waits, then counts when it arrives.
func TestRefundsFindTheirPayment(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	events := "/v1/workspaces/" + ws + "/customer-events"
	owner.must(200, "POST", events, map[string]any{"type": "identify", "userId": "u1", "traits": map[string]any{"name": "Ana"}})
	track := func(name, msg string, props map[string]any) map[string]any {
		t.Helper()
		owner.must(200, "POST", events, map[string]any{"type": "track", "userId": "u1", "event": name, "messageId": msg, "properties": props})
		var got map[string]any
		if err := e.pool.QueryRow(ctx, `SELECT properties FROM customer_events WHERE workspace_id = $1 AND message_id = $2`, ws, msg).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	refunded := func(payment string) (n int, minor int64) {
		t.Helper()
		if err := e.pool.QueryRow(ctx, `SELECT count(*), COALESCE(sum((properties->>'amount_minor')::bigint), 0) FROM customer_events
			WHERE workspace_id = $1 AND name = 'payment.refunded' AND properties->>'payment_id' = $2`, ws, payment).Scan(&n, &minor); err != nil {
			t.Fatal(err)
		}
		return n, minor
	}

	// A full refund by order_id, with no amount: the whole payment.
	track("Order Completed", "order-in_1", map[string]any{"order_id": "in_1", "revenue": 49, "currency": "USD"})
	if p := track("Order Refunded", "refund-re_1", map[string]any{"order_id": "in_1", "refund_id": "re_1"}); p["outflare_refund"] == nil {
		t.Fatalf("refund event not linked: %v", p)
	}
	if n, minor := refunded("in_1"); n != 1 || minor != 4900 {
		t.Fatalf("full refund: %d, %d", n, minor)
	}
	// The same refund again counts nothing more.
	track("Order Refunded", "refund-re_1-again", map[string]any{"order_id": "in_1", "refund_id": "re_1"})
	if n, _ := refunded("in_1"); n != 1 {
		t.Fatalf("resent refund: %d", n)
	}

	// A partial refund naming the order by its messageId.
	track("Order Completed", "order-in_2", map[string]any{"order_id": "in_2", "revenue": "20.00", "currency": "USD"})
	track("Order Refunded", "refund-re_2", map[string]any{"order_id": "order-in_2", "amount": 5})
	if n, minor := refunded("in_2"); n != 1 || minor != 500 {
		t.Fatalf("partial refund by messageId: %d, %d", n, minor)
	}

	// An API refund naming the payment by Outflare's transaction id.
	pay := owner.must(201, "POST", base+"/transactions", map[string]any{"kind": "payment", "amount_minor": 1000, "currency": "USD",
		"source_id": "in_3", "user_id": "u1"})
	r := owner.must(201, "POST", base+"/transactions", map[string]any{"kind": "refund", "amount_minor": 1000, "currency": "USD",
		"source_id": "re_3", "refund_of": pay["id"], "user_id": "u1"})
	if r["refund_of"] != "in_3" {
		t.Fatalf("refund_of kept as sent: %v", r["refund_of"])
	}
	if n, _ := refunded("in_3"); n != 1 {
		t.Fatalf("refund by transaction id: %d", n)
	}

	// A refund sent both as a transaction and as an event counts once:
	// matched on its refund id, or else the same payment and amount.
	owner.must(201, "POST", base+"/transactions", map[string]any{"kind": "payment", "amount_minor": 2100, "currency": "USD",
		"source_id": "in_5", "user_id": "u1"})
	owner.must(201, "POST", base+"/transactions", map[string]any{"kind": "refund", "amount_minor": 800, "currency": "USD",
		"source_id": "re_5", "refund_of": "in_5", "user_id": "u1"})
	track("Order Refunded", "refund-re_5", map[string]any{"order_id": "in_5", "refund_id": "re_5", "amount": 8, "currency": "USD"})
	track("Order Refunded", "refund-other", map[string]any{"order_id": "in_5", "amount": 8, "currency": "USD"})
	if n, minor := refunded("in_5"); n != 1 || minor != 800 {
		t.Fatalf("refund sent both ways: %d, %d", n, minor)
	}
	track("Order Completed", "order-in_6", map[string]any{"order_id": "in_6", "revenue": 11, "currency": "USD"})
	track("Order Refunded", "refund-re_6", map[string]any{"order_id": "in_6", "refund_id": "re_6"})
	owner.must(200, "POST", base+"/transactions", map[string]any{"kind": "refund", "amount_minor": 1100, "currency": "USD",
		"source_id": "re_6", "refund_of": "order-in_6", "user_id": "u1"})
	if n, _ := refunded("in_6"); n != 1 {
		t.Fatalf("event then transaction: %d", n)
	}

	// A refund before its payment waits, then counts when the payment arrives.
	track("Order Refunded", "refund-re_4", map[string]any{"order_id": "in_4", "amount": 3, "currency": "USD"})
	if n, _ := refunded("in_4"); n != 0 {
		t.Fatalf("refund with no payment counted: %d", n)
	}
	track("Order Completed", "order-in_4", map[string]any{"order_id": "in_4", "revenue": 30, "currency": "USD"})
	if n, minor := refunded("in_4"); n != 1 || minor != 300 {
		t.Fatalf("waiting refund: %d, %d", n, minor)
	}

	// A refund naming an id no payment has is listed in Developer setup,
	// and counts once it is linked by hand.
	track("Order Completed", "order-in_7", map[string]any{"order_id": "in_7", "revenue": 12, "currency": "USD"})
	lost := owner.must(201, "POST", base+"/transactions", map[string]any{"kind": "refund", "amount_minor": 1200, "currency": "USD",
		"source_id": "re_7", "refund_of": "typo_7", "user_id": "u1"})
	setup := owner.must(200, "GET", base+"/developer-setup", nil)
	unmatched := setup["unmatched_refunds"].([]any)
	if len(unmatched) != 1 || unmatched[0].(map[string]any)["id"] != lost["id"] {
		t.Fatalf("unmatched refunds: %v", unmatched)
	}
	statuses := map[string]string{}
	for _, c := range setup["checks"].([]any) {
		statuses[c.(map[string]any)["key"].(string)] = c.(map[string]any)["status"].(string)
	}
	if statuses["refunds"] != "warning" || statuses["order_completed"] != "ok" || statuses["signed_up"] != "missing" || statuses["payments"] != "ok" {
		t.Fatalf("checks: %v", statuses)
	}
	link := base + "/transactions/" + lost["id"].(string) + "/refund-of"
	owner.must(400, "POST", link, map[string]any{"payment": "still-wrong"})
	if got := owner.must(200, "POST", link, map[string]any{"payment": "order-in_7"}); got["refund_of"] != "in_7" {
		t.Fatalf("linked: %v", got)
	}
	if n, _ := refunded("in_7"); n != 1 {
		t.Fatalf("linked refund counted: %d", n)
	}
	if u := owner.must(200, "GET", base+"/developer-setup", nil)["unmatched_refunds"].([]any); len(u) != 0 {
		t.Fatalf("still unmatched: %v", u)
	}
}
