package api_test

import (
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestCatalog(t *testing.T) {
	got := track.Capture(t)
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws + "/catalog"

	// Every workspace starts with people and companies, and the shared option lists.
	c := owner.must(200, "GET", base, nil)
	types := c["types"].([]any)
	if len(types) != 2 || types[0].(map[string]any)["key"] != "person" || types[1].(map[string]any)["plural"] != "Companies" {
		t.Fatalf("built-in types: %v", types)
	}
	sets := map[string]bool{}
	for _, s := range c["option_sets"].([]any) {
		sets[s.(map[string]any)["key"].(string)] = true
	}
	for _, k := range []string{"country", "industry", "company_size", "seniority", "lifecycle_stage"} {
		if !sets[k] {
			t.Fatalf("missing option set %s: %v", k, sets)
		}
	}

	// A workspace adds a deal type with a stage list and fields.
	owner.must(400, "POST", base+"/types", map[string]any{"name": "Person"})
	deal := owner.must(201, "POST", base+"/types", map[string]any{"name": "Deal"})
	if deal["key"] != "deal" || deal["plural"] != "Deals" || deal["builtin"] != false {
		t.Fatalf("deal: %v", deal)
	}
	owner.must(400, "POST", base+"/types", map[string]any{"name": "Deal"})
	stages := owner.must(201, "POST", base+"/option-sets", map[string]any{"name": "Deal stage", "options": []map[string]any{
		{"key": "open", "label": "Open"}, {"key": "won", "label": "Won"}, {"key": "lost", "label": "Lost"}}})
	if stages["key"] != "deal_stage" || len(stages["options"].([]any)) != 3 {
		t.Fatalf("stages: %v", stages)
	}
	owner.must(400, "POST", base+"/types/deal/fields", map[string]any{"label": "Stage", "value_type": "status"})
	owner.must(400, "POST", base+"/types/deal/fields", map[string]any{"label": "Stage", "value_type": "status", "option_set": "nope"})
	owner.must(400, "POST", base+"/types/deal/fields", map[string]any{"label": "Name", "value_type": "text"})
	owner.must(201, "POST", base+"/types/deal/fields", map[string]any{"label": "Stage", "value_type": "status", "option_set": "deal_stage", "filterable": true})
	owner.must(201, "POST", base+"/types/deal/fields", map[string]any{"label": "Amount", "value_type": "currency"})
	owner.must(201, "POST", base+"/types/deal/fields", map[string]any{"label": "Company", "value_type": "record_reference", "target_type": "account"})
	owner.must(201, "POST", base+"/types/person/fields", map[string]any{"label": "Plan", "value_type": "text", "write_policy": "source_owned"})
	owner.must(400, "POST", base+"/types/person/fields", map[string]any{"label": "First name", "value_type": "text"})
	owner.must(404, "POST", base+"/types/nope/fields", map[string]any{"label": "X", "value_type": "text"})

	// A field's type never changes; built-ins never change.
	owner.must(400, "PATCH", base+"/types/deal/fields/amount", map[string]any{"value_type": "number"})
	owner.must(200, "PATCH", base+"/types/deal/fields/amount", map[string]any{"label": "Deal value"})
	owner.must(400, "PATCH", base+"/types/person/fields/first_name", map[string]any{"label": "Given name"})
	owner.must(400, "PATCH", base+"/types/person", map[string]any{"name": "Human"})

	// Options are archived, never deleted, so old values keep their meaning.
	upd := owner.must(200, "PATCH", base+"/option-sets/deal_stage", map[string]any{"options": []map[string]any{
		{"key": "open", "label": "Open"}, {"key": "won", "label": "Closed won"}}})
	opts := upd["options"].([]any)
	if len(opts) != 3 || opts[1].(map[string]any)["label"] != "Closed won" || opts[2].(map[string]any)["archived"] != true {
		t.Fatalf("archived option: %v", opts)
	}
	owner.must(400, "PATCH", base+"/option-sets/country", map[string]any{"name": "Nation"})

	// Events: defined, then quarantined and approved.
	owner.must(400, "POST", base+"/events", map[string]any{"name": "Product Activated"})
	ev := owner.must(201, "POST", base+"/events", map[string]any{"name": "product.activated", "label": "Activated",
		"properties": []map[string]any{{"key": "plan", "value_type": "text"}}})
	if ev["status"] != "approved" {
		t.Fatalf("event: %v", ev)
	}
	if e := owner.must(200, "PATCH", base+"/events/product.activated", map[string]any{"status": "deprecated"}); e["status"] != "deprecated" {
		t.Fatalf("deprecated: %v", e)
	}
	if n := len(owner.must(200, "GET", base+"/events?status=approved", nil)["data"].([]any)); n != 0 {
		t.Fatalf("approved events: %d", n)
	}

	// Archiving a type hides it unless asked for.
	owner.must(200, "PATCH", base+"/types/deal", map[string]any{"archived": true})
	if n := len(owner.must(200, "GET", base+"/types", nil)["data"].([]any)); n != 2 {
		t.Fatalf("types without archived: %d", n)
	}
	if n := len(owner.must(200, "GET", base+"/types?archived=true", nil)["data"].([]any)); n != 3 {
		t.Fatalf("types with archived: %d", n)
	}

	// Viewers read the catalog; only admins change it.
	viewer := &client{t: t, h: h, bearer: owner.must(201, "POST", "/v1/workspaces/"+ws+"/api-tokens",
		map[string]any{"name": "r", "scopes": []string{"customers.read"}})["token"].(string)}
	viewer.must(200, "GET", base, nil)
	viewer.must(403, "POST", base+"/types", map[string]any{"name": "Partner"})

	events := got()
	for _, name := range []string{"record_type.created", "record_type.archived", "field.created", "field.updated", "option_set.created",
		"option_set.updated", "event_definition.created", "event_definition.deprecated"} {
		if len(named(events, name)) == 0 {
			t.Fatalf("no %s event", name)
		}
	}
}

func TestRecords(t *testing.T) {
	got := track.Capture(t)
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws + "/records"
	owner.must(201, "POST", "/v1/workspaces/"+ws+"/catalog/types/person/fields", map[string]any{"label": "Plan", "value_type": "text", "write_policy": "source_owned"})

	// A company with a domain, and a person at it.
	owner.must(400, "POST", base, map[string]any{"name": "Acme"})
	owner.must(400, "POST", base, map[string]any{"type": "nope"})
	acme := owner.must(201, "POST", base, map[string]any{"type": "account", "name": "Acme", "origin": "first_party",
		"attributes": map[string]any{"industry": "software", "country": "AU", "website": "https://acme.example.com"},
		"identities": []map[string]any{{"namespace": "domain", "value": "https://www.Acme.example.com/about"}}})
	aid := acme["id"].(string)
	if ids := acme["identities"].([]any); len(ids) != 1 || ids[0].(map[string]any)["value"] != "acme.example.com" || ids[0].(map[string]any)["verified"] != true {
		t.Fatalf("domain identity: %v", ids)
	}
	// Values are checked against the catalog.
	for _, bad := range []map[string]any{
		{"industry": "made_up"}, {"country": "Australia"}, {"website": "javascript:alert(1)"}, {"unknown_field": 1},
		{"company_size": 7},
	} {
		owner.must(400, "POST", base, map[string]any{"type": "account", "name": "X", "attributes": bad})
	}

	ada := owner.must(201, "POST", base, map[string]any{"type": "person", "origin": "first_party",
		"attributes": map[string]any{"first_name": "Ada", "last_name": "Lovelace", "companies": []string{aid}, "seniority": "c_suite"},
		"identities": []map[string]any{
			{"namespace": "user_id", "value": "u_1"},
			{"namespace": "email", "value": " Ada@Example.com ", "verified": true},
			{"namespace": "phone", "value": "+61 412 345 678"},
			{"namespace": "device_token", "value": "tok-ios-1", "properties": map[string]any{"platform": "ios"}},
		}})
	pid := ada["id"].(string)
	if ada["name"] != "Ada Lovelace" || ada["version"] != 1.0 {
		t.Fatalf("person: %v", ada)
	}
	byNS := map[string]map[string]any{}
	for _, i := range ada["identities"].([]any) {
		byNS[i.(map[string]any)["namespace"].(string)] = i.(map[string]any)
	}
	if byNS["email"]["value"] != "ada@example.com" || byNS["email"]["verified"] != true || byNS["phone"]["value"] != "+61412345678" ||
		byNS["phone"]["verified"] != false || byNS["user_id"]["verified"] != true {
		t.Fatalf("identities: %v", byNS)
	}
	owner.must(400, "POST", base, map[string]any{"type": "person", "identities": []map[string]any{{"namespace": "device_token", "value": "x"}}})
	owner.must(400, "POST", base, map[string]any{"type": "person", "attributes": map[string]any{"companies": []string{pid}}})

	// The companies reference is the same graph as links.
	rels := owner.must(200, "GET", base+"/"+aid+"/relationships", nil)["data"].([]any)
	if len(rels) != 1 || rels[0].(map[string]any)["direction"] != "incoming" || rels[0].(map[string]any)["field_key"] != "companies" {
		t.Fatalf("company links: %v", rels)
	}

	// Identity edge cases. An unconfirmed sign-up with Ada's email gets its
	// own record and does not claim the address.
	spoof := owner.must(201, "POST", base, map[string]any{"type": "person", "origin": "first_party",
		"identities": []map[string]any{{"namespace": "user_id", "value": "u_2"}, {"namespace": "email", "value": "ada@example.com"}}})
	sid := spoof["id"].(string)
	// It cannot verify an address another record holds verified.
	e := owner.must(409, "POST", base+"/"+sid+"/identities", map[string]any{"namespace": "email", "value": "ada@example.com", "verified": true})
	if errCode(e) != "identity_taken" || !strings.Contains(e["error"].(map[string]any)["message"].(string), pid) {
		t.Fatalf("taken: %v", e)
	}
	owner.must(409, "POST", base, map[string]any{"type": "person", "identities": []map[string]any{{"namespace": "user_id", "value": "u_1"}}})
	// Lookup names the verified owner first, and lists the unverified holder.
	l := owner.must(200, "GET", base+"/lookup?namespace=email&value=ADA@example.com", nil)
	if l["owner"] != pid || len(l["data"].([]any)) != 2 || l["data"].([]any)[0].(map[string]any)["id"] != pid {
		t.Fatalf("lookup: %v", l)
	}
	if l := owner.must(200, "GET", base+"/lookup?namespace=email&value=nobody@example.com", nil); l["owner"] != nil || len(l["data"].([]any)) != 0 {
		t.Fatalf("lookup nobody: %v", l)
	}

	// Ada changes her email: the old one is closed as changed and kept.
	newEmail := owner.must(201, "POST", base+"/"+pid+"/identities", map[string]any{"namespace": "email", "value": "ada@newco.example.com",
		"verified": true, "replaces": byNS["email"]["id"]})
	if newEmail["verified"] != true {
		t.Fatalf("new email: %v", newEmail)
	}
	all := owner.must(200, "GET", base+"/"+pid+"/identities?closed=true", nil)["data"].([]any)
	closed := 0
	for _, i := range all {
		if i.(map[string]any)["closed_reason"] == "changed" {
			closed++
		}
	}
	if closed != 1 {
		t.Fatalf("closed identities: %v", all)
	}
	// Now the unconfirmed sign-up can prove the old address is theirs.
	if v := owner.must(200, "POST", base+"/"+sid+"/identities", map[string]any{"namespace": "email", "value": "ada@example.com", "verified": true}); v["verified"] != true {
		t.Fatalf("verified: %v", v)
	}
	// Re-sending an identity it already has verified changes nothing.
	owner.must(200, "POST", base+"/"+sid+"/identities", map[string]any{"namespace": "email", "value": "ada@example.com", "verified": true})
	// A changed email that belongs to another account is refused, never merged.
	owner.must(409, "POST", base+"/"+sid+"/identities", map[string]any{"namespace": "email", "value": "ada@newco.example.com", "verified": true})

	// Updates: versioned, recorded, and source precedence keeps manual edits.
	v := owner.must(200, "GET", base+"/"+pid, nil)["version"]
	if v != 2.0 {
		t.Fatalf("version after an email change: %v", v)
	}
	upd := owner.must(200, "PATCH", base+"/"+pid, map[string]any{"version": v, "attributes": map[string]any{"job_title": "Analyst"}})
	if upd["attributes"].(map[string]any)["job_title"] != "Analyst" || upd["attribute_sources"].(map[string]any)["job_title"] != "manual" {
		t.Fatalf("update: %v", upd)
	}
	e = owner.must(409, "PATCH", base+"/"+pid, map[string]any{"version": 1, "name": "Stale"})
	if errCode(e) != "record_changed" {
		t.Fatalf("stale: %v", e)
	}
	enr := owner.must(200, "PATCH", base+"/"+pid, map[string]any{"source": "enrichment", "attributes": map[string]any{"job_title": "Engineer", "city": "London"}})
	if enr["attributes"].(map[string]any)["job_title"] != "Analyst" || enr["attributes"].(map[string]any)["city"] != "London" ||
		len(enr["skipped"].([]any)) != 1 {
		t.Fatalf("precedence: %v", enr)
	}
	// Source-owned fields only take writes from their source.
	owner.must(400, "PATCH", base+"/"+pid, map[string]any{"attributes": map[string]any{"plan": "pro"}})
	owner.must(200, "PATCH", base+"/"+pid, map[string]any{"source": "first_party", "attributes": map[string]any{"plan": "pro"}})
	// Clearing a value.
	if c := owner.must(200, "PATCH", base+"/"+pid, map[string]any{"attributes": map[string]any{"city": nil}}); c["attributes"].(map[string]any)["city"] != nil {
		t.Fatalf("cleared: %v", c)
	}
	hist := owner.must(200, "GET", base+"/"+pid+"/history?field=job_title", nil)["data"].([]any)
	if len(hist) != 1 || hist[0].(map[string]any)["new_value"] != "Analyst" {
		t.Fatalf("history: %v", hist)
	}
	// Dropping Ada's company ends that link.
	owner.must(200, "PATCH", base+"/"+pid, map[string]any{"attributes": map[string]any{"companies": []string{}}})
	if n := len(owner.must(200, "GET", base+"/"+aid+"/relationships", nil)["data"].([]any)); n != 0 {
		t.Fatalf("links after removing company: %d", n)
	}

	// Links that are not fields.
	partner := owner.must(201, "POST", base, map[string]any{"type": "account", "name": "Northpeak"})["id"].(string)
	owner.must(400, "POST", base+"/"+aid+"/relationships", map[string]any{"to": aid, "kind": "partner_of"})
	owner.must(400, "POST", base+"/"+pid+"/relationships", map[string]any{"to": aid, "kind": "companies"})
	link := owner.must(201, "POST", base+"/"+aid+"/relationships", map[string]any{"to": partner, "kind": "partner_of"})
	owner.must(409, "POST", base+"/"+aid+"/relationships", map[string]any{"to": partner, "kind": "partner_of"})
	owner.must(200, "POST", base+"/"+partner+"/relationships/"+link["id"].(string)+"/end", nil)
	owner.must(409, "POST", base+"/"+partner+"/relationships/"+link["id"].(string)+"/end", nil)

	// Lists: by type, search by name or identity, archived hidden.
	if n := len(owner.must(200, "GET", base+"?type=person", nil)["data"].([]any)); n != 2 {
		t.Fatalf("people: %d", n)
	}
	if d := owner.must(200, "GET", base+"?q=newco", nil)["data"].([]any); len(d) != 1 || d[0].(map[string]any)["id"] != pid {
		t.Fatalf("search by email: %v", d)
	}
	owner.must(200, "PATCH", base+"/"+partner, map[string]any{"archived": true})
	if n := len(owner.must(200, "GET", base+"?type=account", nil)["data"].([]any)); n != 1 {
		t.Fatalf("accounts without archived: %d", n)
	}
	page := owner.must(200, "GET", base+"?limit=1", nil)
	if page["next_cursor"] == nil {
		t.Fatalf("no cursor: %v", page)
	}
	page2 := owner.must(200, "GET", base+"?limit=1&cursor="+page["next_cursor"].(string), nil)
	if page2["data"].([]any)[0].(map[string]any)["id"] == page["data"].([]any)[0].(map[string]any)["id"] {
		t.Fatalf("second page repeats the first")
	}

	// Closing an identity.
	owner.must(200, "POST", base+"/"+pid+"/identities/"+byNS["phone"]["id"].(string)+"/close", nil)
	owner.must(409, "POST", base+"/"+pid+"/identities/"+byNS["phone"]["id"].(string)+"/close", nil)

	// Readers read; other workspaces see nothing.
	reader := &client{t: t, h: h, bearer: owner.must(201, "POST", "/v1/workspaces/"+ws+"/api-tokens",
		map[string]any{"name": "r", "scopes": []string{"customers.read"}})["token"].(string)}
	reader.must(200, "GET", base+"/"+pid, nil)
	reader.must(403, "PATCH", base+"/"+pid, map[string]any{"name": "X"})
	ws2 := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Northpeak"})["id"].(string)
	owner.must(404, "GET", "/v1/workspaces/"+ws2+"/records/"+pid, nil)
	owner.must(404, "POST", "/v1/workspaces/"+ws2+"/records/"+pid+"/identities", map[string]any{"namespace": "user_id", "value": "u_9"})

	events := got()
	for _, name := range []string{"record.created", "record.updated", "record.archived", "identity.added", "identity.changed",
		"identity.verified", "identity.closed", "relationship.added", "relationship.ended"} {
		if len(named(events, name)) == 0 {
			t.Fatalf("no %s event", name)
		}
	}
}

func TestRecordsBrandLimited(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws
	acme := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme"})["id"].(string)
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak"})
	shared := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Shared"})["id"].(string)
	theirs := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Theirs", "brand_id": "northpeak",
		"identities": []map[string]any{{"namespace": "user_id", "value": "np_1"}}})["id"].(string)

	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "acme", "scopes": []string{"customers.manage"}, "brand": "acme"})
	agent := &client{t: t, h: h, bearer: tok["token"].(string)}
	// It adds records to its brand, reads shared ones but cannot change them.
	mine := agent.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Mine"})
	if mine["brand_id"] != acme {
		t.Fatalf("brand: %v", mine["brand_id"])
	}
	agent.must(200, "GET", base+"/records/"+shared, nil)
	agent.must(403, "PATCH", base+"/records/"+shared, map[string]any{"name": "X"})
	agent.must(404, "GET", base+"/records/"+theirs, nil)
	if n := len(agent.must(200, "GET", base+"/records", nil)["data"].([]any)); n != 2 {
		t.Fatalf("brand-limited list: %d", n)
	}
	// A claim held by a record it cannot see is refused without naming it.
	e := agent.must(409, "POST", base+"/records/"+mine["id"].(string)+"/identities", map[string]any{"namespace": "user_id", "value": "np_1"})
	if strings.Contains(e["error"].(map[string]any)["message"].(string), theirs) {
		t.Fatalf("leaked owner: %v", e)
	}
}
