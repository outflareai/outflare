package api_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/inbox"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestReplyRules(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, base, _ := sendingSetup(t, e, &fakeClock{t: time.Now()})
	ws := strings.TrimPrefix(base, "/v1/workspaces/")
	ctx := context.Background()
	mb := mailboxIDs(t, owner, base)["ana@acme.test"]

	_, imp := owner.raw("POST", base+"/leads/import?list=Pilot", "text/csv", "email,first_name,company\nkim@prospect.test,Kim,Prospect\nlou@prospect.test,Lou,Prospect\n")
	listID := imp["list_id"].(string)
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{
		"name": "Pilot", "steps": []map[string]any{{"subject": "Hello", "body": "Hi"}, {"delay_days": 2, "body": "Following up"}},
	})["id"].(string)
	other := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "Referrals", "steps": []map[string]any{{"subject": "Hi", "body": "Hi"}}})["id"].(string)
	owner.must(200, "POST", base+"/sequences/"+seq+"/enrollments", map[string]any{"list_id": listID})

	// The defaults apply until the workspace chooses.
	rules := owner.must(200, "GET", base+"/reply-rules", nil)
	if d := rules["defaults"].(map[string]any)["not_now"].(map[string]any); d["action"] != "snooze" || d["days"] != float64(90) {
		t.Fatalf("defaults: %v", rules)
	}
	owner.must(400, "PUT", base+"/reply-rules", map[string]any{"rules": map[string]any{"not_now": map[string]any{"action": "delete_everything"}}})
	owner.must(400, "PUT", base+"/reply-rules", map[string]any{"rules": map[string]any{"interested": map[string]any{"action": "snooze"}}})
	owner.must(400, "PUT", base+"/reply-rules", map[string]any{"rules": map[string]any{"referral": map[string]any{"action": "add_and_enroll"}}})
	owner.must(200, "PUT", base+"/reply-rules", map[string]any{"rules": map[string]any{"referral": map[string]any{"action": "add"}}})
	// The sequence restarts its not-now leads after 30 days instead.
	owner.must(200, "PUT", base+"/reply-rules", map[string]any{"sequence_id": seq, "rules": map[string]any{"not_now": map[string]any{"action": "restart", "days": 30}}})
	if n := len(owner.must(200, "GET", base+"/reply-rules", nil)["data"].([]any)); n != 2 {
		t.Fatalf("sets: %d", n)
	}

	reply := func(from string, inSequence bool, details string) string {
		t.Helper()
		var id string
		err := e.pool.QueryRow(ctx, `
			INSERT INTO inbound_messages (workspace_id, mailbox_id, uidvalidity, uid, from_email, kind, body, lead_id, enrollment_id, sequence_id, details, received_at)
			SELECT $1, $2, 0, nextval('inbound_external_uid_seq'), $3, 'reply', 'thanks', l.id,
				CASE WHEN $4 THEN en.id END, CASE WHEN $4 THEN en.sequence_id END, $5::jsonb, now()
			FROM leads l LEFT JOIN enrollments en ON en.lead_id = l.id AND en.sequence_id = $6
			WHERE l.workspace_id = $1 AND l.email = $3 RETURNING id`, ws, mb, from, inSequence, details, seq).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	rr := &inbox.Rules{Pool: e.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	label := func(id, category string) {
		t.Helper()
		owner.must(200, "PATCH", base+"/inbox/"+id, map[string]any{"category": category})
		var n int
		e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = $1 AND idempotency_key = $2`, inbox.JobRules, "rules:"+id+":"+category).Scan(&n)
		if n != 1 {
			t.Fatalf("rules not queued for %s", category)
		}
		if err := rr.Apply(ctx, ws, id, category); err != nil {
			t.Fatal(err)
		}
	}

	// Not now, in the sequence: it picks up again in 30 days.
	if _, err := e.pool.Exec(ctx, `UPDATE enrollments SET status = 'replied', next_send_at = NULL WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	kim := reply("kim@prospect.test", true, `{}`)
	label(kim, "not_now")
	var status string
	var next time.Time
	e.pool.QueryRow(ctx, `SELECT e.status, e.next_send_at FROM enrollments e JOIN leads l ON l.id = e.lead_id WHERE l.email = 'kim@prospect.test'`).Scan(&status, &next)
	if status != "active" || next.Before(time.Now().Add(29*24*time.Hour)) || next.After(time.Now().Add(31*24*time.Hour)) {
		t.Fatalf("restart: %s %v", status, next)
	}

	// Not now, outside it, with a date: snoozed until then.
	lou := reply("lou@prospect.test", false, `{"return_date": "`+time.Now().AddDate(0, 2, 0).Format(time.DateOnly)+`"}`)
	label(lou, "not_now")
	m := owner.must(200, "GET", base+"/inbox/"+lou, nil)["message"].(map[string]any)
	if s, _ := m["snoozed_until"].(string); !strings.HasPrefix(s, time.Now().AddDate(0, 2, 0).Format(time.DateOnly)) {
		t.Fatalf("snooze: %v", m["snoozed_until"])
	}

	// A referral: the person becomes a lead on the referrer's list.
	ref := reply("kim@prospect.test", false, `{"referral_email": "Sam@Prospect.test", "referral_name": "Sam Lee"}`)
	label(ref, "referral")
	var referredBy, first string
	var onList bool
	e.pool.QueryRow(ctx, `SELECT l.custom->>'referred_by', l.first_name, EXISTS (SELECT 1 FROM lead_list_members m WHERE m.lead_id = l.id AND m.list_id = $2)
		FROM leads l WHERE l.workspace_id = $1 AND l.email = 'sam@prospect.test'`, ws, listID).Scan(&referredBy, &first, &onList)
	if referredBy != "kim@prospect.test" || first != "Sam" || !onList {
		t.Fatalf("referral lead: %q %q %v", referredBy, first, onList)
	}

	// A label waiting for review runs nothing until a person confirms it.
	wait := reply("lou@prospect.test", false, `{"referral_email": "max@prospect.test"}`)
	if _, err := e.pool.Exec(ctx, `UPDATE inbound_messages SET category = 'referral', needs_review = true WHERE id = $1`, wait); err != nil {
		t.Fatal(err)
	}
	if err := rr.Apply(ctx, ws, wait, "referral"); err != nil {
		t.Fatal(err)
	}
	var max int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM leads WHERE workspace_id = $1 AND email = 'max@prospect.test'`, ws).Scan(&max)
	if max != 0 {
		t.Fatal("acted on a reply waiting for review")
	}
	owner.must(200, "PATCH", base+"/inbox/"+wait, map[string]any{"needs_review": false})
	var queued int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = $1 AND idempotency_key = $2`, inbox.JobRules, "rules:"+wait+":referral").Scan(&queued)
	if queued != 1 {
		t.Fatal("confirming did not queue the rules")
	}

	// With "suggest", a person adds the referral from the inbox, and can
	// enrol them at once.
	owner.must(200, "PUT", base+"/reply-rules", map[string]any{"rules": map[string]any{}})
	sug := reply("kim@prospect.test", false, `{"referral_email": "pat@prospect.test"}`)
	label(sug, "referral")
	added := owner.must(200, "POST", base+"/inbox/"+sug+"/referral", map[string]any{"sequence_id": other})
	if added["created"] != true || added["enrolled"] != true || added["email"] != "pat@prospect.test" {
		t.Fatalf("added: %v", added)
	}
	none := reply("kim@prospect.test", false, `{}`)
	owner.must(422, "POST", base+"/inbox/"+none+"/referral", map[string]any{})

	ev := got()
	if n := len(named(ev, "inbox.rule_applied")); n != 3 {
		t.Fatalf("inbox.rule_applied: %d", n)
	}
	if n := len(named(ev, "lead.referral_added")); n != 2 {
		t.Fatalf("lead.referral_added: %d", n)
	}
	if n := len(named(ev, "reply_rules.updated")); n != 3 {
		t.Fatalf("reply_rules.updated: %d", n)
	}
}
