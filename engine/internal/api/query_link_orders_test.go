package api_test

import (
	"strings"
	"testing"
)

// Paid link orders in Query: the donor catalog and the orders, for
// callers with coverage.read. Notes and lost_reason are not columns.
func TestQueryLinkOrders(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "handle": "acme"})["id"].(string)
	root := "/v1/brands/" + brand + "/link-orders"
	otherRoot := "/v1/brands/" + other + "/link-orders"

	created := owner.must(201, "POST", root+"/catalog", map[string]any{
		"domain": "Donor.Example", "pbn": false, "niches": []string{"saas"}, "notes": "keep this off the view",
		"prices": []map[string]any{{"currency": "USD", "amount_minor": 15000}},
	})
	id := created["id"].(string)
	owner.must(201, "POST", root, map[string]any{
		"donor_profile_id": id, "target_url": "https://northpeak.example/blog",
	})
	owner.must(201, "POST", otherRoot+"/catalog", map[string]any{
		"domain": "other-brand.example", "niches": []string{"vpn"},
	})

	out := owner.must(200, "POST", base+"/query?brand=northpeak", map[string]any{
		"sql": "select domain, pbn, source, niches from coverage_donor_profiles order by domain",
	})
	if rows := queryRows(t, out); len(rows) != 1 || rows[0][0] != "donor.example" || rows[0][1] != false || rows[0][2] != "manual" {
		t.Fatalf("donor profiles: %v", out)
	}
	out = owner.must(200, "POST", base+"/query?brand=northpeak", map[string]any{
		"sql": "select status, target_url, donor_amount_minor, currency from coverage_link_orders",
	})
	if rows := queryRows(t, out); len(rows) != 1 || rows[0][0] != "requested" || rows[0][1] != "https://northpeak.example/blog" {
		t.Fatalf("link orders: %v", out)
	}

	// The other brand's catalog is not this brand's.
	out = owner.must(200, "POST", base+"/query?brand=northpeak", map[string]any{
		"sql": "select count(*) from coverage_donor_profiles",
	})
	if rows := queryRows(t, out); rows[0][0] != 1.0 {
		t.Fatalf("brand isolation: %v", out)
	}
	out = owner.must(200, "POST", base+"/query?brand=acme", map[string]any{
		"sql": "select domain from coverage_donor_profiles",
	})
	if rows := queryRows(t, out); len(rows) != 1 || rows[0][0] != "other-brand.example" {
		t.Fatalf("other brand: %v", out)
	}
	out = owner.must(200, "POST", "/v1/brands/"+other+"/query", map[string]any{
		"sql": "select count(*) from coverage_donor_profiles",
	})
	if rows := queryRows(t, out); rows[0][0] != 1.0 {
		t.Fatalf("brand route: %v", out)
	}

	// Notes and lost_reason stay off the views.
	for _, sql := range []string{
		"select notes from coverage_donor_profiles",
		"select notes from coverage_link_orders",
		"select lost_reason from coverage_link_orders",
	} {
		code, got := owner.do("POST", base+"/query?brand=northpeak", map[string]any{"sql": sql})
		if code != 400 || queryCode(got) != "query_invalid" || !strings.Contains(strings.ToLower(got["error"].(map[string]any)["message"].(string)), "does not exist") {
			t.Fatalf("%s: %d %v", sql, code, got)
		}
	}

	// Without coverage.read the tables aren't readable.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "CRM agent", "scopes": []string{"customers.read"}})
	agent := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	tables := agent.must(200, "GET", base+"/query/tables", nil)["data"].([]any)
	for _, tb := range tables {
		m := tb.(map[string]any)
		if (m["name"] == "coverage_donor_profiles" || m["name"] == "coverage_link_orders") && m["available"] != false {
			t.Fatalf("%s available without coverage.read: %v", m["name"], m)
		}
	}
	if st, _ := agent.do("POST", base+"/query?brand=northpeak", map[string]any{"sql": "select count(*) from coverage_link_orders"}); st == 200 {
		out := agent.must(200, "POST", base+"/query?brand=northpeak", map[string]any{"sql": "select count(*) from coverage_link_orders"})
		if rows := queryRows(t, out); rows[0][0] != 0.0 {
			t.Fatalf("link orders without coverage.read: %v", out)
		}
	}

	readerTok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Coverage reader", "scopes": []string{"coverage.read"}})
	reader := &client{t: t, h: e.h, bearer: readerTok["token"].(string)}
	listed := reader.must(200, "GET", base+"/query/tables", nil)["data"].([]any)
	var sawProfiles, sawOrders bool
	for _, tb := range listed {
		m := tb.(map[string]any)
		if m["name"] == "coverage_donor_profiles" && m["available"] == true {
			sawProfiles = true
			for _, col := range m["columns"].([]any) {
				if col.(map[string]any)["name"] == "notes" {
					t.Fatal("notes listed on coverage_donor_profiles")
				}
			}
		}
		if m["name"] == "coverage_link_orders" && m["available"] == true {
			sawOrders = true
			for _, col := range m["columns"].([]any) {
				name := col.(map[string]any)["name"].(string)
				if name == "notes" || name == "lost_reason" {
					t.Fatalf("%s listed on coverage_link_orders", name)
				}
			}
		}
	}
	if !sawProfiles || !sawOrders {
		t.Fatalf("tables with coverage.read: profiles=%v orders=%v in %v", sawProfiles, sawOrders, listed)
	}
	out = reader.must(200, "POST", base+"/query?brand=northpeak", map[string]any{
		"sql": "select count(*) from coverage_donor_profiles",
	})
	if rows := queryRows(t, out); rows[0][0] != 1.0 {
		t.Fatalf("reader query: %v", out)
	}
}
