package api_test

import (
	"context"
	"testing"

	"github.com/outflareai/outflare/engine/internal/customers"
)

// A list's members carry what the list page shows beside the name and
// email: when the record was made, when they signed up, and what they
// have paid less refunds.
func TestSmartListMemberColumns(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	person := func(addr string) string {
		t.Helper()
		rec, err := customers.EnsurePerson(ctx, e.pool, ws, "email", addr, true, "")
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}
	event := func(record, name, msg, props, source string, minutesAgo int) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, brand_id, record_id, kind, name, properties, message_id, source, occurred_at)
			VALUES ($1, outflare_first_brand($1), $2, 'track', $3, $4::jsonb, $5, $6, now() - make_interval(mins => $7))`, ws, record, name, props, msg, source, minutesAgo); err != nil {
			t.Fatal(err)
		}
	}
	// Ana signed up twice (the first counts), paid 49 and 20 euros and one
	// US dollar, and got 9 euros back. A payment the browser claims and one
	// with no usable amount count for nothing. Bo has no events. Cy has a
	// Lifetime revenue field from a billing connection, which wins.
	ana, bo, cy := person("ana@acme.example"), person("bo@acme.example"), person("cy@northpeak.example")
	event(ana, "signed_up", "su-1", `{}`, "server", 600)
	event(ana, "signed_up", "su-2", `{}`, "server", 30)
	event(ana, "payment.succeeded", "p-1", `{"amount_minor": 4900, "currency": "eur"}`, "server", 500)
	event(ana, "payment.succeeded", "p-2", `{"amount_minor": 2000, "currency": "EUR"}`, "server", 400)
	event(ana, "payment.succeeded", "p-3", `{"amount_minor": 100, "currency": "USD"}`, "server", 300)
	event(ana, "payment.refunded", "r-1", `{"amount_minor": 900, "currency": "EUR"}`, "server", 200)
	event(ana, "payment.succeeded", "p-4", `{"amount_minor": 99900, "currency": "EUR"}`, "browser", 100)
	event(ana, "payment.succeeded", "p-5", `{"amount_minor": "lots", "currency": "EUR"}`, "server", 90)
	event(cy, "payment.succeeded", "p-6", `{"amount_minor": 500, "currency": "EUR"}`, "server", 80)
	if _, err := e.pool.Exec(ctx, `UPDATE records SET attributes = attributes || '{"lifetime_revenue": {"amount_minor": 12300, "currency": "GBP"}}' WHERE id = $1`, cy); err != nil {
		t.Fatal(err)
	}

	list := owner.must(201, "POST", base+"/smart-lists", map[string]any{"name": "Customers", "kind": "static"})["id"].(string)
	owner.must(200, "POST", base+"/smart-lists/"+list+"/members", map[string]any{"record_ids": []string{ana, bo, cy}})
	got := map[string]map[string]any{}
	for _, m := range owner.must(200, "GET", base+"/smart-lists/"+list+"/members", nil)["data"].([]any) {
		p := m.(map[string]any)
		got[p["id"].(string)] = p
	}
	if len(got) != 3 {
		t.Fatalf("members: %v", got)
	}
	a := got[ana]
	if rev, _ := a["lifetime_revenue"].(map[string]any); rev["currency"] != "EUR" || rev["amount_minor"] != 6000.0 {
		t.Fatalf("Ana's lifetime revenue: %v", a["lifetime_revenue"])
	}
	var first string
	if err := e.pool.QueryRow(ctx, `SELECT to_char(min(occurred_at) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI') FROM customer_events WHERE record_id = $1 AND name = 'signed_up'`, ana).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if s, _ := a["signed_up_at"].(string); len(s) < 16 || s[:16] != first {
		t.Fatalf("Ana signed up %v, want %s", a["signed_up_at"], first)
	}
	if a["created_at"] == nil || a["email"] != "ana@acme.example" {
		t.Fatalf("Ana: %v", a)
	}
	b := got[bo]
	if _, has := b["lifetime_revenue"]; has || b["signed_up_at"] != nil || b["created_at"] == nil {
		t.Fatalf("Bo has no events: %v", b)
	}
	if rev, _ := got[cy]["lifetime_revenue"].(map[string]any); rev["currency"] != "GBP" || rev["amount_minor"] != 12300.0 {
		t.Fatalf("Cy's lifetime revenue comes from the field: %v", got[cy]["lifetime_revenue"])
	}
}
