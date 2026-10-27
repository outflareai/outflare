package api_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestProjectsBriefAndProjectTokens(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws

	owner.must(400, "POST", base+"/projects", map[string]any{"offer": "no name"})
	owner.must(400, "POST", base+"/projects", map[string]any{"name": "Bad ICP", "icp": map[string]any{"colour": []string{"red"}}})
	p := owner.must(201, "POST", base+"/projects", map[string]any{
		"name":     "Fintech CFOs, AU",
		"offer":    "Month-end close in two days",
		"audience": "CFOs at Series B fintechs",
		"market":   "Australia, English",
		"icp":      map[string]any{"titles": []string{"CFO", " cfo ", "VP Finance"}, "employee_ranges": []string{"51-200"}},
	})
	id := p["id"].(string)
	icp := p["icp"].(map[string]any)
	if p["status"] != "active" || p["brief_version"] != 1.0 || len(icp["titles"].([]any)) != 2 || len(icp["industries"].([]any)) != 0 {
		t.Fatalf("created: %v", p)
	}
	owner.must(400, "POST", base+"/projects", map[string]any{"name": "fintech cfos, au"})
	other := owner.must(201, "POST", base+"/projects", map[string]any{"name": "Agencies, UK"})["id"].(string)

	// The brief, as structured fields and one Markdown document.
	b := owner.must(200, "GET", base+"/projects/"+id+"/brief", nil)
	md := b["markdown"].(string)
	if b["version"] != 1.0 || !strings.Contains(md, "## Offer\n\nMonth-end close") || !strings.Contains(md, "- Titles: CFO, VP Finance") {
		t.Fatalf("brief: %v", b)
	}

	// Saving unchanged fields keeps the version; a real change bumps it,
	// and an edit made against the old version is refused.
	owner.must(200, "PATCH", base+"/projects/"+id, map[string]any{"offer": "Month-end close in two days", "brief_version": 1})
	p = owner.must(200, "PATCH", base+"/projects/"+id, map[string]any{"brief": "Lead with the audit story.", "brief_version": 1})
	if p["brief_version"] != 2.0 {
		t.Fatalf("version after edit: %v", p["brief_version"])
	}
	res := owner.must(409, "PATCH", base+"/projects/"+id, map[string]any{"brief": "Stale edit", "brief_version": 1})
	if res["error"].(map[string]any)["code"] != "brief_changed" {
		t.Fatalf("stale edit: %v", res)
	}
	// Non-brief fields never conflict.
	owner.must(200, "PATCH", base+"/projects/"+id, map[string]any{"daily_send_limit": 500, "brief_version": 1})

	// Archived projects drop out of the default list.
	owner.must(200, "PATCH", base+"/projects/"+other, map[string]any{"status": "archived"})
	if n := len(owner.must(200, "GET", base+"/projects", nil)["data"].([]any)); n != 1 {
		t.Fatalf("default list: %d", n)
	}
	if n := len(owner.must(200, "GET", base+"/projects?status=all", nil)["data"].([]any)); n != 2 {
		t.Fatalf("all: %d", n)
	}

	// A project token sees only its project and is kept off routes that
	// are not project-aware yet.
	owner.must(400, "POST", base+"/api-tokens", map[string]any{"name": "x", "scopes": []string{"projects.read"}, "project_id": "00000000-0000-0000-0000-000000000000"})
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{
		"name": "Fintech agent", "scopes": []string{"projects.read", "projects.manage", "mailboxes.read"}, "project_id": id,
	})
	if tok["api_token"].(map[string]any)["project_id"] != id {
		t.Fatalf("token view: %v", tok)
	}
	agent := &client{t: t, h: h, bearer: tok["token"].(string)}
	list := agent.must(200, "GET", base+"/projects?status=all", nil)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != id {
		t.Fatalf("agent list: %v", list)
	}
	agent.must(404, "GET", base+"/projects/"+other, nil)
	agent.must(404, "GET", base+"/projects/"+other+"/brief", nil)
	agent.must(200, "PATCH", base+"/projects/"+id, map[string]any{"market": "Australia and New Zealand"})
	agent.must(403, "POST", base+"/projects", map[string]any{"name": "Mine now"})
	agent.must(403, "DELETE", base+"/projects/"+id, nil)
	agent.must(403, "POST", base+"/mailboxes", map[string]any{})
	agent.must(404, "GET", base+"/mailboxes?project_id="+other, nil)

	// Deleting waits until the project's tokens are revoked.
	owner.must(409, "DELETE", base+"/projects/"+id, nil)
	owner.must(204, "DELETE", base+"/api-tokens/"+tok["api_token"].(map[string]any)["id"].(string), nil)
	owner.must(204, "DELETE", base+"/projects/"+id, nil)
	owner.must(404, "GET", base+"/projects/"+id, nil)

	// Every change is in the audit log.
	actions := map[string]bool{}
	for _, ev := range owner.must(200, "GET", base+"/audit-events", nil)["data"].([]any) {
		actions[ev.(map[string]any)["action"].(string)] = true
	}
	for _, a := range []string{"project.created", "project.updated", "project.deleted"} {
		if !actions[a] {
			t.Errorf("no %s audit event", a)
		}
	}
}

