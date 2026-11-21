package api_test

import (
	"testing"
)

// Customer accounts: a brand's people setup starts at its business model's
// defaults and can be changed; with accounts on, a group call makes a
// customer account the person is a member of, linked to its company, and
// the person's own company is left alone. A member's role and a person's
// preferred email can be set, and both are in the person's history.
func TestPeopleSetupAndCustomerAccounts(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	bb := brandBase(t, e, ws)

	setup := owner.must(200, "GET", bb+"/people-setup", nil)
	if setup["sells_to"] != "mixed" || setup["accounts"] != false || setup["sells_to_default"] != true {
		t.Fatalf("defaults: %v", setup)
	}
	owner.must(400, "PATCH", bb+"/people-setup", map[string]any{"sells_to": "aliens"})
	owner.must(400, "PATCH", bb+"/people-setup", map[string]any{"color": "red"})
	setup = owner.must(200, "PATCH", bb+"/people-setup", map[string]any{"sells_to": "consumers", "accounts": true})
	if setup["sells_to"] != "consumers" || setup["accounts"] != true || setup["sells_to_default"] != false {
		t.Fatalf("patch: %v", setup)
	}
	if l := setup["lists"].([]any); len(l) != 3 || l[1] != "accounts" {
		t.Fatalf("consumers with accounts should list people, accounts, deals: %v", l)
	}
	setup = owner.must(200, "PATCH", bb+"/people-setup", map[string]any{"sells_to": nil})
	if setup["sells_to"] != "mixed" || setup["sells_to_default"] != true || setup["accounts"] != true {
		t.Fatalf("reset: %v", setup)
	}

	// A group call makes a customer account, not a company.
	ws_ := "/v1/workspaces/" + ws
	owner.must(200, "POST", ws_+"/customer-events", map[string]any{"type": "identify", "userId": "u1",
		"traits": map[string]any{"email": "ada@acme.example", "email_verified": true, "name": "Ada Park"}})
	b := owner.must(200, "POST", ws_+"/customer-events", map[string]any{"type": "group", "userId": "u1", "groupId": "ws_42",
		"traits": map[string]any{"name": "Acme team", "website": "https://acme.example", "plan": "pro"}})
	person := b["results"].([]any)[0].(map[string]any)["record_id"].(string)
	rec := owner.must(200, "GET", bb+"/records/"+person, nil)
	attrs := rec["attributes"].(map[string]any)
	accounts, _ := attrs["accounts"].([]any)
	if len(accounts) != 1 {
		t.Fatalf("person should be a member of one account: %v", attrs)
	}
	if c, _ := attrs["companies"].([]any); len(c) != 0 {
		t.Fatalf("being in an account must not set the person's company: %v", attrs)
	}
	acct := owner.must(200, "GET", bb+"/records/"+accounts[0].(string), nil)
	if acct["type"] != "customer_account" || acct["name"] != "Acme team" {
		t.Fatalf("account: %v", acct)
	}
	co, _ := acct["attributes"].(map[string]any)["company"].(string)
	if co == "" {
		t.Fatalf("account should link to its company: %v", acct["attributes"])
	}
	if c := owner.must(200, "GET", bb+"/records/"+co, nil); c["type"] != "account" {
		t.Fatalf("company: %v", c)
	}
	// The same group id again finds the same account.
	owner.must(200, "POST", ws_+"/customer-events", map[string]any{"type": "group", "userId": "u1", "groupId": "ws_42"})
	if n := len(owner.must(200, "GET", bb+"/records?type=customer_account", nil)["data"].([]any)); n != 1 {
		t.Fatalf("accounts: %d", n)
	}

	// A member's role.
	var link string
	for _, l := range owner.must(200, "GET", bb+"/records/"+person+"/relationships", nil)["data"].([]any) {
		if m := l.(map[string]any); m["field_key"] == "accounts" {
			link = m["id"].(string)
		}
	}
	if link == "" {
		t.Fatal("no accounts link")
	}
	owner.must(400, "PATCH", bb+"/records/"+person+"/relationships/"+link, map[string]any{"properties": map[string]any{"role": "king"}})
	got := owner.must(200, "PATCH", bb+"/records/"+person+"/relationships/"+link, map[string]any{"properties": map[string]any{"role": "admin"}})
	if got["properties"].(map[string]any)["role"] != "admin" {
		t.Fatalf("role: %v", got)
	}

	// A preferred email.
	owner.must(201, "POST", bb+"/records/"+person+"/identities", map[string]any{"namespace": "email", "value": "ada.park@acme.example"})
	var second, phone string
	owner.must(201, "POST", bb+"/records/"+person+"/identities", map[string]any{"namespace": "phone", "value": "+15555550100"})
	for _, i := range owner.must(200, "GET", bb+"/records/"+person+"/identities", nil)["data"].([]any) {
		m := i.(map[string]any)
		if m["value"] == "ada.park@acme.example" {
			second = m["id"].(string)
		}
		if m["namespace"] == "phone" {
			phone = m["id"].(string)
		}
		if m["preferred"] != false {
			t.Fatalf("nothing is preferred yet: %v", m)
		}
	}
	owner.must(400, "POST", bb+"/records/"+person+"/identities/"+phone+"/prefer", nil)
	if p := owner.must(200, "POST", bb+"/records/"+person+"/identities/"+second+"/prefer", nil); p["preferred"] != true {
		t.Fatalf("prefer: %v", p)
	}
	hist := map[string]bool{}
	for _, h := range owner.must(200, "GET", bb+"/records/"+person+"/history", nil)["data"].([]any) {
		m := h.(map[string]any)
		k, _ := m["field_key"].(string)
		if k == "" {
			k, _ = m["field"].(string)
		}
		hist[k] = true
	}
	if !hist["preferred_email"] || !hist["account_role"] || !hist["accounts"] {
		t.Fatalf("history: %v", hist)
	}
}
