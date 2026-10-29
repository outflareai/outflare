package api_test

import (
	"strings"
	"testing"
)

func TestCompanyContextAndMemories(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws

	// A new workspace knows nothing and says what to ask for.
	c := owner.must(200, "GET", base+"/context", nil)
	ctx := c["context"].(map[string]any)
	if ctx["version"] != 1.0 || ctx["ask_for_missing"] != true || len(ctx["missing"].([]any)) != 4 || c["memory_count"] != 0.0 {
		t.Fatalf("empty context: %v", c)
	}
	if !strings.Contains(c["markdown"].(string), "Nothing saved about the company yet") {
		t.Fatalf("markdown: %q", c["markdown"])
	}

	// Filling it bumps the version; an unchanged save does not, and an edit
	// against an old version is refused.
	res := owner.must(200, "PATCH", base+"/context", map[string]any{
		"company": "Acme Analytics", "website": "https://acme-com.example/", "about": "Billing analytics for B2B SaaS.", "version": 1,
	})
	if res["version"] != 2.0 || res["website"] != "acme-com.example" || len(res["missing"].([]any)) != 2 {
		t.Fatalf("after first edit: %v", res)
	}
	owner.must(200, "PATCH", base+"/context", map[string]any{"company": "Acme Analytics", "version": 1})
	e := owner.must(409, "PATCH", base+"/context", map[string]any{"goals": "Stale", "version": 1})
	if e["error"].(map[string]any)["code"] != "context_changed" {
		t.Fatalf("stale edit: %v", e)
	}
	owner.must(400, "PATCH", base+"/context", map[string]any{"company": strings.Repeat("x", 201)})
	res = owner.must(200, "PATCH", base+"/context", map[string]any{"goals": "20 demos a month", "voice": "Plain and short."})
	if res["ask_for_missing"] != false || len(res["missing"].([]any)) != 0 {
		t.Fatalf("complete: %v", res)
	}

	// Dismissing stops agents asking, even with fields still empty.
	owner.must(200, "PATCH", base+"/context", map[string]any{"voice": ""})
	if owner.must(200, "GET", base+"/context", nil)["context"].(map[string]any)["ask_for_missing"] != true {
		t.Fatal("an emptied field should be asked for again")
	}
	if owner.must(200, "PATCH", base+"/context", map[string]any{"onboarding_dismissed": true})["ask_for_missing"] != false {
		t.Fatal("dismissed")
	}

	// Memories: saved once even when repeated, pinned first, searchable.
	bid := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Fintech"})["id"].(string)
	owner.must(400, "POST", base+"/memories", map[string]any{"kind": "fact"})
	owner.must(400, "POST", base+"/memories", map[string]any{"text": "x", "kind": "secret"})
	owner.must(400, "POST", base+"/memories", map[string]any{"text": strings.Repeat("x", 501)})
	m1 := owner.must(201, "POST", base+"/memories", map[string]any{"text": "Never mention pricing in a first email", "kind": "preference"})
	again := owner.must(201, "POST", base+"/memories", map[string]any{"text": "never mention  pricing in a first email", "kind": "preference", "pinned": true})
	if again["id"] != m1["id"] || again["pinned"] != true {
		t.Fatalf("duplicate saved twice: %v %v", m1, again)
	}
	owner.must(201, "POST", base+"/memories", map[string]any{"text": "Only Series A and later", "kind": "decision", "brand_id": "fintech"})
	owner.must(400, "POST", base+"/memories", map[string]any{"text": "Elsewhere", "brand_id": "00000000-0000-0000-0000-000000000000"})
	list := owner.must(200, "GET", base+"/memories", nil)
	if list["total"] != 2.0 || list["data"].([]any)[0].(map[string]any)["id"] != m1["id"] {
		t.Fatalf("list: %v", list)
	}
	if n := owner.must(200, "GET", base+"/memories?q=series", nil)["total"]; n != 1.0 {
		t.Fatalf("search: %v", n)
	}
	if n := owner.must(200, "GET", base+"/memories?brand=none", nil)["total"]; n != 1.0 {
		t.Fatalf("workspace-wide: %v", n)
	}
	page := owner.must(200, "GET", base+"/memories?limit=1", nil)
	if page["next_cursor"] == nil {
		t.Fatalf("paging: %v", page)
	}
	owner.must(400, "GET", base+"/memories?kind=secret", nil)
	c = owner.must(200, "GET", base+"/context", nil)
	if c["memory_count"] != 1.0 || !strings.Contains(c["markdown"].(string), "(preference) Never mention pricing") || c["projects"] != nil {
		t.Fatalf("context with memories: %v", c)
	}

	id := m1["id"].(string)
	if owner.must(200, "PATCH", base+"/memories/"+id, map[string]any{"text": "Never mention pricing before a call"})["text"] != "Never mention pricing before a call" {
		t.Fatal("edit")
	}

	// A token without context.manage reads but cannot write; a brand
	// token sees workspace-wide memories and its own brand's.
	reader := &client{t: t, h: h, bearer: owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "r", "scopes": []string{"workspace.read"}})["token"].(string)}
	reader.must(200, "GET", base+"/context", nil)
	reader.must(403, "POST", base+"/memories", map[string]any{"text": "Nope"})
	reader.must(403, "PATCH", base+"/context", map[string]any{"company": "Nope"})
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Agencies"})
	owner.must(201, "POST", base+"/memories", map[string]any{"text": "Agencies hate long emails", "brand_id": "agencies"})
	owner.must(400, "POST", base+"/api-tokens", map[string]any{"name": "p", "scopes": []string{"workspace.read"}, "project_id": bid})
	btok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "b", "scopes": []string{"workspace.read", "context.manage"}, "brand": "fintech"})
	agent := &client{t: t, h: h, bearer: btok["token"].(string)}
	if n := agent.must(200, "GET", base+"/memories", nil)["total"]; n != 2.0 {
		t.Fatalf("brand token sees %v memories", n)
	}
	mine := agent.must(201, "POST", base+"/memories", map[string]any{"text": "CFOs reply on Tuesdays"})
	if mine["brand_id"] != bid || mine["project_id"] != nil {
		t.Fatalf("brand token saved outside its brand: %v", mine)
	}
	agent.must(403, "DELETE", base+"/memories/"+id, nil)
	agent.must(403, "PATCH", base+"/context", map[string]any{"company": "Mine"})

	owner.must(204, "DELETE", base+"/memories/"+id, nil)
	owner.must(404, "DELETE", base+"/memories/"+id, nil)

	actions := map[string]bool{}
	for _, ev := range owner.must(200, "GET", base+"/audit-events", nil)["data"].([]any) {
		actions[ev.(map[string]any)["action"].(string)] = true
	}
	for _, a := range []string{"context.updated", "memory.created", "memory.updated", "memory.deleted"} {
		if !actions[a] {
			t.Errorf("no %s audit event", a)
		}
	}
}

