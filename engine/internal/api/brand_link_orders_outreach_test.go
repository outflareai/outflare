package api_test

import (
	"testing"
)

// The catalog cell menu: "Find email" reads only stored addresses, and
// "Send to a sequence" adds one to a brand sequence with Outreach's rules.
func TestLinkOrderDonorFindEmailAndSequence(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Elsewhere", "handle": "elsewhere"})["id"].(string)
	root := "/v1/brands/" + brand + "/link-orders"

	donor := owner.must(201, "POST", root+"/catalog", map[string]any{
		"domain": "news.example", "intermediary": "Acme Links", "notes": "Bought through Sam (sam@broker-mail.example).",
	})
	id := donor["id"].(string)

	// Nothing at the site is stored yet: only the intermediary in the notes.
	got := owner.must(200, "GET", root+"/catalog/items/"+id+"/contacts", nil)
	cs := got["contacts"].([]any)
	if got["domain"] != "news.example" || len(cs) != 1 || len(got["searched"].([]any)) != 4 {
		t.Fatalf("contacts before: %v", got)
	}
	if c := cs[0].(map[string]any); c["email"] != "sam@broker-mail.example" || c["source"] != "notes" || c["at_domain"] != false || c["contacted"] != false {
		t.Fatalf("notes contact: %v", c)
	}

	seq := owner.must(201, "POST", "/v1/brands/"+brand+"/sequences", map[string]any{
		"name": "Donor outreach", "steps": []map[string]any{{"subject": "Guest post on {{company}}", "body": "Hi there"}},
	})["id"].(string)
	elsewhere := owner.must(201, "POST", "/v1/brands/"+other+"/sequences", map[string]any{
		"name": "Other brand", "steps": []map[string]any{{"subject": "Hi", "body": "Hi"}},
	})["id"].(string)

	out := owner.must(200, "POST", root+"/catalog/items/"+id+"/outreach", map[string]any{"sequence_id": seq, "email": " Editor@News.example ", "name": "Ana Roe"})
	if out["result"] != "enrolled" || out["email"] != "editor@news.example" || out["sequence_status"] != "draft" || out["sequence_name"] != "Donor outreach" || out["lead_id"] == "" {
		t.Fatalf("enrolled: %v", out)
	}
	// Adding it again changes nothing.
	if again := owner.must(200, "POST", root+"/catalog/items/"+id+"/outreach", map[string]any{"sequence_id": seq, "email": "editor@news.example"}); again["result"] != "already_enrolled" {
		t.Fatalf("again: %v", again)
	}
	enr := owner.must(200, "GET", "/v1/brands/"+brand+"/sequences/"+seq+"/enrollments", nil)["data"].([]any)
	if len(enr) != 1 || enr[0].(map[string]any)["email"] != "editor@news.example" {
		t.Fatalf("enrollments: %v", enr)
	}

	// The address at the site is now stored: first, as one of the
	// company's people, and contacted.
	cs = owner.must(200, "GET", root+"/catalog/items/"+id+"/contacts", nil)["contacts"].([]any)
	if len(cs) != 2 {
		t.Fatalf("contacts after: %v", cs)
	}
	if c := cs[0].(map[string]any); c["email"] != "editor@news.example" || c["at_domain"] != true || c["contacted"] != true || c["lead_id"] == nil || c["source"] != "person" {
		t.Fatalf("site contact: %v", c)
	}

	// A suppressed address is not added, and shows as suppressed.
	owner.must(200, "POST", base+"/suppressions", map[string]any{"values": []string{"blocked@news.example"}, "reason": "do_not_contact"})
	if r := owner.must(200, "POST", root+"/catalog/items/"+id+"/outreach", map[string]any{"sequence_id": seq, "email": "blocked@news.example"}); r["result"] != "suppressed" {
		t.Fatalf("suppressed: %v", r)
	}
	for _, x := range owner.must(200, "GET", root+"/catalog/items/"+id+"/contacts", nil)["contacts"].([]any) {
		if c := x.(map[string]any); c["email"] == "blocked@news.example" && c["suppressed"] != true {
			t.Fatalf("blocked should be suppressed: %v", c)
		}
	}

	// Another brand's sequence, a bad address, an unknown row.
	owner.must(404, "POST", root+"/catalog/items/"+id+"/outreach", map[string]any{"sequence_id": elsewhere, "email": "editor@news.example"})
	owner.must(400, "POST", root+"/catalog/items/"+id+"/outreach", map[string]any{"sequence_id": seq, "email": "not an address"})
	owner.must(400, "POST", root+"/catalog/items/"+id+"/outreach", map[string]any{"email": "editor@news.example"})
	owner.must(404, "GET", root+"/catalog/items/00000000-0000-0000-0000-000000000001/contacts", nil)

	// Catalog rights alone can look but not send: sending is an Outreach
	// write too. Reading the catalog is enough to find stored addresses.
	manager := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Catalog manager", "scopes": []string{"coverage.read", "coverage.manage"}})
	m := &client{t: t, h: e.h, bearer: manager["token"].(string)}
	m.must(200, "GET", root+"/catalog/items/"+id+"/contacts", nil)
	m.must(403, "POST", root+"/catalog/items/"+id+"/outreach", map[string]any{"sequence_id": seq, "email": "editor@news.example"})
	reader := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Catalog reader", "scopes": []string{"coverage.read", "sequences.manage"}})
	rd := &client{t: t, h: e.h, bearer: reader["token"].(string)}
	rd.must(403, "POST", root+"/catalog/items/"+id+"/outreach", map[string]any{"sequence_id": seq, "email": "editor@news.example"})
	both := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Both", "scopes": []string{"coverage.read", "coverage.manage", "sequences.manage"}})
	(&client{t: t, h: e.h, bearer: both["token"].(string)}).must(200, "POST", root+"/catalog/items/"+id+"/outreach", map[string]any{"sequence_id": seq, "email": "desk@news.example"})
}
