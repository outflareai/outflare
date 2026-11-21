package api_test

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestPaymentChecks covers Developer setup's checks that payments sent from
// a brand's server are kept once and as sent (docs/developer-setup.md), and
// merging the payments saved twice before both ways took turns on an id.
func TestPaymentChecks(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	events := "/v1/workspaces/" + ws + "/customer-events"
	for _, u := range []string{"u1", "u2", "u3", "u4", "u5", "u6", "u7"} {
		owner.must(200, "POST", events, map[string]any{"type": "identify", "userId": u})
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	check := func(key string) map[string]any {
		t.Helper()
		for _, c := range owner.must(200, "GET", base+"/developer-setup", nil)["checks"].([]any) {
			if c := c.(map[string]any); c["key"] == key {
				return c
			}
		}
		t.Fatalf("no check %s", key)
		return nil
	}
	want := func(key, status, detail string) {
		t.Helper()
		c := check(key)
		if c["status"] != status || !strings.Contains(c["detail"].(string), detail) {
			t.Fatalf("%s: %v", key, c)
		}
	}
	pay := func(code int, body map[string]any) map[string]any {
		t.Helper()
		body["kind"], body["currency"] = "payment", "USD"
		return owner.must(code, "POST", base+"/transactions", body)
	}
	for _, k := range []string{"order_ids", "matched_by_id", "duplicates", "ids_reused", "retries", "amounts"} {
		want(k, "ok", "")
	}

	// A payment saved twice (as the race fixed in #1185 did): merged, kept
	// once with its person, counted once.
	pay(201, map[string]any{"amount_minor": 4900, "source_id": "in_dup", "user_id": "u1"})
	if _, err := e.pool.Exec(ctx, `INSERT INTO transactions (workspace_id, brand_id, kind, status, amount_minor, currency, brand_currency, source, source_id, occurred_at)
		VALUES ($1, $2, 'payment', 'succeeded', 4900, 'USD', 'USD', 'events', 'in_dup', now())`, ws, brand); err != nil {
		t.Fatal(err)
	}
	want("duplicates", "warning", "1 payment or refund was saved twice")
	if got := owner.must(200, "POST", base+"/developer-setup/merge-duplicates", nil); got["merged"] != float64(1) {
		t.Fatalf("merge: %v", got)
	}
	if n := count(`SELECT count(*) FROM transactions WHERE brand_id = $1 AND source_id = 'in_dup' AND record_id IS NOT NULL`, brand); n != 1 ||
		count(`SELECT count(*) FROM transactions WHERE brand_id = $1 AND source_id = 'in_dup'`, brand) != 1 {
		t.Fatalf("after merge: %d with a person", n)
	}
	if n := count(`SELECT count(*) FROM customer_events WHERE workspace_id = $1 AND name = 'payment.succeeded' AND properties->>'payment_id' = 'in_dup'`, ws); n != 1 {
		t.Fatalf("counted %d times", n)
	}
	if n := count(`SELECT count(*) FROM deleted_rows WHERE workspace_id = $1 AND source_table = 'transactions'`, ws); n != 1 {
		t.Fatalf("recently deleted: %d", n)
	}
	want("duplicates", "ok", "")
	if got := owner.must(200, "POST", base+"/developer-setup/merge-duplicates", nil); got["merged"] != float64(0) {
		t.Fatalf("merge again: %v", got)
	}

	// An order without an id, joined to its transaction by a guess.
	owner.must(200, "POST", events, map[string]any{"type": "track", "userId": "u2", "event": "Order Completed", "messageId": "m-guess",
		"properties": map[string]any{"revenue": 15, "currency": "USD"}})
	want("order_ids", "warning", "1 order in the last 30 days had no order_id")
	pay(200, map[string]any{"amount_minor": 1500, "source_id": "inv_guess", "user_id": "u2"})
	want("matched_by_id", "warning", "matched to their other copy only by a guess")

	// The same id both ways with dollars sent as cents.
	owner.must(200, "POST", events, map[string]any{"type": "track", "userId": "u3", "event": "Order Completed", "messageId": "order-in_amt",
		"properties": map[string]any{"order_id": "in_amt", "revenue": 49, "currency": "USD"}})
	pay(200, map[string]any{"amount_minor": 49, "source_id": "in_amt", "user_id": "u3"})
	want("matched_by_id", "warning", "different amount or currency")

	// A subscription id sent for each renewal.
	pay(201, map[string]any{"amount_minor": 2000, "source_id": "sub_1", "user_id": "u4", "occurred_at": time.Now().Add(-35 * 24 * time.Hour).UTC().Format(time.RFC3339)})
	pay(200, map[string]any{"amount_minor": 2000, "source_id": "sub_1", "user_id": "u4"})
	want("ids_reused", "warning", "1 payment id was sent again")
	// A plain retry of the same id is no renewal.
	pay(200, map[string]any{"amount_minor": 2000, "source_id": "sub_1", "user_id": "u4", "occurred_at": time.Now().Add(-35 * 24 * time.Hour).UTC().Format(time.RFC3339)})
	want("ids_reused", "warning", "1 payment id was sent again")

	// A retry that made a new id.
	pay(201, map[string]any{"amount_minor": 3000, "source_id": "try_1", "user_id": "u5"})
	pay(201, map[string]any{"amount_minor": 3000, "source_id": "try_2", "user_id": "u5"})
	want("retries", "warning", "1 payment in the last 30 days came minutes after another")

	// Dollars sent as cents among the brand's usual payments.
	for i, u := range []string{"u6", "u7", "u6", "u7", "u6"} {
		pay(201, map[string]any{"amount_minor": 4900 + i, "source_id": "usual_" + string(rune('a'+i)), "user_id": u})
	}
	pay(201, map[string]any{"amount_minor": 49, "source_id": "tiny_1", "user_id": "u7"})
	want("amounts", "warning", "50 or more times smaller")
}

func TestPaymentRetryChecksSkipHistory(t *testing.T) {
	e := freshEnv(t)
	admin, ws := setupOwner(t, e.h)
	ctx := context.Background()
	firstReceived := time.Now().UTC().Add(-5 * time.Minute)
	secondReceived := firstReceived.Add(3 * time.Minute)
	for _, source := range []string{"api", "events"} {
		for _, tc := range []struct {
			name                    string
			firstDelay, secondDelay time.Duration
			want                    int
		}{
			{name: "live", want: 1},
			{name: "history", firstDelay: 90 * 24 * time.Hour, secondDelay: 90 * 24 * time.Hour},
			{name: "live_then_history", secondDelay: 90 * 24 * time.Hour},
			{name: "history_then_live", firstDelay: 90 * 24 * time.Hour},
			{name: "at_one_day", firstDelay: 24 * time.Hour, secondDelay: 24 * time.Hour, want: 1},
			// These mixed pairs are still within the occurrence-time window:
			// each side of the pair must satisfy the arrival limit separately.
			{name: "earlier_payment_over_one_day", firstDelay: 24*time.Hour + time.Second, secondDelay: 24 * time.Hour},
			{name: "later_payment_over_one_day", firstDelay: 24 * time.Hour, secondDelay: 24*time.Hour + time.Second},
		} {
			t.Run(source+"/"+tc.name, func(t *testing.T) {
				owner := *admin
				owner.t = t
				brand := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": source + " " + tc.name})["id"].(string)
				base := "/v1/brands/" + brand
				person := owner.must(201, "POST", base+"/records", map[string]any{"type": "person"})["id"].(string)
				if _, err := e.pool.Exec(ctx, `INSERT INTO transactions
					(workspace_id, brand_id, record_id, kind, amount_minor, currency, source, source_id, occurred_at, created_at)
					VALUES ($1, $2, $3, 'payment', 4900, 'USD', $4, 'first', $5, $6),
					       ($1, $2, $3, 'payment', 4900, 'USD', $4, 'second', $7, $8)`,
					ws, brand, person, source, firstReceived.Add(-tc.firstDelay), firstReceived,
					secondReceived.Add(-tc.secondDelay), secondReceived); err != nil {
					t.Fatal(err)
				}
				for _, item := range owner.must(200, "GET", base+"/developer-setup", nil)["checks"].([]any) {
					check := item.(map[string]any)
					if check["key"] != "retries" {
						continue
					}
					status := "ok"
					if tc.want > 0 {
						status = "warning"
					}
					if check["count"] != float64(tc.want) || check["status"] != status {
						t.Fatalf("retries = %v, want count %d and status %s", check, tc.want, status)
					}
					return
				}
				t.Fatal("no retries check")
			})
		}
	}
}
