package api_test

import (
	"testing"
	"time"
)

// TestPartnerLeadExpiry: with a lead expiration, a referred customer who
// hasn't bought within that many days no longer credits the partner; one
// who bought in time keeps crediting them after it.
func TestPartnerLeadExpiry(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	prog := owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate", "lead_expiry_days": 30})
	if prog["lead_expiry_days"] != 30.0 {
		t.Fatalf("lead_expiry_days = %v", prog["lead_expiry_days"])
	}
	owner.must(400, "PATCH", base+"/partner-programs/affiliates", map[string]any{"lead_expiry_days": 0})
	owner.must(200, "PUT", base+"/partner-programs/affiliates/rules", map[string]any{"rules": []any{
		map[string]any{"trigger": "sale", "side": "partner", "kind": "percent", "percent_bp": 2000},
	}})
	ana := owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "ana@acme.example"})
	conv := base + "/partner-conversions"
	ago := func(days int) string { return time.Now().AddDate(0, 0, -days).UTC().Format(time.RFC3339) }

	// Lee signed up 60 days ago and buys only now: the lead has expired.
	owner.must(201, "POST", conv, map[string]any{"type": "signup", "customer_id": "u-lee", "ref": ana["code"], "occurred_at": ago(60)})
	if res := owner.must(200, "POST", conv, map[string]any{"type": "sale", "customer_id": "u-lee", "amount_cents": 1000, "external_id": "o-lee"}); res["reason"] != "lead_expired" {
		t.Fatalf("expired lead = %v", res)
	}
	// Max signed up 10 days ago: credited.
	owner.must(201, "POST", conv, map[string]any{"type": "signup", "customer_id": "u-max", "ref": ana["code"], "occurred_at": ago(10)})
	if res := owner.must(201, "POST", conv, map[string]any{"type": "sale", "customer_id": "u-max", "amount_cents": 1000, "external_id": "o-max"}); res["credited"] != true {
		t.Fatalf("lead in time = %v", res)
	}
	// Kim bought 50 days after signing up, then the program shortened the
	// window to 30 days: as a customer, she keeps crediting Ana.
	owner.must(201, "POST", conv, map[string]any{"type": "signup", "customer_id": "u-kim", "ref": ana["code"], "occurred_at": ago(80)})
	owner.must(200, "PATCH", base+"/partner-programs/affiliates", map[string]any{"lead_expiry_days": 60})
	owner.must(201, "POST", conv, map[string]any{"type": "sale", "customer_id": "u-kim", "amount_cents": 1000, "external_id": "o-kim1", "occurred_at": ago(30)})
	owner.must(200, "PATCH", base+"/partner-programs/affiliates", map[string]any{"lead_expiry_days": 30})
	if res := owner.must(201, "POST", conv, map[string]any{"type": "sale", "customer_id": "u-kim", "amount_cents": 1000, "external_id": "o-kim2"}); res["credited"] != true {
		t.Fatalf("customer after the window = %v", res)
	}
	// Without a lead expiration, Lee's sale is credited.
	if p := owner.must(200, "PATCH", base+"/partner-programs/affiliates", map[string]any{"lead_expiry_days": nil}); p["lead_expiry_days"] != nil {
		t.Fatalf("cleared = %v", p["lead_expiry_days"])
	}
	if res := owner.must(201, "POST", conv, map[string]any{"type": "sale", "customer_id": "u-lee", "amount_cents": 1000, "external_id": "o-lee2"}); res["credited"] != true {
		t.Fatalf("no expiry = %v", res)
	}
}
