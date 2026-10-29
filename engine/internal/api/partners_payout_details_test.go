package api_test

import (
	"context"
	"testing"
)

// TestPartnerPayoutDetailsMissing: an imported partner with no payout
// details keeps their balance until they add details; partners without
// details can be listed and counted.
func TestPartnerPayoutDetailsMissing(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate"})
	owner.must(200, "PATCH", base+"/partner-programs/affiliates", map[string]any{"min_payout_cents": 0})
	ana := owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "ana@acme.example"})["id"].(string)
	bo := owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "bo@acme.example"})["id"].(string)
	cy := owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "cy@acme.example", "payout_method": "PayPal cy@acme.example"})["id"].(string)
	// Ana came over from Tolt, whose API gives no payout details.
	if _, err := e.pool.Exec(context.Background(), `UPDATE partner_memberships SET source = 'import' WHERE id = $1`, ana); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{ana, bo, cy} {
		owner.must(201, "POST", base+"/partners/"+id+"/fees", map[string]any{"amount_cents": 1000, "note": "October"})
	}

	missing := owner.must(200, "GET", base+"/partners?program=affiliates&payout_details=missing", nil)["data"].([]any)
	if len(missing) != 2 {
		t.Fatalf("missing = %d", len(missing))
	}
	if set := owner.must(200, "GET", base+"/partners?program=affiliates&payout_details=set", nil)["data"].([]any); len(set) != 1 || set[0].(map[string]any)["id"] != cy {
		t.Fatalf("set = %v", set)
	}
	owner.must(400, "GET", base+"/partners?payout_details=maybe", nil)
	if n := owner.must(200, "GET", base+"/partner-programs/affiliates/stats", nil)["partners"].(map[string]any)["missing_payout_details"]; n != 2.0 {
		t.Fatalf("stats missing_payout_details = %v", n)
	}

	// Bo was added here, so the owner pays him as before; Ana waits.
	res := owner.must(200, "POST", base+"/partner-payouts", map[string]any{"program": "affiliates"})
	if len(res["data"].([]any)) != 2 || res["waiting_for_details"] != 1.0 {
		t.Fatalf("payouts = %v", res)
	}
	if st := owner.must(200, "GET", base+"/partners/"+ana, nil)["stats"].(map[string]any); st["balance_cents"] != 1000.0 {
		t.Fatalf("ana's balance = %v", st["balance_cents"])
	}
	owner.must(200, "PATCH", base+"/partners/"+ana, map[string]any{"payout_method": "Wise ana@acme.example"})
	res = owner.must(200, "POST", base+"/partner-payouts", map[string]any{"program": "affiliates"})
	if len(res["data"].([]any)) != 1 || res["waiting_for_details"] != 0.0 {
		t.Fatalf("payouts after details = %v", res)
	}
}
