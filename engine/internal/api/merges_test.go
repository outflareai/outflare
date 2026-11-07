package api_test

import (
	"context"
	"net/http"
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

// proofs sends identify calls with an email the person proved they own,
// and reads back who it went to.
type proofs struct {
	t     *testing.T
	owner *client
	base  string
}

func newProofs(t *testing.T, h http.Handler) proofs {
	owner, ws := setupOwner(t, h)
	return proofs{t, owner, "/v1/workspaces/" + ws}
}

func (p proofs) send(event map[string]any) map[string]any {
	p.t.Helper()
	return p.owner.must(200, "POST", p.base+"/customer-events", event)["results"].([]any)[0].(map[string]any)
}

func (p proofs) identify(userID, email string) map[string]any {
	return map[string]any{"type": "identify", "userId": userID, "traits": map[string]any{"email": email, "email_verified": true}}
}

// typed adds a person holding email unverified, as an import does.
func (p proofs) typed(email string, ids ...map[string]any) string {
	p.t.Helper()
	ids = append(ids, map[string]any{"namespace": "email", "value": email})
	return p.owner.must(201, "POST", p.base+"/records", map[string]any{"type": "person", "name": "Imported", "identities": ids})["id"].(string)
}

// pairs lists rec's possible duplicates in its brand.
func (p proofs) pairs(rec string) []any {
	p.t.Helper()
	brand := p.owner.must(200, "GET", p.base+"/records/"+rec, nil)["brand_id"].(string)
	return p.owner.must(200, "GET", "/v1/brands/"+brand+"/records/duplicates?record="+rec, nil)["data"].([]any)
}

// mergedOnly checks an event merged from into its person, left no
// possible duplicate, and returns the person.
func (p proofs) mergedOnly(r map[string]any, from string) string {
	p.t.Helper()
	if m, _ := r["merged"].([]any); len(m) != 1 || m[0] != from {
		p.t.Fatalf("want %s merged in: %v", from, r)
	}
	person := r["record_id"].(string)
	if l := p.pairs(person); len(l) != 0 {
		p.t.Fatalf("possible duplicates left: %v", l)
	}
	if gone := p.owner.must(200, "GET", p.base+"/records/"+from, nil); gone["merged_into"] != person {
		p.t.Fatalf("merged record: %v", gone)
	}
	return person
}

// A verified email that no record holds verified belongs to the only
// record holding it unverified, when that record has no user id of its
// own: it is merged in, never left as a possible duplicate.
func TestIngestMergesTheOnlyUnverifiedCopy(t *testing.T) {
	p := newProofs(t, freshServer(t))

	// A new user: the import is merged into the person their userId makes.
	imported := p.typed("ada@example.com")
	ada := p.mergedOnly(p.send(p.identify("u1", "ada@example.com")), imported)
	if l := p.owner.must(200, "GET", p.base+"/records/lookup?namespace=email&value=ada@example.com", nil); l["owner"] != ada {
		t.Fatalf("owner: %v", l)
	}
	if rec := p.owner.must(200, "GET", p.base+"/records/"+ada, nil); rec["name"] != "Imported" || rec["origin"] != "first_party" {
		t.Fatalf("person: %v", rec)
	}

	// A known user: a track with only their userId made their record first.
	grace := p.send(map[string]any{"type": "track", "userId": "u2", "event": "Signed Up"})["record_id"].(string)
	imported = p.typed("grace@example.com")
	if p.mergedOnly(p.send(p.identify("u2", "grace@example.com")), imported) != grace {
		t.Fatal("the known user's record should be kept")
	}

	// A cold lead who signs up is a customer now, not a third-party lead.
	lead := p.owner.must(201, "POST", p.base+"/leads", map[string]any{"email": "noor@example.com"})
	noor := p.mergedOnly(p.send(p.identify("u3", "noor@example.com")), lead["record_id"].(string))
	if c := p.owner.must(200, "GET", p.base+"/records/"+noor, nil)["contactability"].(map[string]any); c["basis"] != "customer" {
		t.Fatalf("contact basis: %v", c)
	}
	if l := p.owner.must(200, "GET", p.base+"/leads/"+lead["id"].(string), nil); l["record_id"] != noor {
		t.Fatalf("lead's person: %v", l)
	}

	// In the brand the event is for.
	bravo := p.owner.must(201, "POST", p.base+"/brands", map[string]any{"name": "Bravo", "handle": "bravo"})["id"].(string)
	other := p.owner.must(201, "POST", "/v1/brands/"+bravo+"/records", map[string]any{"type": "person", "identities": []map[string]any{
		{"namespace": "email", "value": "ines@example.com"}}})["id"].(string)
	event := p.identify("u4", "ines@example.com")
	event["brand_id"] = bravo
	p.mergedOnly(p.send(event), other)
}

// A copy that may be someone else's is left where it is and listed as a
// possible duplicate, as before.
func TestIngestPairsACopyThatMayBeSomeoneElses(t *testing.T) {
	p := newProofs(t, freshServer(t))

	// Two unverified copies: someone typed another person's address.
	p.typed("lin@example.com")
	p.typed("lin@example.com")
	if r := p.send(p.identify("u1", "lin@example.com")); r["merged"] != nil || len(p.pairs(r["record_id"].(string))) != 2 {
		t.Fatalf("two copies: %v", r)
	}

	// A copy on another user is another account in the product.
	p.typed("max@example.com", map[string]any{"namespace": "user_id", "value": "u2-other"})
	r := p.send(p.identify("u2", "max@example.com"))
	if l := p.pairs(r["record_id"].(string)); r["merged"] != nil || len(l) != 1 || l[0].(map[string]any)["status"] != "blocked" {
		t.Fatalf("another user's copy: %v %v", r, l)
	}

	// A record holding other emails too may be a shared browser or a form
	// someone filled in for others.
	p.typed("noor@example.com", map[string]any{"namespace": "email", "value": "noor.work@example.com"})
	if r := p.send(p.identify("u5", "noor@example.com")); r["merged"] != nil || len(p.pairs(r["record_id"].(string))) != 1 {
		t.Fatalf("a copy beside another email: %v", r)
	}

	// Someone already said these two are different people.
	kai := p.send(map[string]any{"type": "identify", "userId": "u3", "traits": map[string]any{"email": "kai@example.com"}})["record_id"].(string)
	form := p.typed("kai@example.com")
	l := p.pairs(kai)
	if len(l) != 1 {
		t.Fatalf("typed twice: %v", l)
	}
	p.owner.must(200, "POST", p.base+"/records/duplicates/"+l[0].(map[string]any)["id"].(string)+"/dismiss", nil)
	if r := p.send(p.identify("u3", "kai@example.com")); r["record_id"] != kai || r["merged"] != nil {
		t.Fatalf("dismissed pair: %v", r)
	}
	if rec := p.owner.must(200, "GET", p.base+"/records/"+form, nil); rec["merged_into"] != nil {
		t.Fatalf("dismissed record merged: %v", rec)
	}

	// Another brand's person is someone else there.
	bravo := p.owner.must(201, "POST", p.base+"/brands", map[string]any{"name": "Bravo", "handle": "bravo"})["id"].(string)
	other := p.owner.must(201, "POST", "/v1/brands/"+bravo+"/records", map[string]any{"type": "person", "identities": []map[string]any{
		{"namespace": "email", "value": "ines@example.com"}}})["id"].(string)
	if r := p.send(p.identify("u4", "ines@example.com")); r["merged"] != nil || len(p.pairs(r["record_id"].(string))) != 0 || len(p.pairs(other)) != 0 {
		t.Fatalf("another brand's copy: %v", r)
	}
}

// An opt-out stays with the person when its record is merged into them.
func TestIngestMergeKeepsAnOptOut(t *testing.T) {
	e := freshEnv(t)
	p := newProofs(t, e.h)
	optedOut := func(person, email string) {
		t.Helper()
		c := p.owner.must(200, "GET", p.base+"/records/"+person, nil)["contactability"].(map[string]any)
		if c["basis"] != "opted_out" || c["allowed"].(map[string]any)["marketing"] != false {
			t.Fatalf("contactability: %v", c)
		}
		var holder string
		if err := e.pool.QueryRow(context.Background(), `SELECT record_id::text FROM suppressions WHERE value = $1`, email).Scan(&holder); err != nil || holder != person {
			t.Fatalf("suppression's person: %s %v", holder, err)
		}
		check := p.owner.must(200, "POST", p.base+"/recipients/check", map[string]any{"emails": []string{email}})["data"].([]any)[0].(map[string]any)
		if check["allowed"] != false {
			t.Fatalf("recipients check: %v", check)
		}
	}

	// Unsubscribed before signing up.
	p.owner.must(200, "POST", p.base+"/suppressions", map[string]any{"values": []string{"ada@example.com"}, "reason": "unsubscribed"})
	r := p.send(p.identify("u1", "ada@example.com"))
	if m, _ := r["merged"].([]any); len(m) != 1 {
		t.Fatalf("sign-up: %v", r)
	}
	optedOut(r["record_id"].(string), "ada@example.com")

	// Unsubscribed while known only by their userId.
	grace := p.send(map[string]any{"type": "track", "userId": "u2", "event": "Signed Up"})["record_id"].(string)
	p.owner.must(200, "POST", p.base+"/suppressions", map[string]any{"values": []string{"grace@example.com"}, "reason": "unsubscribed"})
	r = p.send(p.identify("u2", "grace@example.com"))
	if m, _ := r["merged"].([]any); r["record_id"] != grace || len(m) != 1 {
		t.Fatalf("known user: %v", r)
	}
	optedOut(grace, "grace@example.com")
}
