package api_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/health"
)

func TestMailboxHealthAndAutoPause(t *testing.T) {
	e := freshEnv(t)
	clock := &fakeClock{t: time.Now()}
	owner, base, _ := sendingSetup(t, e, clock)
	ctx := context.Background()
	ws := base[len("/v1/workspaces/"):]

	var ana, bo string
	for _, m := range owner.must(200, "GET", base+"/mailboxes", nil)["data"].([]any) {
		mm := m.(map[string]any)
		if mm["email"] == "ana@acme.test" {
			ana = mm["id"].(string)
		} else {
			bo = mm["id"].(string)
		}
	}

	// Ana sent 40 emails today and 4 bounced (10%).
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "Health", "steps": []map[string]any{{"subject": "Hi", "body": "Hello"}}})["id"].(string)
	if _, err := e.pool.Exec(ctx, `
		INSERT INTO messages (workspace_id, mailbox_id, from_email, to_email, subject, body, message_id, status, sent_at)
		SELECT $1, $2, 'ana@acme.test', 'lead' || g || '@x.test', 'Hi', 'Hello', 'm' || g, 'sent', now() FROM generate_series(1, 40) g`, ws, ana); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `
		WITH l AS (INSERT INTO leads (workspace_id, email) SELECT $1, 'b' || g || '@x.test' FROM generate_series(1, 4) g RETURNING id)
		INSERT INTO enrollments (workspace_id, sequence_id, lead_id, mailbox_id, status) SELECT $1, $2, id, $3, 'bounced' FROM l`, ws, seq, ana); err != nil {
		t.Fatal(err)
	}
	// Bo can't sign in any more.
	if _, err := e.pool.Exec(ctx, `UPDATE mailboxes SET status = 'error', status_reason = '535-5.7.8 Username and Password not accepted' WHERE id = $1`, bo); err != nil {
		t.Fatal(err)
	}

	out := owner.must(200, "GET", base+"/mailbox-health", nil)
	sum := out["summary"].(map[string]any)
	if sum["critical"] != 2.0 || sum["total"] != 2.0 {
		t.Fatalf("summary: %v", out)
	}
	byEmail := map[string]map[string]any{}
	for _, m := range out["data"].([]any) {
		byEmail[m.(map[string]any)["email"].(string)] = m.(map[string]any)
	}
	if s := firstSignal(byEmail["ana@acme.test"]); s["key"] != "bounce_rate" || s["level"] != "critical" {
		t.Fatalf("ana: %v", byEmail["ana@acme.test"])
	}
	if s := firstSignal(byEmail["bo@acme.test"]); s["key"] != "sign_in" || s["title"] != "Password or app password rejected" {
		t.Fatalf("bo: %v", byEmail["bo@acme.test"])
	}

	// The monitor pauses Ana and says why.
	mon := &health.Monitor{Pool: e.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	paused, err := mon.Tick(ctx)
	if err != nil || len(paused) != 1 || paused[0].Email != "ana@acme.test" {
		t.Fatalf("tick: %v %v", paused, err)
	}
	h := owner.must(200, "GET", base+"/mailbox-health?mailbox_id="+ana, nil)["data"].([]any)[0].(map[string]any)
	if h["level"] != "paused" || firstSignal(h)["key"] != "paused" {
		t.Fatalf("after pause: %v", h)
	}
	events := owner.must(200, "GET", base+"/audit-events", nil)["data"].([]any)
	if events[0].(map[string]any)["action"] != "mailbox.auto_paused" {
		t.Fatalf("audit: %v", events[0])
	}

	// Turned off, nothing is paused.
	owner.must(200, "PUT", base+"/mailbox-health/settings", map[string]any{"auto_pause": false})
	if _, err := e.pool.Exec(ctx, `UPDATE mailboxes SET status = 'active' WHERE id = $1`, ana); err != nil {
		t.Fatal(err)
	}
	if paused, _ := mon.Tick(ctx); len(paused) != 0 {
		t.Fatalf("paused with auto_pause off: %v", paused)
	}
	owner.must(400, "PUT", base+"/mailbox-health/settings", map[string]any{"pause_bounce_rate": 0})
}

func firstSignal(m map[string]any) map[string]any {
	s := m["signals"].([]any)
	if len(s) == 0 {
		return map[string]any{}
	}
	return s[0].(map[string]any)
}

