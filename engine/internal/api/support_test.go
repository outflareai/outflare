package api_test

import (
	"context"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestSupportInbox(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	e.runJobs(t)
	owner, live := setupOwner(t, e.h)
	ctx := context.Background()
	ws := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Help desk", "test": true})["id"].(string)
	base := "/v1/workspaces/" + ws
	if code, res := owner.raw("POST", base+"/mailboxes/import", "text/csv",
		"email,password,smtp_host,smtp_port,imap_host,imap_port\nhelp@northpeak.example,pw,smtp.northpeak.example,465,imap.northpeak.example,993\n"); code != 200 || res["created"] != 1.0 {
		t.Fatalf("import: %d %v", code, res)
	}
	box := owner.must(200, "GET", base+"/mailboxes", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	waitFor(t, owner, base+"/mailboxes/"+box, func(m map[string]any) bool { return m["status"] == "active" })

	// Nothing yet, and mail can't be simulated to a mailbox that isn't
	// for support.
	if l := owner.must(200, "GET", base+"/support/conversations", nil); l["total"] != 0.0 || len(owner.must(200, "GET", base+"/support/mailboxes", nil)["data"].([]any)) != 0 {
		t.Fatalf("empty: %v", l)
	}
	email := map[string]any{"mailbox_id": box, "from": "ana@acme.example", "name": "Ana Silva", "subject": "Refund for my order",
		"body": "Hi, I was charged twice this month. Can I get a refund?"}
	owner.must(404, "POST", base+"/test/support-email", email)
	owner.must(409, "POST", "/v1/workspaces/"+live+"/test/support-email", email)

	// The mailbox answers support from now on.
	owner.must(404, "PUT", base+"/support/mailboxes/00000000-0000-0000-0000-000000000000", nil)
	if m := owner.must(200, "PUT", base+"/support/mailboxes/"+box, nil); m["email"] != "help@northpeak.example" {
		t.Fatalf("support mailbox: %v", m)
	}
	owner.must(200, "PUT", base+"/support/mailboxes/"+box, nil) // again: no change
	if l := owner.must(200, "GET", base+"/support/mailboxes", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("support mailboxes: %v", l)
	}

	// A customer writes in: a conversation starts, with their record.
	first := owner.must(201, "POST", base+"/test/support-email", email)
	conv := first["conversation_id"].(string)
	l := owner.must(200, "GET", base+"/support/conversations", nil)
	c := l["data"].([]any)[0].(map[string]any)
	if l["total"] != 1.0 || c["status"] != "open" || c["subject"] != "Refund for my order" || c["unread"] != true || c["record_id"] == nil ||
		c["contact"].(map[string]any)["email"] != "ana@acme.example" || c["contact"].(map[string]any)["name"] != "Ana Silva" ||
		c["mailbox_email"] != "help@northpeak.example" || c["last_from"] != "customer" || c["messages"] != 1.0 {
		t.Fatalf("conversation: %v", l)
	}
	if counts := l["counts"].(map[string]any); counts["open"] != 1.0 || counts["unassigned"] != 1.0 || counts["mine"] != 0.0 {
		t.Fatalf("counts: %v", counts)
	}
	// It is not in the sales inbox.
	if n := owner.must(200, "GET", base+"/inbox", nil)["data"].([]any); len(n) != 0 {
		t.Fatalf("sales inbox: %v", n)
	}

	// Opening it shows the email and marks it read.
	cb := base + "/support/conversations/" + conv
	full := owner.must(200, "GET", cb, nil)
	msgs := full["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["kind"] != "in" || msgs[0].(map[string]any)["from_email"] != "ana@acme.example" {
		t.Fatalf("messages: %v", full)
	}
	if owner.must(200, "GET", cb, nil)["conversation"].(map[string]any)["unread"] != false {
		t.Fatal("still unread")
	}
	owner.must(404, "GET", base+"/support/conversations/00000000-0000-0000-0000-000000000000", nil)
	owner.must(404, "GET", base+"/support/conversations/nope", nil)

	// A note for the team, then a reply.
	owner.must(400, "POST", cb+"/notes", map[string]any{"body": " "})
	owner.must(201, "POST", cb+"/notes", map[string]any{"body": "Checked Stripe: two charges on the 3rd."})
	owner.must(400, "POST", cb+"/replies", map[string]any{"body": "Sorry", "status": "snoozed"})
	reply := owner.must(201, "POST", cb+"/replies", map[string]any{"body": "Sorry about that, Ana. I've refunded the second charge."})
	sent := waitFor(t, owner, cb, func(m map[string]any) bool {
		for _, x := range m["messages"].([]any) {
			if x.(map[string]any)["id"] == reply["id"] && x.(map[string]any)["status"] == "sent" {
				return true
			}
		}
		return false
	})
	cv := sent["conversation"].(map[string]any)
	if cv["status"] != "pending" || cv["first_reply_at"] == nil || cv["last_from"] != "team" || len(sent["messages"].([]any)) != 3 {
		t.Fatalf("after reply: %v", sent)
	}
	out := sent["messages"].([]any)[2].(map[string]any)
	if out["kind"] != "out" || out["to_email"] != "ana@acme.example" || out["from_email"] != "help@northpeak.example" || out["subject"] != "Re: Refund for my order" || out["author"] == nil {
		t.Fatalf("reply: %v", out)
	}

	// Their answer threads into the same conversation and opens it again.
	var replyID string
	if err := e.pool.QueryRow(ctx, `SELECT message_id FROM support_messages WHERE id = $1`, reply["id"]).Scan(&replyID); err != nil {
		t.Fatal(err)
	}
	again := owner.must(201, "POST", base+"/test/support-email", map[string]any{"mailbox_id": box, "from": "ana@acme.example",
		"subject": "Re: Refund for my order", "body": "Thanks, got it!", "in_reply_to": replyID})
	if again["conversation_id"] != conv {
		t.Fatalf("threaded: %v", again)
	}
	if c := owner.must(200, "GET", cb, nil)["conversation"].(map[string]any); c["status"] != "open" || c["messages"] != 3.0 {
		t.Fatalf("reopened: %v", c)
	}
	// Someone else starts their own.
	other := owner.must(201, "POST", base+"/test/support-email", map[string]any{"mailbox_id": box, "from": "li@globex.example",
		"subject": "How do I export my data?", "body": "Where do I find the export?"})
	if other["conversation_id"] == conv {
		t.Fatal("a different person joined Ana's conversation")
	}

	// Assign, tag and close.
	var me string
	if err := e.pool.QueryRow(ctx, `SELECT user_id::text FROM memberships WHERE workspace_id = $1`, ws).Scan(&me); err != nil {
		t.Fatal(err)
	}
	owner.must(400, "PATCH", cb, map[string]any{"assignee_id": "00000000-0000-0000-0000-000000000000"})
	owner.must(400, "PATCH", cb, map[string]any{"status": "archived"})
	if c := owner.must(200, "PATCH", cb, map[string]any{"assignee_id": me, "tags": []string{"Billing", "billing", "refund"}}); c["assignee"] == nil || len(c["tags"].([]any)) != 2 {
		t.Fatalf("assigned: %v", c)
	}
	if l := owner.must(200, "GET", base+"/support/conversations?assignee=me", nil); l["total"] != 1.0 || l["counts"].(map[string]any)["mine"] != 1.0 {
		t.Fatalf("mine: %v", l)
	}
	if l := owner.must(200, "GET", base+"/support/conversations?assignee=none", nil); l["total"] != 1.0 {
		t.Fatalf("unassigned: %v", l)
	}
	if l := owner.must(200, "GET", base+"/support/conversations?tag=refund", nil); l["total"] != 1.0 {
		t.Fatalf("tag: %v", l)
	}
	if l := owner.must(200, "GET", base+"/support/conversations?q=export", nil); l["total"] != 1.0 {
		t.Fatalf("search: %v", l)
	}
	owner.must(400, "GET", base+"/support/conversations?status=snoozed", nil)
	if c := owner.must(200, "PATCH", cb, map[string]any{"status": "closed"}); c["status"] != "closed" || c["closed_at"] == nil {
		t.Fatalf("closed: %v", c)
	}
	if l := owner.must(200, "GET", base+"/support/conversations?status=open", nil); l["total"] != 1.0 {
		t.Fatalf("open: %v", l)
	}

	// Without an AI key a suggested reply says so, and still finds what's
	// written.
	sug := owner.must(200, "POST", cb+"/suggest-reply", nil)
	if sug["ai"].(map[string]any)["problem"] == nil || sug["audience"] != "customers" {
		t.Fatalf("suggest: %v", sug)
	}

	// With a brand picked, conversations of no brand (this mailbox is
	// shared by every brand) still show.
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "handle": "repocket", "website": "https://repocket.com"})
	if l := owner.must(200, "GET", base+"/support/conversations?status=all&brand=repocket", nil); l["total"] != 2.0 {
		t.Fatalf("brand picked: %v", l)
	}

	stats := owner.must(200, "GET", base+"/support/stats", nil)
	if stats["conversations"] != 2.0 || stats["replied"] != 1.0 || stats["closed"] != 1.0 || stats["median_first_reply_seconds"] == nil {
		t.Fatalf("stats: %v", stats)
	}

	// A mailbox with conversations can go back to sales; replies then say
	// why they can't be sent.
	owner.must(204, "DELETE", base+"/support/mailboxes/"+box, nil)
	if code, res := owner.do("POST", cb+"/replies", map[string]any{"body": "One more thing"}); code != 409 || res["error"].(map[string]any)["code"] != "blocked" {
		t.Fatalf("reply without a support mailbox: %d %v", code, res)
	}

	for _, name := range []string{"support.mailbox_added", "support.conversation_started", "support.message_received", "support.replied",
		"support.reply_sent", "support.note_added", "support.conversation_assigned", "support.conversation_closed",
		"support.reply_suggested", "support.mailbox_removed"} {
		if len(named(got(), name)) == 0 {
			t.Errorf("no %s event", name)
		}
	}
}
