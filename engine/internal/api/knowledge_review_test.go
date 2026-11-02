package api_test

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The knowledge heals itself, with a person in the loop: items that
// disagree become a conflict to settle, a teammate's support reply to a
// question the knowledge couldn't answer becomes a suggested answer, and
// an agent can suggest what it found. Nothing is used until approved, and
// a rejected suggestion isn't made again.
func TestKnowledgeReview(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	e.runJobs(t)
	q := jobs.New(e.pool, slog.New(slog.DiscardHandler))
	q.PollInterval = 20 * time.Millisecond
	e.srv.RegisterJobs(q)
	run, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = q.Run(run, 2); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	owner, _ := setupOwner(t, e.h)
	ws := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Help desk", "test": true})["id"].(string)
	base := "/v1/workspaces/" + ws
	kb := base + "/knowledge"
	sb := kb + "/suggestions"
	cl := httptest.NewServer(fakeClaude{})
	defer cl.Close()
	e.integrations.Client.BaseURLs["claude"] = cl.URL
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})

	if l := owner.must(200, "GET", sb, nil); len(l["data"].([]any)) != 0 || l["counts"].(map[string]any)["answers"] != 0.0 {
		t.Fatalf("empty: %v", l)
	}

	// Two articles give different refund windows: answering notices.
	right := owner.must(201, "POST", kb+"/items", map[string]any{"title": "Refunds", "visibility": "public", "body": "Our refund window is 30 days from payment."})
	wrong := owner.must(201, "POST", kb+"/items", map[string]any{"title": "Billing FAQ", "visibility": "public", "body": "The refund window is 14 days."})
	owner.must(200, "POST", kb+"/ask", map[string]any{"question": "What is your refund window?", "audience": "customers"})
	owner.must(200, "POST", kb+"/ask", map[string]any{"question": "What is your refund window?", "audience": "customers"}) // once only
	l := owner.must(200, "GET", sb+"?kind=conflict", nil)
	if len(l["data"].([]any)) != 1 || l["counts"].(map[string]any)["conflicts"] != 1.0 {
		t.Fatalf("conflict: %v", l)
	}
	c := l["data"].([]any)[0].(map[string]any)
	if c["question"] != "They give different refund windows." || len(c["items"].([]any)) != 2 || c["sensitive"] != true || c["source"] != "answer" {
		t.Fatalf("conflict detail: %v", c)
	}
	// Settled by keeping the right one: the other stops being used.
	owner.must(400, "POST", sb+"/"+c["id"].(string)+"/approve", map[string]any{})
	owner.must(200, "POST", sb+"/"+c["id"].(string)+"/approve", map[string]any{"keep_item_id": right["id"]})
	if it := owner.must(200, "GET", kb+"/items/"+wrong["id"].(string), nil); it["status"] != "draft" {
		t.Fatalf("not unpublished: %v", it)
	}
	owner.must(409, "POST", sb+"/"+c["id"].(string)+"/reject", map[string]any{})

	// Support: the knowledge has no answer, a teammate answers; their reply
	// becomes a suggested answer, without the customer's details.
	if code, res := owner.raw("POST", base+"/mailboxes/import", "text/csv",
		"email,password,smtp_host,smtp_port,imap_host,imap_port\nhelp@northpeak.example,pw,smtp.northpeak.example,465,imap.northpeak.example,993\n"); code != 200 || res["created"] != 1.0 {
		t.Fatalf("import: %d %v", code, res)
	}
	box := owner.must(200, "GET", base+"/mailboxes", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	waitFor(t, owner, base+"/mailboxes/"+box, func(m map[string]any) bool { return m["status"] == "active" })
	owner.must(200, "PUT", base+"/support/mailboxes/"+box, nil)
	email := func(subject, body string) string {
		return base + "/support/conversations/" + owner.must(201, "POST", base+"/test/support-email", map[string]any{"mailbox_id": box,
			"from": "ana@acme.example", "name": "Ana Silva", "subject": subject, "body": body})["conversation_id"].(string)
	}
	csv := email("Export to CSV?", "Is there a way to get the MRR chart out as CSV?")
	if s := owner.must(200, "POST", csv+"/suggest-reply", nil); s["answered"] != false {
		t.Fatalf("csv answered: %v", s)
	}
	owner.must(201, "POST", csv+"/replies", map[string]any{"body": "Hi Ana, yes: open the chart, then More, Export CSV. Jack"})
	charged := email("Charged twice", "I was charged twice in March.")
	owner.must(200, "POST", charged+"/suggest-reply", nil)
	owner.must(201, "POST", charged+"/replies", map[string]any{"body": "Sorry Ana, I've refunded you the second charge."})
	var s map[string]any
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if l := owner.must(200, "GET", sb+"?kind=answer", nil)["data"].([]any); len(l) > 0 {
			s = l[0].(map[string]any)
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if s == nil || s["source"] != "support_reply" || s["title"] != "Can I export a chart to CSV?" || s["body"] != "Yes: open the chart, then More, Export CSV." ||
		s["visibility"] != "customers_ai" {
		t.Fatalf("from reply: %v", s)
	}
	time.Sleep(time.Second) // the refund reply's job: it suggests nothing
	if l := owner.must(200, "GET", sb+"?kind=answer", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("a reply for one customer was suggested: %v", l)
	}
	// Approved as a person edited it, it becomes knowledge the AI uses.
	a := owner.must(200, "POST", sb+"/"+s["id"].(string)+"/approve", map[string]any{"body": "Yes: open any chart, then More, Export CSV.", "visibility": "public"})
	item := owner.must(200, "GET", kb+"/items/"+a["item_id"].(string), nil)
	if a["status"] != "approved" || item["body"] != "Yes: open any chart, then More, Export CSV." || item["visibility"] != "public" || item["status"] != "published" {
		t.Fatalf("approved: %v %v", a, item)
	}
	if ans := owner.must(200, "POST", kb+"/ask", map[string]any{"question": "Can I export a chart?", "audience": "customers"}); ans["answered"] != true {
		t.Fatalf("not used: %v", ans)
	}

	// An agent suggests what it found on Slack; a person rejects it, and it
	// isn't suggested again.
	owner.must(400, "POST", sb, map[string]any{"question": "Do you have an office in Lisbon?", "body": "Yes.", "source": "rumour"})
	owner.must(400, "POST", sb, map[string]any{"question": "", "body": "Yes.", "source": "slack"})
	slack := owner.must(201, "POST", sb, map[string]any{"question": "Do you have an office in Lisbon?", "body": "Yes, on Rua Augusta.", "source": "slack",
		"source_ref": "#general", "source_quote": "we just opened the Lisbon office on Rua Augusta"})
	owner.must(409, "POST", sb, map[string]any{"question": "do you have an office in lisbon?", "body": "Yes.", "source": "slack"})
	owner.must(200, "POST", sb+"/"+slack["id"].(string)+"/reject", map[string]any{"reason": "It's a co-working desk, not an office."})
	owner.must(409, "POST", sb+"/"+slack["id"].(string)+"/approve", map[string]any{})
	owner.must(409, "POST", sb, map[string]any{"question": "Do you have an office in Lisbon?", "body": "Yes.", "source": "agent"})
	if l := owner.must(200, "GET", sb+"?status=all", nil)["data"].([]any); len(l) != 3 {
		t.Fatalf("all: %v", l)
	}
	owner.must(404, "POST", sb+"/00000000-0000-0000-0000-000000000000/approve", map[string]any{})
	owner.must(400, "GET", sb+"?status=maybe", nil)
	// With a brand picked, what is for every brand still shows.
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "handle": "repocket", "website": "https://repocket.com"})
	if l := owner.must(200, "GET", sb+"?status=all&brand=repocket", nil); len(l["data"].([]any)) != 3 {
		t.Fatalf("brand picked: %v", l)
	}
	for _, name := range []string{"knowledge.suggestion_created", "knowledge.suggestion_approved", "knowledge.suggestion_rejected"} {
		if len(named(got(), name)) == 0 {
			t.Errorf("no %s event", name)
		}
	}
}
