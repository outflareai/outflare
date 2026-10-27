package api_test

import (
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestDuplicatesAndMerge(t *testing.T) {
	got := track.Capture(t)
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/catalog/types/person/fields", map[string]any{"label": "Plan", "value_type": "text"})
	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme"})["id"].(string)

	// A CRM import and a form sign-up typed the same email: a possible duplicate.
	crm := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ada L", "source": "crm_sync",
		"attributes": map[string]any{"job_title": "CTO", "companies": []string{acme}},
		"identities": []map[string]any{{"namespace": "email", "value": "ada@example.com"}, {"namespace": "hubspot.contact", "value": "hs-1"}}})["id"].(string)
	form := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "attributes": map[string]any{"plan": "pro", "job_title": "Engineer"},
		"identities": []map[string]any{{"namespace": "email", "value": "ada@example.com"}}})["id"].(string)
	owner.must(201, "POST", base+"/records/"+form+"/consents", map[string]any{"channel": "email", "status": "granted"})
	dups := owner.must(200, "GET", base+"/records/duplicates", nil)["data"].([]any)
	if len(dups) != 1 {
		t.Fatalf("duplicates: %v", dups)
	}
	d := dups[0].(map[string]any)
	if d["reason"] != "email" || d["status"] != "open" || d["mergeable"] != true || len(d["records"].([]any)) != 2 {
		t.Fatalf("duplicate: %v", d)
	}

	// Merge the form sign-up into the CRM record.
	owner.must(400, "POST", base+"/records/"+crm+"/merge", map[string]any{"from": crm})
	owner.must(400, "POST", base+"/records/"+crm+"/merge", map[string]any{"from": acme})
	m := owner.must(200, "POST", base+"/records/"+crm+"/merge", map[string]any{"from": form})
	kept := m["record"].(map[string]any)
	attrs := kept["attributes"].(map[string]any)
	// manual (the form) outranks crm_sync, so its job title wins; plan is new.
	if attrs["plan"] != "pro" || attrs["job_title"] != "Engineer" || len(attrs["companies"].([]any)) != 1 {
		t.Fatalf("merged attributes: %v", attrs)
	}
	ns := map[string]int{}
	for _, i := range kept["identities"].([]any) {
		ns[i.(map[string]any)["namespace"].(string)]++
	}
	if ns["email"] != 1 || ns["hubspot.contact"] != 1 {
		t.Fatalf("merged identities: %v", ns)
	}
	gone := owner.must(200, "GET", base+"/records/"+form, nil)
	if gone["merged_into"] != crm || gone["archived"] != true {
		t.Fatalf("merged record: %v", gone)
	}
	if c := owner.must(200, "GET", base+"/records/"+crm+"/consents", nil)["data"].([]any); len(c) != 1 {
		t.Fatalf("consent moved: %v", c)
	}
	if n := len(owner.must(200, "GET", base+"/records/duplicates", nil)["data"].([]any)); n != 0 {
		t.Fatalf("open duplicates after merge: %d", n)
	}
	owner.must(409, "POST", base+"/records/"+crm+"/merge", map[string]any{"from": form})
	if len(named(got(), "record.merged")) != 1 {
		t.Fatal("record.merged")
	}

	// Two product accounts are never merged, only linked.
	u1 := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "identities": []map[string]any{
		{"namespace": "user_id", "value": "u1"}, {"namespace": "phone", "value": "+61412345678"}}})["id"].(string)
	u2 := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "identities": []map[string]any{
		{"namespace": "user_id", "value": "u2"}, {"namespace": "phone", "value": "+61412345678"}}})["id"].(string)
	e := owner.must(409, "POST", base+"/records/"+u1+"/merge", map[string]any{"from": u2})
	if e["error"].(map[string]any)["code"] != "merge_refused" {
		t.Fatalf("merge two users: %v", e)
	}
	dups = owner.must(200, "GET", base+"/records/duplicates?record="+u1, nil)["data"].([]any)
	if len(dups) != 1 || dups[0].(map[string]any)["status"] != "blocked" || dups[0].(map[string]any)["mergeable"] != false {
		t.Fatalf("blocked pair: %v", dups)
	}
	id := dups[0].(map[string]any)["id"].(string)
	linked := owner.must(200, "POST", base+"/records/duplicates/"+id+"/link", nil)
	if linked["status"] != "linked" {
		t.Fatalf("link: %v", linked)
	}
	owner.must(409, "POST", base+"/records/duplicates/"+id+"/dismiss", nil)
	links := owner.must(200, "GET", base+"/records/"+u1+"/relationships", nil)["data"].([]any)
	if len(links) != 1 || links[0].(map[string]any)["kind"] != "same_human" {
		t.Fatalf("same_human link: %v", links)
	}
	if n := len(owner.must(200, "GET", base+"/records/duplicates?status=all", nil)["data"].([]any)); n != 2 {
		t.Fatalf("all pairs: %d", n)
	}

	// A third record with the phone, dismissed: not the same human.
	third := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "identities": []map[string]any{{"namespace": "phone", "value": "+61412345678"}}})["id"].(string)
	open := owner.must(200, "GET", base+"/records/duplicates?status=open&record="+third, nil)["data"].([]any)
	if len(open) != 2 {
		t.Fatalf("third's pairs: %v", open)
	}
	owner.must(200, "POST", base+"/records/duplicates/"+open[0].(map[string]any)["id"].(string)+"/dismiss", nil)
	owner.must(404, "POST", base+"/records/duplicates/00000000-0000-0000-0000-000000000000/dismiss", nil)
	if len(named(got(), "duplicate.dismissed")) != 1 || len(named(got(), "duplicate.linked")) != 1 {
		t.Fatal("decision events")
	}
}

