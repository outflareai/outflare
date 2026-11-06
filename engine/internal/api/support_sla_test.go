package api_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/support"
	"github.com/outflareai/outflare/engine/internal/track"
)

// A team promises how fast it answers: each conversation knows when its
// next reply and its resolution are due, a missed one is marked and
// recorded, and the inbox can show what is overdue.
func TestSupportSLA(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	e.runJobs(t)
	owner, _ := setupOwner(t, e.h)
	ctx := context.Background()
	ws := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Help desk", "test": true})["id"].(string)
	base := "/v1/workspaces/" + ws
	if code, res := owner.raw("POST", base+"/mailboxes/import", "text/csv",
		"email,password,smtp_host,smtp_port,imap_host,imap_port\nhelp@northpeak.example,pw,smtp.northpeak.example,465,imap.northpeak.example,993\n"); code != 200 || res["created"] != 1.0 {
		t.Fatalf("import: %d %v", code, res)
	}
	box := owner.must(200, "GET", base+"/mailboxes", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	waitFor(t, owner, base+"/mailboxes/"+box, func(m map[string]any) bool { return m["status"] == "active" })
	owner.must(200, "PUT", base+"/support/mailboxes/"+box, nil)
	email := func(subject, body string) string {
		return owner.must(201, "POST", base+"/test/support-email", map[string]any{"mailbox_id": box, "from": "ana@acme.example",
			"subject": subject, "body": body})["conversation_id"].(string)
	}
	sla := func(conv string) map[string]any {
		return owner.must(200, "GET", base+"/support/conversations/"+conv, nil)["conversation"].(map[string]any)["sla"].(map[string]any)
	}
	due := func(v any) time.Time {
		t.Helper()
		s, ok := v.(string)
		if !ok {
			t.Fatalf("not due: %v", v)
		}
		at, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return at
	}
	near := func(a, b time.Time) bool { return a.Sub(b).Abs() < time.Minute }

	// No SLA: nothing is due.
	before := email("Before", "Hello?")
	if s := sla(before); s["reply_due_at"] != nil || s["resolve_due_at"] != nil {
		t.Fatalf("no SLA: %v", s)
	}
	if l := owner.must(200, "GET", base+"/support/sla-policies", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("policies: %v", l)
	}

	// Bad policies are refused.
	sp := base + "/support/sla-policies/default"
	owner.must(400, "PUT", sp, map[string]any{"first_reply_minutes": 0})
	owner.must(400, "PUT", sp, map[string]any{"first_reply_minutes": 60, "timezone": "Mars/Olympus"})
	owner.must(400, "PUT", sp, map[string]any{"first_reply_minutes": 60, "hours": [][]any{{}, {}, {}, {}, {}, {}, {}}})
	owner.must(404, "PUT", base+"/support/sla-policies/no-such-brand", map[string]any{"first_reply_minutes": 60})

	// An hour to answer, 30 minutes for each answer after that, a day to
	// resolve, around the clock. Saving it sets the open conversation's
	// clock too.
	p := owner.must(200, "PUT", sp, map[string]any{"first_reply_minutes": 60, "next_reply_minutes": 30, "resolution_minutes": 1440})
	if p["enabled"] != true || p["brand_id"] == nil || p["timezone"] != "UTC" || p["hours"] != nil || p["first_reply_minutes"] != 60.0 {
		t.Fatalf("policy: %v", p)
	}
	if s := sla(before); s["reply_due_at"] == nil || s["resolve_due_at"] == nil {
		t.Fatalf("recomputed: %v", s)
	}

	conv := email("Charged twice", "I was charged twice.")
	cb := base + "/support/conversations/" + conv
	start := time.Now()
	s := sla(conv)
	if !near(due(s["reply_due_at"]), start.Add(time.Hour)) || !near(due(s["resolve_due_at"]), start.Add(24*time.Hour)) || s["reply_breached_at"] != nil {
		t.Fatalf("due: %v", s)
	}

	// Answering stops the reply clock; the resolution clock runs on.
	owner.must(201, "POST", cb+"/replies", map[string]any{"body": "Refunded."})
	if s := sla(conv); s["reply_due_at"] != nil || s["resolve_due_at"] == nil {
		t.Fatalf("answered: %v", s)
	}
	// Their next email gives the team 30 minutes.
	email("Re: Charged twice", "Thanks, when will it arrive?")
	if s := sla(conv); !near(due(s["reply_due_at"]), time.Now().Add(30*time.Minute)) {
		t.Fatalf("next reply: %v", s)
	}

	// The due time passes: the check marks it missed and records it, once.
	if _, err := e.pool.Exec(ctx, `UPDATE support_conversations SET reply_due_at = now() - interval '1 minute' WHERE id = $1`, conv); err != nil {
		t.Fatal(err)
	}
	watch := &support.SLAWatch{Pool: e.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for range 2 {
		if err := watch.Check(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if s := sla(conv); s["reply_breached_at"] == nil || s["resolve_breached_at"] != nil {
		t.Fatalf("breached: %v", s)
	}
	if n := len(named(got(), "support.sla_breached")); n != 1 {
		t.Fatalf("breach events: %d", n)
	}
	l := owner.must(200, "GET", base+"/support/conversations?sla=overdue", nil)
	if l["total"] != 1.0 || l["data"].([]any)[0].(map[string]any)["id"] != conv || l["counts"].(map[string]any)["overdue"] != 1.0 {
		t.Fatalf("overdue: %v", l)
	}
	owner.must(400, "GET", base+"/support/conversations?sla=late", nil)
	if l := owner.must(200, "GET", base+"/support/conversations?sort=due", nil)["data"].([]any); l[0].(map[string]any)["id"] != conv {
		t.Fatalf("due first: %v", l)
	}
	if st := owner.must(200, "GET", base+"/support/stats", nil); st["sla_missed"] != 1.0 {
		t.Fatalf("stats: %v", st)
	}

	// Closing it stops both clocks and clears the miss from the inbox.
	owner.must(200, "PATCH", cb, map[string]any{"status": "closed"})
	if s := sla(conv); s["reply_due_at"] != nil || s["resolve_due_at"] != nil || s["reply_breached_at"] != nil {
		t.Fatalf("closed: %v", s)
	}
	if c := owner.must(200, "GET", base+"/support/conversations", nil)["counts"].(map[string]any); c["overdue"] != 0.0 {
		t.Fatalf("counts after close: %v", c)
	}

	// Turning the SLA off, or removing it, stops every clock.
	owner.must(200, "PUT", sp, map[string]any{"enabled": false, "first_reply_minutes": 60})
	if s := sla(before); s["reply_due_at"] != nil {
		t.Fatalf("off: %v", s)
	}
	owner.must(204, "DELETE", sp, nil)
	owner.must(404, "DELETE", sp, nil)
	for _, name := range []string{"support.sla_updated", "support.sla_removed"} {
		if len(named(got(), name)) == 0 {
			t.Errorf("no %s event", name)
		}
	}
}
