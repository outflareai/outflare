package api_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// answerLearnReplies is the fake model that learns from past replies: the
// export question asked twice is one answer; the refund for one customer
// teaches nothing.
func answerLearnReplies(w http.ResponseWriter, body string) {
	var pairs []int
	for n := 1; n <= 20; n++ {
		i := strings.Index(body, `pair n=\"`+strconv.Itoa(n)+`\"`)
		if i < 0 {
			break
		}
		end := strings.Index(body[i:], `/pair`)
		if end > 0 && strings.Contains(strings.ToLower(body[i:i+end]), "export") {
			pairs = append(pairs, n)
		}
	}
	answers := []any{}
	if len(pairs) > 0 {
		answers = append(answers, map[string]any{"question": "How do I export a chart to CSV?", "title": "Export a chart to CSV",
			"answer": "Open the chart, then More, Export CSV.", "sensitive": false, "pairs": pairs})
	}
	answerJSON(w, map[string]any{"answers": answers})
}

// Auto email replies wait the brand's hold so a teammate can stop them;
// hand-off topics tag conversations and give hand-offs to the right
// person and team; and past replies become suggested answers.
func TestSupportAssistantHoldTopicsAndLearning(t *testing.T) {
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
	ctx := context.Background()
	owner, _ := setupOwner(t, e.h)
	ws := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Northpeak", "test": true})["id"].(string)
	base := "/v1/workspaces/" + ws
	sb := base + "/support"
	if code, res := owner.raw("POST", base+"/mailboxes/import", "text/csv",
		"email,password,smtp_host,smtp_port,imap_host,imap_port\nhelp@northpeak.example,pw,smtp.northpeak.example,465,imap.northpeak.example,993\n"); code != 200 || res["created"] != 1.0 {
		t.Fatalf("import: %d %v", code, res)
	}
	box := owner.must(200, "GET", base+"/mailboxes", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	waitFor(t, owner, base+"/mailboxes/"+box, func(m map[string]any) bool { return m["status"] == "active" })
	owner.must(200, "PUT", sb+"/mailboxes/"+box, nil)
	owner.must(201, "POST", base+"/knowledge/items", map[string]any{"title": "Export a chart", "visibility": "public",
		"body": "To export a chart, open the chart, then More, Export CSV."})
	cl := httptest.NewServer(fakeClaude{})
	defer cl.Close()
	e.integrations.Client.BaseURLs["claude"] = cl.URL
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})
	e.srv.SetSupportAuto(func(_ context.Context, id string) bool { return id == ws })
	write := func(subject, body string) string {
		return owner.must(201, "POST", base+"/test/support-email", map[string]any{"mailbox_id": box, "from": "ana" + strconv.FormatInt(time.Now().UnixNano(), 36) + "@acme.example",
			"subject": subject, "body": body})["conversation_id"].(string)
	}
	latest := func(conv string) map[string]any {
		t.Helper()
		r := waitFor(t, owner, sb+"/conversations/"+conv+"/assistant", func(m map[string]any) bool { return m["run"] != nil })
		return r["run"].(map[string]any)
	}
	convOf := func(conv string) map[string]any {
		return owner.must(200, "GET", sb+"/conversations/"+conv, nil)["conversation"].(map[string]any)
	}
	messages := func(conv string) []any {
		return owner.must(200, "GET", sb+"/conversations/"+conv, nil)["messages"].([]any)
	}
	// due ends a held reply's hold now.
	due := func(run string) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `UPDATE support_assistant_runs SET send_at = now() WHERE id = $1`, run); err != nil {
			t.Fatal(err)
		}
		if _, err := e.pool.Exec(ctx, `UPDATE jobs SET run_at = now() WHERE kind = 'support.assistant_send' AND payload->>'run_id' = $1`, run); err != nil {
			t.Fatal(err)
		}
	}

	// The hold: 0 to 60 minutes, 5 by default.
	owner.must(400, "PUT", sb+"/assistant-settings/default", map[string]any{"email_mode": "auto", "email_hold_minutes": 61})
	if s := owner.must(200, "PUT", sb+"/assistant-settings/default", map[string]any{"email_mode": "auto", "name": "Northpeak help",
		"find_on_website": false}); s["email_hold_minutes"] != 5.0 {
		t.Fatalf("default hold: %v", s)
	}
	owner.must(200, "PUT", sb+"/assistant-settings/default", map[string]any{"email_mode": "auto", "name": "Northpeak help",
		"find_on_website": false, "email_hold_minutes": 10})

	// An Auto email answer waits as a draft, then goes on its own.
	conv := write("Exporting", "How do I export a chart?")
	r := latest(conv)
	if r["action"] != "answer" || r["outcome"] != "drafted" || r["send_at"] == nil {
		t.Fatalf("held: %v", r)
	}
	if at, _ := time.Parse(time.RFC3339Nano, r["send_at"].(string)); time.Until(at) < 9*time.Minute || time.Until(at) > 11*time.Minute {
		t.Fatalf("send_at: %v", r["send_at"])
	}
	if n := len(messages(conv)); n != 1 {
		t.Fatalf("sent before its hold: %d", n)
	}
	due(r["id"].(string))
	waitFor(t, owner, sb+"/conversations/"+conv+"/assistant", func(m map[string]any) bool { return m["run"].(map[string]any)["outcome"] == "sent" })
	if l := messages(conv); len(l) != 2 || !strings.HasPrefix(l[1].(map[string]any)["body"].(string), "Open the chart, then More, Export CSV.") {
		t.Fatalf("sent: %v", l)
	}

	// Stopped: it stays a draft and never goes.
	conv = write("Exporting", "How do I export a chart please?")
	r = latest(conv)
	stopped := owner.must(200, "POST", sb+"/assistant-runs/"+r["id"].(string)+"/stop", nil)
	if stopped["send_at"] != nil || stopped["outcome"] != "drafted" {
		t.Fatalf("stopped: %v", stopped)
	}
	owner.must(409, "POST", sb+"/assistant-runs/"+r["id"].(string)+"/stop", nil)
	owner.must(404, "POST", sb+"/assistant-runs/00000000-0000-0000-0000-000000000000/stop", nil)
	due(r["id"].(string))
	if _, err := e.pool.Exec(ctx, `UPDATE support_assistant_runs SET send_at = NULL WHERE id = $1`, r["id"]); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if n := len(messages(conv)); n != 1 {
		t.Fatalf("stopped reply went: %d", n)
	}

	// A teammate answered first: the held reply is out of date.
	conv = write("Exporting", "How do I export a chart today?")
	r = latest(conv)
	owner.must(201, "POST", sb+"/conversations/"+conv+"/replies", map[string]any{"body": "Open the chart, then More, Export CSV. Ana"})
	due(r["id"].(string))
	waitFor(t, owner, sb+"/conversations/"+conv+"/assistant", func(m map[string]any) bool { return m["run"].(map[string]any)["outcome"] == "discarded" })
	if l := messages(conv); len(l) != 2 {
		t.Fatalf("both went: %v", l)
	}

	// Hand-off topics.
	var me string
	if err := e.pool.QueryRow(ctx, `SELECT user_id::text FROM memberships WHERE workspace_id = $1 LIMIT 1`, ws).Scan(&me); err != nil {
		t.Fatal(err)
	}
	team := owner.must(201, "POST", base+"/teams", map[string]any{"name": "Finance"})["id"].(string)
	tb := sb + "/handoff-topics"
	owner.must(400, "POST", tb, map[string]any{"name": ""})
	owner.must(400, "POST", tb, map[string]any{"name": "Billing", "assignee_id": "00000000-0000-0000-0000-000000000000"})
	owner.must(400, "POST", tb, map[string]any{"name": "Billing", "team_id": "00000000-0000-0000-0000-000000000000"})
	topic := owner.must(201, "POST", tb, map[string]any{"name": "Billing", "description": "Invoices, payments and plans", "assignee_id": me, "team_id": team})
	if topic["assignee"].(map[string]any)["id"] != me || topic["team"].(map[string]any)["name"] != "Finance" || topic["always_handoff"] != false {
		t.Fatalf("topic: %v", topic)
	}
	owner.must(400, "POST", tb, map[string]any{"name": "billing"})
	owner.must(201, "POST", tb, map[string]any{"name": "Product"})
	if l := owner.must(200, "GET", tb, nil)["data"].([]any); len(l) != 2 {
		t.Fatalf("topics: %v", l)
	}

	// A hand-off on a topic goes to its person and team.
	conv = write("Invoice", "Where is my invoice for March?")
	r = latest(conv)
	c := convOf(conv)
	if r["action"] != "handoff" || r["checks"].(map[string]any)["topic"] != "Billing" || c["topic"].(map[string]any)["name"] != "Billing" ||
		c["assignee"].(map[string]any)["id"] != me || c["team"].(map[string]any)["id"] != team {
		t.Fatalf("handoff to topic: %v %v", r, c)
	}
	if l := owner.must(200, "GET", sb+"/conversations?topic_id="+topic["id"].(string), nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("filter by topic: %v", l)
	}
	// So does one stopped before the AI answers (a request for a person).
	conv = write("Invoice", "My invoice is wrong, I want to talk to a real person")
	r = latest(conv)
	if c := convOf(conv); r["reason"] != "asked_for_person" || c["assignee"] == nil || c["topic"] == nil {
		t.Fatalf("person asked, with topic: %v %v", r, c)
	}
	// A topic the brand always hands to people is never answered.
	owner.must(200, "PATCH", tb+"/"+topic["id"].(string), map[string]any{"always_handoff": true, "assignee_id": nil})
	conv = write("Invoice", "How do I export my invoice?")
	r = latest(conv)
	if c := convOf(conv); r["action"] != "handoff" || r["reason"] != "topic" || c["assignee"] != nil ||
		c["team"].(map[string]any)["id"] != team {
		t.Fatalf("always handoff: %v %v", r, c)
	}
	// An answer is tagged, not assigned.
	conv = write("Export", "How do I export a chart again?")
	r = latest(conv)
	if c := convOf(conv); r["action"] != "answer" || c["topic"] != nil || c["assignee"] != nil {
		t.Fatalf("answer: %v %v", r, c)
	}
	owner.must(204, "DELETE", tb+"/"+topic["id"].(string), nil)
	owner.must(404, "DELETE", tb+"/"+topic["id"].(string), nil)

	// Learning from past replies: closed conversations a person answered.
	ib := base + "/knowledge/reply-imports"
	owner.must(400, "POST", ib, map[string]any{"days": 0})
	for i, q := range []string{"How can I export a chart?", "Can I export my data to CSV?", "Please refund my last payment"} {
		conv := write("Question "+strconv.Itoa(i), q)
		answer := "Open the chart, then More, Export CSV."
		if strings.Contains(q, "refund") {
			answer = "I've refunded you, it should arrive this week."
		}
		owner.must(201, "POST", sb+"/conversations/"+conv+"/replies", map[string]any{"body": answer, "status": "closed"})
		owner.must(200, "PATCH", sb+"/conversations/"+conv, map[string]any{"status": "closed"})
	}
	imp := owner.must(202, "POST", ib, map[string]any{})
	if imp["days"] != 90.0 || imp["status"] != "queued" {
		t.Fatalf("started: %v", imp)
	}
	d := waitFor(t, owner, ib+"/"+imp["id"].(string), func(m map[string]any) bool { return m["status"] == "done" || m["status"] == "failed" })
	if d["status"] != "done" || d["conversations_read"].(float64) < 3 || d["suggestions_filed"] != 1.0 {
		t.Fatalf("learned: %v", d)
	}
	found := false
	for _, sg := range owner.must(200, "GET", base+"/knowledge/suggestions", nil)["data"].([]any) {
		if sg := sg.(map[string]any); sg["title"] == "Export a chart to CSV" && sg["source"] == "support_reply" {
			found = true
		}
	}
	if !found {
		t.Fatal("no suggestion from past replies")
	}
	if l := owner.must(200, "GET", ib, nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("runs: %v", l)
	}

	for _, name := range []string{"support.assistant_stopped", "support.topic_created", "support.topic_updated", "support.topic_deleted",
		"knowledge.reply_learning_started", "knowledge.replies_learned"} {
		if len(named(got(), name)) == 0 {
			t.Errorf("no %s event", name)
		}
	}
}
