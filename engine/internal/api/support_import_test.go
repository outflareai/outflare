package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/connectors/intercom/intercomtest"
	"github.com/outflareai/outflare/engine/internal/track"
)

// TestSupportImportIntercom: connecting Intercom for a brand copies its
// conversations, with Fin's results, into the support inbox and keeps them
// in step; copied conversations are read-only except for notes.
func TestSupportImportIntercom(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	fake := intercomtest.New()
	defer fake.Close()
	e.integrations.Client.BaseURLs["intercom"] = fake.URL
	e.runCRMImports(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "website": "northpeak.example"})["id"].(string)
	sb := "/v1/brands/" + brand + "/support"
	imports := sb + "/imports"

	now := time.Now().Unix()
	fake.Put(intercomtest.Conversation("a", now-3600))
	// An open chat with a visitor who left no email, and no Fin.
	b := intercomtest.Conversation("b", now-1800)
	b["state"], b["priority"], b["ai_agent_participated"], b["ai_agent"], b["conversation_rating"] = "open", "none", false, nil, nil
	b["contacts"] = map[string]any{"type": "contact.list", "contacts": []any{map[string]any{"type": "contact", "id": "c2"}}}
	b["source"].(map[string]any)["author"] = map[string]any{"type": "lead", "id": "c2"}
	b["conversation_parts"] = map[string]any{"conversation_parts": []any{}}
	fake.Put(b)

	// Connected for the brand, it starts copying on its own.
	conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "intercom", "brand_id": brand,
		"credentials": map[string]any{"api_key": intercomtest.Token}})["id"].(string)
	list := owner.must(200, "GET", imports, nil)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["integration_id"] != conn || list[0].(map[string]any)["source"] != "intercom" {
		t.Fatalf("imports: %v", list)
	}
	id := list[0].(map[string]any)["id"].(string)
	imp := waitSupportImport(t, owner, imports+"/"+id, 2)
	if imp["status"] != "idle" || imp["synced_through"] == nil || imp["messages"] != 6.0 || imp["error"] != nil {
		t.Fatalf("import: %v", imp)
	}
	owner.must(409, "POST", imports, map[string]any{"integration_id": conn})
	owner.must(400, "POST", imports, map[string]any{"integration_id": "nope"})
	owner.must(404, "POST", imports, map[string]any{"integration_id": "00000000-0000-0000-0000-000000000000"})

	// The conversations, as Intercom has them.
	l := owner.must(200, "GET", sb+"/conversations?status=all&source=intercom", nil)
	if l["total"] != 2.0 {
		t.Fatalf("conversations: %v", l)
	}
	owner.must(400, "GET", sb+"/conversations?source=zendesk", nil)
	if n := owner.must(200, "GET", sb+"/conversations?status=all&source=outflare", nil)["total"]; n != 0.0 {
		t.Fatalf("outflare conversations: %v", n)
	}
	var convA, convB string
	for _, c := range l["data"].([]any) {
		c := c.(map[string]any)
		switch c["external_id"] {
		case "a":
			convA = c["id"].(string)
		case "b":
			convB = c["id"].(string)
		}
	}
	full := owner.must(200, "GET", sb+"/conversations/"+convA, nil)
	c := full["conversation"].(map[string]any)
	if c["source"] != "intercom" || c["via"] != "imported" || c["status"] != "closed" || c["channel"] != "chat" || c["external_channel"] != "conversation" ||
		c["priority"] != "high" || c["reopens"] != 1.0 || c["external_assignee"] != "Sam Lee" || c["external_team"] != "Support" ||
		c["contact"].(map[string]any)["email"] != "ana@acme.example" || c["contact"].(map[string]any)["name"] != "Ana Silva" ||
		c["tags"].([]any)[0] != "exports" || c["closed_at"] == nil || c["first_reply_at"] == nil || c["unread"] != false || c["last_from"] != "team" {
		t.Fatalf("conversation: %v", c)
	}
	if r := c["rating"].(map[string]any); r["score"] != 5.0 || r["remark"] != "Quick and clear" {
		t.Fatalf("rating: %v", r)
	}
	ai := c["ai"].(map[string]any)
	if ai["agent"] != "fin" || ai["resolution"] != "escalated" || ai["answer_type"] != "ai_answer" || ai["rating"] != 3.0 ||
		ai["sources"].([]any)[0].(map[string]any)["title"] != "Exporting reports" {
		t.Fatalf("fin: %v", ai)
	}
	msgs := full["messages"].([]any)
	want := []struct{ kind, author, by string }{{"in", "customer", ""}, {"out", "ai", "Fin"}, {"note", "agent", "Sam Lee"}, {"in", "customer", ""}, {"out", "agent", "Sam Lee"}}
	if len(msgs) != len(want) {
		t.Fatalf("messages: %v", msgs)
	}
	for i, w := range want {
		m := msgs[i].(map[string]any)
		by, _ := m["external_author"].(string)
		if m["kind"] != w.kind || m["author_type"] != w.author || by != w.by {
			t.Errorf("message %d: %v, want %v", i, m, w)
		}
	}
	// The visitor with no email is a person known by their Intercom id.
	cb := owner.must(200, "GET", sb+"/conversations/"+convB, nil)["conversation"].(map[string]any)
	if cb["record_id"] == nil || cb["ai"] != nil || cb["rating"] != nil || cb["priority"] != "" || cb["status"] != "open" {
		t.Fatalf("lead's conversation: %v", cb)
	}
	var identities int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM record_identities WHERE workspace_id = $1 AND namespace = 'intercom'`, ws).Scan(&identities); err != nil || identities != 2 {
		t.Fatalf("intercom identities: %d %v", identities, err)
	}
	// Nothing is due under an SLA for a conversation answered in Intercom.
	owner.must(200, "PUT", base+"/support/sla-policies/"+brand, map[string]any{"first_reply_minutes": 60, "resolution_minutes": 1440})
	if cb := owner.must(200, "GET", sb+"/conversations/"+convB, nil)["conversation"].(map[string]any); cb["sla"].(map[string]any)["reply_due_at"] != nil {
		t.Fatalf("SLA on a copied conversation: %v", cb["sla"])
	}

	// Read-only, except for notes.
	owner.must(409, "POST", sb+"/conversations/"+convB+"/replies", map[string]any{"body": "Hello"})
	owner.must(409, "PATCH", sb+"/conversations/"+convB, map[string]any{"status": "closed"})
	owner.must(409, "POST", sb+"/conversations/"+convB+"/suggest-reply", nil)
	owner.must(201, "POST", sb+"/conversations/"+convB+"/notes", map[string]any{"body": "Asked in Intercom too."})

	// A change in Intercom comes over on the next pass; what didn't change
	// isn't read again.
	gets := fake.Gets["b"]
	a := intercomtest.Conversation("a", now+60)
	parts := a["conversation_parts"].(map[string]any)
	parts["conversation_parts"] = append(parts["conversation_parts"].([]any), map[string]any{"type": "conversation_part", "id": "a-p6",
		"part_type": "comment", "body": "Thanks!", "created_at": now + 50, "author": map[string]any{"type": "user", "id": "c1"}})
	a["state"] = "open"
	fake.Put(a)
	owner.must(202, "POST", imports+"/"+id+"/sync", nil)
	imp = waitSupportImport(t, owner, imports+"/"+id, 2)
	if imp["messages"] != 7.0 {
		t.Fatalf("after sync: %v", imp)
	}
	if c := owner.must(200, "GET", sb+"/conversations/"+convA, nil); c["conversation"].(map[string]any)["status"] != "open" || len(c["messages"].([]any)) != 6 {
		t.Fatalf("updated conversation: %v", c["conversation"])
	}
	if fake.Gets["b"] != gets {
		t.Errorf("an unchanged conversation was read again: %d reads", fake.Gets["b"])
	}

	// Paused, it does nothing; resumed, it carries on.
	if p := owner.must(200, "PATCH", imports+"/"+id, map[string]any{"paused": true}); p["status"] != "paused" {
		t.Fatalf("paused: %v", p)
	}
	owner.must(400, "POST", imports+"/"+id+"/sync", nil)
	if _, err := e.pool.Exec(ctx, `UPDATE support_imports SET last_synced_at = now() - interval '1 hour' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if n, err := e.srv.QueueSupportImports(ctx); err != nil || n != 0 {
		t.Fatalf("a paused import was scheduled: %d %v", n, err)
	}
	owner.must(400, "PATCH", imports+"/"+id, map[string]any{})
	owner.must(200, "PATCH", imports+"/"+id, map[string]any{"paused": false})
	waitSupportImport(t, owner, imports+"/"+id, 2)
	// The schedule starts the next pass once it is due.
	if _, err := e.pool.Exec(ctx, `UPDATE support_imports SET last_synced_at = now() - interval '1 hour' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	searches := fake.Searches
	if n, err := e.srv.QueueSupportImports(ctx); err != nil || n != 1 {
		t.Fatalf("scheduled: %d %v", n, err)
	}
	waitSupportImport(t, owner, imports+"/"+id, 2)
	if fake.Searches == searches {
		t.Error("the scheduled pass didn't look for changes")
	}

	// Stopping keeps what was copied.
	owner.must(204, "DELETE", imports+"/"+id, nil)
	owner.must(404, "GET", imports+"/"+id, nil)
	if n := owner.must(200, "GET", sb+"/conversations?status=all&source=intercom", nil)["total"]; n != 2.0 {
		t.Fatalf("after stopping: %v", n)
	}
	// It can be started again by hand, for recent history only.
	again := owner.must(201, "POST", imports, map[string]any{"integration_id": conn, "history_from": time.Now().Add(-24 * time.Hour).Format(time.RFC3339)})
	if again["history_from"] == nil || again["status"] != "syncing" {
		t.Fatalf("started again: %v", again)
	}

	evs := got()
	if len(named(evs, "support_import.created")) < 2 || len(named(evs, "support_import.synced")) == 0 || len(named(evs, "support_import.deleted")) != 1 {
		t.Errorf("events: created %d, synced %d, deleted %d", len(named(evs, "support_import.created")), len(named(evs, "support_import.synced")), len(named(evs, "support_import.deleted")))
	}
}

// waitSupportImport polls an import until it has caught up with at least
// n conversations.
func waitSupportImport(t *testing.T, c *client, path string, n float64) map[string]any {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		v := c.must(200, "GET", path, nil)
		if v["status"] == "failed" {
			t.Fatalf("import failed: %v", v)
		}
		if v["status"] == "idle" && v["conversations"].(float64) >= n {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("the import didn't catch up: %v", v)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
