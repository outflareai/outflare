package api_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/smartlists"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestSmartLists(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	person := func(addr, attrs string, brand *string) string {
		t.Helper()
		rec, err := customers.EnsurePerson(ctx, e.pool, ws, "email", addr, true, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.pool.Exec(ctx, `UPDATE records SET attributes = $2, brand_id = $3 WHERE id = $1`, rec, attrs, brand); err != nil {
			t.Fatal(err)
		}
		return rec
	}
	event := func(rec, name, ago string) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, record_id, kind, name, occurred_at, source)
			VALUES ($1, $2, 'track', $3, now() - $4::interval, 'api')`, ws, rec, name, ago); err != nil {
			t.Fatal(err)
		}
	}
	refresh := func(id string) smartlists.Result {
		t.Helper()
		var res smartlists.Result
		if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			var err error
			res, err = smartlists.Refresh(ctx, tx, ws, id)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return res
	}
	members := func(id string) map[string]bool {
		t.Helper()
		out := map[string]bool{}
		for _, m := range owner.must(200, "GET", base+"/smart-lists/"+id+"/members", nil)["data"].([]any) {
			out[m.(map[string]any)["email"].(string)] = true
		}
		return out
	}

	geo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode", "website": "https://geonode.com"})["id"].(string)
	rp := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "website": "https://repocket.com"})["id"].(string)
	ana := person("ana@example.org", `{"plan": "pro", "seats": 12, "tags": ["beta", "vip"]}`, nil)
	bo := person("bo@example.org", `{"plan": "free", "seats": 1}`, &geo)
	cy := person("cy@example.org", `{"plan": "Pro", "seats": "3"}`, &rp)
	event(ana, "payment_succeeded", "10 days")
	event(bo, "payment_succeeded", "200 days")
	event(cy, "signed_up", "1 day")

	// Bad rules are refused with a reason.
	for _, rules := range [][]map[string]any{
		{{"attribute": "plan", "event": "x", "op": "exists"}},
		{{"attribute": "Plan Name", "op": "eq", "value": "pro"}},
		{{"attribute": "plan", "op": "eq"}},
		{{"event": "payment_succeeded", "op": "eq", "value": 1}},
		{{"attribute": "plan", "op": "exists", "within_days": 3}},
		{{"in_list": "00000000-0000-0000-0000-000000000000", "op": "exists"}},
		{{"attribute": "plan", "op": "like", "value": "p"}},
	} {
		owner.must(400, "POST", base+"/smart-lists", map[string]any{"name": "Bad", "rules": rules})
	}
	owner.must(400, "POST", base+"/smart-lists", map[string]any{"rules": []any{}})
	owner.must(400, "POST", base+"/smart-lists", map[string]any{"name": "Bad", "match": "most"})
	owner.must(400, "POST", base+"/smart-lists", map[string]any{"name": "Bad", "brand_id": "nope"})

	// Preview counts without saving.
	paid := []map[string]any{{"attribute": "plan", "op": "eq", "value": "pro"}, {"event": "payment_succeeded", "op": "exists", "within_days": 90}}
	p := owner.must(200, "POST", base+"/smart-lists/preview", map[string]any{"rules": paid})
	if p["members"] != 1.0 || len(p["sample"].([]any)) != 1 || p["sample"].([]any)[0].(map[string]any)["email"] != "ana@example.org" {
		t.Fatalf("preview = %v", p)
	}
	if p := owner.must(200, "POST", base+"/smart-lists/preview", map[string]any{"rules": paid, "match": "any"}); p["members"] != 2.0 {
		t.Fatalf("preview any = %v", p)
	}
	if p := owner.must(200, "POST", base+"/smart-lists/preview", map[string]any{}); p["members"] != 3.0 {
		t.Fatalf("preview everyone = %v", p)
	}

	l := owner.must(201, "POST", base+"/smart-lists", map[string]any{"name": "Paying customers", "rules": paid})
	if l["match"] != "all" || l["members"] != 0.0 || l["refreshed_at"] != nil || len(l["rules"].([]any)) != 2 {
		t.Fatalf("created = %v", l)
	}
	id := l["id"].(string)
	owner.must(409, "POST", base+"/smart-lists", map[string]any{"name": "paying customers"})
	if res := refresh(id); res != (smartlists.Result{Joined: 1, Members: 1}) {
		t.Fatalf("first refresh = %+v", res)
	}
	if m := members(id); len(m) != 1 || !m["ana@example.org"] {
		t.Fatalf("members = %v", m)
	}
	if l := owner.must(200, "GET", base+"/smart-lists/"+id, nil); l["members"] != 1.0 || l["refreshed_at"] == nil {
		t.Fatalf("after refresh = %v", l)
	}

	// Numbers compare as numbers, text ignoring case, and lists by element.
	cases := []struct {
		rule map[string]any
		want []string
	}{
		{map[string]any{"attribute": "seats", "op": "gt", "value": 2}, []string{"ana@example.org", "cy@example.org"}},
		{map[string]any{"attribute": "seats", "op": "lt", "value": "3"}, []string{"bo@example.org"}},
		{map[string]any{"attribute": "plan", "op": "neq", "value": "PRO"}, []string{"bo@example.org"}},
		{map[string]any{"attribute": "tags", "op": "contains", "value": "VIP"}, []string{"ana@example.org"}},
		{map[string]any{"attribute": "plan", "op": "contains", "value": "re"}, []string{"bo@example.org"}},
		{map[string]any{"attribute": "tags", "op": "not_exists"}, []string{"bo@example.org", "cy@example.org"}},
		{map[string]any{"event": "payment_succeeded", "op": "not_exists"}, []string{"cy@example.org"}},
		{map[string]any{"event": "payment_succeeded", "op": "exists"}, []string{"ana@example.org", "bo@example.org"}},
	}
	for _, c := range cases {
		patched := owner.must(200, "PATCH", base+"/smart-lists/"+id, map[string]any{"rules": []any{c.rule}})
		if len(patched["rules"].([]any)) != 1 {
			t.Fatalf("patched = %v", patched)
		}
		refresh(id)
		m := members(id)
		if len(m) != len(c.want) {
			t.Fatalf("%v: members = %v, want %v", c.rule, m, c.want)
		}
		for _, w := range c.want {
			if !m[w] {
				t.Fatalf("%v: members = %v, want %v", c.rule, m, c.want)
			}
		}
	}

	// Leaving and joining: a refresh reports who changed.
	owner.must(200, "PATCH", base+"/smart-lists/"+id, map[string]any{"rules": []any{map[string]any{"attribute": "plan", "op": "eq", "value": "pro"}}})
	if res := refresh(id); res.Members != 2 {
		t.Fatalf("pro refresh = %+v", res)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE records SET attributes = '{"plan": "free"}' WHERE id = $1`, cy); err != nil {
		t.Fatal(err)
	}
	if res := refresh(id); res != (smartlists.Result{Left: 1, Members: 1}) {
		t.Fatalf("after downgrade = %+v", res)
	}

	// A brand's list holds that brand's people and the shared ones.
	everyone := owner.must(201, "POST", base+"/smart-lists", map[string]any{"name": "Geonode people", "brand_id": "geonode"})
	if everyone["brand_id"] != geo {
		t.Fatalf("brand list = %v", everyone)
	}
	refresh(everyone["id"].(string))
	if m := members(everyone["id"].(string)); len(m) != 2 || !m["ana@example.org"] || !m["bo@example.org"] {
		t.Fatalf("geonode members = %v", m)
	}
	if ls := owner.must(200, "GET", base+"/smart-lists?brand=geonode", nil)["data"].([]any); len(ls) != 1 {
		t.Fatalf("geonode lists = %v", ls)
	}

	// A lead list feeds a smart list.
	leads := owner.must(201, "POST", base+"/lead-lists", map[string]any{"name": "Webinar"})["id"].(string)
	lead := owner.must(201, "POST", base+"/leads", map[string]any{"email": "bo@example.org"})["id"].(string)
	owner.must(200, "POST", base+"/lead-lists/"+leads+"/leads", map[string]any{"lead_ids": []string{lead}})
	in := owner.must(201, "POST", base+"/smart-lists", map[string]any{"name": "Came to the webinar", "rules": []any{map[string]any{"in_list": leads, "op": "exists"}}})
	refresh(in["id"].(string))
	if m := members(in["id"].(string)); len(m) != 1 || !m["bo@example.org"] {
		t.Fatalf("in list members = %v", m)
	}

	// A brand-limited token sees its brand's lists and people only, and
	// can't change lists that hold every brand.
	scopes := []string{"customers.read", "customers.manage"}
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Repocket agent", "scopes": scopes, "brand": "repocket"})
	agent := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	agent.must(404, "GET", base+"/smart-lists/"+everyone["id"].(string), nil)
	if ls := agent.must(200, "GET", base+"/smart-lists", nil)["data"].([]any); len(ls) != 2 {
		t.Fatalf("agent lists = %v", ls)
	}
	agent.must(403, "PATCH", base+"/smart-lists/"+id, map[string]any{"name": "Mine"})
	agent.must(403, "POST", base+"/smart-lists", map[string]any{"name": "Everyone"})
	if p := agent.must(200, "POST", base+"/smart-lists/preview", map[string]any{}); p["members"] != 2.0 {
		t.Fatalf("agent preview = %v", p)
	}
	for _, m := range agent.must(200, "GET", base+"/smart-lists/"+id+"/members", nil)["data"].([]any) {
		if m.(map[string]any)["email"] == "bo@example.org" {
			t.Fatalf("agent sees another brand's person: %v", m)
		}
	}
	own := agent.must(201, "POST", base+"/smart-lists", map[string]any{"name": "Repocket people", "brand_id": rp})
	agent.must(202, "POST", base+"/smart-lists/"+own["id"].(string)+"/refresh", nil)

	// Paging.
	page := owner.must(200, "GET", base+"/smart-lists/"+everyone["id"].(string)+"/members?limit=1", nil)
	if len(page["data"].([]any)) != 1 || page["next_cursor"] == nil {
		t.Fatalf("page = %v", page)
	}
	next := owner.must(200, "GET", base+"/smart-lists/"+everyone["id"].(string)+"/members?limit=1&cursor="+page["next_cursor"].(string), nil)
	if len(next["data"].([]any)) != 1 || next["data"].([]any)[0].(map[string]any)["id"] == page["data"].([]any)[0].(map[string]any)["id"] {
		t.Fatalf("next page = %v", next)
	}
	owner.must(400, "GET", base+"/smart-lists/"+id+"/members?before=nope", nil)

	owner.must(204, "DELETE", base+"/smart-lists/"+id, nil)
	owner.must(404, "GET", base+"/smart-lists/"+id, nil)
	if _, err := smartlists.Refresh(ctx, mustTx(t, e), ws, id); err != smartlists.ErrGone {
		t.Fatalf("refresh of a deleted list: %v", err)
	}

	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
	}
	if names["smart_list.created"] != 4 || names["smart_list.updated"] < 9 || names["smart_list.deleted"] != 1 {
		t.Fatalf("events = %v", names)
	}
}

// mustTx opens a transaction that is rolled back when the test ends.
func mustTx(t *testing.T, e *env) pgx.Tx {
	t.Helper()
	tx, err := e.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}
