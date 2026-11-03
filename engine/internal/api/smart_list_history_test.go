package api_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/smartlists"
)

// TestSmartListHistoryAndCompanies covers audience transitions: every join
// and leave is kept and shown, a list can hold companies, lists can pick
// by lifecycle stage per product, and excluding a list of companies
// leaves out the people who work there.
func TestSmartListHistoryAndCompanies(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
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
	changes := func(id, query string) []map[string]any {
		t.Helper()
		var out []map[string]any
		for _, c := range owner.must(200, "GET", base+"/smart-lists/"+id+"/changes"+query, nil)["data"].([]any) {
			out = append(out, c.(map[string]any))
		}
		return out
	}

	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme"})["id"].(string)
	north := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Northpeak"})["id"].(string)
	exec(`UPDATE records SET attributes = '{"segment": "software"}' WHERE id = $1`, acme)
	exec(`UPDATE records SET attributes = '{"segment": "retail"}' WHERE id = $1`, north)
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana",
		"identities": []any{map[string]any{"namespace": "email", "value": "ana@acme.example"}}})["id"].(string)
	bo := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Bo",
		"identities": []any{map[string]any{"namespace": "email", "value": "bo@northpeak.example"}}})["id"].(string)
	exec(`UPDATE records SET attributes = '{"plan": "pro"}' WHERE id = $1`, ana)
	exec(`UPDATE records SET attributes = '{"plan": "free"}' WHERE id = $1`, bo)
	exec(`INSERT INTO record_relationships (workspace_id, from_id, to_id, kind) VALUES ($1, $2, $3, 'companies'), ($1, $4, $5, 'companies')`, ws, ana, acme, bo, north)

	// A people list: history of joins and leaves, on the timeline too.
	pro := owner.must(201, "POST", base+"/smart-lists", map[string]any{"name": "Pro", "rules": []map[string]any{{"attribute": "plan", "op": "eq", "value": "pro"}}})
	if pro["record_type"] != "person" {
		t.Fatalf("default record_type: %v", pro)
	}
	proID := pro["id"].(string)
	refresh(proID)
	exec(`UPDATE records SET attributes = '{"plan": "free"}' WHERE id = $1`, ana)
	exec(`UPDATE records SET attributes = '{"plan": "pro"}' WHERE id = $1`, bo)
	if res := refresh(proID); res.Joined != 1 || res.Left != 1 {
		t.Fatalf("second refresh: %+v", res)
	}
	all := changes(proID, "")
	if len(all) != 3 {
		t.Fatalf("history: %v", all)
	}
	if left := changes(proID, "?change=leave"); len(left) != 1 || left[0]["record_id"] != ana {
		t.Fatalf("leaves: %v", left)
	}
	if page := owner.must(200, "GET", base+"/smart-lists/"+proID+"/changes?limit=2", nil); page["next_cursor"] == nil {
		t.Fatalf("paging: %v", page)
	}
	owner.must(400, "GET", base+"/smart-lists/"+proID+"/changes?change=moved", nil)
	tl := owner.must(200, "GET", base+"/records/"+ana+"/timeline?kinds=list", nil)["data"].([]any)
	if len(tl) != 2 || tl[0].(map[string]any)["title"] != "Pro" {
		t.Fatalf("timeline: %v", tl)
	}

	// A static list writes history too.
	static := owner.must(201, "POST", base+"/smart-lists", map[string]any{"name": "VIP", "kind": "static"})["id"].(string)
	owner.must(200, "POST", base+"/smart-lists/"+static+"/members", map[string]any{"record_ids": []string{ana}})
	owner.must(200, "POST", base+"/smart-lists/"+static+"/members/remove", map[string]any{"record_ids": []string{ana}})
	if h := changes(static, ""); len(h) != 2 || h[0]["change"] != "leave" || h[1]["change"] != "enter" {
		t.Fatalf("static history: %v", h)
	}

	// A list of companies: company fields, and events of their people.
	owner.must(400, "POST", base+"/smart-lists", map[string]any{"name": "Bad", "kind": "static", "record_type": "account"})
	owner.must(400, "POST", base+"/smart-lists", map[string]any{"name": "Bad", "record_type": "account",
		"rules": []map[string]any{{"company_attribute": "segment", "op": "eq", "value": "software"}}})
	owner.must(400, "POST", base+"/smart-lists", map[string]any{"name": "Bad", "record_type": "team"})
	if _, err := customers.Record(ctx, e.pool, ws, customers.Event{RecordID: bo, Name: "proxy.request", MessageID: "h1"}); err != nil {
		t.Fatal(err)
	}
	p := owner.must(200, "POST", base+"/smart-lists/preview", map[string]any{"record_type": "account",
		"rules": []map[string]any{{"event": "proxy.request", "op": "exists"}}})
	if p["members"] != 1.0 || p["sample"].([]any)[0].(map[string]any)["id"] != north {
		t.Fatalf("company preview by their people's events: %v", p)
	}
	soft := owner.must(201, "POST", base+"/smart-lists", map[string]any{"name": "Software companies", "record_type": "account",
		"rules": []map[string]any{{"attribute": "segment", "op": "eq", "value": "software"}}})
	softID := soft["id"].(string)
	if soft["record_type"] != "account" {
		t.Fatalf("company list: %v", soft)
	}
	owner.must(400, "PATCH", base+"/smart-lists/"+softID, map[string]any{"record_type": "person"})
	if res := refresh(softID); res.Members != 1 {
		t.Fatalf("company list members: %+v", res)
	}
	m := owner.must(200, "GET", base+"/smart-lists/"+softID+"/members", nil)["data"].([]any)
	if len(m) != 1 || m[0].(map[string]any)["id"] != acme {
		t.Fatalf("company members: %v", m)
	}

	// Excluding a list of companies leaves out the people who work there.
	why, err := customers.Excluded(ctx, e.pool, ws, ana, customers.Exclusion{Lists: []string{softID}})
	if err != nil || why == "" {
		t.Fatalf("Ana works at a listed company: %q %v", why, err)
	}
	if why, err := customers.Excluded(ctx, e.pool, ws, bo, customers.Exclusion{Lists: []string{softID}}); err != nil || why != "" {
		t.Fatalf("Bo doesn't: %q %v", why, err)
	}
	var excluded bool
	if err := e.pool.QueryRow(ctx, `SELECT `+customers.ExcludedSQL("$1::uuid", "$2::uuid[]", "$3::text[]"), ana, []string{softID}, []string{}).Scan(&excluded); err != nil || !excluded {
		t.Fatalf("ExcludedSQL for a company list: %v %v", excluded, err)
	}

	// Lists by lifecycle stage per product.
	prod := owner.must(201, "POST", base+"/products", map[string]any{"name": "Residential", "key": "resi"})["id"].(string)
	exec(`INSERT INTO lifecycle_states (workspace_id, brand_id, record_id, product_id, level, stage, entered_at, idle_since)
		VALUES ($1, $2, $3, $4, 'company', 'churned', now(), now()), ($1, $2, $5, $4, 'person', 'activated', now(), now())`, ws, brand, acme, prod, bo)
	owner.must(400, "POST", base+"/smart-lists/preview", map[string]any{"rules": []map[string]any{{"product_stage": "churned", "op": "eq"}}})
	owner.must(400, "POST", base+"/smart-lists/preview", map[string]any{"rules": []map[string]any{{"product_stage": "churned", "product": "resi", "op": "exists"}}})
	if p := owner.must(200, "POST", base+"/smart-lists/preview", map[string]any{"record_type": "account",
		"rules": []map[string]any{{"product_stage": "churned", "product": "resi", "op": "eq"}}}); p["members"] != 1.0 {
		t.Fatalf("churned companies: %v", p)
	}
	if p := owner.must(200, "POST", base+"/smart-lists/preview", map[string]any{
		"rules": []map[string]any{{"product_stage": "activated", "product": "RESI", "op": "eq"}}}); p["members"] != 1.0 || p["sample"].([]any)[0].(map[string]any)["id"] != bo {
		t.Fatalf("activated people: %v", p)
	}
	if p := owner.must(200, "POST", base+"/smart-lists/preview", map[string]any{
		"rules": []map[string]any{{"product_stage": "activated", "product": "resi", "op": "neq"}}}); p["members"] != 1.0 {
		t.Fatalf("people not activated: %v", p)
	}

	// An audience rule on a list of companies is about companies.
	owner.must(400, "POST", base+"/rules", map[string]any{"name": "Wrong type", "emit_event": "company.listed",
		"trigger": map[string]any{"kind": "audience", "list_id": softID, "on": "enter"}})
	owner.must(201, "POST", base+"/rules", map[string]any{"name": "Company listed", "emit_event": "company.listed", "record_type": "account",
		"trigger": map[string]any{"kind": "audience", "list_id": softID, "on": "enter"}})
}
