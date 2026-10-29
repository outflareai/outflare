package api_test

import "testing"

// TestPartnerGroups: a program's groups, the default group new partners
// join, moving partners in bulk, and a group's own rate.
func TestPartnerGroups(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "slug": "aff", "kind": "affiliate"})
	prog := base + "/partner-programs/aff"
	std := owner.must(201, "POST", prog+"/groups", map[string]any{"name": "Standard", "default": true})
	if std["default"] != true {
		t.Fatalf("standard = %v", std)
	}
	gold := owner.must(201, "POST", prog+"/groups", map[string]any{"name": "Gold"})["id"].(string)
	owner.must(409, "PATCH", prog+"/groups/"+gold, map[string]any{"name": "Standard"})
	owner.must(404, "PATCH", prog+"/groups/00000000-0000-0000-0000-000000000000", map[string]any{"name": "X"})
	p := owner.must(200, "PATCH", prog+"/groups/"+gold, map[string]any{"name": "Gold tier"})
	if p["default_group_id"] != std["id"] {
		t.Fatalf("default = %v", p["default_group_id"])
	}

	// New partners join the default group unless told otherwise.
	ids := []string{}
	for _, n := range []string{"ana", "bo", "cy"} {
		m := owner.must(201, "POST", base+"/partners", map[string]any{"program": "aff", "email": n + "@example.com", "code": n})
		if m["group_id"] != std["id"] {
			t.Fatalf("%s group = %v", n, m["group_id"])
		}
		ids = append(ids, m["id"].(string))
	}
	// Gold earns 30%; the program 20%.
	owner.must(200, "PUT", prog+"/rules", map[string]any{"rules": []any{
		map[string]any{"trigger": "sale", "kind": "percent", "percent_bp": 2000},
		map[string]any{"trigger": "sale", "kind": "percent", "percent_bp": 3000, "group_id": gold},
	}})
	owner.must(400, "POST", prog+"/groups/"+gold+"/partners", map[string]any{"partner_ids": []string{}})
	owner.must(404, "POST", prog+"/groups/00000000-0000-0000-0000-000000000000/partners", map[string]any{"partner_ids": ids[:1]})
	if res := owner.must(200, "POST", prog+"/groups/"+gold+"/partners", map[string]any{"partner_ids": ids[:2]}); res["moved"] != 2.0 {
		t.Fatalf("moved = %v", res)
	}
	if res := owner.must(200, "POST", prog+"/groups/none/partners", map[string]any{"partner_ids": ids[1:2]}); res["moved"] != 1.0 {
		t.Fatalf("out = %v", res)
	}
	groups := map[string]float64{}
	for _, g := range owner.must(200, "GET", prog, nil)["groups"].([]any) {
		groups[g.(map[string]any)["name"].(string)] = g.(map[string]any)["partners"].(float64)
	}
	if groups["Gold tier"] != 1 || groups["Standard"] != 1 {
		t.Fatalf("groups = %v", groups)
	}
	sale := func(code, order string) float64 {
		t.Helper()
		res := owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "sale", "ref": code, "customer_id": order, "amount_cents": 10000, "external_id": order})
		return res["rewards"].([]any)[0].(map[string]any)["amount_cents"].(float64)
	}
	if sale("ana", "o-1") != 3000 || sale("bo", "o-2") != 2000 || sale("cy", "o-3") != 2000 {
		t.Fatal("group rates")
	}
	// No default: new partners join no group.
	owner.must(200, "PATCH", prog+"/groups/"+std["id"].(string), map[string]any{"default": false})
	if m := owner.must(201, "POST", base+"/partners", map[string]any{"program": "aff", "email": "di@example.com"}); m["group_id"] != nil {
		t.Fatalf("no default group = %v", m["group_id"])
	}
}