// Someone who proves they own an email that a record without its own user
// id holds is that person: the records merge as the events arrive.
func TestIngestMergesOnVerifiedEmail(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws
	imported := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ada", "identities": []map[string]any{
		{"namespace": "email", "value": "ada@example.com", "verified": true}}})["id"].(string)
	owner.must(201, "POST", base+"/records/"+imported+"/consents", map[string]any{"channel": "email", "status": "granted"})
	b := owner.must(200, "POST", base+"/customer-events", map[string]any{"type": "track", "userId": "u1", "event": "Signed Up"})
	person := b["results"].([]any)[0].(map[string]any)["record_id"].(string)
	b = owner.must(200, "POST", base+"/customer-events", map[string]any{"type": "identify", "userId": "u1", "traits": map[string]any{"email": "ada@example.com", "email_verified": true}})
	r := b["results"].([]any)[0].(map[string]any)
	if r["record_id"] != person || len(r["merged"].([]any)) != 1 || r["merged"].([]any)[0] != imported {
		t.Fatalf("ingest merge: %v", r)
	}
	if l := owner.must(200, "GET", base+"/records/lookup?namespace=email&value=ada@example.com", nil); l["owner"] != person {
		t.Fatalf("owner after merge: %v", l)
	}
	if c := owner.must(200, "GET", base+"/records/"+person+"/consents", nil)["data"].([]any); len(c) != 1 {
		t.Fatalf("consent followed the merge: %v", c)
	}

	// Another user proving the same email is another account: listed, not merged.
	b = owner.must(200, "POST", base+"/customer-events", map[string]any{"type": "identify", "userId": "u2", "traits": map[string]any{"email": "ada@example.com", "email_verified": true}})
	r = b["results"].([]any)[0].(map[string]any)
	if r["merged"] != nil || r["warnings"] == nil {
		t.Fatalf("second user: %v", r)
	}
	dups := owner.must(200, "GET", base+"/records/duplicates", nil)["data"].([]any)
	if len(dups) != 1 || dups[0].(map[string]any)["status"] != "blocked" {
		t.Fatalf("blocked pair: %v", dups)
	}
}
