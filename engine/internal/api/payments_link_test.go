package api_test

import (
	"context"
	"testing"
)

// TestOnePaymentCountedOnce covers the owner's decision of 2026-11-04: a
// payment counts once, whether it arrives as a transaction, an Order
// Completed event, or both, matched on the order id or else the same
// person, amount and currency within 15 minutes.
func TestOnePaymentCountedOnce(t *testing.T) {
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
	var ana string
	if err := e.pool.QueryRow(ctx, `SELECT record_id::text FROM record_identities WHERE workspace_id = $1 AND namespace = 'user_id' AND value = 'u1'`, ws).Scan(&ana); err != nil {
		t.Fatal(err)
	}
	order := func(props map[string]any, msg string) map[string]any {
		t.Helper()
		owner.must(200, "POST", events, map[string]any{"type": "track", "userId": "u1", "event": "Order Completed", "messageId": msg, "properties": props})
		var got map[string]any
		if err := e.pool.QueryRow(ctx, `SELECT properties FROM customer_events WHERE workspace_id = $1 AND message_id = $2`, ws, msg).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	payments := func() int {
		return count(`SELECT count(*) FROM customer_events WHERE workspace_id = $1 AND name = 'payment.succeeded'`, ws)
	}
	transactions := func() int { return count(`SELECT count(*) FROM transactions WHERE brand_id = $1`, brand) }

	// A transaction with the user's own id finds the person and counts as a
	// payment.
	pay := owner.must(201, "POST", base+"/transactions", map[string]any{"kind": "payment", "amount_minor": 4900, "currency": "USD",
		"source_id": "ch_1", "user_id": "u1"})
	if pay["record_id"] != ana || payments() != 1 {
		t.Fatalf("payment by user_id: %v, %d payments", pay, payments())
	}
	// Its Order Completed, with the same id: the same payment.
	if p := order(map[string]any{"order_id": "ch_1", "revenue": 49, "currency": "USD"}, "order-ch_1"); p["outflare_payment"] != "payment:"+brand+":ch_1" {
		t.Fatalf("order after its transaction: %v", p)
	}
	if transactions() != 1 || payments() != 1 {
		t.Fatalf("counted twice: %d transactions, %d payments", transactions(), payments())
	}

	// The order first, then its transaction.
	if p := order(map[string]any{"order_id": "ch_2", "revenue": "20.50"}, "order-ch_2"); p["outflare_payment"] != "payment:"+brand+":ch_2" {
		t.Fatalf("order as a payment: %v", p)
	}
	var amount int64
	var source string
	if err := e.pool.QueryRow(ctx, `SELECT amount_minor, source FROM transactions WHERE brand_id = $1 AND source_id = 'ch_2'`, brand).Scan(&amount, &source); err != nil || amount != 2050 || source != "events" {
		t.Fatalf("order's transaction: %d %s %v", amount, source, err)
	}
	same := owner.must(200, "POST", base+"/transactions", map[string]any{"kind": "payment", "amount_minor": 2050, "currency": "USD",
		"source_id": "ch_2", "user_id": "u1"})
	if same["source"] != "events" || transactions() != 2 || payments() != 2 {
		t.Fatalf("transaction after its order: %v, %d transactions, %d payments", same, transactions(), payments())
	}

	// No shared id: the same person, amount and currency within 15 minutes.
	order(map[string]any{"revenue": 15, "currency": "USD"}, "m9")
	if transactions() != 3 || payments() != 3 {
		t.Fatalf("order without an id: %d transactions, %d payments", transactions(), payments())
	}
	owner.must(200, "POST", base+"/transactions", map[string]any{"kind": "payment", "amount_minor": 1500, "currency": "USD",
		"source_id": "inv_9", "user_id": "u1"})
	// A different amount is another payment.
	owner.must(201, "POST", base+"/transactions", map[string]any{"kind": "payment", "amount_minor": 1600, "currency": "USD",
		"source_id": "inv_10", "user_id": "u1"})
	if transactions() != 4 || payments() != 4 {
		t.Fatalf("fuzzy match: %d transactions, %d payments", transactions(), payments())
	}

	// An order with no money is no payment; a refund comes off its payment.
	if p := order(map[string]any{"order_id": "free_1"}, "order-free_1"); p["outflare_payment"] != nil || transactions() != 4 {
		t.Fatalf("free order: %v", p)
	}
	owner.must(201, "POST", base+"/transactions", map[string]any{"kind": "refund", "amount_minor": 900, "currency": "USD",
		"source_id": "re_1", "refund_of": "ch_1", "user_id": "u1"})
	if n := count(`SELECT count(*) FROM customer_events WHERE workspace_id = $1 AND name = 'payment.refunded' AND properties->>'payment_id' = 'ch_1'`, ws); n != 1 {
		t.Fatalf("refund: %d", n)
	}

	// A transaction without a person counts once its order names them.
	owner.must(201, "POST", base+"/transactions", map[string]any{"kind": "payment", "amount_minor": 700, "currency": "USD", "source_id": "ch_3"})
	if payments() != 4 {
		t.Fatalf("payment with no person counted: %d", payments())
	}
	order(map[string]any{"order_id": "ch_3", "revenue": 7}, "order-ch_3")
	if transactions() != 6 || payments() != 5 {
		t.Fatalf("order naming the payer: %d transactions, %d payments", transactions(), payments())
	}
}
