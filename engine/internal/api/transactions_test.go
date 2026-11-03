package api_test

import (
	"context"
	"math"
	"testing"
)

// TestTransactions covers the owner's decision of 2026-11-02: one row per
// payment, refund, chargeback or credit per brand, saved once per source
// id, converted to the brand's currency, listed per person, in Query, and
// recorded for webhooks.
func TestTransactions(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana"})["id"].(string)
	bo := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Bo"})["id"].(string)
	owner.must(201, "POST", base+"/products", map[string]any{"name": "Residential", "key": "resi"})

	for _, bad := range []map[string]any{
		{"kind": "gift", "amount_minor": 100, "currency": "USD", "source_id": "x"},
		{"kind": "payment", "amount_minor": -1, "currency": "USD", "source_id": "x"},
		{"kind": "payment", "amount_minor": 100, "currency": "dollars", "source_id": "x"},
		{"kind": "payment", "amount_minor": 100, "currency": "USD"},
		{"kind": "payment", "amount_minor": 100, "currency": "USD", "source_id": "x", "refund_of": "y"},
		{"kind": "payment", "amount_minor": 100, "currency": "USD", "source_id": "x", "product": "nope"},
	} {
		owner.must(400, "POST", base+"/transactions", bad)
	}

	// A payment in the brand's currency: converted as it is.
	pay := owner.must(201, "POST", base+"/transactions", map[string]any{"kind": "payment", "amount_minor": 4900, "currency": "usd",
		"source_id": "inv_1", "record_id": ana, "product": "resi", "status": "pending", "description": "Pro plan"})
	if pay["brand_amount_minor"] != 4900.0 || pay["brand_currency"] != "USD" || pay["product_id"] == nil || pay["status"] != "pending" {
		t.Fatalf("payment: %v", pay)
	}
	// Sent again: the same one, with its new status.
	again := owner.must(200, "POST", base+"/transactions", map[string]any{"kind": "payment", "amount_minor": 4900, "currency": "USD",
		"source_id": "inv_1", "record_id": ana, "status": "succeeded"})
	if again["id"] != pay["id"] || again["status"] != "succeeded" || again["updated_at"] == nil {
		t.Fatalf("again: %v", again)
	}
	owner.must(200, "POST", base+"/transactions", map[string]any{"kind": "payment", "amount_minor": 4900, "currency": "USD", "source_id": "inv_1", "status": "succeeded"})
	owner.must(201, "POST", base+"/transactions", map[string]any{"kind": "refund", "amount_minor": 900, "currency": "USD", "source_id": "re_1",
		"record_id": ana, "refund_of": "inv_1"})

	// Another currency, before its rate is known: converted later.
	sek := owner.must(201, "POST", base+"/transactions", map[string]any{"kind": "payment", "amount_minor": 10000, "currency": "SEK",
		"source_id": "inv_2", "record_id": bo})
	var rate float64
	if err := e.pool.QueryRow(ctx, `SELECT usd_per_unit::float8 FROM exchange_rates WHERE currency = 'SEK' AND day > current_date - 10 ORDER BY day DESC LIMIT 1`).Scan(&rate); err == nil {
		t.Skip("a SEK rate is already stored: the test needs one missing")
	}
	if sek["brand_amount_minor"] != nil {
		t.Fatalf("converted with no rate: %v", sek)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO exchange_rates (currency, day, usd_per_unit) VALUES ('SEK', current_date, 0.095)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		e.pool.Exec(context.Background(), `DELETE FROM exchange_rates WHERE currency = 'SEK' AND day = current_date`)
	}) //nolint:errcheck
	if n, err := e.srv.ConvertTransactions(ctx); err != nil || n < 1 {
		t.Fatalf("convert: %d %v", n, err)
	}
	got := owner.must(200, "GET", base+"/transactions/"+sek["id"].(string), nil)
	if v, _ := got["brand_amount_minor"].(float64); math.Abs(v-950) > 1 {
		t.Fatalf("100 SEK at 0.095: %v", got["brand_amount_minor"])
	}

	// Lists: all, one person's, some kinds, paged.
	if l := owner.must(200, "GET", base+"/transactions", nil)["data"].([]any); len(l) != 3 {
		t.Fatalf("all: %v", l)
	}
	if l := owner.must(200, "GET", base+"/transactions?record="+ana, nil)["data"].([]any); len(l) != 2 {
		t.Fatalf("Ana's: %v", l)
	}
	if l := owner.must(200, "GET", base+"/transactions?kind=refund", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("refunds: %v", l)
	}
	page := owner.must(200, "GET", base+"/transactions?limit=2", nil)
	if page["next_cursor"] == nil {
		t.Fatalf("paging: %v", page)
	}
	if rest := owner.must(200, "GET", base+"/transactions?limit=2&cursor="+page["next_cursor"].(string), nil)["data"].([]any); len(rest) != 1 {
		t.Fatalf("second page: %v", rest)
	}
	owner.must(400, "GET", base+"/transactions?kind=gift", nil)
	owner.must(404, "GET", base+"/transactions/00000000-0000-0000-0000-000000000000", nil)

	// Audit actions (the webhooks): one created per transaction, one
	// updated for the status change.
	var created, updated int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE action = 'transaction.created'), count(*) FILTER (WHERE action = 'transaction.updated')
		FROM audit_events WHERE workspace_id = $1`, ws).Scan(&created, &updated); err != nil || created != 3 || updated != 1 {
		t.Fatalf("audit: %d created, %d updated, %v", created, updated, err)
	}

	// Query has them.
	q := owner.must(200, "POST", base+"/query", map[string]any{"sql": "SELECT kind, amount_minor FROM transactions ORDER BY amount_minor"})
	if q["row_count"] != 3.0 {
		t.Fatalf("query: %v", q)
	}

	// The brand's currency: new transactions convert to it.
	b := owner.must(200, "PATCH", "/v1/workspaces/"+ws+"/brands/"+brand, map[string]any{"currency": "sek"})
	if b["currency"] != "SEK" {
		t.Fatalf("brand currency: %v", b)
	}
	owner.must(400, "PATCH", "/v1/workspaces/"+ws+"/brands/"+brand, map[string]any{"currency": "krona"})
	in := owner.must(201, "POST", base+"/transactions", map[string]any{"kind": "credit", "amount_minor": 500, "currency": "SEK", "source_id": "cr_1"})
	if in["brand_currency"] != "SEK" || in["brand_amount_minor"] != 500.0 {
		t.Fatalf("in the brand's currency: %v", in)
	}
}
