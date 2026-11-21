package api_test

import (
	"strings"
	"testing"
)

// The data catalog tells an agent what a brand's data holds, read live
// with the caller's own access.
func TestDataCatalog(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws
	rp := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Summitly", "website": "summitly.example"})["id"].(string)
	gn := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "website": "northpeak.example"})["id"].(string)
	owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ada", "brand_id": rp,
		"identities": []map[string]any{{"namespace": "email", "value": "ada@acme.example"}}})
	owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Bea", "brand_id": rp})
	owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Grace", "brand_id": gn})

	cat := owner.must(200, "GET", "/v1/brands/"+rp+"/data/catalog", nil)
	tables := map[string]map[string]any{}
	for _, x := range cat["data"].([]any) {
		m := x.(map[string]any)
		tables[m["name"].(string)] = m
	}
	people := tables["people"]
	if people == nil || people["area"] != "customers" || people["rows"] != float64(2) || people["available"] != true ||
		people["source"] == "" || people["time_column"] != "created_at" || people["first_at"] == nil || len(people["examples"].([]any)) == 0 {
		t.Fatalf("people: %v", people)
	}
	if tables["transactions"]["rows"] != float64(0) || tables["deals"] == nil {
		t.Fatalf("transactions or deals: %v %v", tables["transactions"], tables["deals"])
	}
	if !strings.Contains(cat["how_to_use"].(string), "run_query") {
		t.Fatalf("how_to_use: %v", cat["how_to_use"])
	}

	// One table: every column explained, with how often it is filled and
	// its common values.
	one := owner.must(200, "GET", "/v1/brands/"+rp+"/data/catalog/people", nil)
	cols := map[string]map[string]any{}
	for _, x := range one["columns"].([]any) {
		m := x.(map[string]any)
		if m["description"] == "" {
			t.Fatalf("column without a description: %v", m)
		}
		cols[m["name"].(string)] = m
	}
	if cols["email"]["personal"] != true || cols["email"]["filled"] != 0.5 {
		t.Fatalf("email: %v", cols["email"])
	}
	if cols["brand_id"]["joins_to"] != "brands.id" || cols["created_at"]["unit"] != "UTC timestamp" {
		t.Fatalf("brand_id or created_at: %v %v", cols["brand_id"], cols["created_at"])
	}
	if vals, _ := cols["origin"]["values"].([]any); len(vals) == 0 {
		t.Fatalf("origin has no common values: %v", cols["origin"])
	}
	if one["table"].(map[string]any)["rows"] != float64(2) {
		t.Fatalf("table: %v", one["table"])
	}
	tx := owner.must(200, "GET", "/v1/brands/"+rp+"/data/catalog/transactions", nil)
	for _, x := range tx["columns"].([]any) {
		if m := x.(map[string]any); m["name"] == "brand_amount_minor" && !strings.Contains(m["unit"].(string), "brand_currency") {
			t.Fatalf("brand_amount_minor: %v", m)
		}
	}
	if code, _ := owner.do("GET", "/v1/brands/"+rp+"/data/catalog/nope", nil); code != 404 {
		t.Fatalf("unknown table: %d", code)
	}

	// A token without a table's scope sees it described, without numbers.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Agent", "scopes": []string{"customers.read"}, "brand": "summitly"})
	agent := &client{t: t, h: h, bearer: tok["token"].(string)}
	deals := agent.must(200, "GET", "/v1/brands/"+rp+"/data/catalog/deals", nil)
	if deals["table"].(map[string]any)["available"] != false || deals["table"].(map[string]any)["rows"] != nil ||
		!strings.Contains(deals["note"].(string), "outcomes.read") {
		t.Fatalf("deals without outcomes.read: %v", deals)
	}
	ac := agent.must(200, "GET", "/v1/brands/"+rp+"/data/catalog", nil)
	for _, x := range ac["data"].([]any) {
		m := x.(map[string]any)
		if m["name"] == "people" && m["rows"] != float64(2) {
			t.Fatalf("agent people: %v", m)
		}
		if m["name"] == "deals" && m["rows"] != nil {
			t.Fatalf("agent deals counted without the scope: %v", m)
		}
	}
}

// numbers=false answers at once: the written half, and the numbers only
// when a kept copy already holds them.
func TestDataCatalogWithoutNumbers(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws
	np := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "website": "northpeak.example"})["id"].(string)
	owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ada", "brand_id": np})

	people := func(path string) map[string]any {
		for _, x := range owner.must(200, "GET", "/v1/brands/"+np+"/data/catalog"+path, nil)["data"].([]any) {
			if m := x.(map[string]any); m["name"] == "people" {
				return m
			}
		}
		t.Fatal("no people table")
		return nil
	}
	if p := people("?numbers=false"); p["rows"] != nil || p["description"] == "" || p["available"] != true {
		t.Fatalf("without numbers: %v", p)
	}
	if p := people(""); p["rows"] != float64(1) {
		t.Fatalf("with numbers: %v", p)
	}
	if p := people("?numbers=false"); p["rows"] != float64(1) {
		t.Fatalf("the kept numbers: %v", p)
	}

	one := owner.must(200, "GET", "/v1/brands/"+np+"/data/catalog/people?numbers=false", nil)
	if tb := one["table"].(map[string]any); tb["rows"] != nil || len(one["columns"].([]any)) == 0 {
		t.Fatalf("one table without numbers: %v", tb)
	}
	owner.must(200, "GET", "/v1/brands/"+np+"/data/catalog/people", nil)
	one = owner.must(200, "GET", "/v1/brands/"+np+"/data/catalog/people?numbers=false", nil)
	if tb := one["table"].(map[string]any); tb["rows"] != float64(1) {
		t.Fatalf("one table, the kept numbers: %v", tb)
	}
}
