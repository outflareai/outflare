package api_test

import (
	"context"
	"testing"
)

// TestQueryEndpoints covers the owner's rules for published query
// endpoints: only owners and admins, signed in, publish them; only API
// tokens holding query.endpoints and limited to the brand call them; the
// query reads what its publisher could; parameters are bound, never
// written into the SQL; every call is logged; and a rate limit and a row
// cap hold.
func TestQueryEndpoints(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	wsBase := "/v1/workspaces/" + ws
	for _, n := range []string{"Ana", "Bo", "Cy"} {
		owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": n})
	}
	sq := owner.must(201, "POST", base+"/queries", map[string]any{"name": "People by name",
		"sql": "SELECT name FROM people WHERE $1::text IS NULL OR name = $1::text ORDER BY name"})["id"].(string)
	publish := map[string]any{"saved_query_id": sq, "slug": "people", "name": "People",
		"params": []map[string]any{{"name": "name", "type": "text"}}, "row_cap": 2, "rate_per_minute": 4}

	// Who may publish: owners and admins, signed in; not operators or tokens.
	op, _ := joinAs(t, owner, ws, "op@acme.example", map[string]any{"role": "operator"})
	if code, out := op.do("POST", base+"/query-endpoints", publish); code != 403 || out["error"].(map[string]any)["code"] != "admin_only" {
		t.Fatalf("operator published: %d %v", code, out)
	}
	script := agentToken(t, owner, wsBase, "Script", "customers.read", "customers.manage")
	if code, _ := script.do("POST", base+"/query-endpoints", publish); code != 403 {
		t.Fatalf("token published: %d", code)
	}
	for _, bad := range []map[string]any{
		{"saved_query_id": sq, "slug": "Bad Slug", "name": "x"},
		{"saved_query_id": sq, "slug": "nop", "name": "x"}, // uses $1 with no parameter declared
		{"saved_query_id": sq, "slug": "x", "name": "x", "params": []map[string]any{{"name": "name", "type": "money"}}},
		{"saved_query_id": sq, "slug": "x", "name": "x", "params": []map[string]any{{"name": "name", "type": "text"}}, "row_cap": 0},
	} {
		owner.must(400, "POST", base+"/query-endpoints", bad)
	}
	ep := owner.must(201, "POST", base+"/query-endpoints", publish)
	if ep["version"] != 1.0 || ep["status"] != "active" || len(ep["scopes"].([]any)) == 0 {
		t.Fatalf("published: %v", ep)
	}
	owner.must(409, "POST", base+"/query-endpoints", publish)
	admin, _ := joinAs(t, owner, ws, "admin@acme.example", map[string]any{"role": "admin"})
	admin.must(200, "PATCH", base+"/query-endpoints/people", map[string]any{"description": "For the board report"})

	// Who may call: a token with query.endpoints for this brand only.
	caller := func(scopes []string, forBrand bool) *client {
		body := map[string]any{"name": "Reporting tool", "scopes": scopes}
		if forBrand {
			body["brand"] = brand
		}
		out := owner.must(201, "POST", wsBase+"/api-tokens", body)
		return &client{t: t, h: owner.h, bearer: out["token"].(string)}
	}
	run := base + "/query-endpoints/people/run"
	if code, out := owner.do("POST", run, nil); code != 403 || out["error"].(map[string]any)["code"] != "token_required" {
		t.Fatalf("session called: %d %v", code, out)
	}
	if code, out := caller([]string{"query.endpoints"}, false).do("POST", run, nil); code != 403 || out["error"].(map[string]any)["code"] != "brand_token_required" {
		t.Fatalf("workspace token called: %d %v", code, out)
	}
	if code, _ := caller([]string{"customers.read"}, true).do("POST", run, nil); code != 403 {
		t.Fatalf("token without query.endpoints called: %d", code)
	}
	tok := caller([]string{"query.endpoints"}, true)

	// It reads what the publisher could, with the row cap and bound parameters.
	all := tok.must(200, "POST", run, nil)
	if all["row_count"] != 2.0 || all["truncated"] != true || all["version"] != 1.0 {
		t.Fatalf("run: %v", all)
	}
	one := tok.must(200, "POST", run, map[string]any{"params": map[string]any{"name": "Bo"}})
	if rows := one["rows"].([]any); len(rows) != 1 || rows[0].([]any)[0] != "Bo" {
		t.Fatalf("bound parameter: %v", one)
	}
	// A value is data, never SQL.
	if inj := tok.must(200, "POST", run, map[string]any{"params": map[string]any{"name": "x' OR '1'='1"}}); inj["row_count"] != 0.0 {
		t.Fatalf("parameter as SQL: %v", inj)
	}
	tok.must(400, "POST", base+"/query-endpoints/"+ep["id"].(string)+"/run", map[string]any{"params": map[string]any{"other": 1}})

	// The rate limit: four calls a minute per token, counting refused calls.
	if code, out := tok.do("POST", run, nil); code != 429 || out["error"].(map[string]any)["code"] != "rate_limited" {
		t.Fatalf("rate limit: %d %v", code, out)
	}

	// Every call is logged, newest first.
	calls := owner.must(200, "GET", base+"/query-endpoints/people/calls", nil)["data"].([]any)
	if len(calls) != 5 {
		t.Fatalf("calls: %v", calls)
	}
	first, last := calls[len(calls)-1].(map[string]any), calls[0].(map[string]any)
	if first["outcome"] != "success" || first["rows"] != 2.0 || first["token_id"] == nil || last["error_code"] != "rate_limited" {
		t.Fatalf("call log: %v", calls)
	}
	if got := owner.must(200, "GET", base+"/query-endpoints/people", nil); got["calls_24h"] != 5.0 || got["last_call_at"] == nil {
		t.Fatalf("endpoint: %v", got)
	}

	// Turned off, it answers nothing; a new version of the query changes
	// nothing until the endpoint moves to it.
	owner.must(200, "PATCH", base+"/queries/"+sq, map[string]any{"sql": "SELECT 1 AS one"})
	if got := owner.must(200, "GET", base+"/query-endpoints/people", nil); got["version"] != 1.0 {
		t.Fatalf("pinned version: %v", got)
	}
	owner.must(400, "PATCH", base+"/query-endpoints/people", map[string]any{"slug": "other"})
	owner.must(200, "PATCH", base+"/query-endpoints/people", map[string]any{"status": "disabled"})
	tok2 := caller([]string{"query.endpoints"}, true)
	tok2.must(404, "POST", run, nil)
	if l := owner.must(200, "GET", base+"/query-endpoints", nil)["data"].([]any); len(l) != 1 || l[0].(map[string]any)["status"] != "disabled" {
		t.Fatalf("list: %v", l)
	}
}
