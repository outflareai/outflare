package api_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/inbox"
)

// Automation stops when the AI is unsure: a label below the sure line runs
// no reply rule (no snooze, no restart, no move), raises no alert, and
// waits for a person. Once a person confirms it, the rules run once.
func TestUnsureLabelStopsAutomationUntilAPersonConfirms(t *testing.T) {
	e := freshEnv(t)
	owner, base, _ := sendingSetup(t, e, &fakeClock{t: time.Now()})
	ws := base[len("/v1/workspaces/"):]
	ctx := context.Background()
	mb := mailboxIDs(t, owner, base)["ana@acme.test"]
	_, imp := owner.raw("POST", base+"/leads/import?list=Pilot", "text/csv", "email\nkim@prospect.test\n")
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{
		"name": "Pilot", "steps": []map[string]any{{"subject": "Hello", "body": "Hi"}, {"delay_days": 2, "body": "Following up"}},
	})["id"].(string)
	owner.must(200, "POST", base+"/sequences/"+seq+"/enrollments", map[string]any{"list_id": imp["list_id"]})
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})

	var id string
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO inbound_messages (workspace_id, mailbox_id, uidvalidity, uid, from_email, kind, body, lead_id, enrollment_id, sequence_id, received_at)
			SELECT $1, $2, 0, nextval('inbound_external_uid_seq'), l.email, 'reply', 'Hmm, not sure about the timing.', l.id, en.id, en.sequence_id, now()
			FROM leads l JOIN enrollments en ON en.lead_id = l.id AND en.sequence_id = $3
			WHERE l.workspace_id = $1 AND l.email = 'kim@prospect.test' RETURNING id`, ws, mb, seq).Scan(&id); err != nil {
			return err
		}
		return inbox.QueueLabel(ctx, tx, ws, id)
	}); err != nil {
		t.Fatal(err)
	}
	l := &inbox.Labeller{Pool: e.pool, Models: e.srv.AIModels(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := l.Label(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	rulesQueued := func() (n int) {
		t.Helper()
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = $1 AND idempotency_key = $2`, inbox.JobRules, "rules:"+id+":not_now").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	m := owner.must(200, "GET", base+"/inbox/"+id, nil)["message"].(map[string]any)
	if m["category"] != "not_now" || m["needs_review"] != true || m["snoozed_until"] != nil {
		t.Fatalf("unsure label: %v", m)
	}
	if rulesQueued() != 0 {
		t.Fatal("reply rules queued for a label the AI was unsure of")
	}
	// Even a stray run of the rules does nothing while it waits.
	rr := &inbox.Rules{Pool: e.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := rr.Apply(ctx, ws, id, "not_now"); err != nil {
		t.Fatal(err)
	}
	if m := owner.must(200, "GET", base+"/inbox/"+id, nil)["message"].(map[string]any); m["snoozed_until"] != nil {
		t.Fatalf("snoozed while waiting for a person: %v", m)
	}

	// A person confirms the label: the rules run, once.
	owner.must(200, "PATCH", base+"/inbox/"+id, map[string]any{"needs_review": false})
	if rulesQueued() != 1 {
		t.Fatal("confirming did not queue the rules")
	}
	if err := rr.Apply(ctx, ws, id, "not_now"); err != nil {
		t.Fatal(err)
	}
	if m := owner.must(200, "GET", base+"/inbox/"+id, nil)["message"].(map[string]any); m["snoozed_until"] == nil {
		t.Fatalf("not snoozed after a person confirmed: %v", m)
	}
}
