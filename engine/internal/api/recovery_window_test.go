package api_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/smartlists"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The 7-day recovery window (plans/customer-data-platform/README.md): a
// deleted record, field value, identity, link or event is hidden
// everywhere, shows in Recently deleted, comes back whole without firing
// anything, and is purged after 7 days. Erasure stays permanent.
func TestRecoveryWindow(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	acme := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	rp := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "website": "repocket.com"})["id"].(string)
	brand := "/v1/brands/" + acme

	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
	}
	event := func(rec, name, msg string, props map[string]any) string {
		t.Helper()
		ok, err := customers.Record(ctx, e.pool, ws, customers.Event{RecordID: rec, Name: name, MessageID: msg, Properties: props})
		if err != nil || !ok {
			t.Fatalf("event %s: %v %v", name, ok, err)
		}
		var id string
		if err := e.pool.QueryRow(ctx, `SELECT id::text FROM customer_events WHERE workspace_id = $1 AND message_id = $2`, ws, msg).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// What would make something fire or be sent: rule and journey jobs,
	// and webhook deliveries.
	firing := func() int {
		t.Helper()
		return count(`SELECT count(*) FROM jobs WHERE workspace_id = $1 AND kind LIKE ANY ('{rule.%,journey.%}'::text[])`, ws) +
			count(`SELECT count(*) FROM webhook_deliveries WHERE workspace_id = $1`, ws)
	}

	// Rules that would fire on Ana's event and her fields, and a webhook
	// for record changes.
	rule := owner.must(201, "POST", base+"/rules", map[string]any{"name": "Activated", "trigger": map[string]any{"kind": "event", "event": "product_activated"},
		"emit_event": "customer.activated"})["id"].(string)
	owner.must(200, "POST", base+"/rules/"+rule+"/activate", nil)
	change := owner.must(201, "POST", base+"/rules", map[string]any{"name": "Title set", "trigger": map[string]any{"kind": "change"},
		"conditions": []map[string]any{{"attribute": "job_title", "op": "eq", "value": "Head of Growth"}}, "emit_event": "customer.title_set"})["id"].(string)
	owner.must(200, "POST", base+"/rules/"+change+"/activate", nil)
	revenue := owner.must(201, "POST", base+"/rules", map[string]any{"name": "Big spender", "trigger": map[string]any{"kind": "change"},
		"conditions": []map[string]any{{"attribute": "lifetime_revenue", "op": "exists"}}, "emit_event": "customer.big_spender"})
	if id, ok := revenue["id"].(string); ok {
		owner.must(200, "POST", base+"/rules/"+id+"/activate", nil)
	}
	owner.must(201, "POST", base+"/webhooks", map[string]any{"url": "https://example.com/hook", "events": []string{"record.*", "identity.*", "relationship.*"}})

	company := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Northpeak", "brand_id": acme,
		"identities": []map[string]any{{"namespace": "domain", "value": "northpeak.example"}}})["id"].(string)
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana Lima", "brand_id": acme,
		"attributes": map[string]any{"first_name": "Ana", "job_title": "Head of Growth"},
		"identities": []map[string]any{{"namespace": "user_id", "value": "u_ana"}, {"namespace": "email", "value": "ana@example.com", "verified": true}}})["id"].(string)
	owner.must(201, "POST", base+"/records/"+ana+"/relationships", map[string]any{"to": company, "kind": "member_of"})
	activated := event(ana, "product_activated", "m-act", nil)
	paid1 := event(ana, "payment.succeeded", "m-pay1", map[string]any{"amount_minor": 2000, "currency": "USD", "provider": "stripe"})
	event(ana, "payment.succeeded", "m-pay2", map[string]any{"amount_minor": 3000, "currency": "USD", "provider": "stripe"})
	if _, err := e.pool.Exec(ctx, `INSERT INTO attribution_conversions (event_id, workspace_id, record_id, brand_id, name, occurred_at, value_minor, currency)
		SELECT id, workspace_id, record_id, brand_id, name, occurred_at, 2000, 'USD' FROM customer_events WHERE id = $1`, paid1); err != nil {
		t.Fatal(err)
	}
	everyone := owner.must(201, "POST", base+"/smart-lists", map[string]any{"name": "Everyone"})["id"].(string)
	refresh := func() smartlists.Result {
		t.Helper()
		var res smartlists.Result
		if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			var err error
			res, err = smartlists.Refresh(ctx, tx, ws, everyone)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := refresh(); res.Members != 1 {
		t.Fatalf("list: %+v", res)
	}
	reader := agentToken(t, owner, base, "Reader", "customers.read")
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Repocket app", "scopes": []string{"customers.manage", "customers.read"}, "brand": "repocket"})
	other := &client{t: t, h: e.h, bearer: tok["token"].(string)}

	// ---- A record ----
	rec := owner.must(200, "GET", base+"/records/"+ana, nil)
	other.must(404, "POST", "/v1/brands/"+acme+"/records/"+ana+"/delete", nil)
	owner.must(404, "POST", "/v1/brands/"+rp+"/records/"+ana+"/delete", nil)
	reader.must(403, "POST", brand+"/records/"+ana+"/delete", nil)
	owner.must(409, "POST", brand+"/records/"+ana+"/delete", map[string]any{"version": 99})
	del := owner.must(200, "POST", brand+"/records/"+ana+"/delete", map[string]any{"version": rec["version"]})
	if del["kind"] != "record" || del["record_id"] != ana || del["label"] != "Ana Lima" || del["restored_at"] != nil {
		t.Fatalf("deletion: %v", del)
	}
	counts := del["counts"].(map[string]any)
	if counts["record_identities"] != 2.0 || counts["record_relationships"] != 1.0 || counts["customer_events"] != 3.0 || counts["attribution_conversions"] != 1.0 {
		t.Fatalf("counts: %v", counts)
	}
	deleted, _ := time.Parse(time.RFC3339Nano, del["deleted_at"].(string))
	purge, _ := time.Parse(time.RFC3339Nano, del["purge_after"].(string))
	if d := purge.Sub(deleted); d < 167*time.Hour || d > 169*time.Hour {
		t.Fatalf("purged after %v", d)
	}
	recordDel := del["id"].(string)

	// Hidden everywhere.
	owner.must(404, "GET", base+"/records/"+ana, nil)
	owner.must(404, "PATCH", base+"/records/"+ana, map[string]any{"name": "Ana"})
	for _, q := range []string{"", "?archived=true", "?archived=all", "?q=ana"} {
		for _, r := range owner.must(200, "GET", base+"/records"+q, nil)["data"].([]any) {
			if r.(map[string]any)["id"] == ana {
				t.Fatalf("listed with %q", q)
			}
		}
	}
	if hits := owner.must(200, "GET", base+"/records/lookup?namespace=email&value=ana@example.com", nil)["data"].([]any); len(hits) != 0 {
		t.Fatalf("lookup: %v", hits)
	}
	if hits := owner.must(200, "GET", base+"/search?q=ana", nil)["data"].([]any); len(hits) != 0 {
		t.Fatalf("search: %v", hits)
	}
	for _, sql := range []string{
		"select count(*) from records where name = 'Ana Lima'",
		"select count(*) from people where name = 'Ana Lima'",
		"select count(*) from record_identities where value = 'ana@example.com'",
		"select count(*) from customer_events where name = 'product_activated'",
	} {
		if rows := queryRows(t, owner.must(200, "POST", base+"/query", map[string]any{"sql": sql})); rows[0][0] != 0.0 {
			t.Fatalf("%s: %v", sql, rows)
		}
	}
	for _, l := range owner.must(200, "GET", base+"/records/"+company+"/relationships", nil)["data"].([]any) {
		t.Fatalf("company still linked: %v", l)
	}
	if res := refresh(); res.Members != 0 || res.Left != 0 {
		t.Fatalf("list after delete: %+v", res)
	}
	for _, m := range owner.must(200, "GET", base+"/smart-lists/"+everyone+"/members", nil)["data"].([]any) {
		if m.(map[string]any)["id"] == ana {
			t.Fatal("deleted person listed as a member")
		}
	}
	if n := count(`SELECT count(*) FROM attribution_conversions WHERE event_id = $1`, paid1); n != 0 {
		t.Fatalf("attribution kept: %d", n)
	}
	// An email sent to her identity now finds nobody; a sync would make a
	// new person.
	if n := count(`SELECT count(*) FROM record_identities WHERE workspace_id = $1 AND value = 'ana@example.com'`, ws); n != 0 {
		t.Fatalf("identity still there: %d", n)
	}
	// Recently deleted, in its brand only.
	list := owner.must(200, "GET", brand+"/recently-deleted", nil)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != recordDel {
		t.Fatalf("recently deleted: %v", list)
	}
	if by := list[0].(map[string]any)["deleted_by"].(map[string]any); by["actor_type"] != "human" || by["user_id"] == nil {
		t.Fatalf("deleted by: %v", by)
	}
	reader.must(200, "GET", brand+"/recently-deleted", nil)
	other.must(404, "GET", brand+"/recently-deleted", nil)
	if l := owner.must(200, "GET", "/v1/brands/"+rp+"/recently-deleted", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("other brand: %v", l)
	}
	other.must(404, "POST", brand+"/recently-deleted/"+recordDel+"/restore", nil)
	owner.must(404, "POST", "/v1/brands/"+rp+"/recently-deleted/"+recordDel+"/restore", nil)
	reader.must(403, "POST", brand+"/recently-deleted/"+recordDel+"/restore", nil)

	// Restoring brings everything back as it was, and fires nothing.
	before := firing()
	back := owner.must(200, "POST", brand+"/recently-deleted/"+recordDel+"/restore", nil)
	if back["outcome"] != "restored" || back["restored_at"] == nil || len(back["not_restored"].([]any)) != 0 {
		t.Fatalf("restore: %v", back)
	}
	if n := firing(); n != before {
		t.Fatalf("restoring fired %d rule, journey or webhook jobs", n-before)
	}
	again := owner.must(200, "GET", base+"/records/"+ana, nil)
	if again["archived"] != false || again["attributes"].(map[string]any)["job_title"] != "Head of Growth" || len(again["identities"].([]any)) != 2 {
		t.Fatalf("restored record: %v", again)
	}
	if l := owner.must(200, "GET", base+"/records/"+ana+"/relationships", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("links: %v", l)
	}
	if n := count(`SELECT count(*) FROM customer_events WHERE record_id = $1`, ana); n != 3 {
		t.Fatalf("events back: %d", n)
	}
	if n := count(`SELECT count(*) FROM attribution_conversions WHERE event_id = $1`, paid1); n != 1 {
		t.Fatalf("attribution back: %d", n)
	}
	if res := refresh(); res.Members != 1 || res.Joined != 0 {
		t.Fatalf("list after restore: %+v (restoring is not joining)", res)
	}
	owner.must(409, "POST", brand+"/recently-deleted/"+recordDel+"/restore", nil)
	if l := owner.must(200, "GET", brand+"/recently-deleted", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("restored still listed: %v", l)
	}

	// ---- A field value, an identity, a link, an event and a payment ----
	fv := owner.must(200, "POST", brand+"/records/"+ana+"/fields/job_title/delete", nil)
	if fv["kind"] != "field_value" || fv["label"] != "job_title" {
		t.Fatalf("field value: %v", fv)
	}
	owner.must(404, "POST", brand+"/records/"+ana+"/fields/job_title/delete", nil)
	if _, ok := owner.must(200, "GET", base+"/records/"+ana, nil)["attributes"].(map[string]any)["job_title"]; ok {
		t.Fatal("field value still there")
	}
	owner.must(400, "POST", brand+"/records/"+ana+"/fields/lifetime_revenue/delete", nil)

	idents := owner.must(200, "GET", base+"/records/"+ana, nil)["identities"].([]any)
	var email string
	for _, i := range idents {
		if i.(map[string]any)["namespace"] == "email" {
			email = i.(map[string]any)["id"].(string)
		}
	}
	idDel := owner.must(200, "POST", brand+"/records/"+ana+"/identities/"+email+"/delete", nil)
	if idDel["label"] != "ana@example.com" {
		t.Fatalf("identity: %v", idDel)
	}
	link := owner.must(200, "GET", base+"/records/"+ana+"/relationships", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	linkDel := owner.must(200, "POST", brand+"/records/"+company+"/relationships/"+link+"/delete", nil)
	if l := owner.must(200, "GET", base+"/records/"+ana+"/relationships", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("link still there: %v", l)
	}
	evDel := owner.must(200, "POST", brand+"/customer-events/"+activated+"/delete", nil)
	if evDel["label"] != "product_activated" {
		t.Fatalf("event: %v", evDel)
	}
	owner.must(404, "POST", "/v1/brands/"+rp+"/customer-events/"+paid1+"/delete", nil)
	payDel := owner.must(200, "POST", brand+"/customer-events/"+paid1+"/delete", nil)
	lifetime := func() any {
		return owner.must(200, "GET", base+"/records/"+ana, nil)["attributes"].(map[string]any)["lifetime_revenue"]
	}
	if v, ok := lifetime().(map[string]any); !ok || v["amount_minor"] != 3000.0 {
		t.Fatalf("revenue after a payment was deleted: %v", lifetime())
	}
	timeline := owner.must(200, "GET", base+"/records/"+ana+"/timeline", nil)["data"].([]any)
	for _, it := range timeline {
		if id, _ := it.(map[string]any)["id"].(string); id == activated || id == paid1 {
			t.Fatalf("deleted event on the timeline: %v", it)
		}
	}
	if l := owner.must(200, "GET", brand+"/recently-deleted", nil)["data"].([]any); len(l) != 5 || l[0].(map[string]any)["id"] != payDel["id"] {
		t.Fatalf("recently deleted, newest first: %v", l)
	}
	if l := owner.must(200, "GET", brand+"/recently-deleted?kind=event", nil)["data"].([]any); len(l) != 2 {
		t.Fatalf("events deleted: %v", l)
	}

	// Another record takes her email meanwhile: that identity can't come
	// back, and the rest of what's restored does.
	bo := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Bo", "brand_id": acme,
		"identities": []map[string]any{{"namespace": "email", "value": "ana@example.com", "verified": true}}})["id"].(string)
	before = firing()
	part := owner.must(200, "POST", brand+"/recently-deleted/"+idDel["id"].(string)+"/restore", nil)
	if part["outcome"] != "not_restored" || part["restored_at"] != nil {
		t.Fatalf("taken: %v", part)
	}
	if nr := part["not_restored"].([]any); len(nr) != 1 || nr[0].(map[string]any)["reason"] != "taken" {
		t.Fatalf("not restored: %v", nr)
	}
	owner.must(200, "DELETE", base+"/records/"+bo, nil)
	if r := owner.must(200, "POST", brand+"/recently-deleted/"+idDel["id"].(string)+"/restore", nil); r["outcome"] != "restored" {
		t.Fatalf("retry: %v", r)
	}
	for _, d := range []map[string]any{fv, linkDel, evDel, payDel} {
		if r := owner.must(200, "POST", brand+"/recently-deleted/"+d["id"].(string)+"/restore", nil); r["outcome"] != "restored" {
			t.Fatalf("restore %s: %v", d["kind"], r)
		}
	}
	if n := firing(); n != before {
		t.Fatalf("restoring fired %d rule, journey or webhook jobs", n-before)
	}
	rec = owner.must(200, "GET", base+"/records/"+ana, nil)
	if rec["attributes"].(map[string]any)["job_title"] != "Head of Growth" || rec["attributes"].(map[string]any)["lifetime_revenue"].(map[string]any)["amount_minor"] != 5000.0 {
		t.Fatalf("restored fields: %v", rec["attributes"])
	}
	if n := count(`SELECT count(*) FROM customer_events WHERE record_id = $1`, ana); n != 3 {
		t.Fatalf("events: %d", n)
	}
	// A field set again since keeps its new value.
	fv = owner.must(200, "POST", brand+"/records/"+ana+"/fields/first_name/delete", nil)
	owner.must(200, "PATCH", base+"/records/"+ana, map[string]any{"attributes": map[string]any{"first_name": "Anna"}})
	if r := owner.must(200, "POST", brand+"/recently-deleted/"+fv["id"].(string)+"/restore", nil); r["outcome"] != "not_restored" ||
		r["not_restored"].([]any)[0].(map[string]any)["reason"] != "changed" {
		t.Fatalf("changed since: %v", r)
	}

	// ---- Purge after 7 days ----
	del = owner.must(200, "POST", brand+"/records/"+ana+"/delete", nil)
	if _, err := e.pool.Exec(ctx, `UPDATE deletions SET deleted_at = deleted_at - interval '8 days', purge_after = now() - interval '1 day' WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	owner.must(410, "POST", brand+"/recently-deleted/"+del["id"].(string)+"/restore", nil)
	if l := owner.must(200, "GET", brand+"/recently-deleted", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("expired still listed: %v", l)
	}
	n, err := e.srv.PurgeDeletions(ctx)
	if err != nil || n == 0 {
		t.Fatalf("purged %d: %v", n, err)
	}
	for _, sql := range []string{
		`SELECT count(*) FROM records WHERE id = $1`, `SELECT count(*) FROM deleted_rows WHERE record_id = $1`,
		`SELECT count(*) FROM customer_events WHERE record_id = $1`, `SELECT count(*) FROM deletions WHERE record_id = $1`,
	} {
		if n := count(sql, ana); n != 0 {
			t.Fatalf("%s after purge: %d", sql, n)
		}
	}
	if n := count(`SELECT count(*) FROM deletions WHERE workspace_id = $1`, ws); n != 0 {
		t.Fatalf("deletions left: %d", n)
	}
	owner.must(404, "GET", brand+"/recently-deleted/"+del["id"].(string), nil)

	// ---- Erasure stays permanent, also from Recently deleted ----
	cy := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Cy", "brand_id": acme,
		"identities": []map[string]any{{"namespace": "email", "value": "cy@example.com"}}})["id"].(string)
	event(cy, "product_activated", "m-cy", nil)
	cyDel := owner.must(200, "POST", brand+"/records/"+cy+"/delete", nil)
	erased := owner.must(200, "DELETE", base+"/records/"+cy+"?suppress=true", nil)
	if erased["suppressed"] != 1.0 {
		t.Fatalf("erased: %v", erased)
	}
	owner.must(404, "POST", brand+"/recently-deleted/"+cyDel["id"].(string)+"/restore", nil)
	if n := count(`SELECT count(*) FROM deleted_rows WHERE workspace_id = $1`, ws); n != 0 {
		t.Fatalf("erasure left %d deleted rows", n)
	}

	names := []string{}
	for _, ev := range got() {
		names = append(names, ev.Name)
	}
	for _, want := range []string{"record.soft_deleted", "field_value.deleted", "identity.deleted", "relationship.deleted", "customer_event.deleted", "deletion.restored", "deletion.purged"} {
		if !slices.Contains(names, want) {
			t.Errorf("no %s event", want)
		}
	}
}
