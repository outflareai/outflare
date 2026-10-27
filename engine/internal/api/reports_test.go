package api_test

import (
	"context"
	"testing"
	"time"
)

func TestReports(t *testing.T) {
	e := freshEnv(t)
	owner, base, _ := sendingSetup(t, e, &fakeClock{t: time.Now()})
	ws := base[len("/v1/workspaces/"):]
	ctx := context.Background()
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "Q4 founders", "steps": []map[string]any{{"subject": "Hi", "body": "A"}, {"delay_days": 2, "body": "B"}}})["id"].(string)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Ten leads; Ana sends all ten their first step, Bo five follow-ups.
	exec(`INSERT INTO leads (workspace_id, email) SELECT $1, 'l' || g || '@x.test' FROM generate_series(1, 10) g`, ws)
	exec(`INSERT INTO enrollments (workspace_id, sequence_id, lead_id, mailbox_id)
		SELECT $1, $2, l.id, (SELECT id FROM mailboxes WHERE email = 'ana@acme.test') FROM leads l WHERE l.workspace_id = $1`, ws, seq)
	exec(`INSERT INTO messages (workspace_id, enrollment_id, sequence_id, step_position, lead_id, mailbox_id, from_email, to_email, subject, body, message_id, status, sent_at)
		SELECT $1, e.id, $2, 0, e.lead_id, e.mailbox_id, 'ana@acme.test', l.email, 'Hi', 'A', 'a-' || l.email, 'sent', now() - interval '2 days'
		FROM enrollments e JOIN leads l ON l.id = e.lead_id WHERE e.sequence_id = $2`, ws, seq)
	exec(`INSERT INTO messages (workspace_id, enrollment_id, sequence_id, step_position, lead_id, mailbox_id, from_email, to_email, subject, body, message_id, status, sent_at)
		SELECT $1, e.id, $2, 1, e.lead_id, (SELECT id FROM mailboxes WHERE email = 'bo@acme.test'), 'bo@acme.test', l.email, '', 'B', 'b-' || l.email, 'sent', now()
		FROM enrollments e JOIN leads l ON l.id = e.lead_id WHERE e.sequence_id = $2 AND l.email IN ('l1@x.test','l2@x.test','l3@x.test','l4@x.test','l5@x.test')`, ws, seq)
	// One bounced, two replied to the first step (one asking to be removed).
	exec(`UPDATE enrollments SET status = 'bounced' WHERE lead_id = (SELECT id FROM leads WHERE email = 'l9@x.test')`)
	exec(`INSERT INTO inbound_messages (workspace_id, mailbox_id, uidvalidity, uid, from_email, kind, opt_out, lead_id, enrollment_id, sequence_id, reply_to_message_id, received_at)
		SELECT $1, m.mailbox_id, 1, row_number() OVER (), m.to_email, 'reply', m.to_email = 'l7@x.test', m.lead_id, m.enrollment_id, m.sequence_id, m.id, now()
		FROM messages m WHERE m.to_email IN ('l6@x.test', 'l7@x.test') AND m.step_position = 0`, ws)

	rep := owner.must(200, "GET", base+"/reports", nil)
	tot := rep["totals"].(map[string]any)
	if tot["sent"] != 15.0 || tot["contacted"] != 10.0 || tot["new_leads"] != 10.0 || tot["follow_ups"] != 5.0 || tot["bounced"] != 1.0 ||
		tot["replied_leads"] != 2.0 || tot["opt_outs"] != 1.0 || tot["reply_rate"] != 20.0 {
		t.Fatalf("totals: %v", tot)
	}

	steps := owner.must(200, "GET", base+"/reports?group_by=step", nil)["rows"].([]any)
	s0 := steps[0].(map[string]any)
	if s0["name"] != "Step 1" || s0["label"] != "Step 1" || s0["sent"] != 10.0 || s0["replies"] != 2.0 || steps[1].(map[string]any)["sent"] != 5.0 {
		t.Fatalf("by step: %v", steps)
	}
	mbs := owner.must(200, "GET", base+"/reports?group_by=mailbox", nil)["rows"].([]any)
	if mbs[0].(map[string]any)["name"] != "ana@acme.test" || mbs[0].(map[string]any)["sent"] != 10.0 {
		t.Fatalf("by mailbox: %v", mbs)
	}
	seqs := owner.must(200, "GET", base+"/reports?group_by=sequence", nil)["rows"].([]any)
	if seqs[0].(map[string]any)["name"] != "Q4 founders" {
		t.Fatalf("by sequence: %v", seqs)
	}
	days := owner.must(200, "GET", base+"/reports?group_by=day&timezone=Australia/Sydney", nil)["rows"].([]any)
	if len(days) < 2 {
		t.Fatalf("by day: %v", days)
	}
	only := owner.must(200, "GET", base+"/reports?domain=ACME.test&group_by=week", nil)["totals"].(map[string]any)
	if only["sent"] != 15.0 {
		t.Fatalf("domain filter: %v", only)
	}
	owner.must(400, "GET", base+"/reports?group_by=planet", nil)
	owner.must(400, "GET", base+"/reports?from=2026-10-10&to=2026-10-01", nil)
}
