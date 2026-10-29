package api_test

import (
	"strconv"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// Deleting a person erases them: the record, the records merged into it,
// their leads and product events, and with suppress=true their emails go
// on the opt-out list. Deals stay, and a company's people stay.
func TestDeleteRecord(t *testing.T) {
	got := track.Capture(t)
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws

	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme",
		"identities": []map[string]any{{"namespace": "domain", "value": "acme.example"}}})["id"].(string)
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "origin": "first_party",
		"attributes": map[string]any{"first_name": "Ana", "companies": []string{acme}},
		"identities": []map[string]any{{"namespace": "user_id", "value": "u_ana"}, {"namespace": "email", "value": "ana@acme.example", "verified": true}}})
	id := ana["id"].(string)
	ben := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ben",
		"attributes": map[string]any{"companies": []string{acme}}})["id"].(string)
	// A lead with her email, a product event, and an older copy merged in.
	owner.must(201, "POST", base+"/leads", map[string]any{"email": "Ana@acme.example", "first_name": "Ana"})
	owner.must(200, "POST", base+"/customer-events", map[string]any{"type": "track", "userId": "u_ana", "event": "Report Created"})
	dup := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana D",
		"identities": []map[string]any{{"namespace": "email", "value": "ana.old@acme.example"}}})["id"].(string)
	owner.must(200, "POST", base+"/records/"+id+"/merge", map[string]any{"from": dup})
	v := int(owner.must(200, "GET", base+"/records/"+id, nil)["version"].(float64))

	// An agent must say which version it read; a stale one is refused.
	agent := agentToken(t, owner, base, "Agent", "customers.read", "customers.manage")
	agent.header = map[string]string{"Outflare-Client": "mcp/0.1.0", "Outflare-Tool": "delete_record"}
	if code, out := agent.do("DELETE", base+"/records/"+id, nil); code != 400 || errCode(out) != "version_required" {
		t.Fatalf("agent without version: %d %v", code, out)
	}
	if v < 2 {
		t.Fatalf("the merge should have moved the version: %d", v)
	}
	if code, out := owner.do("DELETE", base+"/records/"+id+"?version=1", nil); code != 409 || errCode(out) != "record_changed" {
		t.Fatalf("stale version: %d %v", code, out)
	}
	// A reader can't.
	viewer := agentToken(t, owner, base, "Viewer", "customers.read")
	viewer.must(403, "DELETE", base+"/records/"+id, nil)

	out := agent.must(200, "DELETE", base+"/records/"+id+"?suppress=true&version="+strconv.Itoa(v), nil)
	erased := out["erased"].(map[string]any)
	if erased["records"] != 2.0 || erased["leads"] != 1.0 || erased["customer_events"] != 1.0 || out["suppressed"] != 2.0 {
		t.Fatalf("erased: %v", out)
	}
	owner.must(404, "GET", base+"/records/"+id, nil)
	owner.must(404, "GET", base+"/records/"+dup, nil)
	if n := len(owner.must(200, "GET", base+"/leads?q=ana", nil)["data"].([]any)); n != 0 {
		t.Fatalf("leads left: %d", n)
	}
	if l := owner.must(200, "GET", base+"/records/lookup?namespace=user_id&value=u_ana", nil); len(l["data"].([]any)) != 0 {
		t.Fatalf("identity left: %v", l)
	}
	sup := owner.must(200, "GET", base+"/suppressions?source=record_deleted", nil)["data"].([]any)
	if len(sup) != 2 {
		t.Fatalf("suppressions: %v", sup)
	}
	// Her company and its other people stay.
	owner.must(200, "GET", base+"/records/"+acme, nil)
	owner.must(200, "GET", base+"/records/"+ben, nil)

	// Deleting a company keeps its people, unlinked.
	owner.must(200, "DELETE", base+"/records/"+acme, nil)
	if rels := owner.must(200, "GET", base+"/records/"+ben+"/relationships", nil)["data"].([]any); len(rels) != 0 {
		t.Fatalf("links left: %v", rels)
	}
	owner.must(200, "GET", base+"/records/"+ben, nil)
	owner.must(404, "DELETE", base+"/records/"+acme, nil)

	if len(named(got(), "record.deleted")) != 2 {
		t.Fatal("no record.deleted events")
	}
}
