package api_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/mailbox/mailtest"
)

// fakeClock is a settable clock for the send engine.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Set(t time.Time)     { c.mu.Lock(); c.t = t; c.mu.Unlock() }
func (c *fakeClock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

var sydney, _ = time.LoadLocation("Australia/Sydney")

// pump runs one scheduler tick and waits for the send jobs it queued.
func (e *env) pump(t *testing.T) int {
	t.Helper()
	ctx := context.Background()
	n, err := e.send.Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var pending int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'sequence.send' AND status IN ('queued', 'running')`).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if pending == 0 {
			return n
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d send jobs still pending", pending)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// sendingSetup connects two working mailboxes on a fake server and returns
// the owner, the workspace path and the server.
func sendingSetup(t *testing.T, e *env, clock *fakeClock) (*client, string, *mailtest.Server) {
	t.Helper()
	srv := mailtest.Start(t, map[string]string{"ana@acme.test": "pw1", "bo@acme.test": "pw2"})
	e.mail.Dialer.TLSConfig = srv.ClientTLS
	e.send.Now = clock.Now
	// A real SPF record, so the allocator counts these mailboxes as ready.
	e.dns.txt["acme.test"] = []string{"v=spf1 include:_spf.google.com ~all"}
	e.runJobs(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	// These tests are about sending; verification has its own tests.
	owner.must(200, "PUT", base+"/verification-settings", map[string]any{"verify_before_send": false})
	csv := fmt.Sprintf("email,password,smtp_host,smtp_port,imap_host,imap_port,display_name\n"+
		"ana@acme.test,pw1,%[1]s,%[2]d,%[1]s,%[3]d,Ana Smith\n"+
		"bo@acme.test,pw2,%[1]s,%[2]d,%[1]s,%[3]d,Bo Jones\n", srv.Host, srv.SMTPPort, srv.IMAPPort)
	if code, res := owner.raw("POST", base+"/mailboxes/import", "text/csv", csv); code != 200 || res["created"] != 2.0 {
		t.Fatalf("mailbox import: %d %v", code, res)
	}
	for _, m := range owner.must(200, "GET", base+"/mailboxes", nil)["data"].([]any) {
		waitFor(t, owner, base+"/mailboxes/"+m.(map[string]any)["id"].(string), func(m map[string]any) bool { return m["status"] == "active" })
	}
	return owner, base, srv
}

func parse(t *testing.T, m mailtest.Message) *mail.Message {
	t.Helper()
	msg, err := mail.ReadMessage(bytes.NewReader(m.Data))
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func enrollments(t *testing.T, c *client, base, seq string) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, en := range c.must(200, "GET", base+"/sequences/"+seq+"/enrollments", nil)["data"].([]any) {
		m := en.(map[string]any)
		out[m["email"].(string)] = m
	}
	return out
}

func TestSequenceSendsThroughPolicy(t *testing.T) {
	e := freshEnv(t)
	clock := &fakeClock{}
	// A Saturday in Sydney: outside the default Monday to Friday window.
	clock.Set(time.Date(2030, 1, 5, 11, 0, 0, 0, sydney))
	owner, base, srv := sendingSetup(t, e, clock)

	code, imp := owner.raw("POST", base+"/leads/import?list=Pilot", "text/csv",
		"email,first_name,company\nli@one-io.example,Li,One\nmo@two-io.example,Mo,\nni@three-io.example,Ni,Three\nnofirst@four-io.example,,Four\nsup@five-io.example,Sue,Five\n")
	if code != 200 || imp["created"] != 5.0 {
		t.Fatalf("lead import: %d %v", code, imp)
	}
	listID := imp["list_id"].(string)
	owner.must(200, "POST", base+"/suppressions", map[string]any{"values": []string{"sup@five-io.example"}, "reason": "do_not_contact"})

	seq := owner.must(201, "POST", base+"/sequences", map[string]any{
		"name": "Pilot", "target_daily": 1000, "timezone": "Australia/Sydney", "send_start": "09:00", "send_end": "17:00",
		"steps": []map[string]any{
			{"subject": "Quick question for {{company|your team}}", "body": "Hi {{first_name}},\n\nWorth a chat?\n\n{{sender_first_name}}"},
			{"delay_days": 2, "subject": "", "body": "Hi {{first_name}}, bumping this."},
		},
	})
	seqID := seq["id"].(string)
	if seq["status"] != "draft" || len(seq["steps"].([]any)) != 2 {
		t.Fatalf("created: %v", seq)
	}
	owner.must(400, "PUT", base+"/sequences/"+seqID+"/steps", map[string]any{"steps": []map[string]any{{"subject": "", "body": "x"}}})
	owner.must(400, "POST", base+"/sequences", map[string]any{"name": "Bad", "timezone": "Mars/Base"})

	res := owner.must(200, "POST", base+"/sequences/"+seqID+"/enrollments", map[string]any{"list_id": listID})
	if res["enrolled"] != 4.0 || res["suppressed"] != 1.0 {
		t.Fatalf("enroll: %v", res)
	}
	if res = owner.must(200, "POST", base+"/sequences/"+seqID+"/enrollments", map[string]any{"list_id": listID}); res["enrolled"] != 0.0 || res["already_enrolled"] != 4.0 {
		t.Fatalf("re-enroll: %v", res)
	}

	// Preview renders for a lead, showing what is missing.
	ens := enrollments(t, owner, base, seqID)
	pv := owner.must(200, "POST", base+"/sequences/"+seqID+"/preview", map[string]any{"lead_id": ens["nofirst@four-io.example"]["lead_id"]})
	first := pv["data"].([]any)[0].(map[string]any)
	if first["subject"] != "Quick question for Four" || fmt.Sprint(first["missing"]) != "[first_name]" {
		t.Fatalf("preview: %v", pv)
	}

	// A draft sequence sends nothing.
	if n := e.pump(t); n != 0 {
		t.Fatalf("draft sent %d", n)
	}
	owner.must(200, "PATCH", base+"/sequences/"+seqID, map[string]any{"status": "active"})

	// Saturday: everything waits for Monday.
	e.pump(t)
	if len(srv.Messages()) != 0 {
		t.Fatalf("sent on a Saturday")
	}
	ens = enrollments(t, owner, base, seqID)
	if r := ens["li@one-io.example"]["status_reason"]; r != "outside_sending_window" {
		t.Fatalf("window deferral: %v", ens["li@one-io.example"])
	}

	// Tuesday morning: one email per mailbox (the minimum gap holds the
	// third back) and the lead with no first name is blocked.
	clock.Set(time.Date(2030, 1, 8, 10, 0, 0, 0, sydney))
	e.pump(t)
	msgs := srv.Messages()
	if len(msgs) != 2 || msgs[0].From == msgs[1].From {
		t.Fatalf("first round: %d messages %v", len(msgs), msgs)
	}
	ens = enrollments(t, owner, base, seqID)
	if ens["nofirst@four-io.example"]["status"] != "blocked" || !strings.Contains(fmt.Sprint(ens["nofirst@four-io.example"]["status_reason"]), "missing_variable: first_name") {
		t.Fatalf("missing variable: %v", ens["nofirst@four-io.example"])
	}
	clock.Add(5 * time.Minute)
	e.pump(t)
	if msgs = srv.Messages(); len(msgs) != 3 {
		t.Fatalf("second round: %d", len(msgs))
	}

	firsts := map[string]*mail.Message{}
	for _, m := range msgs {
		p := parse(t, m)
		firsts[m.To[0]] = p
		if !strings.HasPrefix(p.Header.Get("List-Unsubscribe"), "<http://localhost:8080/u/") || p.Header.Get("List-Unsubscribe-Post") != "List-Unsubscribe=One-Click" {
			t.Fatalf("unsubscribe headers: %v", p.Header)
		}
	}
	if s := firsts["mo@two-io.example"].Header.Get("Subject"); s != "Quick question for your team" {
		t.Fatalf("fallback subject: %q", s)
	}
	if b, _ := io.ReadAll(firsts["li@one-io.example"].Body); !strings.Contains(string(b), "Hi Li,") {
		t.Fatalf("body: %s", b)
	}

	// Li unsubscribes. GET only shows the page; POST does it.
	unsub := strings.Trim(firsts["li@one-io.example"].Header.Get("List-Unsubscribe"), "<>")
	path := strings.TrimPrefix(unsub, "http://localhost:8080")
	if code, body := plain(e.h, "GET", path); code != 200 || !strings.Contains(body, "<form") {
		t.Fatalf("unsubscribe page: %d %s", code, body)
	}
	if ens = enrollments(t, owner, base, seqID); ens["li@one-io.example"]["status"] != "active" {
		t.Fatalf("GET unsubscribed: %v", ens["li@one-io.example"])
	}
	if code, body := plain(e.h, "POST", path); code != 200 || !strings.Contains(body, "unsubscribed") {
		t.Fatalf("unsubscribe: %d %s", code, body)
	}
	plain(e.h, "POST", path) // idempotent
	if code, _ := plain(e.h, "POST", path[:len(path)-2]+"xx"); code != 404 {
		t.Fatalf("forged token: %d", code)
	}
	if ens = enrollments(t, owner, base, seqID); ens["li@one-io.example"]["status"] != "unsubscribed" {
		t.Fatalf("after unsubscribe: %v", ens["li@one-io.example"])
	}
	d := owner.must(200, "POST", base+"/recipients/check", map[string]any{"emails": []string{"li@one-io.example"}})["data"].([]any)[0].(map[string]any)
	if d["allowed"] != false {
		t.Fatalf("unsubscribed lead allowed: %v", d)
	}

	// Two days later the follow-up goes in the same thread, from the same mailbox.
	clock.Add(48 * time.Hour)
	e.pump(t)
	clock.Add(5 * time.Minute)
	e.pump(t)
	msgs = srv.Messages()
	if len(msgs) != 5 {
		t.Fatalf("follow-ups: %d messages", len(msgs))
	}
	for _, m := range msgs[3:] {
		p := parse(t, m)
		orig := firsts[m.To[0]]
		if p.Header.Get("In-Reply-To") != orig.Header.Get("Message-Id") || p.Header.Get("Subject") != "Re: "+orig.Header.Get("Subject") {
			t.Fatalf("threading for %s: %v vs %v", m.To[0], p.Header, orig.Header)
		}
		if p.Header.Get("From") != orig.Header.Get("From") {
			t.Fatalf("mailbox changed mid-thread for %s", m.To[0])
		}
	}
	ens = enrollments(t, owner, base, seqID)
	for _, who := range []string{"mo@two-io.example", "ni@three-io.example"} {
		if ens[who]["status"] != "completed" {
			t.Fatalf("%s: %v", who, ens[who])
		}
	}

	got := owner.must(200, "GET", base+"/sequences/"+seqID, nil)
	stats := got["stats"].(map[string]any)
	if stats["sent"] != 5.0 || stats["completed"] != 2.0 || stats["unsubscribed"] != 1.0 || stats["blocked"] != 1.0 {
		t.Fatalf("stats: %v", stats)
	}
	if out := owner.must(200, "GET", base+"/messages?sequence_id="+seqID+"&status=sent", nil); len(out["data"].([]any)) != 5 {
		t.Fatalf("messages: %v", out)
	}
	owner.must(400, "DELETE", base+"/sequences/"+seqID, nil) // active
	owner.must(200, "PATCH", base+"/sequences/"+seqID, map[string]any{"status": "paused"})
	owner.must(204, "DELETE", base+"/sequences/"+seqID, nil)
}

func TestSequenceBounceLimitAndTestSend(t *testing.T) {
	e := freshEnv(t)
	clock := &fakeClock{}
	clock.Set(time.Date(2030, 1, 8, 10, 0, 0, 0, time.UTC))
	owner, base, srv := sendingSetup(t, e, clock)
	srv.Reject("gone@one-io.example")

	_, imp := owner.raw("POST", base+"/leads/import?list=L", "text/csv", "email\ngone@one-io.example\nok@two-io.example\nlater@three-io.example\n")
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{
		"name": "Capped", "daily_new_limit": 2, "send_days": []int{1, 2, 3, 4, 5, 6, 7},
		"steps": []map[string]any{{"subject": "Hello", "body": "Hi there"}},
	})
	seqID := seq["id"].(string)
	owner.must(200, "POST", base+"/sequences/"+seqID+"/enrollments", map[string]any{"list_id": imp["list_id"]})
	owner.must(200, "PATCH", base+"/sequences/"+seqID, map[string]any{"status": "active"})
	for range 3 {
		e.pump(t)
		clock.Add(5 * time.Minute)
	}

	// The cap lets two leads start today; the third waits for tomorrow.
	ens := enrollments(t, owner, base, seqID)
	waiting := 0
	for _, en := range ens {
		if en["status"] == "active" && en["status_reason"] == "sequence_daily_new_limit" {
			waiting++
		}
	}
	if waiting != 1 {
		t.Fatalf("daily new limit: %v", ens)
	}
	clock.Add(24 * time.Hour)
	e.pump(t)
	ens = enrollments(t, owner, base, seqID)
	if ens["gone@one-io.example"]["status"] != "bounced" || ens["ok@two-io.example"]["status"] != "completed" || ens["later@three-io.example"]["status"] != "completed" {
		t.Fatalf("after two days: %v", ens)
	}
	d := owner.must(200, "POST", base+"/recipients/check", map[string]any{"emails": []string{"gone@one-io.example"}})["data"].([]any)[0].(map[string]any)
	if !strings.Contains(fmt.Sprint(d["reasons"]), "suppressed_email:bounced") {
		t.Fatalf("bounce not suppressed: %v", d)
	}

	// Test sends go through the recipient check.
	mb := owner.must(200, "GET", base+"/mailboxes", nil)["data"].([]any)[0].(map[string]any)
	testPath := base + "/mailboxes/" + mb["id"].(string) + "/test-send"
	before := len(srv.Messages())
	owner.must(409, "POST", testPath, map[string]any{"to": "gone@one-io.example"})
	clock.Add(time.Hour)
	if r := owner.must(200, "POST", testPath, map[string]any{"to": "me@acme.test"}); r["status"] != "sent" {
		t.Fatalf("test send: %v", r)
	}
	if len(srv.Messages()) != before+1 {
		t.Fatalf("test send not delivered")
	}
	owner.must(200, "PATCH", base+"/mailboxes/"+mb["id"].(string), map[string]any{"min_gap_seconds": 0})
	owner.must(400, "PATCH", base+"/mailboxes/"+mb["id"].(string), map[string]any{"min_gap_seconds": -1})

	events := owner.must(200, "GET", base+"/audit-events", nil)["data"].([]any)
	seen := map[string]bool{}
	for _, ev := range events {
		seen[ev.(map[string]any)["action"].(string)] = true
	}
	for _, a := range []string{"sequence.created", "sequence.activated", "sequence.leads_enrolled", "lead.bounced", "mailbox.test_sent"} {
		if !seen[a] {
			t.Errorf("no %s audit event", a)
		}
	}
}

func TestSequenceScopesAndIsolation(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "S", "steps": []map[string]any{{"subject": "Hi", "body": "x"}}})
	seqID := seq["id"].(string)

	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "r", "scopes": []string{"sequences.read"}})
	reader := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	reader.must(200, "GET", base+"/sequences/"+seqID, nil)
	reader.must(403, "POST", base+"/sequences", map[string]any{"name": "No"})
	reader.must(403, "PATCH", base+"/sequences/"+seqID, map[string]any{"status": "active"})

	other := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Other"})
	ob := "/v1/workspaces/" + other["id"].(string)
	owner.must(404, "GET", ob+"/sequences/"+seqID, nil)
	owner.must(404, "PATCH", ob+"/sequences/"+seqID, map[string]any{"name": "x"})
	if out := owner.must(200, "GET", ob+"/sequences", nil); len(out["data"].([]any)) != 0 {
		t.Fatalf("leak: %v", out)
	}
	// A lead from another workspace cannot be enrolled.
	lead := owner.must(201, "POST", ob+"/leads", map[string]any{"email": "x@y-io.example"})
	if r := owner.must(200, "POST", base+"/sequences/"+seqID+"/enrollments", map[string]any{"lead_ids": []string{lead["id"].(string)}}); r["enrolled"] != 0.0 {
		t.Fatalf("cross-workspace enroll: %v", r)
	}
	owner.must(400, "PATCH", base+"/sequences/"+seqID, map[string]any{"send_start": "18:00"})
	empty := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "Empty"})
	owner.must(400, "PATCH", base+"/sequences/"+empty["id"].(string), map[string]any{"status": "active"})
}

// plain sends a request without credentials and returns the body as text.
func plain(h http.Handler, method, path string) (int, string) {
	req := httptest.NewRequest(method, path, nil)
	if method == "POST" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}
