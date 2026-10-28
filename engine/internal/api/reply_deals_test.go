package api_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/inbox"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Replies move deals in the workspace's own CRM: an interested reply opens
// a deal, a meeting moves it on, a later "not now" never moves it back, and
// a closed deal is left alone for a new one.
func TestReplyDeals(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, base, _ := sendingSetup(t, e, &fakeClock{t: time.Now()})
	ws := strings.TrimPrefix(base, "/v1/workspaces/")
	ctx := context.Background()
	mb := mailboxIDs(t, owner, base)["ana@acme.test"]
	cat := base + "/catalog"

	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 20 * time.Millisecond
	(&inbox.Rules{Pool: e.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Register(q)
	e.srv.RegisterJobs(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })

	// Every workspace has the Deal type, with stages and links to its
	// people and company.
	deal := func(stage string) map[string]any {
		return map[string]any{"type": "deal", "field": "stage", "stage": stage, "closed": []string{"won", "lost"}}
	}
	rules := base + "/reply-rules"
	// A deal type needs a way to point at the reply's person or company.
	owner.must(201, "POST", cat+"/types", map[string]any{"name": "Opportunity"})
	owner.must(201, "POST", cat+"/types/opportunity/fields", map[string]any{"label": "Stage", "value_type": "status", "option_set": "deal_stage"})
	owner.must(400, "PUT", rules, map[string]any{"rules": map[string]any{"interested": map[string]any{"deal": map[string]any{
		"type": "opportunity", "field": "stage", "stage": "qualified"}}}})
	owner.must(400, "PUT", rules, map[string]any{"rules": map[string]any{"interested": map[string]any{"deal": deal("signed")}}})
	owner.must(400, "PUT", rules, map[string]any{"rules": map[string]any{"interested": map[string]any{"deal": deal("won")}}})
	owner.must(400, "PUT", rules, map[string]any{"rules": map[string]any{"auto_reply": map[string]any{"deal": deal("qualified")}}})
	owner.must(400, "PUT", rules, map[string]any{"rules": map[string]any{"out_of_office": map[string]any{"action": "reschedule", "deal": deal("qualified")}}})
	owner.must(400, "PUT", rules, map[string]any{"rules": map[string]any{"interested": map[string]any{"action": "snooze", "deal": deal("qualified")}}})
	owner.must(200, "PUT", rules, map[string]any{"rules": map[string]any{
		"interested":     map[string]any{"deal": deal("qualified")},
		"meeting_booked": map[string]any{"deal": deal("meeting")},
		"not_now":        map[string]any{"action": "nothing", "deal": deal("lead")},
	}})

	recs := base + "/records"
	acct := owner.must(201, "POST", recs, map[string]any{"type": "account", "name": "Prospect"})["id"].(string)
	kim := owner.must(201, "POST", recs, map[string]any{"type": "person", "name": "Kim Lee",
		"attributes": map[string]any{"companies": []string{acct}}})["id"].(string)

	label := func(cat string) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, `
			INSERT INTO inbound_messages (workspace_id, mailbox_id, uidvalidity, uid, from_email, kind, body, record_id, category, received_at)
			VALUES ($1, $2, 0, nextval('inbound_external_uid_seq'), 'kim@prospect.test', 'reply', 'ok', $3, $4, now()) RETURNING id`,
			ws, mb, kim, cat).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if err := inbox.Classified(ctx, e.pool, inbox.Classification{WorkspaceID: ws, InboundID: id, MailboxID: mb,
			Source: "mailbox", Category: &cat, By: "rules", Confidence: 1}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	deals := func(want int) []map[string]any {
		t.Helper()
		var out []map[string]any
		for i := 0; i < 400; i++ {
			out = out[:0]
			rows, err := e.pool.Query(ctx, `SELECT id::text, name, COALESCE(attributes ->> 'stage', ''), attributes ->> 'associated_company' IS NOT NULL
				FROM records WHERE workspace_id = $1 AND record_type = 'deal' ORDER BY created_at`, ws)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var id, name, stage string
				var linked bool
				if err := rows.Scan(&id, &name, &stage, &linked); err != nil {
					t.Fatal(err)
				}
				out = append(out, map[string]any{"id": id, "name": name, "stage": stage, "company": linked})
			}
			rows.Close()
			if len(out) == want {
				return out
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("deals: want %d, have %v", want, out)
		return nil
	}
	stageIs := func(id, stage string) {
		t.Helper()
		for i := 0; i < 400; i++ {
			var s string
			e.pool.QueryRow(ctx, `SELECT COALESCE(attributes ->> 'stage', '') FROM records WHERE id = $1`, id).Scan(&s)
			if s == stage {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("deal %s never reached %s", id, stage)
	}
	applied := func(want int) {
		t.Helper()
		var n int
		for i := 0; i < 400; i++ {
			e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'inbox.rule_applied' AND data ->> 'action' = 'deal'`, ws).Scan(&n)
			if n == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("deal moves: want %d, have %d", want, n)
	}

	// An interested reply opens a deal for Kim's company.
	label("interested")
	d := deals(1)[0]
	if d["name"] != "Prospect" || d["stage"] != "qualified" || d["company"] != true {
		t.Fatalf("opened: %v", d)
	}
	id := d["id"].(string)
	applied(1)
	// It is the same deal in outreach outcomes, under the same id.
	if od := owner.must(200, "GET", base+"/deals/"+id, nil); od["source"] != "crm" || od["state"] != "open" || od["stage"] != "Qualified" || od["name"] != "Prospect" {
		t.Fatalf("outcomes deal: %v", od)
	}

	// A meeting moves it on; a later "not now" never moves it back.
	label("meeting_booked")
	stageIs(id, "meeting")
	applied(2)
	label("not_now")
	label("question") // no rule for it
	time.Sleep(200 * time.Millisecond)
	stageIs(id, "meeting")
	applied(2)

	// A stage a person set is theirs: rules never override it.
	owner.must(200, "PATCH", recs+"/"+id, map[string]any{"attributes": map[string]any{"stage": "qualified"}})
	label("meeting_booked")
	time.Sleep(200 * time.Millisecond)
	stageIs(id, "qualified")

	// Once the deal is won, a new interested reply opens a new deal.
	owner.must(200, "PATCH", recs+"/"+id, map[string]any{"attributes": map[string]any{"stage": "won",
		"value": map[string]any{"amount_minor": 250000, "currency": "EUR"}, "close_date": "2026-10-20"}})
	if od := owner.must(200, "GET", base+"/deals/"+id, nil); od["state"] != "won" || od["amount_cents"] != 250000.0 || od["currency"] != "EUR" ||
		!strings.HasPrefix(od["closed_at"].(string), "2026-10-20") {
		t.Fatalf("won deal in outcomes: %v", od)
	}
	label("interested")
	if d := deals(2)[1]; d["stage"] != "qualified" {
		t.Fatalf("new deal: %v", d)
	}
	applied(3)

	ev := got()
	if n := len(named(ev, "inbox.rule_applied")); n < 3 {
		t.Fatalf("events: %d", n)
	}
}
