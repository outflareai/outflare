package api_test

import "testing"

// A mission aiming at referred revenue counts credited partner sales less
// refunds from when it starts.
func TestMissionReferredRevenue(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	mission := owner.must(201, "POST", base+"/missions", map[string]any{"name": "Partners", "goal_metric": "revenue_referred", "goal_target": 300})["id"].(string)
	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate", "landing_url": "https://northpeak.example/"})
	owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "ana@acme.example", "code": "ana"})
	conv := base + "/partner-conversions"
	for _, c := range []string{"u-1", "u-2", "u-3", "u-4"} {
		owner.must(201, "POST", conv, map[string]any{"type": "sale", "customer_id": c, "ref": "ana", "amount_cents": 10000, "external_id": "o-" + c})
	}
	owner.must(201, "POST", conv, map[string]any{"type": "refund", "customer_id": "u-4", "refund_of": "o-u-4", "amount_cents": 5000, "external_id": "r-1"})
	// A sale nobody referred doesn't count.
	owner.must(200, "POST", conv, map[string]any{"type": "sale", "customer_id": "u-9", "amount_cents": 10000})

	m := owner.must(200, "GET", base+"/missions/"+mission, nil)["mission"].(map[string]any)
	if p := m["progress"].(map[string]any); p["revenue_referred_cents"] != 35000.0 {
		t.Fatalf("progress = %v", p)
	}
	if g := m["goal"].(map[string]any); g["current"] != 350.0 || g["reached"] != true {
		t.Fatalf("goal = %v", g)
	}
}
