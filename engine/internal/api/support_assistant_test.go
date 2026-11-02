package api_test

import (
	"context"
	"encoding/json"
	"io"
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

// answerSupportAssistant is the fake model for the support assistant: it
// reads what the customer last wrote.
func answerSupportAssistant(w http.ResponseWriter, body string) {
	msg := body
	for _, tag := range []string{`\u003ccustomer\u003e`, "<customer>"} {
		if i := strings.LastIndex(body, tag); i >= 0 {
			msg = body[i:]
			break
		}
	}
	for _, tag := range []string{`\u003c/customer\u003e`, "</customer>"} {
		if i := strings.Index(msg, tag); i >= 0 {
			msg = msg[:i]
		}
	}
	rewrite := strings.Contains(body, "It broke these rules")
	switch {
	case strings.Contains(msg, "price"):
		answerJSON(w, map[string]any{"action": "answer", "reply": "Exports are included, and the plan costs $99 a month.", "cited": []int{1}, "reason": ""})
	case strings.Contains(msg, "sloppy") && !rewrite:
		answerJSON(w, map[string]any{"action": "answer", "reply": "Great question! Open the chart — then More, Export CSV. I hope this helps.", "cited": []int{1}, "reason": ""})
	case strings.Contains(msg, "which chart"):
		answerJSON(w, map[string]any{"action": "clarify", "reply": "Which chart do you want to export?", "cited": []int{}, "reason": ""})
	case strings.Contains(msg, "export"):
		answerJSON(w, map[string]any{"action": "answer", "reply": "Open the chart, then More, Export CSV.", "cited": []int{1}, "reason": ""})
	default:
		answerJSON(w, map[string]any{"action": "handoff", "reply": "", "cited": []int{}, "reason": "not_in_knowledge"})
	}
}

// The support assistant drafts short answers from approved knowledge for a
// person to send, asks one question back, or hands the conversation to a
// person; code stops it before the AI on sensitive topics, and its checks
// stop a reply that breaks the style rules or says what the articles don't.
func TestSupportAssistant(t *testing.T) {
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
	write := func(subject, body string) string {
		return owner.must(201, "POST", base+"/test/support-email", map[string]any{"mailbox_id": box, "from": "ana" + strconv.FormatInt(time.Now().UnixNano(), 36) + "@acme.example",
			"subject": subject, "body": body})["conversation_id"].(string)
	}
	latest := func(conv string) map[string]any {
		t.Helper()
		r := waitFor(t, owner, sb+"/conversations/"+conv+"/assistant", func(m map[string]any) bool { return m["run"] != nil })
		return r["run"].(map[string]any)
	}

	// Off by default: a message runs nothing.
	if l := owner.must(200, "GET", sb+"/assistant-settings", nil); len(l["data"].([]any)) != 0 || l["auto_available"] != false {
		t.Fatalf("settings: %v", l)
	}
	off := write("Export", "How do I export a chart?")
	time.Sleep(200 * time.Millisecond)
	if r := owner.must(200, "GET", sb+"/conversations/"+off+"/assistant", nil); r["run"] != nil {
		t.Fatalf("ran while off: %v", r)
	}
	// Without an AI key, asking for a draft says so.
	if res := owner.must(409, "POST", sb+"/conversations/"+off+"/assistant", nil); res["error"].(map[string]any)["code"] != "connector_needed" {
		t.Fatalf("no key: %v", res)
	}
	cl := httptest.NewServer(fakeClaude{})
	defer cl.Close()
	e.integrations.Client.BaseURLs["claude"] = cl.URL
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})

	// Sending on its own is Cloud's; drafting is here.
	owner.must(403, "PUT", sb+"/assistant-settings/default", map[string]any{"email_mode": "auto"})
	owner.must(400, "PUT", sb+"/assistant-settings/default", map[string]any{"email_mode": "sometimes"})
	owner.must(404, "PUT", sb+"/assistant-settings/nobrand", map[string]any{"email_mode": "draft"})
	if s := owner.must(200, "PUT", sb+"/assistant-settings/default", map[string]any{"email_mode": "draft", "name": "Northpeak help"}); s["email_mode"] != "draft" || s["chat_mode"] != "off" {
		t.Fatalf("saved: %v", s)
	}

	// It drafts an answer from the knowledge, citing it.
	conv := write("Exporting", "How do I export a chart to CSV?")
	r := latest(conv)
	if r["action"] != "answer" || r["outcome"] != "drafted" || r["reply"] != "Open the chart, then More, Export CSV." || len(r["cited"].([]any)) != 1 ||
		r["trigger"] != "message" || r["channel"] != "email" || r["checks"].(map[string]any)["grounded"] != true {
		t.Fatalf("answer: %v", r)
	}
	// Sent as drafted: counted as sent. A draft is used once.
	owner.must(201, "POST", sb+"/conversations/"+conv+"/replies", map[string]any{"body": r["reply"], "assistant_run_id": r["id"]})
	if r := owner.must(200, "GET", sb+"/conversations/"+conv+"/assistant", nil)["run"].(map[string]any); r["outcome"] != "sent" {
		t.Fatalf("sent: %v", r)
	}
	owner.must(400, "POST", sb+"/conversations/"+conv+"/replies", map[string]any{"body": "Again", "assistant_run_id": r["id"]})

	// One question back when that settles it; edited before sending.
	conv = write("Export", "I want to export, which chart though?")
	r = latest(conv)
	if r["action"] != "clarify" || r["reply"] != "Which chart do you want to export?" {
		t.Fatalf("clarify: %v", r)
	}
	owner.must(201, "POST", sb+"/conversations/"+conv+"/replies", map[string]any{"body": "Which chart is it?", "assistant_run_id": r["id"]})
	if r := owner.must(200, "GET", sb+"/conversations/"+conv+"/assistant", nil)["run"].(map[string]any); r["outcome"] != "edited" {
		t.Fatalf("edited: %v", r)
	}

	// A reply that breaks the style rules is rewritten once.
	conv = write("Export", "sloppy: how do I export?")
	if r := latest(conv); r["action"] != "answer" || r["reply"] != "Open the chart, then More, Export CSV." || r["checks"].(map[string]any)["rewritten"] == nil {
		t.Fatalf("rewritten: %v", r)
	}
	// One that says what the articles don't goes to a person.
	conv = write("Export", "What's the price to export?")
	if r := latest(conv); r["action"] != "handoff" || r["reason"] != "not_grounded" || r["reply"] != "" || r["outcome"] != "discarded" {
		t.Fatalf("not grounded: %v", r)
	}
	// Sensitive topics and asking for a person never reach the AI.
	conv = write("Charge", "I'll file a chargeback unless you export my data")
	if r := latest(conv); r["action"] != "handoff" || r["reason"] != "sensitive_topic" || r["model"] != "" {
		t.Fatalf("sensitive: %v", r)
	}
	conv = write("Help", "Can I talk to a real person about exports?")
	if r := latest(conv); r["action"] != "handoff" || r["reason"] != "asked_for_person" {
		t.Fatalf("person: %v", r)
	}
	// What the knowledge doesn't cover is handed off and marked as a gap,
	// so the team's reply is offered as the answer.
	conv = write("Invoices", "Do you take payment by wire to Lisbon?")
	if r := latest(conv); r["action"] != "handoff" || r["reason"] != "not_in_knowledge" {
		t.Fatalf("gap: %v", r)
	}
	var gap *string
	if err := e.pool.QueryRow(context.Background(), `SELECT knowledge_gap FROM support_conversations WHERE id = $1`, conv).Scan(&gap); err != nil || gap == nil {
		t.Fatalf("gap marked: %v %v", gap, err)
	}

	// A person can ask for a draft, and discard it.
	r = owner.must(200, "POST", sb+"/conversations/"+off+"/assistant", nil)
	if r["action"] != "answer" || r["trigger"] != "person" {
		t.Fatalf("by hand: %v", r)
	}
	owner.must(200, "POST", sb+"/assistant-runs/"+r["id"].(string)+"/discard", nil)
	owner.must(409, "POST", sb+"/assistant-runs/"+r["id"].(string)+"/discard", nil)
	owner.must(404, "POST", sb+"/assistant-runs/00000000-0000-0000-0000-000000000000/discard", nil)

	st := owner.must(200, "GET", sb+"/assistant/stats", nil)
	if st["runs"].(float64) < 8 || st["drafts"].(map[string]any)["sent"] != 1.0 || st["drafts"].(map[string]any)["edited"] != 1.0 ||
		st["handoff_reasons"].(map[string]any)["sensitive_topic"] != 1.0 {
		t.Fatalf("stats: %v", st)
	}
	owner.must(204, "DELETE", sb+"/assistant-settings/default", nil)
	owner.must(404, "DELETE", sb+"/assistant-settings/default", nil)

	if len(named(got(), "support.assistant_ran")) < 8 || len(named(got(), "support.assistant_updated")) != 1 || len(named(got(), "support.assistant_discarded")) != 1 {
		t.Fatalf("events: %d", len(got()))
	}
}

