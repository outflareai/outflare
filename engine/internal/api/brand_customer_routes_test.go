package api_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// Brands own their data, step 2b part 2 (plans/brands/README.md, decision
// 6): the customer API is served under /v1/brands/{brand}/, where every row
// is that brand's, and another brand's caller gets 404. The workspace
// routes keep working for older callers: lists default to the first brand
// and take ?brand=.
func TestBrandCustomerRoutes(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	first := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	alpha := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Alpha", "handle": "alpha"})["id"].(string)
	bravo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Bravo", "handle": "bravo"})["id"].(string)
	a, b := "/v1/brands/"+alpha, "/v1/brands/"+bravo

	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Bravo app", "scopes": []string{"customers.manage", "customers.read", "catalog.manage"}, "brand": "bravo"})
	bravoApp := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	op, _ := joinAs(t, owner, ws, "op@example.com", map[string]any{"role": "operator", "brand_ids": []string{"bravo"}})

	// Records: made in the route's brand, and only there.
	ana := owner.must(201, "POST", a+"/records", map[string]any{"type": "person", "name": "Ana",
		"identities": []map[string]any{{"namespace": "email", "value": "ana@acme.example"}}})
	if ana["brand_id"] != alpha {
		t.Fatalf("made in %v, want alpha", ana["brand_id"])
	}
	anaID := ana["id"].(string)
	owner.must(400, "POST", a+"/records", map[string]any{"type": "person", "name": "Elsewhere", "brand_id": bravo})
	owner.must(201, "POST", a+"/records", map[string]any{"type": "person", "name": "Same", "brand_id": "alpha"})
	acme := owner.must(201, "POST", a+"/records", map[string]any{"type": "account", "name": "Acme"})["id"].(string)
	ben := owner.must(201, "POST", b+"/records", map[string]any{"type": "person", "name": "Ben",
		"identities": []map[string]any{{"namespace": "email", "value": "ben@northpeak.example"}}})["id"].(string)

	owner.must(200, "GET", a+"/records/"+anaID, nil)
	for _, sub := range []string{"", "/history", "/identities", "/relationships", "/timeline", "/consents"} {
		owner.must(200, "GET", a+"/records/"+anaID+sub, nil)
		owner.must(404, "GET", b+"/records/"+anaID+sub, nil)
		bravoApp.must(404, "GET", a+"/records/"+anaID+sub, nil)
		bravoApp.must(404, "GET", b+"/records/"+anaID+sub, nil)
		op.must(404, "GET", a+"/records/"+anaID+sub, nil)
	}
	bravoApp.must(200, "GET", b+"/records/"+ben, nil)
	op.must(200, "GET", b+"/records/"+ben, nil)
	owner.must(404, "PATCH", b+"/records/"+anaID, map[string]any{"name": "Ana L"})
	upd := owner.must(200, "PATCH", a+"/records/"+anaID, map[string]any{"name": "Ana Lima"})
	owner.must(400, "PATCH", a+"/records/"+anaID, map[string]any{"brand_id": bravo})
	owner.must(404, "DELETE", b+"/records/"+anaID+"?version="+fmt.Sprint(upd["version"]), nil)

	// Identities, links and consent, in the brand.
	id := owner.must(201, "POST", a+"/records/"+anaID+"/identities", map[string]any{"namespace": "phone", "value": "+15555550100"})["id"].(string)
	owner.must(404, "POST", b+"/records/"+anaID+"/identities/"+id+"/close", nil)
	owner.must(200, "POST", a+"/records/"+anaID+"/identities/"+id+"/close", nil)
	link := owner.must(201, "POST", a+"/records/"+anaID+"/relationships", map[string]any{"to": acme, "kind": "member_of"})["id"].(string)
	owner.must(400, "POST", a+"/records/"+anaID+"/relationships", map[string]any{"to": ben, "kind": "member_of"})
	owner.must(404, "POST", b+"/records/"+anaID+"/relationships/"+link+"/end", nil)
	owner.must(200, "POST", a+"/records/"+anaID+"/relationships/"+link+"/end", nil)
	owner.must(201, "POST", a+"/records/"+anaID+"/consents", map[string]any{"channel": "email", "status": "granted"})
	owner.must(404, "POST", b+"/records/"+anaID+"/consents", map[string]any{"channel": "email", "status": "withdrawn"})
	if c := owner.must(200, "GET", a+"/records/"+anaID+"/consents", nil)["data"].([]any); len(c) != 1 {
		t.Fatalf("consents: %v", c)
	}
	owner.must(409, "POST", a+"/records/"+anaID+"/review", map[string]any{"decision": "approve"})
	owner.must(404, "POST", b+"/records/"+anaID+"/review", map[string]any{"decision": "approve"})

	// Lists: the brand route lists its brand; the workspace route the
	// first brand unless ?brand= says otherwise.
	names := func(c *client, path string) []string {
		t.Helper()
		var out []string
		for _, r := range c.must(200, "GET", path, nil)["data"].([]any) {
			out = append(out, fmt.Sprint(r.(map[string]any)["name"]))
		}
		slices.Sort(out)
		return out
	}
	if n := names(owner, a+"/records?type=person"); strings.Join(n, ",") != "Ana Lima,Same" {
		t.Fatalf("alpha's people: %v", n)
	}
	if n := names(bravoApp, b+"/records"); strings.Join(n, ",") != "Ben" {
		t.Fatalf("bravo's records: %v", n)
	}
	owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Firstling"})
	if n := names(owner, base+"/records"); strings.Join(n, ",") != "Firstling" {
		t.Fatalf("workspace records default to the first brand: %v", n)
	}
	if n := names(owner, base+"/records?brand=alpha&type=person"); strings.Join(n, ",") != "Ana Lima,Same" {
		t.Fatalf("workspace records of alpha: %v", n)
	}
	if n := names(owner, base+"/records?brand=all&type=person"); len(n) != 4 {
		t.Fatalf("workspace records of every brand: %v", n)
	}
	// A brand-limited caller's workspace list is its own brand's.
	if n := names(op, base+"/records"); strings.Join(n, ",") != "Ben" {
		t.Fatalf("operator's workspace records: %v", n)
	}
	bravoApp.must(404, "GET", a+"/records", nil)
	op.must(404, "GET", a+"/records", nil)
	// The path's brand can't be widened.
	if n := names(owner, a+"/records?brand=all&type=person"); len(n) != 2 {
		t.Fatalf("?brand= on a brand route: %v", n)
	}

	// Duplicates and merges, in the brand.
	dup := owner.must(201, "POST", a+"/records", map[string]any{"type": "person", "name": "Ana again",
		"identities": []map[string]any{{"namespace": "email", "value": "ana@acme.example"}}})["id"].(string)
	pairs := owner.must(200, "GET", a+"/records/duplicates", nil)["data"].([]any)
	if len(pairs) != 1 {
		t.Fatalf("alpha's pairs: %v", pairs)
	}
	if l := owner.must(200, "GET", b+"/records/duplicates", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("bravo's pairs: %v", l)
	}
	if l := owner.must(200, "GET", base+"/records/duplicates", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("the first brand's pairs: %v", l)
	}
	pair := pairs[0].(map[string]any)["id"].(string)
	owner.must(404, "POST", b+"/records/duplicates/"+pair+"/dismiss", nil)
	bravoApp.must(404, "POST", a+"/records/duplicates/"+pair+"/dismiss", nil)
	owner.must(400, "POST", b+"/records/"+anaID+"/merge", map[string]any{"from": dup})
	owner.must(400, "POST", a+"/records/"+anaID+"/merge", map[string]any{"from": ben})
	owner.must(200, "POST", a+"/records/"+anaID+"/merge", map[string]any{"from": dup})

	// Stored product events, per brand.
	owner.must(200, "POST", base+"/customer-events", map[string]any{"brand_id": "alpha", "batch": []any{
		map[string]any{"type": "track", "event": "trial.started", "userId": "u_a", "traits": map[string]any{}},
	}})
	if l := owner.must(200, "GET", a+"/customer-events", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("alpha's events: %v", l)
	}
	if l := bravoApp.must(200, "GET", b+"/customer-events", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("bravo's events: %v", l)
	}
	bravoApp.must(404, "GET", a+"/customer-events", nil)
	if l := owner.must(200, "GET", base+"/customer-events", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("the first brand's events: %v", l)
	}

	// Smart and static lists, per brand.
	list := owner.must(201, "POST", a+"/smart-lists", map[string]any{"name": "Hands off", "kind": "static"})
	if list["brand_id"] != alpha {
		t.Fatalf("list made in %v", list["brand_id"])
	}
	lid := list["id"].(string)
	owner.must(400, "POST", a+"/smart-lists", map[string]any{"name": "Other", "kind": "static", "brand_id": bravo})
	owner.must(400, "PATCH", a+"/smart-lists/"+lid, map[string]any{"brand_id": bravo})
	owner.must(200, "POST", a+"/smart-lists/"+lid+"/members", map[string]any{"record_ids": []string{anaID, ben}})
	if m := owner.must(200, "GET", a+"/smart-lists/"+lid+"/members", nil)["data"].([]any); len(m) != 1 {
		t.Fatalf("members: %v", m)
	}
	for _, sub := range []string{"", "/members"} {
		owner.must(404, "GET", b+"/smart-lists/"+lid+sub, nil)
		bravoApp.must(404, "GET", b+"/smart-lists/"+lid+sub, nil)
	}
	owner.must(404, "DELETE", b+"/smart-lists/"+lid, nil)
	if n := names(owner, a+"/smart-lists"); strings.Join(n, ",") != "Hands off" {
		t.Fatalf("alpha's lists: %v", n)
	}
	if n := names(owner, b+"/smart-lists"); len(n) != 0 {
		t.Fatalf("bravo's lists: %v", n)
	}
	if n := names(owner, base+"/smart-lists"); slices.Contains(n, "Hands off") {
		t.Fatalf("the first brand's lists: %v", n)
	}
	if n := names(owner, base+"/smart-lists?brand=alpha"); !slices.Contains(n, "Hands off") {
		t.Fatalf("workspace lists of alpha: %v", n)
	}
	prev := owner.must(200, "POST", b+"/smart-lists/preview", map[string]any{"rules": []any{}, "brand_id": alpha})
	if prev["members"] != 1.0 {
		t.Fatalf("bravo's preview counts %v", prev)
	}

	// CRM imports, per brand.
	if l := owner.must(200, "GET", a+"/crm-imports", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("crm imports: %v", l)
	}
	owner.must(400, "POST", a+"/crm-imports", map[string]any{})
	owner.must(404, "POST", a+"/crm-imports", map[string]any{"integration_id": first})
	owner.must(404, "GET", a+"/crm-imports/"+first, nil)
	bravoApp.must(404, "GET", a+"/crm-imports", nil)
}

