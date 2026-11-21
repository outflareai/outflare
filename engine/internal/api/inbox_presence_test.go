package api_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/support"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Online and Away: a new chat goes to whoever is online, on to the next
// teammate when they don't answer, and carries on by email once the
// visitor has left.
func TestInboxPresence(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	e.runJobs(t)
	owner, _ := setupOwner(t, e.h)
	ctx := context.Background()
	ws := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Help desk", "test": true})["id"].(string)
	base := "/v1/workspaces/" + ws
	brand := brandOf(t, e, `SELECT outflare_first_brand($1)::text`, ws)
	pb := "/v1/brands/" + brand + "/inbox-presence"
	me := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	key := owner.must(201, "POST", base+"/support/chat-widgets", map[string]any{"allowed_origins": []string{"northpeak.example"}})["key"].(string)
	cb := "/chat/" + key

	// Nobody has used it: nothing is claimed, and a chat is nobody's.
	if p := owner.must(200, "GET", pb, nil); p["status"] != nil || len(p["online"].([]any)) != 0 {
		t.Fatalf("before: %v", p)
	}
	_, s := chatCall(t, e.h, "POST", cb+"/session", "", map[string]any{})
	ana := s["token"].(string)
	if v, ok := s["team_online"]; !ok || v != nil || s["waiting"] != nil {
		t.Fatalf("session before presence: %v", s)
	}
	chatCall(t, e.h, "POST", cb+"/messages", ana, map[string]any{"body": "Hello?"})
	first := owner.must(200, "GET", base+"/support/conversations", nil)["data"].([]any)[0].(map[string]any)
	if first["assignee"] != nil {
		t.Fatalf("assigned with nobody online: %v", first)
	}

	// Go online: only online or away, and only a change is an event.
	owner.must(400, "PUT", pb, map[string]any{"status": "busy"})
	for range 2 {
		if p := owner.must(200, "PUT", pb, map[string]any{"status": "online"}); p["status"] != "online" || len(p["online"].([]any)) != 1 {
			t.Fatalf("online: %v", p)
		}
	}
	if n := len(named(got(), "inbox.presence_changed")); n != 1 {
		t.Fatalf("%d presence events, want 1", n)
	}

	// A new chat goes to the person online.
	_, s = chatCall(t, e.h, "POST", cb+"/session", "", map[string]any{})
	li := s["token"].(string)
	if s["team_online"] != true {
		t.Fatalf("team online: %v", s)
	}
	chatCall(t, e.h, "POST", cb+"/messages", li, map[string]any{"body": "Do you ship to Lisbon?"})
	var conv string
	if err := e.pool.QueryRow(ctx, `SELECT id::text FROM support_conversations WHERE workspace_id = $1 AND assignee_id = $2`, ws, me).Scan(&conv); err != nil {
		t.Fatalf("not assigned to the person online: %v", err)
	}

	// Nobody answers: it goes to the next teammate online, once.
	acc := &api.Accounts{Pool: e.pool, PublicURL: "https://app.example.com", Mailer: e.outbox, Log: slog.Default()}
	bea, _, err := acc.CreateAccount(ctx, nil, httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil),
		api.NewAccount{Email: "bea@example.com", Name: "Bea", Password: "correct horse battery", WorkspaceName: "Bea's"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO memberships (workspace_id, user_id, role) VALUES ($1, $2, 'operator')`, ws, bea); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO inbox_presence (workspace_id, brand_id, user_id, status) VALUES ($1, $2, $3, 'online')`, ws, brand, bea); err != nil {
		t.Fatal(err)
	}
	pass := func() {
		t.Helper()
		rows, err := e.pool.Query(ctx, `SELECT payload FROM jobs WHERE kind = $1 AND payload->>'conversation_id' = $2 ORDER BY created_at`, support.JobChatUnanswered, conv)
		if err != nil {
			t.Fatal(err)
		}
		var payloads []json.RawMessage
		for rows.Next() {
			var p json.RawMessage
			if err := rows.Scan(&p); err != nil {
				t.Fatal(err)
			}
			payloads = append(payloads, p)
		}
		for _, p := range payloads {
			if err := support.ChatUnanswered(e.pool)(ctx, jobs.Job{Payload: p, WorkspaceID: &ws}); err != nil {
				t.Fatal(err)
			}
		}
	}
	assignee := func() string {
		t.Helper()
		return brandOf(t, e, `SELECT COALESCE(assignee_id::text, '') FROM support_conversations WHERE id = $1`, conv)
	}
	pass()
	if assignee() != bea {
		t.Fatalf("not passed on: %s", assignee())
	}
	pass() // both have had it: it stays where it is
	if assignee() != bea {
		t.Fatalf("passed on again: %s", assignee())
	}

	// Away: the visitor is told the team will answer by email, once the
	// brand has a mailbox to answer from, and can leave an address.
	owner.must(200, "PUT", pb, map[string]any{"status": "away"})
	if _, err := e.pool.Exec(ctx, `UPDATE inbox_presence SET status = 'away' WHERE user_id = $1`, bea); err != nil {
		t.Fatal(err)
	}
	if _, p := chatCall(t, e.h, "GET", cb+"/messages", li, nil); p["waiting"] != nil {
		t.Fatalf("waiting without a mailbox: %v", p)
	}
	if code, res := owner.raw("POST", base+"/mailboxes/import", "text/csv",
		"email,password,smtp_host,smtp_port,imap_host,imap_port\nhelp@northpeak.example,pw,smtp.northpeak.example,465,imap.northpeak.example,993\n"); code != 200 || res["created"] != 1.0 {
		t.Fatalf("import: %d %v", code, res)
	}
	box := owner.must(200, "GET", base+"/mailboxes", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	waitFor(t, owner, base+"/mailboxes/"+box, func(m map[string]any) bool { return m["status"] == "active" })
	owner.must(200, "PUT", base+"/support/mailboxes/"+box, nil)
	if _, p := chatCall(t, e.h, "GET", cb+"/messages", li, nil); p["waiting"] != "email_needed" {
		t.Fatalf("waiting: %v", p)
	}
	if code, _ := chatCall(t, e.h, "POST", cb+"/email", li, map[string]any{"email": "not an address"}); code != 400 {
		t.Fatalf("bad address: %d", code)
	}
	if code, _ := chatCall(t, e.h, "POST", cb+"/email", "", map[string]any{"email": "li@acme.example"}); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	if code, r := chatCall(t, e.h, "POST", cb+"/email", li, map[string]any{"email": "li@acme.example"}); code != 200 || r["saved"] != true {
		t.Fatalf("email: %d %v", code, r)
	}
	if _, p := chatCall(t, e.h, "GET", cb+"/messages", li, nil); p["waiting"] != "email" {
		t.Fatalf("waiting with an address: %v", p)
	}

	// While the visitor is there a reply only shows in the chat; once
	// they have gone it also goes to their email.
	cv := base + "/support/conversations/" + conv
	here := owner.must(201, "POST", cv+"/replies", map[string]any{"body": "Yes, we do."})
	if st := brandOf(t, e, `SELECT status || ':' || COALESCE(mailbox_id::text, '') FROM support_messages WHERE id = $1`, here["id"].(string)); st != "sent:" {
		t.Fatalf("reply while there: %s", st)
	}
	if _, p := chatCall(t, e.h, "GET", cb+"/messages", li, nil); p["waiting"] != nil {
		t.Fatalf("waiting after an answer: %v", p)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE support_conversations SET visitor_seen_at = now() - interval '10 minutes' WHERE id = $1`, conv); err != nil {
		t.Fatal(err)
	}
	gone := owner.must(201, "POST", cv+"/replies", map[string]any{"body": "It takes three days."})
	waitFor(t, owner, cv, func(m map[string]any) bool {
		for _, x := range m["messages"].([]any) {
			if x.(map[string]any)["id"] == gone["id"] && x.(map[string]any)["status"] == "sent" {
				return true
			}
		}
		return false
	})
	if to := brandOf(t, e, `SELECT to_email || ':' || mailbox_id::text FROM support_messages WHERE id = $1`, gone["id"].(string)); to != "li@acme.example:"+box {
		t.Fatalf("reply by email: %s", to)
	}
	// It still shows in the chat when they come back.
	if _, p := chatCall(t, e.h, "GET", cb+"/messages", li, nil); len(p["messages"].([]any)) != 3 {
		t.Fatalf("chat after the email: %v", p)
	}
	if len(named(got(), "support.chat_email_left")) != 1 || len(named(got(), "support.reply_sent")) != 1 {
		t.Fatalf("events: %v", got())
	}
}
