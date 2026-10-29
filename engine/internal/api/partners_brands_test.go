package api_test

import "testing"

// TestPartnersBrandLimited: someone kept to one brand works in that brand's
// partner programs, reads the shared ones without changing them, and never
// sees another brand's programs, partners, rewards or payouts.
func TestPartnersBrandLimited(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode", "website": "geonode.com"})
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "website": "repocket.com"})

	partner := map[string]string{}
	reward := map[string]string{}
	for slug, brand := range map[string]any{"geo": "geonode", "rp": "repocket", "shared": nil} {
		owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": slug, "slug": slug, "kind": "influencer", "brand": brand})
		partner[slug] = owner.must(201, "POST", base+"/partners", map[string]any{"program": slug, "email": slug + "@example.com"})["id"].(string)
		reward[slug] = owner.must(201, "POST", base+"/partners/"+partner[slug]+"/fees", map[string]any{"amount_cents": 10000, "note": "video"})["id"].(string)
	}

	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Geonode team", "scopes": []string{"partners.read", "partners.manage", "partners.conversions"}, "brand": "geonode"})["token"].(string)
	geo := &client{t: t, h: e.h, bearer: tok}
	count := func(path string) int {
		t.Helper()
		return len(geo.must(200, "GET", base+path, nil)["data"].([]any))
	}

	// Programs: their brand's and the shared one, which they only read.
	if n := count("/partner-programs"); n != 2 {
		t.Fatalf("programs: %d", n)
	}
	geo.must(404, "GET", base+"/partner-programs/rp", nil)
	geo.must(200, "GET", base+"/partner-programs/shared", nil)
	geo.must(200, "PATCH", base+"/partner-programs/geo", map[string]any{"hold_days": 10})
	geo.must(403, "PATCH", base+"/partner-programs/shared", map[string]any{"hold_days": 10})
	geo.must(404, "PATCH", base+"/partner-programs/rp", map[string]any{"hold_days": 10})
	geo.must(403, "PATCH", base+"/partner-programs/geo", map[string]any{"brand": "repocket"})
	geo.must(403, "POST", base+"/partner-programs", map[string]any{"name": "Theirs", "kind": "affiliate", "brand": "repocket"})
	geo.must(403, "POST", base+"/partner-programs", map[string]any{"name": "Everyone's", "kind": "affiliate"})
	geo.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Geonode referrals", "kind": "referral", "brand": "geonode"})

	// Partners follow their program.
	if n := count("/partners"); n != 2 {
		t.Fatalf("partners: %d", n)
	}
	geo.must(404, "GET", base+"/partners/"+partner["rp"], nil)
	geo.must(200, "GET", base+"/partners/"+partner["shared"], nil)
	geo.must(403, "PATCH", base+"/partners/"+partner["shared"], map[string]any{"note": "x"})
	geo.must(200, "PATCH", base+"/partners/"+partner["geo"], map[string]any{"note": "x"})
	geo.must(404, "POST", base+"/partners/"+partner["rp"]+"/fees", map[string]any{"amount_cents": 100})
	geo.must(403, "POST", base+"/partners", map[string]any{"program": "shared", "email": "new@example.com"})

	// Rewards and payouts too.
	if n := count("/partner-rewards"); n != 2 {
		t.Fatalf("rewards: %d", n)
	}
	geo.must(404, "POST", base+"/partner-rewards/"+reward["rp"]+"/approve", map[string]any{})
	geo.must(403, "POST", base+"/partner-rewards/"+reward["shared"]+"/approve", map[string]any{})
	geo.must(400, "POST", base+"/partner-rewards/"+reward["geo"]+"/approve", map[string]any{}) // let in; a fee is approved already
	bulk := geo.must(200, "POST", base+"/partner-rewards/bulk", map[string]any{"action": "approve", "ids": []string{reward["rp"], reward["shared"], reward["geo"]}})
	why := map[string]string{}
	for _, sk := range bulk["skipped"].([]any) {
		why[sk.(map[string]any)["id"].(string)] = sk.(map[string]any)["reason"].(string)
	}
	if bulk["done"] != float64(0) || why[reward["rp"]] != "not_found" || why[reward["shared"]] != "shared_program" || why[reward["geo"]] != "approved" {
		t.Fatalf("bulk = %v", bulk)
	}
	made := geo.must(200, "POST", base+"/partner-payouts", map[string]any{})["data"].([]any)
	if len(made) != 1 || made[0].(map[string]any)["partner_id"] != partner["geo"] {
		t.Fatalf("payouts made: %v", made)
	}
	rp := owner.must(200, "POST", base+"/partner-payouts", map[string]any{"program": "rp"})["data"].([]any)[0].(map[string]any)["id"].(string)
	geo.must(404, "POST", base+"/partner-payouts/"+rp+"/paid", map[string]any{"reference": "x"})
	if n := count("/partner-payouts"); n != 1 {
		t.Fatalf("payouts: %d", n)
	}

	// Conversions are reported for the whole workspace.
	geo.must(403, "POST", base+"/partner-conversions", map[string]any{"type": "signup", "email": "c@example.com", "code": "x"})
}