func TestProjectOwnsRecordsAndHoldsSends(t *testing.T) {
	e := freshEnv(t)
	clock := &fakeClock{}
	clock.Set(time.Date(2030, 1, 8, 10, 0, 0, 0, time.UTC))
	owner, base, srv := sendingSetup(t, e, clock)

	pid := owner.must(201, "POST", base+"/projects", map[string]any{"name": "Fintech", "daily_send_limit": 1})["id"].(string)
	_, imp := owner.raw("POST", base+"/leads/import?list=L", "text/csv", "email\none@one-io.example\ntwo@two-io.example\nthree@three-io.example\n")
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{
		"name": "Capped", "target_daily": 1000, "send_days": []int{1, 2, 3, 4, 5, 6, 7},
		"steps": []map[string]any{{"subject": "Hello", "body": "Hi there"}},
	})
	seqID := seq["id"].(string)

	owner.must(400, "POST", base+"/projects/"+pid+"/add", map[string]any{})
	moved := owner.must(200, "POST", base+"/projects/"+pid+"/add", map[string]any{
		"domains": []string{"ACME.test"}, "lead_list_ids": []string{imp["list_id"].(string)}, "sequence_ids": []string{seqID},
	})["moved"].(map[string]any)
	if moved["domains"] != 1.0 || moved["mailboxes"] != 2.0 || moved["lead_lists"] != 1.0 || moved["sequences"] != 1.0 {
		t.Fatalf("moved: %v", moved)
	}
	p := owner.must(200, "GET", base+"/projects/"+pid, nil)
	if c := p["counts"].(map[string]any); c["domains"] != 1.0 || c["mailboxes"] != 2.0 || c["lead_lists"] != 1.0 || c["sequences"] != 1.0 {
		t.Fatalf("counts: %v", c)
	}

	// The project's limit holds all of its sequences to one send.
	owner.must(200, "POST", base+"/sequences/"+seqID+"/enrollments", map[string]any{"list_id": imp["list_id"]})
	owner.must(200, "PATCH", base+"/sequences/"+seqID, map[string]any{"status": "active"})
	for range 3 {
		e.pump(t)
		clock.Add(5 * time.Minute)
	}
	if n := len(srv.Messages()); n != 1 {
		t.Fatalf("sent under a project limit of 1: %d", n)
	}
	reasons := map[string]int{}
	for _, en := range enrollments(t, owner, base, seqID) {
		reasons[fmt.Sprint(en["status"], ":", en["status_reason"])]++
	}
	if reasons["active:project_daily_limit"] != 2 {
		t.Fatalf("reasons: %v", reasons)
	}
	if got := owner.must(200, "GET", base+"/projects/"+pid, nil)["sent_24h"]; got != 1.0 {
		t.Fatalf("sent_24h: %v", got)
	}

	// Pausing the project holds everything, with the limit lifted.
	owner.must(200, "PATCH", base+"/projects/"+pid, map[string]any{"daily_send_limit": nil, "status": "paused"})
	clock.Add(2 * time.Hour)
	for range 2 {
		e.pump(t)
		clock.Add(20 * time.Minute)
	}
	if n := len(srv.Messages()); n != 1 {
		t.Fatalf("sent while paused: %d", n)
	}
	owner.must(200, "PATCH", base+"/projects/"+pid, map[string]any{"status": "active"})
	for range 3 {
		e.pump(t)
		clock.Add(20 * time.Minute)
	}
	if n := len(srv.Messages()); n != 3 {
		t.Fatalf("sent after resume: %d", n)
	}

	// A sequence outside the project cannot be given the project's mailboxes.
	outside := owner.must(201, "POST", base+"/sequences", map[string]any{
		"name": "Outside", "steps": []map[string]any{{"subject": "Hello", "body": "Hi"}},
	})["id"].(string)
	boxes := mailboxIDs(t, owner, base)
	res := owner.must(200, "POST", base+"/mailboxes/assign", map[string]any{"mailbox_ids": []string{boxes["ana@acme.test"]}, "sequence_id": outside})
	r0 := res["data"].([]any)[0].(map[string]any)
	if r0["result"] != "refused" || !strings.Contains(r0["message"].(string), "Fintech") {
		t.Fatalf("cross-project assign: %v", res)
	}

	// Removing only touches records in this project.
	moved = owner.must(200, "POST", base+"/projects/"+pid+"/remove", map[string]any{
		"mailbox_ids": []string{boxes["ana@acme.test"]}, "sequence_ids": []string{outside},
	})["moved"].(map[string]any)
	if moved["mailboxes"] != 1.0 || moved["sequences"] != 0.0 {
		t.Fatalf("removed: %v", moved)
	}

	// Records carry their project, and lists filter by it.
	inProj := owner.must(201, "POST", base+"/sequences", map[string]any{
		"name": "Born in project", "project_id": pid, "steps": []map[string]any{{"subject": "Hi", "body": "Hi"}},
	})
	if inProj["project_id"] != pid {
		t.Fatalf("sequence project: %v", inProj["project_id"])
	}
	owner.must(400, "POST", base+"/sequences", map[string]any{"name": "Nowhere", "project_id": "00000000-0000-0000-0000-000000000000"})
	if n := len(owner.must(200, "GET", base+"/sequences?project_id="+pid, nil)["data"].([]any)); n != 2 {
		t.Fatalf("project sequences: %d", n)
	}
	if n := len(owner.must(200, "GET", base+"/sequences?project_id=none", nil)["data"].([]any)); n != 1 {
		t.Fatalf("shared sequences: %d", n)
	}
	owner.must(200, "PATCH", base+"/sequences/"+outside, map[string]any{"project_id": pid})
	if n := len(owner.must(200, "GET", base+"/sequences?project_id=none", nil)["data"].([]any)); n != 0 {
		t.Fatalf("shared sequences after move: %d", n)
	}
	mbs := owner.must(200, "GET", base+"/mailboxes?project_id="+pid, nil)["data"].([]any)
	if len(mbs) != 1 || mbs[0].(map[string]any)["email"] != "bo@acme.test" {
		t.Fatalf("project mailboxes: %v", mbs)
	}
	if n := len(owner.must(200, "GET", base+"/mailboxes?project_id=none", nil)["data"].([]any)); n != 1 {
		t.Fatalf("shared mailboxes: %d", n)
	}
	doms := owner.must(200, "GET", base+"/sending-domains?project_id="+pid, nil)["data"].([]any)
	if len(doms) != 1 || doms[0].(map[string]any)["project_id"] != pid {
		t.Fatalf("project domains: %v", doms)
	}
	owner.must(400, "GET", base+"/mailboxes?project_id=nope", nil)

	// A project token reads only its project's domains, mailboxes and sequences.
	shared := owner.must(201, "POST", base+"/sequences", map[string]any{
		"name": "Shared", "steps": []map[string]any{{"subject": "Hi", "body": "Hi"}},
	})["id"].(string)
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{
		"name": "Fintech reader", "scopes": []string{"mailboxes.read", "sequences.read"}, "project_id": pid,
	})
	agent := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	if n := len(agent.must(200, "GET", base+"/sequences", nil)["data"].([]any)); n != 3 {
		t.Fatalf("agent sequences: %d", n)
	}
	agent.must(200, "GET", base+"/sequences/"+seqID, nil)
	agent.must(200, "GET", base+"/sequences/"+seqID+"/enrollments", nil)
	agent.must(404, "GET", base+"/sequences/"+shared, nil)
	agent.must(404, "GET", base+"/sequences/"+shared+"/enrollments", nil)
	agent.must(404, "GET", base+"/sequences?project_id=none", nil)
	if mbs := agent.must(200, "GET", base+"/mailboxes", nil)["data"].([]any); len(mbs) != 1 {
		t.Fatalf("agent mailboxes: %v", mbs)
	}
	agent.must(200, "GET", base+"/mailboxes/"+boxes["bo@acme.test"], nil)
	agent.must(404, "GET", base+"/mailboxes/"+boxes["ana@acme.test"], nil)
	agent.must(200, "GET", base+"/sending-domains/acme.test", nil)
	agent.must(403, "PATCH", base+"/sequences/"+seqID, map[string]any{"name": "Renamed"})

	// With sequences.manage, a project token builds and runs sequences, but
	// only inside its project and only with its project's leads.
	_, sharedImp := owner.raw("POST", base+"/leads/import?list=Shared", "text/csv", "email\nzed@zed-io.example\none@one-io.example\n")
	sharedList := sharedImp["list_id"].(string)
	writer := owner.must(201, "POST", base+"/api-tokens", map[string]any{
		"name": "Fintech writer", "scopes": []string{"sequences.read", "sequences.manage"}, "project_id": pid,
	})
	agent = &client{t: t, h: e.h, bearer: writer["token"].(string)}
	agent.must(400, "POST", base+"/sequences", map[string]any{"name": "Elsewhere", "project_id": nil})
	made := agent.must(201, "POST", base+"/sequences", map[string]any{
		"name": "Agent built", "steps": []map[string]any{{"subject": "Hi {{first_name|there}}", "body": "Hello"}},
	})
	madeID := made["id"].(string)
	if made["project_id"] != pid {
		t.Fatalf("agent sequence project: %v", made["project_id"])
	}
	agent.must(200, "PATCH", base+"/sequences/"+madeID, map[string]any{"name": "Agent built, v2"})
	agent.must(400, "PATCH", base+"/sequences/"+madeID, map[string]any{"project_id": nil})
	agent.must(200, "PUT", base+"/sequences/"+madeID+"/steps", map[string]any{"steps": []map[string]any{{"subject": "Hi", "body": "Hello again"}}})
	agent.must(404, "PATCH", base+"/sequences/"+shared, map[string]any{"name": "Mine now"})
	agent.must(404, "PUT", base+"/sequences/"+shared+"/steps", map[string]any{"steps": []map[string]any{{"subject": "Hi", "body": "Hi"}}})
	agent.must(404, "POST", base+"/sequences/"+shared+"/enrollments", map[string]any{"list_id": imp["list_id"]})
	agent.must(404, "DELETE", base+"/sequences/"+shared, nil)
	agent.must(404, "PATCH", base+"/sequences/not-an-id", map[string]any{"name": "x"})
	agent.must(400, "POST", base+"/sequences/"+madeID+"/enrollments", map[string]any{"list_id": sharedList})
	enrolled := agent.must(200, "POST", base+"/sequences/"+madeID+"/enrollments", map[string]any{"list_id": imp["list_id"], "skip_active_elsewhere": false})
	if enrolled["enrolled"] != 3.0 {
		t.Fatalf("agent enrolled: %v", enrolled)
	}
	// Lead ids reach only leads on the project's lists: zed is only on the shared list.
	var zed, one string
	for _, m := range owner.must(200, "GET", base+"/lead-lists/"+sharedList+"/members", nil)["data"].([]any) {
		lead := m.(map[string]any)
		switch lead["email"] {
		case "zed@zed-io.example":
			zed = lead["id"].(string)
		case "one@one-io.example":
			one = lead["id"].(string)
		}
	}
	other := agent.must(201, "POST", base+"/sequences", map[string]any{"name": "By id", "steps": []map[string]any{{"subject": "Hi", "body": "Hi"}}})["id"].(string)
	byID := agent.must(200, "POST", base+"/sequences/"+other+"/enrollments", map[string]any{"lead_ids": []string{zed, one}, "skip_active_elsewhere": false})
	if byID["enrolled"] != 1.0 {
		t.Fatalf("enrolled by id: %v", byID)
	}
	agent.must(204, "DELETE", base+"/sequences/"+other, nil)
}
