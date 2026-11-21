package api_test

import (
	"context"
	"testing"
)

// A failed payment and a chargeback each write one event on the person's
// timeline, once, and neither counts as money.
func TestPaymentFailedAndDisputedEvents(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana"})["id"].(string)
	count := func(name string) int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM customer_events WHERE workspace_id = $1 AND record_id = $2 AND name = $3`, ws, ana, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// A declined card: payment.failed, and no money.
	failed := map[string]any{"kind": "payment", "amount_minor": 4900, "currency": "USD", "source_id": "inv_1", "record_id": ana, "status": "failed"}
	owner.must(201, "POST", base+"/transactions", failed)
	owner.must(200, "POST", base+"/transactions", failed)
	if count("payment.failed") != 1 || count("payment.succeeded") != 0 {
		t.Fatalf("failed: %d failed, %d succeeded", count("payment.failed"), count("payment.succeeded"))
	}
	var props map[string]any
	if err := e.pool.QueryRow(ctx, `SELECT properties FROM customer_events WHERE workspace_id = $1 AND record_id = $2 AND name = 'payment.failed'`, ws, ana).Scan(&props); err != nil {
		t.Fatal(err)
	}
	if props["payment_id"] != "inv_1" || props["amount_minor"] != 4900.0 || props["currency"] != "USD" {
		t.Fatalf("payment.failed properties: %v", props)
	}
	// The retry goes through: the payment counts, and the failure stays in the history.
	failed["status"] = "succeeded"
	owner.must(200, "POST", base+"/transactions", failed)
	if count("payment.failed") != 1 || count("payment.succeeded") != 1 {
		t.Fatalf("retried: %d failed, %d succeeded", count("payment.failed"), count("payment.succeeded"))
	}

	// A chargeback: payment.disputed, and its money comes off as a refund.
	dispute := map[string]any{"kind": "chargeback", "amount_minor": 4900, "currency": "USD", "source_id": "dp_1", "record_id": ana, "refund_of": "inv_1"}
	owner.must(201, "POST", base+"/transactions", dispute)
	owner.must(200, "POST", base+"/transactions", dispute)
	if count("payment.disputed") != 1 || count("payment.refunded") != 1 {
		t.Fatalf("disputed: %d disputed, %d refunded", count("payment.disputed"), count("payment.refunded"))
	}
	// A plain refund is not a dispute.
	owner.must(201, "POST", base+"/transactions", map[string]any{"kind": "payment", "amount_minor": 900, "currency": "USD", "source_id": "inv_2", "record_id": ana})
	owner.must(201, "POST", base+"/transactions", map[string]any{"kind": "refund", "amount_minor": 900, "currency": "USD", "source_id": "re_1", "record_id": ana, "refund_of": "inv_2"})
	if count("payment.disputed") != 1 || count("payment.failed") != 1 {
		t.Fatalf("refund: %d disputed, %d failed", count("payment.disputed"), count("payment.failed"))
	}
	// Nobody to put it on: nothing is written.
	owner.must(201, "POST", base+"/transactions", map[string]any{"kind": "payment", "amount_minor": 100, "currency": "USD", "source_id": "inv_3", "status": "failed"})
	if count("payment.failed") != 1 {
		t.Fatalf("no person: %d", count("payment.failed"))
	}
}
