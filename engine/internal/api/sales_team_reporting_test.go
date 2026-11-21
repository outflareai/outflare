package api_test

import (
	"context"
	"net/url"
	"testing"
)

func TestSalesTeamReportingActionsAndIsolation(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	company := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Northpeak Example"})
	p := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Example Buyer", "attributes": map[string]any{"companies": []string{company["id"].(string)}}})
	brand := p["brand_id"].(string)
	root := "/v1/brands/" + brand + "/sales"
	rep, rid := joinAs(t, owner, ws, "rep@northpeak.example", map[string]any{"role": "operator"})
	ae, aid := joinAs(t, owner, ws, "ae@northpeak.example", map[string]any{"role": "operator"})
	manager, mid := joinAs(t, owner, ws, "manager@northpeak.example", map[string]any{"role": "operator"})
	for id, role := range map[string]string{rid: "sdr", aid: "ae", mid: "manager"} {
		owner.must(200, "PUT", root+"/team/"+id, map[string]any{"role": role})
	}
	leadID := "00000000-0000-4000-8000-000000000081"
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(context.Background(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO sales_leads(id,workspace_id,brand_id,record_id,source,source_id,source_sealed)VALUES($1,$2,$3,$4,'test','team-report',decode('00','hex'))`, leadID, ws, brand, p["id"])
	path := root + "/leads/" + leadID
	lead := rep.must(200, "POST", path+"/actions", map[string]any{"action": "read", "version": 1})
	rep.must(200, "POST", path+"/actions", map[string]any{"action": "read", "version": 1})
	lead = rep.must(200, "POST", path+"/actions", map[string]any{"action": "assign", "version": lead["version"], "assignee_id": aid})
	// The person who assigned it gets credit, not the current owner.
	lead = manager.must(200, "POST", path+"/actions", map[string]any{"action": "archive", "version": lead["version"]})
	exec(`UPDATE sales_events SET occurred_at='2026-11-10T00:00:00Z' WHERE workspace_id=$1 AND brand_id=$2`, ws, brand)
	exec(`UPDATE audit_events SET created_at='2026-11-10T00:00:00Z' WHERE workspace_id=$1 AND brand_id=$2 AND action='sales.lead_seen'`, ws, brand)
	// Include exact lower bound; exclude exact upper bound and older dates.
	for _, at := range []string{"2026-11-09T23:59:59Z", "2026-11-11T00:00:00Z"} {
		exec(`INSERT INTO sales_events(id,workspace_id,brand_id,record_id,lead_id,actor_id,actor_role,action,content_sealed,occurred_at)VALUES(gen_random_uuid(),$1,$2,$3,$4,$5,'sdr','details_updated',decode('00','hex'),$6)`, ws, brand, p["id"], leadID, rid, at)
	}
	// Retained audits predate the dedicated Seen event. New linked audits must
	// not duplicate the same action in the report.
	exec(`INSERT INTO audit_events(workspace_id,brand_id,actor_id,actor_type,action,target_type,target_id,created_at)
 SELECT $1,$2,actor_id,'human','sales.lead_seen','sales_lead',$3,'2026-11-10T01:00:00Z' FROM users WHERE id=$4`, ws, brand, leadID, rid)
	window := "?from=2026-11-10T00:00:00Z&to=2026-11-11T00:00:00Z"
	reportPath := root + "/reporting/team"
	report := owner.must(200, "GET", reportPath+window, nil)
	member := func(report map[string]any, id string) map[string]any {
		t.Helper()
		for _, v := range report["members"].([]any) {
			m := v.(map[string]any)
			if m["user_id"] == id {
				return m
			}
		}
		t.Fatal("missing member", id)
		return nil
	}
	counts := member(report, rid)["counts"].(map[string]any)
	if counts["lead_seen"] != float64(1) || counts["lead_assigned"] != float64(1) || counts["details_updated"] != nil {
		t.Fatal("unique actor/date counts", counts)
	}
	if len(member(report, aid)["counts"].(map[string]any)) != 0 {
		t.Fatal("assignment credited to recipient")
	}
	if member(report, mid)["counts"].(map[string]any)["lead_archived"] != float64(1) {
		t.Fatal("missing archiver")
	}
	if len(report["data"].([]any)) != 5 {
		t.Fatal("log must preserve new and legacy reads", report)
	}
	projection := owner.must(200, "GET", reportPath+window+"&fields=action,occurred_at", nil)
	projected := projection["data"].([]any)[0].(map[string]any)
	if len(projected) != 3 || projected["id"] == nil || projected["action"] == nil || projected["occurred_at"] == nil || len(projection["members"].([]any)) != len(report["members"].([]any)) {
		t.Fatal("field selection must preserve paging and summary while projecting log rows", projection)
	}
	manager.must(200, "GET", reportPath+window, nil)
	rep.must(403, "GET", reportPath+window, nil)
	token := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Read report", "scopes": []string{"customers.read"}})
	reader := &client{t: t, h: e.h, bearer: token["token"].(string)}
	reader.must(403, "GET", reportPath+window, nil)
	for _, query := range []string{"", "?from=invalid&to=invalid", window + "&user_id=invalid", window + "&action=not%20an%20action", window + "&cursor=broken"} {
		owner.must(400, "GET", reportPath+query, nil)
	}
	filtered := manager.must(200, "GET", reportPath+window+"&user_id="+rid+"&action=lead_seen&search=northpeak", nil)
	if len(filtered["data"].([]any)) != 3 || member(filtered, mid)["counts"].(map[string]any)["lead_archived"] != float64(1) {
		t.Fatal("filter changed summary or missed company", filtered)
	}
	if len(owner.must(200, "GET", reportPath+window+"&search=%25", nil)["data"].([]any)) != 0 {
		t.Fatal("search treated percent as wildcard")
	}
	// A later read never erases the historical day's evidence.
	ae.must(200, "POST", path+"/actions", map[string]any{"action": "reopen", "version": lead["version"]})
	rep.must(200, "POST", path+"/actions", map[string]any{"action": "read", "version": 1})
	report = owner.must(200, "GET", reportPath+window, nil)
	if member(report, rid)["counts"].(map[string]any)["lead_seen"] != float64(1) {
		t.Fatal("later read erased history")
	}
	// The same timestamp can span pages without dropping or duplicating actions.
	exec(`INSERT INTO sales_events(id,workspace_id,brand_id,record_id,lead_id,actor_id,actor_role,action,content_sealed,occurred_at)
 SELECT gen_random_uuid(),$1,$2,$3,$4,$5,'sdr','feedback_added',decode('00','hex'),'2026-11-10T12:00:00Z' FROM generate_series(1,53)`, ws, brand, p["id"], leadID, rid)
	page := owner.must(200, "GET", reportPath+window+"&action=feedback_added", nil)
	seen := map[string]bool{}
	for _, v := range page["data"].([]any) {
		seen[v.(map[string]any)["id"].(string)] = true
	}
	if len(seen) != 50 || page["next_cursor"] == nil {
		t.Fatal("first page", page)
	}
	page = owner.must(200, "GET", reportPath+window+"&action=feedback_added&cursor="+url.QueryEscape(page["next_cursor"].(string)), nil)
	for _, v := range page["data"].([]any) {
		id := v.(map[string]any)["id"].(string)
		if seen[id] {
			t.Fatal("duplicate action on page two")
		}
		seen[id] = true
	}
	if len(seen) != 53 || page["next_cursor"] != nil || member(page, rid)["counts"].(map[string]any)["feedback_added"] != float64(1) {
		t.Fatal("pagination or unique-lead count", page)
	}
	// A page can end on a legacy audit ID and continue into native Seen events.
	exec(`INSERT INTO audit_events(workspace_id,brand_id,actor_id,actor_type,action,target_type,target_id,created_at)
 SELECT $1,$2,u.actor_id,'human','sales.lead_seen','sales_lead',$3,'2026-11-10T01:00:00Z' FROM users u CROSS JOIN generate_series(1,51) WHERE u.id=$4`, ws, brand, leadID, rid)
	page = owner.must(200, "GET", reportPath+window+"&action=lead_seen", nil)
	if len(page["data"].([]any)) != 50 || page["next_cursor"] == nil {
		t.Fatal("legacy first page", page)
	}
	page = owner.must(200, "GET", reportPath+window+"&action=lead_seen&cursor="+url.QueryEscape(page["next_cursor"].(string)), nil)
	if len(page["data"].([]any)) != 4 || page["next_cursor"] != nil {
		t.Fatal("legacy/native page boundary", page)
	}
	owner.must(200, "PUT", root+"/team/"+rid, map[string]any{"role": "none"})
	former := member(owner.must(200, "GET", reportPath+window, nil), rid)
	if former["active"] != false || former["role"] != "sdr" {
		t.Fatal("former member lost history", former)
	}
	otherBrand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Other Example", "handle": "other-team-report"})["id"].(string)
	otherReport := owner.must(200, "GET", "/v1/brands/"+otherBrand+"/sales/reporting/team"+window, nil)
	if len(otherReport["data"].([]any)) != 0 || len(otherReport["members"].([]any)) != 0 {
		t.Fatal("cross-brand activity", otherReport)
	}
	manager.must(403, "GET", "/v1/brands/"+otherBrand+"/sales/reporting/team"+window, nil)
	otherWS := owner.must(201, "POST", "/v1/workspaces", map[string]string{"name": "Other workspace"})["id"].(string)
	otherOwner, _ := joinAs(t, owner, otherWS, "other-admin@northpeak.example", map[string]any{"role": "admin"})
	otherOwner.must(404, "GET", reportPath+window, nil)
	// Archived conversations remain visible to management; deleted records do not.
	owner.must(200, "DELETE", base+"/records/"+p["id"].(string), nil)
	if len(owner.must(200, "GET", reportPath+window, nil)["data"].([]any)) != 0 {
		t.Fatal("deleted record still appears")
	}
}
