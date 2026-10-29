package api_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// runToltImport imports from the fake into a new program and returns the
// finished import.
func runToltImport(t *testing.T, tolt *fakeTolt) (e *env, owner *client, base string, v map[string]any) {
	t.Helper()
	e = freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base = "/v1/workspaces/" + ws
	srv := httptest.NewServer(tolt)
	t.Cleanup(srv.Close)
	e.integrations.Client.BaseURLs["tolt"] = srv.URL
	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate"})
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "tolt", "credentials": map[string]string{"api_key": "tlt_test_not_a_real_key"}})
	imports := base + "/partner-programs/affiliates/imports"
	imp := owner.must(201, "POST", imports, map[string]any{})
	e.runCRMImports(t)
	for range 1500 {
		v = owner.must(200, "GET", imports+"/"+imp["id"].(string), nil)
		if v["status"] == "done" || v["status"] == "failed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if v["status"] != "done" {
		t.Fatalf("import = %v", v)
	}
	return e, owner, base, v
}

func manyPartners(n int) (partners, links []map[string]any) {
	for i := range n {
		id := fmt.Sprintf("p%04d", i)
		partners = append(partners, map[string]any{"id": id, "first_name": "Partner", "email": id + "@acme.example", "status": "active"})
		links = append(links, map[string]any{"id": "l" + id, "partner_id": id, "param": "ref", "value": "CODE" + id})
	}
	return
}

func importIssues(v map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, i := range v["summary"].(map[string]any)["issues"].([]any) {
		m := i.(map[string]any)
		out[m["reason"].(string)] = m
	}
	return out
}

// TestPartnerImportListCapped: Tolt's partner list stops at 500 of 934,
// saying there are no more. The import asks once more after the last
// partner's id, gets the rest, and the summary reports what Tolt counted.
// A promo code whose partner is under a field the import doesn't read is
// listed with its field names.
func TestPartnerImportListCapped(t *testing.T) {
	tolt := &fakeTolt{data: map[string][]map[string]any{}, fullPages: true, capAt: 500}
	tolt.set("/programs", map[string]any{"id": "prg_1", "name": "Northpeak partners", "currency": "usd", "status": "active"})
	partners, links := manyPartners(934)
	tolt.set("/partners", partners...)
	tolt.set("/links", links...)
	tolt.set("/promotion-codes", map[string]any{"id": "pc_1", "code": "SAVE10", "affiliate": map[string]any{"ref": "p0001"}})
	tolt.set("/customers")
	tolt.set("/transactions")
	tolt.set("/commissions")
	e, _, _, v := runToltImport(t, tolt)

	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM partner_memberships`).Scan(&n); err != nil || n != 934 {
		t.Fatalf("partners = %d, %v", n, err)
	}
	for _, k := range v["summary"].(map[string]any)["kinds"].([]any) {
		if m := k.(map[string]any); m["kind"] == "partners" && (m["reported"] != 934.0 || m["created"] != 934.0) {
			t.Fatalf("partners = %v", m)
		}
	}
	issues := importIssues(v)
	if issues["partners_unlisted"] != nil {
		t.Fatalf("unlisted = %v", issues["partners_unlisted"])
	}
	pc := issues["partner_id_missing"]
	if msg, _ := pc["message"].(string); pc["external_id"] != "pc_1" || !strings.Contains(msg, "affiliate") || strings.Contains(msg, "p0001") {
		t.Fatalf("promo code issue = %v", pc)
	}
}

// TestPartnerImportReconcile: Tolt counts partners its list never gives.
// The summary says how many, and that none of them has referrals, since
// every customer names a partner who came over.
func TestPartnerImportReconcile(t *testing.T) {
	for _, whole := range []bool{false, true} {
		t.Run(fmt.Sprintf("whole=%v", whole), func(t *testing.T) { reconcile(t, whole) })
	}
}

// reconcile runs it with total_count per status, or (whole) the whole
// list's count on every status's page.
func reconcile(t *testing.T, whole bool) {
	tolt := &fakeTolt{data: map[string][]map[string]any{}, fullPages: true, hidden: 3, wholeTotal: whole}
	tolt.set("/programs", map[string]any{"id": "prg_1", "name": "Northpeak partners", "currency": "usd", "status": "active"})
	partners, links := manyPartners(5)
	tolt.set("/partners", partners...)
	tolt.set("/links", links...)
	tolt.set("/promotion-codes")
	tolt.set("/customers", map[string]any{"id": "cus_1", "partner_id": "p0001", "customer_id": "u-1"})
	tolt.set("/transactions")
	tolt.set("/commissions")
	_, _, _, v := runToltImport(t, tolt)
	u := importIssues(v)["partners_unlisted"]
	msg, _ := u["message"].(string)
	if !strings.Contains(msg, "reports 8 partners and its partner list gave 5, so it left out 3") || !strings.Contains(msg, "none of the others has referrals") {
		t.Fatalf("unlisted = %v", u)
	}
}
