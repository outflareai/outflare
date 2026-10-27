package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestCustomerEvents(t *testing.T) {
	got := track.Capture(t)
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/catalog/types/person/fields", map[string]any{"label": "Plan", "value_type": "text"})
	results := func(body map[string]any) []map[string]any {
		var out []map[string]any
		for _, r := range body["results"].([]any) {
			out = append(out, r.(map[string]any))
		}
		return out
	}

	// An anonymous visitor's page views wait unmatched.
	b := owner.must(200, "POST", base+"/customer-events", map[string]any{"source": "segment", "batch": []map[string]any{
		{"type": "page", "anonymousId": "anon-1", "name": "Pricing", "messageId": "m1", "properties": map[string]any{"path": "/pricing"}},
		{"type": "track", "anonymousId": "anon-1", "event": "Viewed Demo", "messageId": "m2"},
		{"type": "track", "event": "No One"},
		{"type": "launch", "userId": "u1"},
	}})
	res := results(b)
	if b["accepted"] != 2.0 || b["rejected"] != 2.0 || res[0]["name"] != "page.viewed" || res[1]["name"] != "viewed_demo" || res[0]["record_id"] != nil {
		t.Fatalf("anonymous batch: %v", b)
	}
	if res[2]["error"].(map[string]any)["code"] != "invalid_request" {
		t.Fatalf("rejected: %v", res[2])
	}
	// A retry is stored once.
	if again := owner.must(200, "POST", base+"/customer-events", map[string]any{"type": "page", "anonymousId": "anon-1", "messageId": "m1"}); again["duplicate"] != 1.0 {
		t.Fatalf("duplicate: %v", again)
	}

	// Signing up links the visitor, takes their traits and claims their earlier events.
	b = owner.must(200, "POST", base+"/customer-events", map[string]any{
		"type": "identify", "userId": "u1", "anonymousId": "anon-1", "messageId": "m3", "timestamp": time.Now().Add(-time.Minute).Format(time.RFC3339),
		"traits": map[string]any{"email": "Ada@Example.com", "firstName": "Ada", "lastName": "Lovelace", "plan": "pro", "favourite_colour": "teal", "country": "Narnia"},
	})
	res = results(b)
	ada, _ := res[0]["record_id"].(string)
	if ada == "" {
		t.Fatalf("identify: %v", b)
	}
	if ig := res[0]["ignored_traits"].([]any); len(ig) != 2 || ig[0] != "country" || ig[1] != "favourite_colour" {
		t.Fatalf("ignored traits: %v", ig)
	}
	rec := owner.must(200, "GET", base+"/records/"+ada, nil)
	attrs := rec["attributes"].(map[string]any)
	if rec["name"] != "Ada Lovelace" || rec["origin"] != "first_party" || attrs["plan"] != "pro" || rec["attribute_sources"].(map[string]any)["plan"] != "first_party" {
		t.Fatalf("person: %v", rec)
	}
	ids := map[string]map[string]any{}
	for _, i := range rec["identities"].([]any) {
		ids[i.(map[string]any)["namespace"].(string)] = i.(map[string]any)
	}
	if ids["user_id"]["value"] != "u1" || ids["anonymous_id"]["value"] != "anon-1" || ids["email"]["value"] != "ada@example.com" || ids["email"]["verified"] != false {
		t.Fatalf("identities: %v", ids)
	}
	if n := len(owner.must(200, "GET", base+"/customer-events?matched=false", nil)["data"].([]any)); n != 0 {
		t.Fatalf("unmatched after identify: %d", n)
	}

	// A later track by userId alone finds her; one by the anonymous id too.
	b = owner.must(200, "POST", base+"/customer-events", map[string]any{"batch": []map[string]any{
		{"type": "track", "userId": "u1", "event": "Order Completed", "properties": map[string]any{"total": 42}},
		{"type": "track", "anonymous_id": "anon-1", "event": "product.activated"},
	}})
	for _, r := range results(b) {
		if r["record_id"] != ada {
			t.Fatalf("matched to %v: %v", r["record_id"], b)
		}
	}

	// An unverified email never matches anyone: another user with the same
	// typed email is their own person.
	b = owner.must(200, "POST", base+"/customer-events", map[string]any{"type": "identify", "userId": "u2", "traits": map[string]any{"email": "ada@example.com"}})
	other := results(b)[0]["record_id"]
	if other == ada || other == nil {
		t.Fatalf("unverified email matched: %v", b)
	}
	// A verified one is claimed once, and a second user never takes it.
	owner.must(200, "POST", base+"/customer-events", map[string]any{"type": "identify", "userId": "u1", "traits": map[string]any{"email": "ada@example.com", "email_verified": true}})
	b = owner.must(200, "POST", base+"/customer-events", map[string]any{"type": "identify", "userId": "u2", "traits": map[string]any{"email": "ada@example.com", "email_verified": true}})
	if w := results(b)[0]["warnings"]; w == nil || results(b)[0]["record_id"] != other {
		t.Fatalf("second user and verified email: %v", b)
	}
	if l := owner.must(200, "GET", base+"/records/lookup?namespace=email&value=ada@example.com", nil); l["owner"] != ada {
		t.Fatalf("owner: %v", l)
	}
	// alias never merges two users.
	b = owner.must(200, "POST", base+"/customer-events", map[string]any{"type": "alias", "userId": "u1", "previousId": "u2"})
	if results(b)[0]["warnings"] == nil {
		t.Fatalf("alias: %v", b)
	}

	// group makes the company and adds it to her companies.
	b = owner.must(200, "POST", base+"/customer-events", map[string]any{"type": "group", "userId": "u1", "groupId": "org_9",
		"traits": map[string]any{"name": "Acme", "website": "https://www.acme.example/about", "industry": "software"}})
	acct, _ := b["results"].([]any)[0].(map[string]any)["record_id"].(string)
	if acct != ada {
		t.Fatalf("group record: %v", b)
	}
	rec = owner.must(200, "GET", base+"/records/"+ada, nil)
	companies, _ := rec["attributes"].(map[string]any)["companies"].([]any)
	if len(companies) != 1 {
		t.Fatalf("companies: %v", rec["attributes"])
	}
	company := owner.must(200, "GET", base+"/records/"+companies[0].(string), nil)
	if company["name"] != "Acme" || company["type"] != "account" {
		t.Fatalf("company: %v", company)
	}
	if l := owner.must(200, "GET", base+"/records/lookup?namespace=domain&value=acme.example", nil); l["owner"] != companies[0] {
		t.Fatalf("company domain: %v", l)
	}

	// Names land in the catalog: track names quarantined, the rest approved.
	defs := map[string]string{}
	for _, d := range owner.must(200, "GET", base+"/catalog/events", nil)["data"].([]any) {
		defs[d.(map[string]any)["name"].(string)] = d.(map[string]any)["status"].(string)
	}
	if defs["viewed_demo"] != "quarantined" || defs["order_completed"] != "quarantined" || defs["page.viewed"] != "approved" || defs["profile.identified"] != "approved" {
		t.Fatalf("event definitions: %v", defs)
	}

	// Consent is a log; the latest per channel, topic and purpose is current.
	cbase := base + "/records/" + ada + "/consents"
	owner.must(400, "POST", cbase, map[string]any{"channel": "fax", "status": "granted"})
	owner.must(400, "POST", cbase, map[string]any{"channel": "email", "status": "maybe"})
	owner.must(201, "POST", cbase, map[string]any{"channel": "email", "status": "granted", "source": "signup_form", "evidence": map[string]any{"form": "footer"}})
	owner.must(201, "POST", cbase, map[string]any{"channel": "email", "topic": "product_updates", "status": "withdrawn", "source": "preference_center"})
	cur := owner.must(200, "GET", cbase, nil)["data"].([]any)
	if len(cur) != 2 {
		t.Fatalf("current consents: %v", cur)
	}
	owner.must(201, "POST", cbase, map[string]any{"channel": "email", "status": "withdrawn", "source": "unsubscribe_link"})
	if n := len(owner.must(200, "GET", cbase+"?history=true", nil)["data"].([]any)); n != 3 {
		t.Fatalf("consent history: %d", n)
	}
	owner.must(400, "POST", base+"/records/"+companies[0].(string)+"/consents", map[string]any{"channel": "email", "status": "granted"})

	// The timeline has her events, changes, identities and consents, newest first, and pages.
	tl := owner.must(200, "GET", base+"/records/"+ada+"/timeline?limit=200", nil)["data"].([]any)
	kinds := map[string]int{}
	for _, it := range tl {
		kinds[it.(map[string]any)["kind"].(string)]++
	}
	if kinds["event"] < 6 || kinds["attribute_change"] == 0 || kinds["identity"] < 3 || kinds["consent"] != 3 {
		t.Fatalf("timeline kinds: %v", kinds)
	}
	page := owner.must(200, "GET", base+"/records/"+ada+"/timeline?limit=2&kinds=event", nil)
	next := owner.must(200, "GET", base+"/records/"+ada+"/timeline?limit=2&kinds=event&cursor="+page["next_cursor"].(string), nil)
	if next["data"].([]any)[0].(map[string]any)["id"] == page["data"].([]any)[1].(map[string]any)["id"] {
		t.Fatalf("timeline paging repeated an item")
	}
	owner.must(400, "GET", base+"/records/"+ada+"/timeline?kinds=nope", nil)

	if len(named(got(), "customer_events.ingested")) == 0 || len(named(got(), "consent.withdrawn")) != 2 {
		t.Fatal("ingest and consent events")
	}

	// A viewer reads but cannot send events.
	viewer := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "read", "scopes": []string{"customers.read"}})
	v := &client{t: t, h: h, bearer: viewer["token"].(string)}
	v.must(200, "GET", base+"/customer-events", nil)
	v.must(403, "POST", base+"/customer-events", map[string]any{"type": "track", "userId": "u1", "event": "x"})
}

