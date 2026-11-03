package api_test

import "testing"

// The workspace switch "Only owners and admins can edit records": members
// and their tokens read records but can't change them; owners, admins and
// their tokens can; only owners and admins flip it.
func TestRecordsEditOwnersOnly(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	member, _ := joinAs(t, owner, ws, "op@acme.example", map[string]any{"role": "operator"})
	admin, adminID := joinAs(t, owner, ws, "admin@acme.example", map[string]any{"role": "admin"})
	adminToken := agentToken(t, admin, base, "Admin script", "customers.read", "customers.manage")
	rec := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana"})["id"].(string)

	// Off: everyone who may change records can.
	member.must(200, "PATCH", base+"/records/"+rec, map[string]any{"name": "Ana B"})

	// Only owners and admins turn it on.
	if code, _ := admin.do("PATCH", base, map[string]any{"records_edit_owners_only": true}); code != 200 {
		t.Fatalf("admin turning it on: %d", code)
	}
	if got := owner.must(200, "GET", base, nil); got["records_edit_owners_only"] != true {
		t.Fatalf("workspace: %v", got)
	}

	// On: a member reads but can't write, nor can their token.
	member.must(200, "GET", base+"/records/"+rec, nil)
	for _, c := range []struct {
		method, path string
		body         map[string]any
	}{
		{"PATCH", "/records/" + rec, map[string]any{"name": "Ana C"}},
		{"POST", "/records", map[string]any{"type": "person", "name": "Bo"}},
		{"POST", "/records/" + rec + "/identities", map[string]any{"namespace": "email", "value": "ana@acme.example"}},
	} {
		code, out := member.do(c.method, base+c.path, c.body)
		if code != 403 || out["error"].(map[string]any)["code"] != "records_locked" {
			t.Fatalf("member %s %s: %d %v", c.method, c.path, code, out)
		}
	}
	// Owners, admins and their tokens still can.
	admin.must(200, "PATCH", base+"/records/"+rec, map[string]any{"name": "Ana E"})
	adminToken.must(200, "PATCH", base+"/records/"+rec, map[string]any{"name": "Ana D"})
	ownerToken := agentToken(t, owner, base, "Owner script", "customers.read", "customers.manage")
	ownerToken.must(200, "PATCH", base+"/records/"+rec, map[string]any{"name": "Ana F"})

	// A token follows its maker: once the admin is a member, their token
	// can't change records either.
	owner.must(200, "PATCH", base+"/members/"+adminID, map[string]any{"role": "operator"})
	if code, out := adminToken.do("PATCH", base+"/records/"+rec, map[string]any{"name": "Ana H"}); code != 403 {
		t.Fatalf("a demoted admin's token wrote: %d %v", code, out)
	}

	// A member can't turn it off.
	if code, _ := member.do("PATCH", base, map[string]any{"records_edit_owners_only": false}); code != 403 {
		t.Fatalf("member turning it off: %d", code)
	}
	owner.must(200, "PATCH", base, map[string]any{"records_edit_owners_only": false})
	member.must(200, "PATCH", base+"/records/"+rec, map[string]any{"name": "Ana G"})
}
