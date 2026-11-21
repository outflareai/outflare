package api_test

import (
	"testing"
)

// One People list: a person's Source is set once from how they arrived (or
// what an import says), a cold contact gets no marketing email until they
// opt in, Source can be changed but not emptied, and people go straight
// from the list into a sequence.
func TestPeopleSourceMarketingAndSequences(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	bb := brandBase(t, e, ws)

	src := func(r map[string]any) any { return r["attributes"].(map[string]any)["source"] }
	cold := owner.must(201, "POST", bb+"/records", map[string]any{"type": "person", "origin": "prospect",
		"attributes": map[string]any{"first_name": "Ivy"}, "identities": []map[string]any{{"namespace": "email", "value": "ivy@cedar.example"}}})
	if src(cold) != "cold" {
		t.Fatalf("a prospect should start as Cold: %v", cold["attributes"])
	}
	signup := owner.must(201, "POST", bb+"/records", map[string]any{"type": "person", "origin": "first_party",
		"identities": []map[string]any{{"namespace": "email", "value": "ana@acme.example", "verified": true}}})
	if src(signup) != "signup" {
		t.Fatalf("someone from the brand's own site should start as Sign-up: %v", signup["attributes"])
	}
	// Its own history has the guess.
	hist := owner.must(200, "GET", bb+"/records/"+cold["id"].(string)+"/history", nil)
	found := false
	for _, h := range hist["data"].([]any) {
		if m, _ := h.(map[string]any); m["field_key"] == "source" || m["field"] == "source" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no Source change in the history: %v", hist)
	}

	// An import that says where people are from; Cold means no marketing email.
	imp := owner.must(200, "POST", bb+"/records/import", map[string]any{"source": "cold", "people": []map[string]any{{"email": "dan@cedar.example", "name": "Dan Reyes"}}})
	if imp["created"] != 1.0 {
		t.Fatalf("import: %v", imp)
	}
	owner.must(400, "POST", bb+"/records/import", map[string]any{"source": "lead", "people": []map[string]any{{"email": "x@cedar.example"}}})

	no := owner.must(200, "GET", bb+"/records?type=person&marketing=no", nil)["data"].([]any)
	yes := owner.must(200, "GET", bb+"/records?type=person&marketing=yes", nil)["data"].([]any)
	if len(no) != 2 || len(yes) != 1 {
		t.Fatalf("marketing filter: %d no, %d yes", len(no), len(yes))
	}
	for _, r := range no {
		m := r.(map[string]any)
		if src(m) != "cold" || m["contactability"].(map[string]any)["allowed"].(map[string]any)["marketing"] != false {
			t.Fatalf("a cold contact may be marketed to: %v", m)
		}
	}
	coldOnly := owner.must(200, "GET", bb+"/records?type=person&attribute=source:cold", nil)["data"].([]any)
	if len(coldOnly) != 2 {
		t.Fatalf("Cold filter: %v", coldOnly)
	}

	// Changed by hand, kept through later writes, never emptied.
	id := cold["id"].(string)
	got := owner.must(200, "PATCH", bb+"/records/"+id, map[string]any{"version": cold["version"], "attributes": map[string]any{"source": "event"}})
	if src(got) != "event" {
		t.Fatalf("Source should change by hand: %v", got["attributes"])
	}
	got = owner.must(200, "PATCH", bb+"/records/"+id, map[string]any{"version": got["version"], "attributes": map[string]any{"job_title": "CTO"}})
	if src(got) != "event" {
		t.Fatalf("another write lost Source: %v", got["attributes"])
	}
	owner.must(400, "POST", bb+"/records/"+id+"/fields/source/delete", map[string]any{"version": got["version"]})

	// Straight from People into a sequence.
	seq := owner.must(201, "POST", bb+"/sequences", map[string]any{"name": "S", "steps": []map[string]any{{"subject": "Hi", "body": "x"}}})
	noEmail := owner.must(201, "POST", bb+"/records", map[string]any{"type": "person", "origin": "prospect", "name": "No Email"})
	res := owner.must(200, "POST", bb+"/sequences/"+seq["id"].(string)+"/enrollments", map[string]any{"record_ids": []string{id, noEmail["id"].(string)}})
	if res["enrolled"] != 1.0 || res["no_email"] != 1.0 {
		t.Fatalf("enroll people: %v", res)
	}
	owner.must(400, "POST", bb+"/sequences/"+seq["id"].(string)+"/enrollments", map[string]any{"record_ids": []string{id}, "lead_ids": []string{id}})
}
