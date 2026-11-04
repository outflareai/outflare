package api_test

import (
	"context"
	"testing"
)

// TestTransactionsRecoveryWindow covers the owner's decision of 2026-10-28
// that the 7-day recovery window covers transactions too: a deleted one is
// hidden from the list, Query and the record, isn't added back by the same
// source id, comes back as it was without transaction.created again
// (onto the record kept after a merge), stays when its person is erased,
// and is purged after 7 days.
func TestTransactionsRecoveryWindow(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana"})["id"].(string)
	ana2 := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana again"})["id"].(string)
	bo := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Bo"})["id"].(string)
	pay := func(src, rec string) string {
		return owner.must(201, "POST", base+"/transactions", map[string]any{"kind": "payment", "amount_minor": 4900, "currency": "USD",
			"source_id": src, "record_id": rec, "description": "Test payment"})["id"].(string)
	}
	keep, test, gone := pay("inv_1", ana), pay("inv_test", ana2), pay("inv_bo", bo)
	count := func(path string) int { return len(owner.must(200, "GET", base+path, nil)["data"].([]any)) }
	audit := func(action string) int {
		var n int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = $2`, ws, action).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	owner.must(404, "POST", base+"/transactions/00000000-0000-0000-0000-000000000000/delete", nil)
	owner.must(404, "POST", base+"/transactions/nope/delete", nil)

	// Deleted: hidden everywhere, listed in Recently deleted.
	del := owner.must(200, "POST", base+"/transactions/"+test+"/delete", nil)
	if del["kind"] != "transaction" || del["record_id"] != ana2 || del["label"] != "Test payment" || del["target"] != test {
		t.Fatalf("deletion: %v", del)
	}
	owner.must(404, "POST", base+"/transactions/"+test+"/delete", nil)
	owner.must(404, "GET", base+"/transactions/"+test, nil)
	if n := count("/transactions"); n != 2 {
		t.Fatalf("list: %d", n)
	}
	if n := count("/transactions?record=" + ana2); n != 0 {
		t.Fatalf("record: %d", n)
	}
	if q := owner.must(200, "POST", base+"/query", map[string]any{"sql": "SELECT id FROM transactions"}); q["row_count"] != 2.0 {
		t.Fatalf("query: %v", q)
	}
	if n := count("/recently-deleted?kind=transaction"); n != 1 {
		t.Fatalf("recently deleted: %d", n)
	}
	if audit("transaction.deleted") != 1 {
		t.Fatal("transaction.deleted not recorded")
	}

	// Sent again while deleted: not added back.
	again := owner.must(409, "POST", base+"/transactions", map[string]any{"kind": "payment", "amount_minor": 4900, "currency": "USD", "source_id": "inv_test"})
	if again["error"].(map[string]any)["code"] != "transaction_deleted" {
		t.Fatalf("again: %v", again)
	}

	// Its person merged into Ana meanwhile: it comes back on Ana, once.
	owner.must(200, "POST", base+"/records/"+ana+"/merge", map[string]any{"from": ana2})
	res := owner.must(200, "POST", base+"/recently-deleted/"+del["id"].(string)+"/restore", nil)
	if res["outcome"] != "restored" {
		t.Fatalf("restore: %v", res)
	}
	back := owner.must(200, "GET", base+"/transactions/"+test, nil)
	if back["record_id"] != ana || back["source_id"] != "inv_test" || back["amount_minor"] != 4900.0 {
		t.Fatalf("restored: %v", back)
	}
	if n := count("/transactions?record=" + ana); n != 2 {
		t.Fatalf("Ana's after restore: %d", n)
	}
	if audit("transaction.created") != 3 {
		t.Fatal("restoring sent transaction.created again")
	}
	owner.must(409, "POST", base+"/recently-deleted/"+del["id"].(string)+"/restore", nil)

	// Erasing a person keeps their deleted transaction, unlinked.
	d2 := owner.must(200, "POST", base+"/transactions/"+gone+"/delete", nil)
	owner.must(200, "DELETE", base+"/records/"+bo, nil)
	left := owner.must(200, "GET", base+"/recently-deleted/"+d2["id"].(string), nil)
	if left["record_id"] != nil {
		t.Fatalf("after erasure: %v", left)
	}
	owner.must(200, "POST", base+"/recently-deleted/"+d2["id"].(string)+"/restore", nil)
	if tx := owner.must(200, "GET", base+"/transactions/"+gone, nil); tx["record_id"] != nil {
		t.Fatalf("restored after erasure: %v", tx)
	}

	// After 7 days: purged, and the source id can be sent again.
	d3 := owner.must(200, "POST", base+"/transactions/"+keep+"/delete", nil)
	if _, err := e.pool.Exec(ctx, `UPDATE deletions SET purge_after = now() - interval '1 minute' WHERE id = $1`, d3["id"]); err != nil {
		t.Fatal(err)
	}
	if n, err := e.srv.PurgeDeletions(ctx); err != nil || n < 1 {
		t.Fatalf("purge: %d %v", n, err)
	}
	var held int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM deleted_rows WHERE workspace_id = $1 AND source_table = 'transactions'`, ws).Scan(&held); err != nil || held != 0 {
		t.Fatalf("held after purge: %d %v", held, err)
	}
	owner.must(201, "POST", base+"/transactions", map[string]any{"kind": "payment", "amount_minor": 4900, "currency": "USD", "source_id": "inv_1"})
}
