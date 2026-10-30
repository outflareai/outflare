package api_test

import "testing"

// TestPartnerSafeguards: the self-referral block is on by default and a
// program can turn it off; "Powered by Outflare" shows on the portal
// unless the program hides it.
func TestPartnerSafeguards(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	prog := owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate", "landing_url": "https://northpeak.example/"})
	if prog["block_self_referrals"] != true {
		t.Fatalf("block_self_referrals = %v", prog["block_self_referrals"])
	}
	owner.must(200, "PUT", base+"/partner-programs/affiliates/rules", map[string]any{"rules": []any{
		map[string]any{"trigger": "sale", "side": "partner", "kind": "percent", "percent_bp": 2000},
	}})
	ana := owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "ana@acme.example"})
	conv := base + "/partner-conversions"
	self := map[string]any{"type": "sale", "email": "ana@acme.example", "ref": ana["code"], "amount_cents": 1000}
	if res := owner.must(200, "POST", conv, self); res["reason"] != "self_referral" {
		t.Fatalf("blocked self-referral = %v", res)
	}
	if p := owner.must(200, "PATCH", base+"/partner-programs/affiliates", map[string]any{"block_self_referrals": false}); p["block_self_referrals"] != false {
		t.Fatalf("program = %v", p["block_self_referrals"])
	}
	self["external_id"] = "ch_self"
	if res := owner.must(201, "POST", conv, self); res["credited"] != true || res["partner_id"] != ana["id"] {
		t.Fatalf("allowed self-referral = %v", res)
	}

	id := prog["id"].(string)
	owner.must(200, "PUT", base+"/partner-programs/"+id+"/portal", map[string]any{"enabled": true})
	portal := &portalClient{t: t, h: e.h, base: "https://outflare.test/p/" + id + "/"}
	if info := portal.must(200, "GET", "api/program", nil); info["powered_by"] != true {
		t.Fatalf("powered_by = %v", info["powered_by"])
	}
	saved := owner.must(200, "PUT", base+"/partner-programs/"+id+"/portal", map[string]any{"settings": map[string]any{"hide_powered_by": true}})
	if saved["portal"].(map[string]any)["hide_powered_by"] != true {
		t.Fatalf("portal = %v", saved["portal"])
	}
	if info := portal.must(200, "GET", "api/program", nil); info["powered_by"] != false {
		t.Fatalf("hidden powered_by = %v", info["powered_by"])
	}
}
