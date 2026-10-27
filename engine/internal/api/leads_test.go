package api_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestLeadImportMergeAndLists(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	csv := "Email,First Name,Company Name,Industry\n" +
		"ana@acme-io.example,Ana,Acme,SaaS\n" +
		"bo@beta-io.example,Bo,Beta,\n" +
		"ANA@acme-io.example,,,Fintech\n" +
		"broken,X,Y,Z\n"
	code, res := owner.raw("POST", base+"/leads/import?list=Q4%20founders", "text/csv", csv)
	if code != 200 || res["created"] != 2.0 || res["updated"] != 0.0 || res["duplicates"] != 1.0 || len(res["errors"].([]any)) != 1 {
		t.Fatalf("import: %d %v", code, res)
	}
	listID := res["list_id"].(string)

	// Re-import: non-empty fields overwrite, empty ones keep, custom merges.
	csv2 := "email,first_name,title,seniority\nana@acme-io.example,Anna,CEO,C-level\ncy@gamma-io.example,Cy,,\n"
	code, res = owner.raw("POST", base+"/leads/import", "text/csv", csv2)
	if code != 200 || res["created"] != 1.0 || res["updated"] != 1.0 {
		t.Fatalf("reimport: %d %v", code, res)
	}

	out := owner.must(200, "GET", base+"/leads?q=acme", nil)
	data := out["data"].([]any)
	if len(data) != 1 || out["total"] != 1.0 {
		t.Fatalf("search: %v", out)
	}
	ana := data[0].(map[string]any)
	custom := ana["custom"].(map[string]any)
	if ana["first_name"] != "Anna" || ana["company"] != "Acme" || ana["title"] != "CEO" || custom["industry"] != "Fintech" || custom["seniority"] != "C-level" {
		t.Fatalf("merged lead: %v", ana)
	}
	if ids := ana["list_ids"].([]any); len(ids) != 1 || ids[0] != listID {
		t.Fatalf("list membership: %v", ana)
	}

	// List filter and pagination.
	if out = owner.must(200, "GET", base+"/leads?list_id="+listID, nil); out["total"] != 2.0 {
		t.Fatalf("list filter: %v", out)
	}
	p1 := owner.must(200, "GET", base+"/leads?limit=2", nil)
	if len(p1["data"].([]any)) != 2 || p1["next_after"] == nil {
		t.Fatalf("page 1: %v", p1)
	}
	p2 := owner.must(200, "GET", base+"/leads?limit=2&after="+p1["next_after"].(string), nil)
	if d := p2["data"].([]any); len(d) != 1 || d[0].(map[string]any)["email"] != "cy@gamma-io.example" || p2["next_after"] != nil {
		t.Fatalf("page 2: %v", p2)
	}

	// Lists: counts, add and remove members.
	lists := owner.must(200, "GET", base+"/lead-lists", nil)["data"].([]any)
	if len(lists) != 1 || lists[0].(map[string]any)["leads"] != 2.0 {
		t.Fatalf("lists: %v", lists)
	}
	cy := owner.must(200, "GET", base+"/leads?q=gamma", nil)["data"].([]any)[0].(map[string]any)
	if r := owner.must(200, "POST", base+"/lead-lists/"+listID+"/leads", map[string]any{"lead_ids": []string{cy["id"].(string)}}); r["added"] != 1.0 {
		t.Fatalf("add: %v", r)
	}
	owner.must(204, "DELETE", base+"/lead-lists/"+listID+"/leads/"+cy["id"].(string), nil)
	owner.must(404, "DELETE", base+"/lead-lists/"+listID+"/leads/"+cy["id"].(string), nil)
	owner.must(409, "POST", base+"/lead-lists", map[string]any{"name": "q4 FOUNDERS"})

	// Single upsert and patch.
	created := owner.must(201, "POST", base+"/leads", map[string]any{"email": "dee@delta-io.example", "first_name": "Dee", "custom": map[string]string{"Score": "9"}, "list_id": listID})
	if created["custom"].(map[string]any)["score"] != "9" || len(created["list_ids"].([]any)) != 1 {
		t.Fatalf("created: %v", created)
	}
	owner.must(200, "POST", base+"/leads", map[string]any{"email": "DEE@delta-io.example", "company": "Delta"})
	path := base + "/leads/" + created["id"].(string)
	got := owner.must(200, "PATCH", path, map[string]any{"title": "VP", "custom": map[string]any{"score": nil, "tier": "A"}})
	if got["company"] != "Delta" || got["title"] != "VP" || got["first_name"] != "Dee" || got["custom"].(map[string]any)["tier"] != "A" || got["custom"].(map[string]any)["score"] != nil {
		t.Fatalf("patched: %v", got)
	}
	owner.must(400, "POST", base+"/leads", map[string]any{"email": "nope"})

	// Deleting a list keeps its leads.
	owner.must(204, "DELETE", base+"/lead-lists/"+listID, nil)
	if out = owner.must(200, "GET", base+"/leads", nil); out["total"] != 4.0 {
		t.Fatalf("leads after list delete: %v", out)
	}
}