func TestWorkspaceStatus(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws

	st := owner.must(200, "GET", base+"/status", nil)
	if st["can_send"] != false {
		t.Fatalf("can_send: %v", st)
	}
	codes := map[string]map[string]any{}
	for _, b := range st["blockers"].([]any) {
		b := b.(map[string]any)
		codes[b["code"].(string)] = b
	}
	if codes["no_mailboxes"] == nil || codes["context_missing"] == nil {
		t.Fatalf("blockers: %v", st["blockers"])
	}
	if st["blockers"].([]any)[0].(map[string]any)["code"] != "no_mailboxes" {
		t.Fatal("worst first")
	}
	fix := codes["context_missing"]["fix"].(map[string]any)
	if fix["tool"] != "update_context" || fix["who"] != "agent" || !strings.HasSuffix(fix["page"].(string), "/company") {
		t.Fatalf("fix: %v", fix)
	}
	if !strings.Contains(st["summary"].(string), "no mailboxes connected") {
		t.Fatalf("summary: %v", st["summary"])
	}
	steps := st["setup"].([]any)
	if len(steps) != 6 || steps[0].(map[string]any)["done"] != false {
		t.Fatalf("setup: %v", steps)
	}

	// Filling the context clears its blocker and its step.
	owner.must(200, "PATCH", base+"/context", map[string]any{"company": "Acme", "about": "x", "goals": "y", "voice": "z"})
	st = owner.must(200, "GET", base+"/status", nil)
	for _, b := range st["blockers"].([]any) {
		if b.(map[string]any)["code"] == "context_missing" {
			t.Fatal("context_missing after filling it")
		}
	}
	if st["setup"].([]any)[0].(map[string]any)["done"] != true {
		t.Fatal("context step")
	}
}