// Query for one brand: every customer table holds the brand's rows only,
// whatever the SQL does, and each record type of the brand is a table.
func TestBrandQuery(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	alpha := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Alpha", "handle": "alpha"})["id"].(string)
	bravo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Bravo", "handle": "bravo"})["id"].(string)
	a, b := "/v1/brands/"+alpha, "/v1/brands/"+bravo

	for _, p := range []struct{ brand, name, email string }{{a, "Ana", "ana@acme.example"}, {b, "Ben", "ben@northpeak.example"}} {
		rec := owner.must(201, "POST", p.brand+"/records", map[string]any{"type": "person", "name": p.name,
			"identities": []map[string]any{{"namespace": "email", "value": p.email}}})["id"].(string)
		owner.must(201, "POST", p.brand+"/records/"+rec+"/consents", map[string]any{"channel": "email", "status": "granted"})
		owner.must(201, "POST", p.brand+"/records", map[string]any{"type": "account", "name": p.name + " Co"})
	}
	owner.must(200, "POST", base+"/customer-events", map[string]any{"brand_id": "bravo", "batch": []any{
		map[string]any{"type": "track", "event": "trial.started", "userId": "u_b", "traits": map[string]any{}},
	}})
	owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Firstling"})

	// Each brand has its own subscription type; Alpha's has fields.
	for _, br := range []string{a, b} {
		owner.must(201, "POST", br+"/catalog/types", map[string]any{"name": "Subscription", "key": "subscription", "plural": "Subscriptions"})
	}
	owner.must(201, "POST", a+"/catalog/types/subscription/fields", map[string]any{"label": "Plan", "value_type": "text"})
	owner.must(201, "POST", a+"/catalog/types/subscription/fields", map[string]any{"label": "Seats", "value_type": "number"})
	owner.must(201, "POST", a+"/records", map[string]any{"type": "subscription", "name": "Alpha sub", "attributes": map[string]any{"plan": "pro", "seats": 5}})
	owner.must(201, "POST", b+"/records", map[string]any{"type": "subscription", "name": "Bravo sub"})

	rows := func(c *client, path, sql string) [][]any {
		t.Helper()
		return queryRows(t, c.must(200, "POST", path, map[string]any{"sql": sql}))
	}
	one := func(path, sql string) any {
		t.Helper()
		r := rows(owner, path, sql)
		if len(r) != 1 || len(r[0]) != 1 {
			t.Fatalf("%s: %v", sql, r)
		}
		return r[0][0]
	}
	q := a + "/query"
	if r := rows(owner, q, "select name, email from people order by name"); len(r) != 1 || r[0][0] != "Ana" {
		t.Fatalf("alpha's people: %v", r)
	}
	// Crafted SQL reaches no other brand's rows.
	for _, sql := range []string{
		"select count(*) from people where brand_id = '" + bravo + "'",
		"select count(*) from records where brand_id <> '" + alpha + "'",
		"select count(*) from records where brand_id is null",
		"with r as (select * from records) select count(*) from r where name like 'B%' or name = 'Firstling'",
		"select count(*) from (select * from customer_events) e",
		"select count(*) from record_identities i where i.value like '%northpeak%'",
		"select count(*) from consents c join records r on r.id = c.record_id where r.brand_id = '" + bravo + "'",
		"select count(*) from brands where id <> '" + alpha + "'",
		"select count(*) from records r1 cross join lateral (select 1 from records r2 where r2.id = r1.id and r2.brand_id = '" + bravo + "') x",
		"select count(*) from subscription where brand_id <> '" + alpha + "'",
	} {
		if n := one(q, sql); n != 0.0 {
			t.Fatalf("%s: %v rows of another brand", sql, n)
		}
	}
	if n := one(q, "select count(*) from records"); n != 3.0 {
		t.Fatalf("alpha's records: %v", n)
	}
	if n := one(b+"/query", "select count(*) from customer_events"); n != 1.0 {
		t.Fatalf("bravo's events: %v", n)
	}
	for _, sql := range []string{"select * from public.records", "select * from public.query_scopes", "select * from pg_temp.subscription, public.records"} {
		if code, out := owner.do("POST", q, map[string]any{"sql": sql}); code != 400 {
			t.Fatalf("%s: %d %v", sql, code, out)
		}
	}
	// Outside a brand's tables nothing else is made: no temporary view
	// survives into another query, and the query can't make one.
	if code, out := owner.do("POST", q, map[string]any{"sql": "select * from pg_temp.people"}); code != 400 {
		t.Fatalf("a temporary people: %d %v", code, out)
	}
	if code, _ := owner.do("POST", q, map[string]any{"sql": "create temporary view x as select 1"}); code != 400 {
		t.Fatalf("a query made a view: %d", code)
	}
	// Leads belong to a brand (step 5): a brand's query reads its own.
	if r := rows(owner, q, "select count(*) from leads"); len(r) != 1 || r[0][0] != 0.0 {
		t.Fatalf("leads on a brand query: %v", r)
	}

	// Record types are tables, with a column per field.
	if r := rows(owner, q, "select name, plan, seats from subscription"); len(r) != 1 || r[0][0] != "Alpha sub" || r[0][1] != "pro" || r[0][2] != 5.0 {
		t.Fatalf("alpha's subscriptions: %v", r)
	}
	if r := rows(owner, b+"/query", "select name from subscription"); len(r) != 1 || r[0][0] != "Bravo sub" {
		t.Fatalf("bravo's subscriptions: %v", r)
	}
	if r := rows(owner, q, "select name from deal"); len(r) != 0 {
		t.Fatalf("deals: %v", r)
	}
	tables := owner.must(200, "GET", a+"/query/tables", nil)["data"].([]any)
	var sub map[string]any
	for _, tb := range tables {
		if m := tb.(map[string]any); m["name"] == "subscription" {
			sub = m
		}
	}
	if sub == nil || sub["available"] != true {
		t.Fatalf("no subscription table: %v", tables)
	}
	var cols []string
	for _, c := range sub["columns"].([]any) {
		cols = append(cols, c.(map[string]any)["name"].(string))
	}
	if !slices.Contains(cols, "plan") || !slices.Contains(cols, "seats") {
		t.Fatalf("subscription columns: %v", cols)
	}
	if code, _ := owner.do("POST", base+"/query?brand=all", map[string]any{"sql": "select * from subscription"}); code != 400 {
		t.Fatal("type tables exist outside a brand's query")
	}

	// Another brand's callers get 404.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Bravo agent", "scopes": []string{"customers.read"}, "brand": "bravo"})
	agent := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	agent.must(404, "POST", q, map[string]any{"sql": "select 1"})
	agent.must(404, "GET", a+"/query/tables", nil)
	if r := rows(agent, b+"/query", "select name from people where name <> ''"); len(r) != 1 || r[0][0] != "Ben" {
		t.Fatalf("bravo's agent: %v", r)
	}

	// The workspace route queries the first brand, ?brand= another, and
	// ?brand=all every brand as before.
	if r := rows(owner, base+"/query", "select name from people"); len(r) != 1 || r[0][0] != "Firstling" {
		t.Fatalf("workspace query: %v", r)
	}
	if r := rows(owner, base+"/query?brand=bravo", "select name from people where name <> ''"); len(r) != 1 || r[0][0] != "Ben" {
		t.Fatalf("workspace query of bravo: %v", r)
	}
	if r := rows(owner, base+"/query?brand=all", "select name from people where name <> ''"); len(r) != 3 {
		t.Fatalf("workspace query of every brand: %v", r)
	}

	ran := named(got(), "query.ran")
	if len(ran) == 0 || !slices.ContainsFunc(ran, func(ev track.Event) bool { return ev.Props["brand_route"] == true && ev.Props["type_tables"] == 1 }) {
		t.Fatalf("query.ran: %v", ran)
	}
}
