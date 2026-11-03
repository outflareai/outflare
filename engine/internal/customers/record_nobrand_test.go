package customers_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
)

// A workspace without a brand still records events: the event has no brand,
// and the lifecycle and metric steps, which need one, are skipped.
func TestRecordWithoutBrand(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ws := workspace(t, pool)
	if _, err := pool.Exec(ctx, `DELETE FROM brands WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	var rec string
	if err := pool.QueryRow(ctx, `INSERT INTO records (workspace_id, record_type, name) VALUES ($1, 'person', 'Kim') RETURNING id::text`, ws).Scan(&rec); err != nil {
		t.Fatal(err)
	}
	inTx(t, pool, func(tx pgx.Tx) error {
		ok, err := customers.Record(ctx, tx, ws, customers.Event{RecordID: rec, Name: "meeting.booked", Source: "meetings", OccurredAt: time.Now(),
			MessageID: "meeting:test:booked", Product: "pro"})
		if err != nil || !ok {
			t.Fatalf("record: %v %v", ok, err)
		}
		return nil
	})
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM customer_events WHERE workspace_id = $1 AND brand_id IS NULL`, ws).Scan(&n); err != nil || n != 1 {
		t.Fatalf("events: %d %v", n, err)
	}
}