// answerFindAnswer is the fake model looking for a missing answer in the
// website's pages or Slack threads: it finds the two this test plants.
func answerFindAnswer(w http.ResponseWriter, body string) {
	none := map[string]any{"found": false, "title": "", "answer": "", "source": 0, "quote": "", "sensitive": false}
	for _, f := range []struct{ asked, quote, title, answer string }{
		{"bank transfer", "Annual plans can be paid by bank transfer.", "Can I pay by bank transfer?", "Yes, annual plans can be paid by bank transfer. Ask for an invoice from the billing page."},
		{"two factor", "Settings, Security, Reset two factor.", "How do I reset two-factor sign-in?", "Go to Settings, Security, Reset two factor. You get an email to set it up again."},
	} {
		q := strings.Index(body, "Sources:")
		i := strings.Index(body, f.quote)
		if q < 0 || i < 0 || !strings.Contains(strings.ToLower(body[:q]), f.asked) {
			continue
		}
		j := strings.LastIndex(body[:i], `n=\"`)
		n, _ := strconv.Atoi(string(body[j+4]))
		answerJSON(w, map[string]any{"found": true, "title": f.title, "answer": f.answer, "source": n, "quote": f.quote, "sensitive": false})
		return
	}
	answerJSON(w, none)
}

// fakeSlackAPI is a Slack workspace with a #support channel where the team
// once answered how to reset two-factor sign-in.
func fakeSlackAPI(joined *[]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer xoxb-test-not-a-real-token" {
			io.WriteString(w, `{"ok":false,"error":"invalid_auth"}`)
			return
		}
		switch r.URL.Path {
		case "/auth.test":
			io.WriteString(w, `{"ok":true,"team":"Northpeak","team_id":"T1"}`)
		case "/conversations.list":
			io.WriteString(w, `{"ok":true,"channels":[{"id":"C1","name":"support"},{"id":"C2","name":"leadership","is_private":true}]}`)
		case "/conversations.join":
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			*joined = append(*joined, b["channel"])
			io.WriteString(w, `{"ok":true}`)
		case "/conversations.history":
			io.WriteString(w, `{"ok":true,"messages":[{"ts":"1790000000.000100","user":"U1","text":"A customer can't sign in, how do we reset two factor?","reply_count":1},`+
				`{"ts":"1790000100.000100","user":"U2","text":"Lunch is here"}]}`)
		case "/conversations.replies":
			io.WriteString(w, `{"ok":true,"messages":[{"ts":"1790000000.000100","user":"U1","text":"A customer can't sign in, how do we reset two factor?"},`+
				`{"ts":"1790000200.000100","user":"U3","text":"Settings, Security, Reset two factor. They get an email to set it up again."}]}`)
		case "/chat.getPermalink":
			io.WriteString(w, `{"ok":true,"permalink":"https://northpeak.slack.test/archives/C1/p1790000000000100"}`)
		default:
			io.WriteString(w, `{"ok":false,"error":"unknown_method"}`)
		}
	})
}