func TestSuppressionAndPolicyCheck(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	owner.raw("POST", base+"/leads/import", "text/csv", "email\nana@acme-io.example\nbo@rival-com.example\ncy@ok-io.example\n")

	res := owner.must(200, "POST", base+"/suppressions", map[string]any{
		"values": []string{"ana@ACME-io.example", "@rival-com.example", "ana@acme-io.example", "bad value"}, "reason": "unsubscribed",
	})
	if res["added"] != 2.0 || len(res["invalid"].([]any)) != 1 {
		t.Fatalf("add: %v", res)
	}
	if res = owner.must(200, "POST", base+"/suppressions", map[string]any{"values": []string{"rival-com.example"}}); res["added"] != 0.0 || res["existing"] != 1.0 {
		t.Fatalf("re-add: %v", res)
	}
	owner.must(400, "POST", base+"/suppressions", map[string]any{"values": []string{"x.com"}, "reason": "because"})

	check := owner.must(200, "POST", base+"/recipients/check", map[string]any{"emails": []string{"Ana@acme-io.example", "dan@rival-com.example", "cy@ok-io.example", "junk"}})["data"].([]any)
	want := []struct {
		allowed bool
		reason  string
	}{{false, "suppressed_email:unsubscribed"}, {false, "suppressed_domain:unsubscribed"}, {true, ""}, {false, "invalid_email"}}
	for i, w := range want {
		d := check[i].(map[string]any)
		reasons := fmt.Sprint(d["reasons"])
		if d["allowed"] != w.allowed || (w.reason != "" && !strings.Contains(reasons, w.reason)) {
			t.Errorf("%d: %v", i, d)
		}
	}

	if out := owner.must(200, "GET", base+"/leads?suppressed=true", nil); out["total"] != 2.0 {
		t.Fatalf("suppressed filter: %v", out)
	}
	code, imp := owner.raw("POST", base+"/leads/import", "text/csv", "email\nnew@rival-com.example\n")
	if code != 200 || imp["suppressed"] != 1.0 {
		t.Fatalf("import suppressed count: %v", imp)
	}

	// Deleting with suppress=true keeps the person from being re-imported and emailed.
	cy := owner.must(200, "GET", base+"/leads?q=cy@", nil)["data"].([]any)[0].(map[string]any)
	owner.must(204, "DELETE", base+"/leads/"+cy["id"].(string)+"?suppress=true", nil)
	d := owner.must(200, "POST", base+"/recipients/check", map[string]any{"emails": []string{"cy@ok-io.example"}})["data"].([]any)[0].(map[string]any)
	if d["allowed"] != false {
		t.Fatalf("forgotten lead still allowed: %v", d)
	}

	// Operators can add suppressions but not remove them.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "agent", "scopes": []string{"leads.read", "leads.manage"}})
	agent := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	agent.must(200, "POST", base+"/suppressions", map[string]any{"values": []string{"z@z-io.example"}})
	sup := owner.must(200, "GET", base+"/suppressions?q=z-io.example", nil)["data"].([]any)[0].(map[string]any)
	agent.must(403, "DELETE", base+"/suppressions/"+sup["id"].(string), nil)
	owner.must(204, "DELETE", base+"/suppressions/"+sup["id"].(string), nil)

	list := owner.must(200, "GET", base+"/suppressions", nil)
	if list["total"] != 3.0 {
		t.Fatalf("suppressions: %v", list)
	}

	// Another workspace sees none of it.
	other := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Other"})
	ob := "/v1/workspaces/" + other["id"].(string)
	if out := owner.must(200, "GET", ob+"/leads", nil); out["total"] != 0.0 {
		t.Fatalf("leak: %v", out)
	}
	d = owner.must(200, "POST", ob+"/recipients/check", map[string]any{"emails": []string{"ana@acme-io.example"}})["data"].([]any)[0].(map[string]any)
	if d["allowed"] != true {
		t.Fatalf("suppression leaked across workspaces: %v", d)
	}
	owner.must(404, "GET", ob+"/leads/"+cy["id"].(string), nil)

	events := owner.must(200, "GET", base+"/audit-events", nil)["data"].([]any)
	seen := map[string]bool{}
	for _, ev := range events {
		seen[ev.(map[string]any)["action"].(string)] = true
	}
	for _, a := range []string{"leads.imported", "suppressions.added", "suppression.removed", "lead.deleted"} {
		if !seen[a] {
			t.Errorf("no %s audit event", a)
		}
	}
}

func TestLeadImportScale(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	var b strings.Builder
	b.WriteString("email,first_name,company,city\n")
	const n = 20_000
	for i := range n {
		fmt.Fprintf(&b, "lead%d@company%d.com,Name%d,Company %d,Sydney\n", i, i%5000, i, i%5000)
	}
	start := time.Now()
	code, res := owner.raw("POST", "/v1/workspaces/"+ws+"/leads/import?list=Big", "text/csv", b.String())
	took := time.Since(start)
	if code != 200 || res["created"] != float64(n) {
		t.Fatalf("import: %d %v", code, res)
	}
	t.Logf("imported %d leads in %s", n, took)
	start = time.Now()
	code, res = owner.raw("POST", "/v1/workspaces/"+ws+"/leads/import", "text/csv", b.String())
	if code != 200 || res["updated"] != float64(n) {
		t.Fatalf("reimport: %d %v", code, res)
	}
	t.Logf("re-imported %d leads in %s", n, time.Since(start))
}
