package api_test

import (
	"context"
	"testing"
	"time"
)

// Partners read the events and transactions a workspace already sends: a
// Signed Up event credits the partner it names, a program's own event pays
// its bounty, and the partner conversions API stores the same events and
// transactions, so a customer reported both ways counts once.
func TestPartnersFromEvents(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	prog := owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate",
		"landing_url": "https://northpeak.example/", "brand": brand, "auto_approve": true, "hold_days": 0,
		"rules": []map[string]any{
			{"trigger": "signup", "kind": "fixed", "amount_cents": 500},
			{"trigger": "trial_started", "kind": "fixed", "amount_cents": 200},
			{"trigger": "sale", "kind": "percent", "percent_bp": 2000},
		}})
	if prog["sales_source"] != "transactions" {
		t.Fatalf("a brand's program takes its sales from transactions by default: %v", prog["sales_source"])
	}
	gil := owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "gil@example.com", "code": "gil"})
	e.runCRMImports(t) // runs the jobs
	event := func(body map[string]any) {
		t.Helper()
		owner.must(200, "POST", base+"/customer-events", body)
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	wait := func(what string, want int, q string, args ...any) {
		t.Helper()
		for range 250 {
			if count(q, args...) == want {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		time.Sleep(150 * time.Millisecond) // let a wrong second one show up
		if n := count(q, args...); n != want {
			t.Fatalf("%s = %d, want %d", what, n, want)
		}
	}
	conversions := `SELECT count(*) FROM partner_conversions WHERE workspace_id = $1 AND type = $2`

	// Ann signs up: the Signed Up event names the partner, and the backend
	// also reports it to the partner conversions API. One sign-up.
	event(map[string]any{"type": "track", "userId": "u_ann", "event": "Signed Up", "messageId": "su-ann", "properties": map[string]any{"ref": "gil"}})
	wait("sign-ups", 1, conversions, ws, "signup")
	res := owner.must(200, "POST", base+"/partner-conversions", map[string]any{"type": "signup", "customer_id": "u_ann", "external_id": "signup-u_ann"})
	if res["duplicate"] != true || res["partner_id"] != gil["id"] {
		t.Fatalf("the API's sign-up after the event = %v", res)
	}
	if n := count(conversions+` AND source = 'events'`, ws, "signup"); n != 1 {
		t.Fatalf("event sign-ups = %d", n)
	}

	// A program's own event pays its bounty, once per event.
	trial := map[string]any{"type": "track", "userId": "u_ann", "event": "Trial Started", "messageId": "trial-ann"}
	event(trial)
	event(trial)
	wait("trial conversions", 1, conversions+` AND event_name = 'trial_started'`, ws, "custom")
	// An event no rule names is no conversion.
	event(map[string]any{"type": "track", "userId": "u_ann", "event": "Report Viewed", "messageId": "rv-ann"})

	// A sale reported to the API is a transaction: the same payment sent
	// to the transactions API is that one, one payment and one sale.
	owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "sale", "customer_id": "u_ann", "external_id": "ch_1",
		"amount_cents": 4900, "currency": "USD"})
	owner.must(200, "POST", "/v1/brands/"+brand+"/transactions", map[string]any{"kind": "payment", "amount_minor": 4900, "currency": "USD",
		"source_id": "ch_1", "user_id": "u_ann"})
	wait("sales", 1, conversions, ws, "sale")
	if n := count(`SELECT count(*) FROM transactions WHERE brand_id = $1 AND source_id = 'ch_1'`, brand); n != 1 {
		t.Fatalf("transactions for ch_1 = %d", n)
	}
	if n := count(`SELECT count(*) FROM customer_events WHERE workspace_id = $1 AND name = 'payment.succeeded' AND properties->>'payment_id' = 'ch_1'`, ws); n != 1 {
		t.Fatalf("payment events for ch_1 = %d", n)
	}

	// Bo's payment arrives before the sign-up that names the partner: it is
	// credited once the referral is made.
	event(map[string]any{"type": "identify", "userId": "u_bo", "messageId": "id-bo", "traits": map[string]any{"email": "bo@northpeak.example"}})
	owner.must(201, "POST", "/v1/brands/"+brand+"/transactions", map[string]any{"kind": "payment", "amount_minor": 1000, "currency": "USD",
		"source_id": "ch_2", "user_id": "u_bo"})
	event(map[string]any{"type": "track", "userId": "u_bo", "event": "Signed Up", "messageId": "su-bo", "properties": map[string]any{"ref": "gil"}})
	wait("sales after Bo's sign-up", 2, conversions, ws, "sale")

	// A deleted payment takes its commission back.
	var ch1 string
	if err := e.pool.QueryRow(ctx, `SELECT id::text FROM transactions WHERE brand_id = $1 AND source_id = 'ch_1'`, brand).Scan(&ch1); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "POST", "/v1/brands/"+brand+"/transactions/"+ch1+"/delete", nil)
	wait("refunds", 1, conversions, ws, "refund")

	// An existing user whose first event arrives now didn't just sign up.
	event(map[string]any{"type": "identify", "userId": "u_old", "messageId": "id-old",
		"traits": map[string]any{"email": "old@northpeak.example", "created_at": "2024-01-02T03:04:05Z"}})
	if n := count(`SELECT count(*) FROM jobs WHERE kind = $1 AND idempotency_key LIKE 'partner-event:%' AND payload->>'record_id' =
		(SELECT r.id::text FROM record_identities i JOIN records r ON r.id = i.record_id WHERE i.workspace_id = $2 AND i.namespace = 'user_id' AND i.value = 'u_old')`,
		"partners.event_conversion", ws); n != 0 {
		t.Fatalf("an existing user's first identify was taken as a sign-up (%d jobs)", n)
	}

	st := owner.must(200, "GET", base+"/partners/"+gil["id"].(string), nil)["stats"].(map[string]any)
	if st["customers"] != float64(2) {
		t.Fatalf("stats = %v", st)
	}
}