func TestEventNames(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	cases := map[string]string{"Order Completed": "order_completed", "Signed Up!": "signed_up", "product.activated": "product.activated",
		"checkoutStarted": "checkout_started", "2FA Enabled": "event_2fa_enabled", "Cart . Viewed": "cart.viewed"}
	for in, want := range cases {
		b := owner.must(200, "POST", "/v1/workspaces/"+ws+"/customer-events", map[string]any{"type": "track", "userId": "u1", "event": in})
		if got := b["results"].([]any)[0].(map[string]any)["name"]; got != want {
			t.Errorf("%q: got %v, want %s", in, got, want)
		}
	}
}

// The customers package is what lifecycle email and other features use.
func TestCustomersHelpers(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	ada := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "identities": []map[string]any{
		{"namespace": "email", "value": "ada@example.com", "verified": true}}})["id"].(string)
	owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "identities": []map[string]any{
		{"namespace": "email", "value": "typo@example.com"}}})

	if id, ok, err := customers.OwnerOf(ctx, e.pool, ws, "email", " ADA@example.com"); err != nil || !ok || id != ada {
		t.Fatalf("owner: %v %v %v", id, ok, err)
	}
	if _, ok, _ := customers.OwnerOf(ctx, e.pool, ws, "email", "typo@example.com"); ok {
		t.Fatal("an unverified email has no owner")
	}

	consent := func() string {
		st, _, err := customers.Consent(ctx, e.pool, ws, ada, "email", "product_updates", "")
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	if consent() != "" {
		t.Fatal("no consent yet")
	}
	cbase := base + "/records/" + ada + "/consents"
	owner.must(201, "POST", cbase, map[string]any{"channel": "email", "status": "granted", "occurred_at": time.Now().Add(-time.Hour)})
	if consent() != customers.Granted {
		t.Fatal("channel grant covers the topic")
	}
	owner.must(201, "POST", cbase, map[string]any{"channel": "email", "topic": "product_updates", "status": "withdrawn", "occurred_at": time.Now().Add(-30 * time.Minute)})
	if consent() != customers.Withdrawn {
		t.Fatal("topic withdrawal wins")
	}
	owner.must(201, "POST", cbase, map[string]any{"channel": "email", "topic": "product_updates", "status": "granted", "occurred_at": time.Now().Add(-20 * time.Minute)})
	owner.must(201, "POST", cbase, map[string]any{"channel": "email", "status": "withdrawn"})
	if consent() != customers.Withdrawn {
		t.Fatal("a later withdrawal from the channel wins")
	}

	ev := customers.Event{RecordID: ada, Name: "email.delivered", Source: "lifecycle", MessageID: "email-1", Properties: map[string]any{"email_id": "e1"}}
	if ok, err := customers.Record(ctx, e.pool, ws, ev); err != nil || !ok {
		t.Fatalf("record: %v %v", ok, err)
	}
	if ok, _ := customers.Record(ctx, e.pool, ws, ev); ok {
		t.Fatal("recorded twice")
	}
	tl := owner.must(200, "GET", base+"/records/"+ada+"/timeline?kinds=event", nil)["data"].([]any)
	if len(tl) != 1 || tl[0].(map[string]any)["title"] != "email.delivered" || tl[0].(map[string]any)["source"] != "lifecycle" {
		t.Fatalf("timeline: %v", tl)
	}
}

