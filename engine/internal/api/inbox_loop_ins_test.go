package api_test

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/track"
)

// A teammate looped in by someone else has it as new until they open it,
// and gets one email when they don't; looping yourself in tells nobody.
func TestInboxLoopIns(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, base, _ := sendingSetup(t, e, &fakeClock{t: time.Now()})
	ws := base[len("/v1/workspaces/"):]
	ctx := context.Background()
	mb := mailboxIDs(t, owner, base)["ana@acme.test"]
	brand := brandOf(t, e, `SELECT outflare_first_brand($1)::text`, ws)
	bb := "/v1/brands/" + brand
	var me string
	if err := e.pool.QueryRow(ctx, `SELECT user_id::text FROM memberships WHERE workspace_id = $1`, ws).Scan(&me); err != nil {
		t.Fatal(err)
	}
	var reply string
	if err := e.pool.QueryRow(ctx, `INSERT INTO inbound_messages (workspace_id, mailbox_id, uidvalidity, uid, from_email, from_name, subject, kind, body, received_at)
		VALUES ($1, $2, 0, 1, 'one@a.test', 'Olu One', 'Re: pricing', 'reply', 'Tell me more', now()) RETURNING id::text`, ws, mb).Scan(&reply); err != nil {
		t.Fatal(err)
	}
	acc := &api.Accounts{Pool: e.pool, PublicURL: "https://app.example.com", Mailer: e.outbox, Log: slog.Default()}
	bea, _, err := acc.CreateAccount(ctx, nil, httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil),
		api.NewAccount{Email: "bea@example.com", Name: "Bea", Password: "correct horse battery", WorkspaceName: "Bea's"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO memberships (workspace_id, user_id, role) VALUES ($1, $2, 'operator')`, ws, bea); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE users SET email_verified_at = now() WHERE id = $1`, bea); err != nil {
		t.Fatal(err)
	}
	newOnes := func() []any {
		t.Helper()
		return owner.must(200, "GET", bb+"/inbox-loop-ins", nil)["loop_ins"].([]any)
	}
	emailJobs := func() (n int) {
		t.Helper()
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'inbox.loop_in_email' AND workspace_id = $1`, ws).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Looping myself in is not news, and queues no email.
	owner.must(200, "PUT", bb+"/inbox/"+reply+"/followers/"+me, nil)
	if l := newOnes(); len(l) != 0 || emailJobs() != 0 {
		t.Fatalf("looping myself in: %v, %d email jobs", l, emailJobs())
	}
	owner.must(200, "DELETE", bb+"/inbox/"+reply+"/followers/"+me, nil)

	// Bea looped me in: it is new to me until I open the reply.
	if _, err := e.pool.Exec(ctx, `INSERT INTO inbox_followers (workspace_id, brand_id, inbound_id, user_id, added_by) VALUES ($1, $2, $3, $4, $5)`,
		ws, brand, reply, me, bea); err != nil {
		t.Fatal(err)
	}
	l := newOnes()
	if len(l) != 1 {
		t.Fatalf("new loop-ins: %v", l)
	}
	if one := l[0].(map[string]any); one["kind"] != "reply" || one["id"] != reply || one["name"] != "Olu One" || one["subject"] != "Re: pricing" ||
		one["by"].(map[string]any)["name"] != "Bea" {
		t.Fatalf("loop-in: %v", one)
	}
	owner.must(200, "GET", bb+"/inbox/"+reply, nil)
	if l := newOnes(); len(l) != 0 {
		t.Fatalf("still new after opening: %v", l)
	}

	// I loop Bea in: one email job, however often it is asked; she hasn't
	// opened it, so the job emails her a link and nothing about the customer.
	owner.must(200, "PUT", bb+"/inbox/"+reply+"/followers/"+bea, nil)
	owner.must(200, "PUT", bb+"/inbox/"+reply+"/followers/"+bea, nil)
	if n := emailJobs(); n != 1 {
		t.Fatalf("%d email jobs, want 1", n)
	}
	before := e.outbox.count()
	if err := e.srv.RunLoopInEmail(ctx, ws, "reply", reply, bea); err != nil {
		t.Fatal(err)
	}
	if e.outbox.count() != before+1 {
		t.Fatalf("emails sent: %d", e.outbox.count()-before)
	}
	e.outbox.mu.Lock()
	m := e.outbox.sent[len(e.outbox.sent)-1]
	e.outbox.mu.Unlock()
	if m.To != "bea@example.com" || !strings.Contains(m.Text, "/inbox/"+reply) || strings.Contains(m.Text, "Olu") || strings.Contains(m.Text, "pricing") {
		t.Fatalf("email: to %s\n%s", m.To, m.Text)
	}
	if ev := named(got(), "inbox.loop_in_emailed"); len(ev) != 1 || ev[0].Props["kind"] != "reply" {
		t.Fatalf("inbox.loop_in_emailed events: %v", ev)
	}

	// Once she has opened it, or was taken off again, no email goes.
	if _, err := e.pool.Exec(ctx, `UPDATE inbox_followers SET seen_at = now() WHERE inbound_id = $1 AND user_id = $2`, reply, bea); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.RunLoopInEmail(ctx, ws, "reply", reply, bea); err != nil || e.outbox.count() != before+1 {
		t.Fatalf("email after she opened it: %v, %d sent", err, e.outbox.count()-before)
	}
	if err := e.srv.RunLoopInEmail(ctx, ws, "support", reply, bea); err != nil || e.outbox.count() != before+1 {
		t.Fatalf("email for a conversation she isn't on: %v", err)
	}
}

// Asking who is viewing a conversation says you are there; others who
// asked lately are listed, with whether they are writing.
func TestInboxViewing(t *testing.T) {
	e := freshEnv(t)
	owner, base, _ := sendingSetup(t, e, &fakeClock{t: time.Now()})
	ws := base[len("/v1/workspaces/"):]
	ctx := context.Background()
	mb := mailboxIDs(t, owner, base)["ana@acme.test"]
	brand := brandOf(t, e, `SELECT outflare_first_brand($1)::text`, ws)
	bb := "/v1/brands/" + brand
	var me, reply string
	if err := e.pool.QueryRow(ctx, `SELECT user_id::text FROM memberships WHERE workspace_id = $1`, ws).Scan(&me); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO inbound_messages (workspace_id, mailbox_id, uidvalidity, uid, from_email, kind, body, received_at)
		VALUES ($1, $2, 0, 1, 'one@a.test', 'reply', 'Tell me more', now()) RETURNING id::text`, ws, mb).Scan(&reply); err != nil {
		t.Fatal(err)
	}
	acc := &api.Accounts{Pool: e.pool, PublicURL: "https://app.example.com", Mailer: e.outbox, Log: slog.Default()}
	bea, _, err := acc.CreateAccount(ctx, nil, httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil),
		api.NewAccount{Email: "bea@example.com", Name: "Bea", Password: "correct horse battery", WorkspaceName: "Bea's"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO memberships (workspace_id, user_id, role) VALUES ($1, $2, 'operator')`, ws, bea); err != nil {
		t.Fatal(err)
	}
	viewers := func(extra string) []any {
		t.Helper()
		return owner.must(200, "GET", bb+"/inbox-viewing?kind=reply&id="+reply+extra, nil)["viewers"].([]any)
	}
	owner.must(400, "GET", bb+"/inbox-viewing?kind=chat&id="+reply, nil)
	owner.must(404, "GET", bb+"/inbox-viewing?kind=support&id="+reply, nil)

	// Alone: nobody else, and I am now down as writing.
	if v := viewers("&typing=true"); len(v) != 0 {
		t.Fatalf("viewers when alone: %v", v)
	}
	var typing bool
	if err := e.pool.QueryRow(ctx, `SELECT typing_at IS NOT NULL FROM inbox_viewing WHERE item_id = $1 AND user_id = $2`, reply, me).Scan(&typing); err != nil || !typing {
		t.Fatalf("my own row: typing %v, %v", typing, err)
	}

	// Bea has it open and is writing.
	if _, err := e.pool.Exec(ctx, `INSERT INTO inbox_viewing (workspace_id, brand_id, kind, item_id, user_id, typing_at) VALUES ($1, $2, 'reply', $3, $4, now())`,
		ws, brand, reply, bea); err != nil {
		t.Fatal(err)
	}
	v := viewers("")
	if len(v) != 1 || v[0].(map[string]any)["name"] != "Bea" || v[0].(map[string]any)["typing"] != true {
		t.Fatalf("viewers: %v", v)
	}
	// She stopped writing a while ago, then closed it.
	if _, err := e.pool.Exec(ctx, `UPDATE inbox_viewing SET typing_at = now() - interval '1 minute' WHERE user_id = $1`, bea); err != nil {
		t.Fatal(err)
	}
	if v := viewers(""); len(v) != 1 || v[0].(map[string]any)["typing"] != false {
		t.Fatalf("viewers after she stopped writing: %v", v)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE inbox_viewing SET seen_at = now() - interval '2 minutes' WHERE user_id = $1`, bea); err != nil {
		t.Fatal(err)
	}
	if v := viewers(""); len(v) != 0 {
		t.Fatalf("viewers after she left: %v", v)
	}
}
