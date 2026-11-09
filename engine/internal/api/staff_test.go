package api_test

import (
	"testing"
)

func TestTeamDirectory(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	// Departments, and a team that sits in one.
	sales := owner.must(201, "POST", base+"/departments", map[string]any{"name": "Sales"})["id"].(string)
	owner.must(400, "POST", base+"/departments", map[string]any{"name": "sales"})
	team := owner.must(201, "POST", base+"/teams", map[string]any{"name": "Enterprise", "department_id": sales})
	if team["department_id"] != sales {
		t.Fatalf("team department: %v", team)
	}

	// People, with a manager and a team.
	lead := owner.must(201, "POST", base+"/staff", map[string]any{"name": "Ana Silva", "email": "Ana@Acme.example", "title": "Head of sales", "department_id": sales})
	if lead["email"] != "ana@acme.example" || lead["status"] != "active" || len(lead["accounts"].([]any)) != 0 {
		t.Fatalf("lead: %v", lead)
	}
	anaID := lead["id"].(string)
	owner.must(400, "POST", base+"/staff", map[string]any{"name": "Copy", "email": "ana@acme.example"})
	owner.must(400, "POST", base+"/staff", map[string]any{"name": "Nobody", "department_id": "00000000-0000-4000-8000-000000000000"})
	ben := owner.must(201, "POST", base+"/staff", map[string]any{
		"name": "Ben Ito", "email": "ben@acme.example", "department_id": sales, "team_id": team["id"], "manager_id": anaID, "started_on": "2026-11-02",
	})
	benID := ben["id"].(string)
	if ben["manager_id"] != anaID || ben["started_on"] != "2026-11-02" {
		t.Fatalf("ben: %v", ben)
	}
	owner.must(400, "PATCH", base+"/staff/"+benID, map[string]any{"manager_id": benID})
	owner.must(200, "PATCH", base+"/departments/"+sales, map[string]any{"head_id": anaID})
	depts := owner.must(200, "GET", base+"/departments", nil)["data"].([]any)
	if d := depts[0].(map[string]any); d["head_id"] != anaID || d["people"] != 2.0 || d["teams"] != 1.0 {
		t.Fatalf("departments: %v", depts)
	}
	inTeam := owner.must(200, "GET", base+"/staff?team_id="+team["id"].(string), nil)["data"].([]any)
	if len(inTeam) != 1 || inTeam[0].(map[string]any)["id"] != benID {
		t.Fatalf("by team: %v", inTeam)
	}

	// A company Telegram account, handed to Ben.
	tg := owner.must(201, "POST", base+"/staff-accounts", map[string]any{"kind": "telegram", "address": "@Acme_Sales_1", "label": "Sales 1", "holder_id": benID})
	tgID := tg["id"].(string)
	if tg["address"] != "acme_sales_1" || tg["holder_id"] != benID || len(tg["holders"].([]any)) != 1 {
		t.Fatalf("telegram account: %v", tg)
	}
	owner.must(400, "POST", base+"/staff-accounts", map[string]any{"kind": "telegram", "address": "acme_sales_1"})
	owner.must(400, "POST", base+"/staff-accounts", map[string]any{"kind": "telegram", "address": "x"})
	phone := owner.must(201, "POST", base+"/staff-accounts", map[string]any{"kind": "phone", "address": "+44 7700 900123", "renews_on": "2027-01-31"})
	if phone["address"] != "+447700900123" || phone["renews_on"] != "2027-01-31" || phone["holder_id"] != nil {
		t.Fatalf("phone: %v", phone)
	}
	got := owner.must(200, "GET", base+"/staff/"+benID, nil)
	if acc := got["accounts"].([]any); len(acc) != 1 || acc[0].(map[string]any)["id"] != tgID {
		t.Fatalf("ben's accounts: %v", got)
	}

	// Ben leaves: someone who held an account can't be removed, and can't be
	// given a new one. The account passes to Ana; its history keeps Ben.
	owner.must(400, "DELETE", base+"/staff/"+benID, nil)
	left := owner.must(200, "PATCH", base+"/staff/"+benID, map[string]any{"status": "left"})
	if left["status"] != "left" || left["left_on"] == nil {
		t.Fatalf("left: %v", left)
	}
	owner.must(400, "PATCH", base+"/staff-accounts/"+phone["id"].(string), map[string]any{"holder_id": benID})
	moved := owner.must(200, "PATCH", base+"/staff-accounts/"+tgID, map[string]any{"holder_id": anaID})
	hs := moved["holders"].([]any)
	if moved["holder_id"] != anaID || len(hs) != 2 || hs[0].(map[string]any)["staff_id"] != anaID || hs[1].(map[string]any)["ended_at"] == nil {
		t.Fatalf("handover: %v", moved)
	}
	if list := owner.must(200, "GET", base+"/staff-accounts?holder_id="+anaID, nil)["data"].([]any); len(list) != 1 {
		t.Fatalf("by holder: %v", list)
	}

	// Retiring ends the holder; a held account can't be removed.
	ret := owner.must(200, "PATCH", base+"/staff-accounts/"+tgID, map[string]any{"status": "retired"})
	if ret["holder_id"] != nil || ret["status"] != "retired" {
		t.Fatalf("retired: %v", ret)
	}
	owner.must(400, "DELETE", base+"/staff-accounts/"+tgID, nil)
	owner.must(204, "DELETE", base+"/staff-accounts/"+phone["id"].(string), nil)
	owner.must(400, "PATCH", base+"/staff-accounts/"+tgID, map[string]any{"kind": "email"})

	// Someone added by mistake is removed; deleting a department keeps its people.
	tmp := owner.must(201, "POST", base+"/staff", map[string]any{"name": "Typo"})["id"].(string)
	owner.must(204, "DELETE", base+"/staff/"+tmp, nil)
	owner.must(404, "GET", base+"/staff/"+tmp, nil)
	owner.must(204, "DELETE", base+"/departments/"+sales, nil)
	if a := owner.must(200, "GET", base+"/staff/"+anaID, nil); a["department_id"] != nil {
		t.Fatalf("department kept: %v", a)
	}

	// Another workspace sees none of it.
	ws2 := owner.must(201, "POST", "/v1/workspaces", map[string]string{"name": "Second"})["id"].(string)
	b2 := "/v1/workspaces/" + ws2
	owner.must(404, "GET", b2+"/staff/"+anaID, nil)
	owner.must(404, "GET", b2+"/staff-accounts/"+tgID, nil)
	owner.must(404, "PATCH", b2+"/staff-accounts/"+tgID, map[string]any{"holder_id": nil})
	if l := owner.must(200, "GET", b2+"/staff", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("leak: %v", l)
	}
	owner.must(400, "POST", b2+"/staff", map[string]any{"name": "Cross", "manager_id": anaID})
}