func TestEnsurePerson(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	// Nobody holds it: a new person with the address verified.
	id, err := customers.EnsurePerson(ctx, e.pool, ws, "email", "New@Example.com", true, "")
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := customers.EnsurePerson(ctx, e.pool, ws, "email", "new@example.com", true, ""); again != id {
		t.Fatalf("second call made another person")
	}
	// One unverified copy is verified where it is.
	one := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "identities": []map[string]any{{"namespace": "email", "value": "one@example.com"}}})["id"].(string)
	if got, _ := customers.EnsurePerson(ctx, e.pool, ws, "email", "one@example.com", true, ""); got != one {
		t.Fatalf("one unverified copy: %s", got)
	}
	// Two people typed the same address: the inbox's owner gets their own record.
	a := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "identities": []map[string]any{{"namespace": "email", "value": "two@example.com"}}})["id"].(string)
	b := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "identities": []map[string]any{{"namespace": "email", "value": "two@example.com"}}})["id"].(string)
	got, _ := customers.EnsurePerson(ctx, e.pool, ws, "email", "two@example.com", true, "")
	if got == a || got == b {
		t.Fatalf("picked one of two unverified copies")
	}
	if err := customers.RecordConsent(ctx, e.pool, ws, customers.ConsentChange{RecordID: got, Channel: "email", Topic: "product_updates", Status: customers.Withdrawn, Source: "unsubscribe_link"}); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := customers.Consent(ctx, e.pool, ws, got, "email", "product_updates", "marketing"); st != customers.Withdrawn {
		t.Fatalf("consent: %s", st)
	}
	if err := customers.RecordConsent(ctx, e.pool, ws, customers.ConsentChange{RecordID: a, Channel: "email", Status: "maybe"}); err == nil {
		t.Fatal("bad status accepted")
	}
}
