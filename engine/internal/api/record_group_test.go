package api_test

import (
	"context"
	"testing"
)

// A company's group rolls its children up to the parent, never loops,
// stays inside its brand, and shows the share of wallet once the company
// has an estimate.
func TestRecordGroup(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	company := func(name string) string {
		t.Helper()
		return owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": name})["id"].(string)
	}
	parent := func(id string, to any) map[string]any {
		return map[string]any{"attributes": map[string]any{"parent_company": to}}
	}
	group, eu, us, lab := company("Acme Group"), company("Acme Europe"), company("Acme US"), company("Acme Labs")
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana",
		"identities": []map[string]any{{"namespace": "email", "value": "ana@acme.example"}}, "attributes": map[string]any{"companies": []string{eu}}})["id"].(string)

	// A parent can be given when the company is made.
	popup := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme Pop-up", "attributes": map[string]any{"parent_company": us}})["id"].(string)
	if got := owner.must(200, "GET", base+"/records/"+us+"/group", nil); len(got["companies"].([]any)) != 2 {
		t.Fatalf("made with a parent: %v", got)
	}
	owner.must(200, "PATCH", base+"/records/"+popup, parent(popup, nil))
	// Alone, a company is its own group.
	got := owner.must(200, "GET", base+"/records/"+eu+"/group", nil)
	if got["root_id"] != eu || got["parent"] != nil || len(got["companies"].([]any)) != 1 || got["wallet"] != nil {
		t.Fatalf("alone: %v", got)
	}
	// A person has no group.
	if got := owner.must(200, "GET", base+"/records/"+ana+"/group", nil); len(got["companies"].([]any)) != 0 {
		t.Fatalf("person: %v", got)
	}

	owner.must(200, "PATCH", base+"/records/"+eu, parent(eu, group))
	owner.must(200, "PATCH", base+"/records/"+us, parent(us, group))
	owner.must(200, "PATCH", base+"/records/"+lab, parent(lab, eu))
	// Never itself, never one of its own children.
	owner.must(400, "PATCH", base+"/records/"+eu, parent(eu, eu))
	owner.must(400, "PATCH", base+"/records/"+group, parent(group, lab))
	// The change is in the record's own history.
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM attribute_changes WHERE record_id = $1 AND field_key = 'parent_company'`, eu).Scan(&n); err != nil || n != 1 {
		t.Fatalf("history: %d %v", n, err)
	}

	if _, err := e.pool.Exec(ctx, `INSERT INTO transactions (workspace_id, brand_id, record_id, kind, amount_minor, brand_amount_minor, source, source_id, occurred_at) VALUES
		($1, $2, $3, 'payment', 30000, 30000, 'test', 'g1', now() - interval '20 days'),
		($1, $2, $4, 'payment', 10000, 10000, 'test', 'g2', now() - interval '90 days'),
		($1, $2, $5, 'payment', 5000, 5000, 'test', 'g3', now() - interval '3 days'),
		($1, $2, $3, 'payment', 99000, 99000, 'test', 'g4', now() - interval '400 days')`, ws, brand, eu, ana, us); err != nil {
		t.Fatal(err)
	}
	owner.must(201, "POST", base+"/signals", map[string]any{"record_id": lab, "type": "upsell", "title": "Asked about seats"})

	got = owner.must(200, "GET", base+"/records/"+lab+"/group", nil)
	if got["root_id"] != group || got["parent"].(map[string]any)["name"] != "Acme Europe" {
		t.Fatalf("from the bottom: %v", got)
	}
	rows := got["companies"].([]any)
	if len(rows) != 4 {
		t.Fatalf("companies: %v", rows)
	}
	names := []string{}
	for _, r := range rows {
		names = append(names, r.(map[string]any)["name"].(string))
	}
	// A parent comes before its children.
	if names[0] != "Acme Group" || names[1] != "Acme Europe" || names[2] != "Acme Labs" || names[3] != "Acme US" {
		t.Fatalf("order: %v", names)
	}
	europe := rows[1].(map[string]any)
	if europe["paid_12m_minor"] != float64(40000) || europe["people"] != float64(1) || europe["depth"] != float64(1) || europe["parent_id"] != group {
		t.Fatalf("europe: %v", europe)
	}
	totals := got["totals"].(map[string]any)
	if totals["companies"] != float64(4) || totals["paid_12m_minor"] != float64(45000) || totals["open_signals"] != float64(1) || totals["wallet_minor"] != nil {
		t.Fatalf("totals: %v", totals)
	}

	// The kit adds the wallet fields; an estimate gives the share.
	if plan := owner.must(200, "POST", base+"/kits/share-of-wallet/apply", map[string]any{}); plan["applied"] != true {
		t.Fatalf("kit: %v", plan)
	}
	var currency string
	if err := e.pool.QueryRow(ctx, `SELECT currency FROM brands WHERE id = $1`, brand).Scan(&currency); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "PATCH", base+"/records/"+eu, map[string]any{"attributes": map[string]any{
		"wallet_estimate": map[string]any{"amount_minor": 160000, "currency": currency}, "wallet_confidence": "told_us", "suppliers": "Globex"}})
	got = owner.must(200, "GET", base+"/records/"+eu+"/group", nil)
	wallet := got["wallet"].(map[string]any)
	if wallet["estimate_minor"] != float64(160000) || wallet["share"] != 0.25 || wallet["confidence"] != "told_us" || wallet["suppliers"] != "Globex" || wallet["source"] != nil {
		t.Fatalf("wallet: %v", wallet)
	}
	if totals := got["totals"].(map[string]any); totals["wallet_minor"] != float64(160000) || totals["share"] != 0.25 {
		t.Fatalf("group wallet: %v", totals)
	}

	// Taking the parent away splits the group.
	owner.must(200, "PATCH", base+"/records/"+eu, parent(eu, nil))
	if got := owner.must(200, "GET", base+"/records/"+group+"/group", nil); len(got["companies"].([]any)) != 2 {
		t.Fatalf("after split: %v", got["companies"])
	}

	// Another brand's company is not a parent, and its group is not found here.
	other := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Second"})["id"].(string)
	far := owner.must(201, "POST", "/v1/brands/"+other+"/records", map[string]any{"type": "account", "name": "Elsewhere"})["id"].(string)
	owner.must(404, "GET", base+"/records/"+far+"/group", nil)
	if code, _ := owner.do("PATCH", base+"/records/"+us, parent(us, far)); code < 400 {
		t.Fatalf("parent in another brand allowed: %d", code)
	}
}
