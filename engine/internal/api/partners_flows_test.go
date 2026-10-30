package api_test

import (
	"context"
	"testing"
)

// TestPartnerFlows: flows with conditions, their own approval, one that
// adds to the exclusive cash flow, an override, and one that moves the
// partner to another group.
func TestPartnerFlows(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "slug": "aff", "kind": "affiliate"})
	prog := base + "/partner-programs/aff"
	gold := owner.must(201, "POST", prog+"/groups", map[string]any{"name": "Gold"})["id"].(string)
	ana := owner.must(201, "POST", base+"/partners", map[string]any{"program": "aff", "email": "ana@example.com", "code": "ana"})["id"].(string)

	cond := func(field, op string, v any) map[string]any {
		return map[string]any{"groups": []any{map[string]any{"conditions": []any{map[string]any{"field": field, "op": op, "value": v}}}}}
	}
	flows := func(code int, rules ...any) map[string]any {
		t.Helper()
		return owner.must(code, "PUT", prog+"/rules", map[string]any{"rules": rules})
	}
	flows(400, map[string]any{"trigger": "sale", "kind": "percent", "percent_bp": 2000, "conditions": cond("partner.mood", "eq", "x")})
	flows(400, map[string]any{"trigger": "sale", "kind": "group"})
	flows(400, map[string]any{"trigger": "sale", "kind": "percent", "percent_bp": 2000, "approval": "after_days"})
	flows(400, map[string]any{"trigger": "sale", "kind": "percent", "percent_bp": 2000, "overrides": []any{map[string]any{"product_id": "pro", "amount_cents": 5}}})
	got := flows(200,
		map[string]any{"name": "Base", "trigger": "sale", "kind": "percent", "percent_bp": 2000, "approval": "auto",
			"overrides": []any{map[string]any{"product_id": "pro", "percent_bp": 3000}}},
		map[string]any{"name": "Loyalty bonus", "trigger": "sale", "kind": "fixed", "amount_cents": 500, "stacking": "adds", "approval": "manual",
			"conditions": cond("partner.transactions", "gte", 1)},
		map[string]any{"name": "Promote", "trigger": "sale", "kind": "group", "target_group_id": gold,
			"conditions": cond("partner.revenue_cents", "gte", 15000)},
	)["rules"].([]any)
	if len(got) != 3 || got[1].(map[string]any)["stacking"] != "adds" || got[2].(map[string]any)["target_group_id"] != gold {
		t.Fatalf("flows = %v", got)
	}

	rewards := func(res map[string]any) map[float64]string {
		out := map[float64]string{}
		for _, w := range res["rewards"].([]any) {
			out[w.(map[string]any)["amount_cents"].(float64)] = w.(map[string]any)["status"].(string)
		}
		return out
	}
	sale := func(order, product string) map[string]any {
		t.Helper()
		return owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "sale", "ref": "ana", "customer_id": order,
			"amount_cents": 10000, "external_id": order, "product_id": product})
	}
	// First sale: the base rate, approved at once (approval auto); no bonus
	// yet (no earlier sales), and 10,000 of revenue isn't enough for Gold.
	if r := rewards(sale("o-1", "")); len(r) != 1 || r[2000] != "approved" {
		t.Fatalf("first sale = %v", r)
	}
	// Second, of the pro plan: its override (30%), the bonus on top, held for
	// a person, and Ana moves to Gold.
	if r := rewards(sale("o-2", "pro")); len(r) != 2 || r[3000] != "approved" || r[500] != "pending" {
		t.Fatalf("second sale = %v", r)
	}
	if p := owner.must(200, "GET", base+"/partners/"+ana, nil); p["group_id"] != gold {
		t.Fatalf("group = %v", p["group_id"])
	}
	// The approval loop leaves a manual flow's reward alone.
	if err := e.srv.ApproveDuePartnerRewards(ctx); err != nil {
		t.Fatal(err)
	}
	var pending int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM partner_rewards WHERE workspace_id = $1 AND status = 'pending'`, ws).Scan(&pending) //nolint:errcheck
	var switched int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'partner.group_switched'`, ws).Scan(&switched) //nolint:errcheck
	if pending != 1 || switched != 1 {
		t.Fatalf("pending %d, switched %d", pending, switched)
	}
}