func TestExplainProviderErrors(t *testing.T) {
	for in, want := range map[string]string{
		"550 5.7.1 Your account has been disabled":                                                     "Account suspended or disabled",
		"550 5.4.5 Daily user sending quota exceeded":                                                  "Provider's sending limit reached",
		"535 5.7.139 Authentication unsuccessful, SmtpClientAuthentication is disabled for the Tenant": "SMTP or IMAP access is turned off",
		"oauth2: invalid_grant Token has been expired or revoked":                                      "Sign-in permission expired or revoked",
		"dial tcp: i/o timeout":                                                                        "Can't reach the mail server",
	} {
		if got, _ := health.Explain(in, "google"); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// A mailbox that sends through a connected sequencer is counted from what
// the sequencer reports (mailbox_platform_days), on top of Outflare's own
// sends, in both its health and its activity by day.
func TestMailboxActivityCountsSequencerSends(t *testing.T) {
	e := freshEnv(t)
	clock := &fakeClock{t: time.Now()}
	owner, base, _ := sendingSetup(t, e, clock)
	ctx := context.Background()
	ws := base[len("/v1/workspaces/"):]
	var ana string
	for _, m := range owner.must(200, "GET", base+"/mailboxes", nil)["data"].([]any) {
		if mm := m.(map[string]any); mm["email"] == "ana@acme.test" {
			ana = mm["id"].(string)
		}
	}
	var integration string
	if err := e.pool.QueryRow(ctx, `
		INSERT INTO integrations (workspace_id, connector, name, credentials) VALUES ($1, 'instantly', 'Acme Instantly', '\x00') RETURNING id::text`, ws).Scan(&integration); err != nil {
		t.Fatal(err)
	}
	// Outflare sent 5 today; the sequencer sent 60 over yesterday and
	// today, with 6 bounces and 3 replies.
	if _, err := e.pool.Exec(ctx, `
		INSERT INTO messages (workspace_id, mailbox_id, from_email, to_email, subject, body, message_id, status, sent_at)
		SELECT $1, $2, 'ana@acme.test', 'lead' || g || '@x.test', 'Hi', 'Hello', 'a' || g, 'sent', now() FROM generate_series(1, 5) g`, ws, ana); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `
		INSERT INTO mailbox_platform_days (mailbox_id, integration_id, workspace_id, brand_id, day, sent, replies, bounced)
		SELECT m.id, $2, $3, m.brand_id, (now() AT TIME ZONE 'UTC')::date - v.ago, v.sent, v.replies, v.bounced
		FROM mailboxes m, (VALUES (1, 40, 2, 4), (0, 20, 1, 2), (30, 500, 50, 50)) v(ago, sent, replies, bounced) WHERE m.id = $1`, ana, integration, ws); err != nil {
		t.Fatal(err)
	}

	out := owner.must(200, "GET", base+"/mailboxes/"+ana+"/activity?days=3", nil)["data"].([]any)
	if len(out) != 3 {
		t.Fatalf("days: %v", out)
	}
	day := func(i int) map[string]any { return out[i].(map[string]any) }
	if day(0)["sent"] != 0.0 || day(1)["sent"] != 40.0 || day(1)["bounced"] != 4.0 || day(1)["replies"] != 2.0 ||
		day(2)["sent"] != 25.0 || day(2)["bounced"] != 2.0 || day(2)["replies"] != 1.0 {
		t.Fatalf("activity: %v", out)
	}
	if day(0)["date"].(string) >= day(2)["date"].(string) {
		t.Fatalf("not oldest first: %v", out)
	}
	owner.must(400, "GET", base+"/mailboxes/"+ana+"/activity?days=0", nil)
	owner.must(400, "GET", base+"/mailboxes/"+ana+"/activity?days=91", nil)
	owner.must(404, "GET", base+"/mailboxes/00000000-0000-0000-0000-000000000000/activity", nil)

	// Health adds the last 7 days, not the month-old ones: 65 sent, 6 bounced (9.2%).
	for _, m := range owner.must(200, "GET", base+"/mailbox-health?mailbox_id="+ana, nil)["data"].([]any) {
		st := m.(map[string]any)["stats"].(map[string]any)
		if st["sent_7d"] != 65.0 || st["bounced_7d"] != 6.0 || st["replies_7d"] != 3.0 || st["sent_24h"] != 25.0 {
			t.Fatalf("health stats: %v", st)
		}
		if s := firstSignal(m.(map[string]any)); s["key"] != "bounce_rate" {
			t.Fatalf("first signal: %v", s)
		}
	}
}
