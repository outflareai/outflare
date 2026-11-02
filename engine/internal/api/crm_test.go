package api_test

import (
	"context"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestCRMActivitiesViewsAndIsolation(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	person := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana"})
	id := person["id"].(string)
	brand := person["brand_id"].(string)
	crm := "/v1/brands/" + brand + "/crm"
	user := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	task := owner.must(201, "POST", crm+"/activities", map[string]any{"record_id": id, "kind": "task", "title": "Call Ana", "assigned_to": user, "due_at": "2026-11-05T09:00:00Z"})
	taskID := task["id"].(string)
	if task["version"] != float64(1) || task["created_by"] != user || task["brand_id"] != brand {
		t.Fatalf("task: %v", task)
	}
	owner.must(400, "POST", crm+"/activities", map[string]any{"record_id": id, "kind": "task", "title": "Invalid assignee", "assigned_to": "00000000-0000-0000-0000-000000000001"})
	owner.must(400, "POST", crm+"/activities", map[string]any{"record_id": id, "kind": "invalid", "title": "Invalid kind"})
	owner.must(409, "PATCH", crm+"/activities/"+taskID, map[string]any{"title": "Stale", "version": 2})
	updated := owner.must(200, "PATCH", crm+"/activities/"+taskID, map[string]any{"version": 1, "status": "done", "due_at": nil, "assigned_to": nil})
	if updated["due_at"] != nil || updated["assigned_to"] != nil || updated["status"] != "done" || updated["version"] != float64(2) {
		t.Fatalf("update: %v", updated)
	}
	if len(owner.must(200, "GET", crm+"/activities?status=open", nil)["data"].([]any)) != 0 {
		t.Fatal("completed task in open list")
	}
	note := owner.must(201, "POST", crm+"/activities", map[string]any{"record_id": id, "kind": "note", "title": "Discovery", "body": "## Goals\n\nImprove sales."})
	if len(owner.must(200, "GET", crm+"/activities?kind=note&record_id="+id, nil)["data"].([]any)) != 1 {
		t.Fatal("missing record note")
	}

	view := owner.must(201, "POST", crm+"/views", map[string]any{"name": "My contacts", "record_type": "person", "config": map[string]any{"layout": "table", "columns": []string{"companies"}}})
	viewID := view["id"].(string)
	inv := owner.must(201, "POST", base+"/invites", map[string]any{"email": "rep@acme.example", "role": "operator"})
	rep := &client{t: t, h: e.h}
	rep.must(200, "POST", "/v1/invites/accept", map[string]any{"token": inv["token"], "name": "Rep", "password": "long enough password"})
	if len(rep.must(200, "GET", crm+"/views", nil)["data"].([]any)) != 0 {
		t.Fatal("private view visible to another user")
	}
	owner.must(200, "PATCH", crm+"/views/"+viewID, map[string]any{"version": 1, "shared": true})
	if len(rep.must(200, "GET", crm+"/views", nil)["data"].([]any)) != 1 {
		t.Fatal("shared view hidden")
	}
	rep.must(404, "PATCH", crm+"/views/"+viewID, map[string]any{"version": 2, "name": "Not mine"})
	rep.must(404, "DELETE", crm+"/views/"+viewID, nil)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Other", "handle": "other"})["id"].(string)
	otherCRM := "/v1/brands/" + other + "/crm"
	if len(owner.must(200, "GET", otherCRM+"/activities", nil)["data"].([]any)) != 0 {
		t.Fatal("activity crossed brands")
	}
	owner.must(404, "PATCH", otherCRM+"/activities/"+taskID, map[string]any{"version": 2, "title": "Wrong brand"})
	// A brand route hides records belonging to another brand.
	owner.must(404, "POST", otherCRM+"/activities", map[string]any{"record_id": id, "kind": "task", "title": "Wrong brand"})
	owner.must(404, "POST", otherCRM+"/views", map[string]any{"brand_id": brand, "name": "Wrong brand", "record_type": "person"})
	owner.must(200, "DELETE", crm+"/activities/"+note["id"].(string), nil)
	owner.must(200, "DELETE", crm+"/views/"+viewID, nil)
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND action IN ('activities.created','activities.updated','activities.deleted','views.created','views.updated','views.deleted')`, ws).Scan(&n); err != nil || n != 7 {
		t.Fatalf("audit events: %d, %v", n, err)
	}
	count := 0
	for _, event := range events() {
		if event.Name == "activities.created" {
			count++
		}
	}
	if count < 2 {
		t.Fatal("activity creation not tracked")
	}
}

func TestCRMSortedRecordsAndLeadRelationship(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	company := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme", "identities": []map[string]any{{"namespace": "domain", "value": "acme.example"}}})
	for _, name := range []string{"Zoe", "Ana", "Ben"} {
		owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": name})
	}
	page := owner.must(200, "GET", base+"/records?type=person&sort=name&direction=asc&limit=1", nil)
	if page["total"] != float64(3) || page["data"].([]any)[0].(map[string]any)["name"] != "Ana" {
		t.Fatalf("server sort/page: %v", page)
	}
	nextPage := owner.must(200, "GET", base+"/records?type=person&sort=name&direction=asc&limit=1&cursor="+page["next_cursor"].(string), nil)
	if nextPage["data"].([]any)[0].(map[string]any)["name"] != "Ben" || nextPage["next_cursor"] == nil {
		t.Fatalf("sorted cursor: %v", nextPage)
	}
	owner.must(400, "GET", base+"/records?sort=attributes%3Bdrop", nil)
	lastPage := owner.must(200, "GET", base+"/records?type=person&sort=name&direction=asc&limit=1&cursor="+nextPage["next_cursor"].(string), nil)
	if lastPage["data"].([]any)[0].(map[string]any)["name"] != "Zoe" || lastPage["next_cursor"] != nil {
		t.Fatalf("last sorted page: %v", lastPage)
	}
	defaultPage := owner.must(200, "GET", base+"/records?limit=1", nil)
	owner.must(400, "GET", base+"/records?sort=name&cursor="+defaultPage["next_cursor"].(string), nil)
	lead := owner.must(201, "POST", base+"/leads", map[string]any{"email": "ana@acme.example", "first_name": "Ana", "company_record_id": company["id"]})
	person := owner.must(200, "GET", base+"/records/"+lead["record_id"].(string), nil)
	refs := person["attributes"].(map[string]any)["companies"].([]any)
	if len(refs) != 1 || refs[0] != company["id"] {
		t.Fatalf("lead company: %v", refs)
	}
	links := owner.must(200, "GET", base+"/records/"+person["id"].(string)+"/relationships", nil)["data"].([]any)
	if len(links) != 1 {
		t.Fatalf("relationship missing: %v", links)
	}
	owner.must(200, "PATCH", base+"/leads/"+lead["id"].(string), map[string]any{"title": "Founder", "company_record_id": company["id"]})
	for _, currency := range []string{"USD", "EUR"} {
		owner.must(201, "POST", base+"/records", map[string]any{"type": "deal", "name": currency + " opportunity", "attributes": map[string]any{"stage": "lead", "value": map[string]any{"amount_minor": 10000, "currency": currency}}})
	}
	summary := owner.must(200, "GET", "/v1/brands/"+company["brand_id"].(string)+"/crm/summary", nil)
	if len(summary["pipeline"].([]any)) != 2 {
		t.Fatalf("currencies combined: %v", summary)
	}
	found := owner.must(200, "GET", base+"/records?ids="+company["id"].(string), nil)["data"].([]any)
	if len(found) != 1 || found[0].(map[string]any)["id"] != company["id"] {
		t.Fatalf("record ids filter: %v", found)
	}
	brand2 := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Second", "handle": "second"})["id"].(string)
	company2 := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Second company", "brand_id": brand2})
	lead2 := owner.must(201, "POST", base+"/leads", map[string]any{"email": "new@second.example", "company_record_id": company2["id"]})
	person2 := owner.must(200, "GET", base+"/records/"+lead2["record_id"].(string), nil)
	visible := owner.must(200, "GET", base+"/leads?brand=second", nil)["data"].([]any)
	if len(visible) != 1 || visible[0].(map[string]any)["id"] != lead2["id"] {
		t.Fatalf("new CRM lead disappeared from selected brand: %v", visible)
	}
	if person2["brand_id"] != brand2 {
		t.Fatalf("new lead created in the wrong brand: %v", person2)
	}
	owner.must(404, "POST", base+"/leads", map[string]any{"email": "absent@acme.example", "company_record_id": "00000000-0000-0000-0000-000000000001"})
	if len(owner.must(200, "GET", base+"/leads?q=absent", nil)["data"].([]any)) != 0 {
		t.Fatal("failed relationship left a partial lead")
	}
}

func TestNativeCRMListEntries(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/catalog/types", map[string]any{"key": "list_sample", "name": "Sample entry", "plural": "Sample entries"})
	owner.must(201, "POST", base+"/catalog/types/list_sample/fields", map[string]any{"key": "outflare_parent_record", "label": "Linked record", "value_type": "record_reference", "target_type": "person"})
	person := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana"})["id"].(string)
	other := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ben"})["id"].(string)
	input := map[string]any{"type": "list_sample", "name": "Sample entry", "attributes": map[string]any{"outflare_parent_record": person}, "identities": []map[string]any{{"namespace": "outflare.list_entry", "value": "list_sample:" + person, "verified": true, "source": "manual"}}}
	entry := owner.must(201, "POST", base+"/records", input)
	owner.must(409, "POST", base+"/records", input)
	owner.must(400, "PATCH", base+"/records/"+entry["id"].(string), map[string]any{"version": entry["version"], "attributes": map[string]any{"outflare_parent_record": other}})
}
