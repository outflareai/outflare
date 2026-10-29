package api_test

import "testing"

// TestPartnerOwnRate: a rule for one partner gives them their own rate,
// which beats the program's, and nobody else gets it.
func TestPartnerOwnRate(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "slug": "aff", "kind": "affiliate"})
	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Other", "slug": "other", "kind": "affiliate"})
	ana := owner.must(201, "POST", base+"/partners", map[string]any{"program": "aff", "email": "ana@example.com", "code": "ana"})["id"].(string)
	owner.must(201, "POST", base+"/partners", map[string]any{"program": "aff", "email": "bo@example.com", "code": "bo"})
	stranger := owner.must(201, "POST", base+"/partners", map[string]any{"program": "other", "email": "cy@example.com"})["id"].(string)

	rule := func(partner any, bp int) map[string]any {
		return map[string]any{"trigger": "sale", "kind": "percent", "percent_bp": bp, "partner_id": partner}
	}
	put := func(code int, rules ...any) map[string]any {
		t.Helper()
		return owner.must(code, "PUT", base+"/partner-programs/aff/rules", map[string]any{"rules": rules})
	}
	put(400, rule(nil, 2000), rule(stranger, 3500))    // a partner in another program
	put(400, rule(nil, 2000), rule("not-an-id", 3500)) // not a partner
	group := owner.must(201, "POST", base+"/partner-programs/aff/groups", map[string]any{"name": "Gold"})["id"].(string)
	both := rule(ana, 3500)
	both["group_id"] = group
	put(400, both)
	got := put(200, rule(nil, 2000), rule(ana, 3500))["rules"].([]any)
	if len(got) != 2 || got[1].(map[string]any)["partner_id"] != ana || got[0].(map[string]any)["partner_id"] != nil {
		t.Fatalf("rules = %v", got)
	}

	sale := func(code, order string) int64 {
		t.Helper()
		res := owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "sale", "ref": code, "customer_id": order, "amount_cents": 10000, "external_id": order})
		rw := res["rewards"].([]any)
		if len(rw) != 1 {
			t.Fatalf("%s rewards = %v", code, rw)
		}
		return int64(rw[0].(map[string]any)["amount_cents"].(float64))
	}
	if got := sale("ana", "o-1"); got != 3500 {
		t.Fatalf("ana earned %d, want her own 35%%", got)
	}
	if got := sale("bo", "o-2"); got != 2000 {
		t.Fatalf("bo earned %d, want the program's 20%%", got)
	}
}

// TestPartnerRewardsBulk: many held rewards approved or rejected at once,
// by id or for a whole program, each recorded as if done alone.
func TestPartnerRewardsBulk(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "slug": "aff", "kind": "affiliate"})
	owner.must(201, "POST", base+"/partners", map[string]any{"program": "aff", "email": "ana@example.com", "code": "ana"})
	ids := []any{}
	for _, order := range []string{"o-1", "o-2", "o-3", "o-4"} {
		res := owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "sale", "ref": "ana", "customer_id": order, "amount_cents": 10000, "external_id": order})
		ids = append(ids, res["rewards"].([]any)[0].(map[string]any)["id"])
	}
	bulk := func(code int, body map[string]any) map[string]any {
		t.Helper()
		return owner.must(code, "POST", base+"/partner-rewards/bulk", body)
	}
	bulk(400, map[string]any{"action": "approve"})
	bulk(400, map[string]any{"action": "pay", "ids": ids})
	bulk(400, map[string]any{"action": "approve", "ids": ids, "note": "x"})

	res := bulk(200, map[string]any{"action": "approve", "ids": []any{ids[0], ids[1], "00000000-0000-0000-0000-000000000000", "nope"}})
	if res["done"] != float64(2) || len(res["skipped"].([]any)) != 2 || res["more"] != false {
		t.Fatalf("approve = %v", res)
	}
	res = bulk(200, map[string]any{"action": "approve", "ids": []any{ids[0]}})
	if res["done"] != float64(0) || res["skipped"].([]any)[0].(map[string]any)["reason"] != "approved" {
		t.Fatalf("again = %v", res)
	}
	if res = bulk(200, map[string]any{"action": "reject", "program": "aff", "note": "Test orders"}); res["done"] != float64(2) {
		t.Fatalf("reject = %v", res)
	}
	if res = bulk(200, map[string]any{"action": "reject", "program": "aff"}); res["done"] != float64(0) || len(res["skipped"].([]any)) != 0 {
		t.Fatalf("nothing left = %v", res)
	}
	byStatus := map[string]int{}
	for _, w := range owner.must(200, "GET", base+"/partner-rewards", nil)["data"].([]any) {
		byStatus[w.(map[string]any)["status"].(string)]++
	}
	if byStatus["approved"] != 2 || byStatus["rejected"] != 2 {
		t.Fatalf("rewards = %v", byStatus)
	}
}