// When the knowledge has no answer, the assistant looks on the business's
// website, then in the Slack channels picked for it, and suggests what it
// finds for a person to approve. With Auto allowed by an extension, checked
// answers go out on their own, signed as the assistant; on chat, a hand-off
// tells the visitor a person will reply. A person can try it on a made-up
// question first.
func TestSupportAssistantSourcesAndAuto(t *testing.T) {
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
	ws := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Northpeak", "test": true})["id"].(string)
	base := "/v1/workspaces/" + ws
	sb := base + "/support"
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `INSERT INTO workspace_context (workspace_id, website) VALUES ($1, 'https://northpeak.example')
		ON CONFLICT (workspace_id) DO UPDATE SET website = EXCLUDED.website`, ws); err != nil {
		t.Fatal(err)
	}
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
	var joined []string
	slack := httptest.NewServer(fakeSlackAPI(&joined))
	defer slack.Close()
	e.integrations.Client.BaseURLs["slack"] = slack.URL
	write := func(subject, body string) string {
		return owner.must(201, "POST", base+"/test/support-email", map[string]any{"mailbox_id": box, "from": "ana" + strconv.FormatInt(time.Now().UnixNano(), 36) + "@acme.example",
			"subject": subject, "body": body})["conversation_id"].(string)
	}
	latest := func(conv string) map[string]any {
		t.Helper()
		return waitFor(t, owner, sb+"/conversations/"+conv+"/assistant", func(m map[string]any) bool { return m["run"] != nil })["run"].(map[string]any)
	}
	suggestion := func(source string) map[string]any {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			for _, s := range owner.must(200, "GET", base+"/knowledge/suggestions", nil)["data"].([]any) {
				if s := s.(map[string]any); s["source"] == source {
					return s
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("no %s suggestion", source)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	// Slack (knowledge) is its own connection: it reads only the channels
	// picked, and joins the public ones to read them.
	sk := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "slack_knowledge", "credentials": map[string]any{"api_key": "xoxb-test-not-a-real-token"}})
	skID := sk["id"].(string)
	if l := owner.must(200, "GET", sb+"/assistant/slack-channels?integration_id="+skID, nil)["data"].([]any); len(l) != 2 {
		t.Fatalf("channels: %v", l)
	}
	owner.must(400, "GET", sb+"/assistant/slack-channels?integration_id=nope", nil)
	owner.must(400, "PUT", sb+"/assistant-settings/default", map[string]any{"email_mode": "draft", "slack_integration_id": skID, "slack_channels": []string{"C9"}})
	owner.must(400, "PUT", sb+"/assistant-settings/default", map[string]any{"email_mode": "draft", "slack_channels": []string{"C1"}})
	set := owner.must(200, "PUT", sb+"/assistant-settings/default", map[string]any{"email_mode": "draft", "slack_integration_id": skID, "slack_channels": []string{"C1", "C2"}})
	if set["find_on_website"] != true || set["slack_integration_id"] != skID || len(set["slack_channels"].([]any)) != 2 ||
		set["slack_channels"].([]any)[0].(map[string]any)["name"] != "support" {
		t.Fatalf("settings: %v", set)
	}
	if len(joined) != 1 || joined[0] != "C1" {
		t.Fatalf("joined %v; only the public channel is joined", joined)
	}

	// The website answers this one: suggested for review, with the page.
	conv := write("Paying", "Can I pay by bank transfer?")
	if r := latest(conv); r["action"] != "handoff" || r["reason"] != "not_in_knowledge" {
		t.Fatalf("gap: %v", r)
	}
	s := suggestion("website")
	if s["title"] != "Can I pay by bank transfer?" || s["source_ref"] != "https://northpeak.example/faq" || s["status"] != "pending" ||
		!strings.Contains(s["source_quote"].(string), "bank transfer") {
		t.Fatalf("website suggestion: %v", s)
	}
	// Slack answers this one: the website has nothing.
	write("Locked out", "How do I reset two factor? I lost my phone.")
	s = suggestion("slack")
	if s["title"] != "How do I reset two-factor sign-in?" || s["source_ref"] != "https://northpeak.slack.test/archives/C1/p1790000000000100" {
		t.Fatalf("slack suggestion: %v", s)
	}
	// Approved, it answers the next customer.
	owner.must(200, "POST", base+"/knowledge/suggestions/"+s["id"].(string)+"/approve", map[string]any{})

	// Auto is an extension's to allow; then answers go out on their own.
	if l := owner.must(200, "GET", sb+"/assistant-settings", nil); l["auto_available"] != false {
		t.Fatalf("auto before: %v", l)
	}
	e.srv.SetSupportAuto(func(_ context.Context, id string) bool { return id == ws })
	if l := owner.must(200, "GET", sb+"/assistant-settings", nil); l["auto_available"] != true {
		t.Fatalf("auto after: %v", l)
	}
	owner.must(200, "PUT", sb+"/assistant-settings/default", map[string]any{"email_mode": "auto", "chat_mode": "auto", "name": "Northpeak help",
		"find_on_website": false})
	conv = write("Exporting", "How do I export a chart?")
	r := latest(conv)
	waitFor(t, owner, sb+"/conversations/"+conv+"/assistant", func(m map[string]any) bool { return m["run"].(map[string]any)["outcome"] == "sent" })
	msgs := owner.must(200, "GET", sb+"/conversations/"+conv, nil)["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if r["action"] != "answer" || last["kind"] != "out" ||
		last["body"] != "Open the chart, then More, Export CSV.\n\nNorthpeak help (AI assistant). Ask for a person any time." {
		t.Fatalf("auto email: %v %v", r, last)
	}
	// A hand-off is never sent, only left for the team.
	conv = write("Help", "I want to talk to a real person")
	if r := latest(conv); r["action"] != "handoff" || r["outcome"] != "discarded" {
		t.Fatalf("auto handoff: %v", r)
	}
	if n := len(owner.must(200, "GET", sb+"/conversations/"+conv, nil)["messages"].([]any)); n != 1 {
		t.Fatalf("email handoff sent something: %d messages", n)
	}

	// On chat: an answer appears in the visitor's chat, and a hand-off tells
	// them a person will reply, once.
	key := owner.must(201, "POST", sb+"/chat-widgets", map[string]any{"allowed_origins": []string{"northpeak.example"}})["key"].(string)
	cb := "/chat/" + key
	_, sess := chatCall(t, e.h, "POST", cb+"/session", "", map[string]any{})
	tok := sess["token"].(string)
	chatCall(t, e.h, "POST", cb+"/messages", tok, map[string]any{"body": "how do I export a chart?"})
	poll := func(n int) []any {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			_, p := chatCall(t, e.h, "GET", cb+"/messages", tok, nil)
			if l := p["messages"].([]any); len(l) >= n || time.Now().After(deadline) {
				return l
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	l := poll(2)
	if len(l) != 2 || l[1].(map[string]any)["body"] != "Open the chart, then More, Export CSV.\n\nNorthpeak help (AI assistant)" {
		t.Fatalf("chat answer: %v", l)
	}
	chatCall(t, e.h, "POST", cb+"/messages", tok, map[string]any{"body": "Can I talk to a real person?"})
	l = poll(4)
	if len(l) != 4 || !strings.HasPrefix(l[3].(map[string]any)["body"].(string), handoffNoteTest) {
		t.Fatalf("chat handoff: %v", l)
	}
	chatCall(t, e.h, "POST", cb+"/messages", tok, map[string]any{"body": "Hello? I need a human"})
	time.Sleep(500 * time.Millisecond)
	if l = poll(5); len(l) != 5 {
		t.Fatalf("told twice: %v", l)
	}

	// Trying it on a made-up question stores and sends nothing.
	owner.must(400, "POST", sb+"/assistant/test", map[string]any{"question": ""})
	owner.must(400, "POST", sb+"/assistant/test", map[string]any{"question": "Hi", "channel": "sms"})
	owner.must(404, "POST", sb+"/assistant/test", map[string]any{"question": "Hi", "brand": "nobrand"})
	if res := owner.must(200, "POST", sb+"/assistant/test", map[string]any{"question": "How do I export a chart?", "channel": "chat"}); res["action"] != "answer" ||
		len(res["cited"].([]any)) != 1 || res["reply"] != "Open the chart, then More, Export CSV." {
		t.Fatalf("test answer: %v", res)
	}
	if res := owner.must(200, "POST", sb+"/assistant/test", map[string]any{"question": "My lawyer will be in touch about this chargeback"}); res["action"] != "handoff" ||
		res["reason"] != "sensitive_topic" {
		t.Fatalf("test handoff: %v", res)
	}
	if len(named(got(), "support.assistant_sent")) < 2 || len(named(got(), "knowledge.answer_searched")) < 2 || len(named(got(), "support.assistant_tested")) != 2 {
		t.Fatalf("events: sent %d searched %d tested %d", len(named(got(), "support.assistant_sent")), len(named(got(), "knowledge.answer_searched")),
			len(named(got(), "support.assistant_tested")))
	}
}

const handoffNoteTest = "I've passed this to the team."
