package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/linkbuild"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestLinkPitchesFromOpenerToLive(t *testing.T) {
	e := freshEnv(t)
	owner, _ := setupOwner(t, e.h)
	events := track.Capture(t)
	ctx := context.Background()
	ws := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Sandbox", "test": true})["id"].(string)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})
	siteID := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.example.com/"})["id"].(string)
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "Link pitches", "steps": []map[string]any{
		{"subject": "Your hiking links", "body": "Hi {{first_name|there}},\n\n{{custom.opener}}\n\nThanks"}}})["id"].(string)
	svc := e.runLinkChecks(t)

	// A checked prospect with a contact on its contact page.
	p := owner.must(200, "POST", base+"/link-prospects", map[string]any{"site_id": siteID, "prospects": []any{
		map[string]any{"page_url": "https://blog.example.org/links", "method": "broken_link"}}})["data"].([]any)[0].(map[string]any)
	pid := p["id"].(string)
	owner.must(202, "POST", base+"/link-prospects/"+pid+"/check", nil)
	waitFor(t, owner, base+"/link-prospects/"+pid, func(m map[string]any) bool { return len(m["contacts"].([]any)) > 0 })

	// The sequence's pitches wait for approval by default.
	seqs := owner.must(200, "GET", base+"/link-building/sequences", nil)["data"].([]any)
	if len(seqs) != 1 || seqs[0].(map[string]any)["approve_pitches"] != true || seqs[0].(map[string]any)["uses_opener"] != true {
		t.Fatalf("sequences: %v", seqs)
	}

	// Pitching: the AI writes an opener, and the pitch waits as a draft.
	pitch := owner.must(201, "POST", base+"/link-prospects/"+pid+"/pitches", map[string]any{"sequence_id": seq})
	if pitch["status"] != "writing" || pitch["contact_email"] != "editor@blog.example.org" {
		t.Fatalf("pitch: %v", pitch)
	}
	owner.must(409, "POST", base+"/link-prospects/"+pid+"/pitches", map[string]any{"sequence_id": seq})
	path := base + "/link-pitches/" + pitch["id"].(string)
	draft := waitFor(t, owner, path, func(m map[string]any) bool { return m["status"] == "draft" })
	if draft["opener"] == "" || draft["reason"] != "" {
		t.Fatalf("draft: %v", draft)
	}
	owner.must(400, "PATCH", path, map[string]any{"opener": ""})
	owner.must(200, "PATCH", path, map[string]any{"opener": "Your hiking pricing page has a broken tool link; our pack guide could replace it."})

	// A draft is counted as waiting, for the sidebar and for agents.
	if st := owner.must(200, "GET", base+"/stats", nil); st["link_pitches_waiting"] != 1.0 {
		t.Errorf("stats waiting = %v", st["link_pitches_waiting"])
	}
	if b := blocker(owner, base, "link_pitches_waiting"); b == nil || b["count"] != 1.0 || b["area"] != "sequences" {
		t.Errorf("blocker = %v", b)
	}

	// Approving enrolls the contact's lead with the opener.
	sent := owner.must(200, "POST", path+"/approve", nil)
	if sent["status"] != "enrolled" || sent["lead_id"] == nil || sent["stage"] != "pitched" {
		t.Fatalf("approved: %v", sent)
	}
	owner.must(409, "POST", path+"/approve", nil)
	if st := owner.must(200, "GET", base+"/stats", nil); st["link_pitches_waiting"] != 0.0 {
		t.Errorf("stats waiting after approval = %v", st["link_pitches_waiting"])
	}
	if b := blocker(owner, base, "link_pitches_waiting"); b != nil {
		t.Errorf("blocker after approval = %v", b)
	}
	var opener string
	var enrolled int
	if err := e.pool.QueryRow(ctx, `SELECT l.custom->>'opener', (SELECT count(*) FROM enrollments n WHERE n.lead_id = l.id AND n.sequence_id = $2)
		FROM leads l WHERE l.id = $1`, sent["lead_id"], seq).Scan(&opener, &enrolled); err != nil || enrolled != 1 ||
		opener != "Your hiking pricing page has a broken tool link; our pack guide could replace it." {
		t.Fatalf("lead: %q %d %v", opener, enrolled, err)
	}

	// A reply makes it replied; the AI reads it as a yes, so it is agreed
	// and the reply is tagged.
	var mb, inbound string
	if err := e.pool.QueryRow(ctx, `INSERT INTO mailboxes (workspace_id, email, display_name, provider, smtp_host, smtp_port, smtp_username, imap_host, imap_port,
		imap_username, credentials, status, daily_limit, source, has_login)
		VALUES ($1, 'pitch@example.com', '', 'other', '', 0, '', '', 0, '', '\x', 'active', 10, 'emailbison', false) RETURNING id`, ws).Scan(&mb); err != nil {
		t.Fatal(err)
	}
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO inbound_messages (workspace_id, mailbox_id, uidvalidity, uid, from_email, kind, body, lead_id, sequence_id, received_at)
			VALUES ($1, $2, 0, nextval('inbound_external_uid_seq'), 'editor@blog.example.org', 'reply', 'Happy to add it this week.', $3, $4, now())
			RETURNING id`, ws, mb, sent["lead_id"], seq).Scan(&inbound); err != nil {
			return err
		}
		return linkbuild.Replied(ctx, tx, ws, sent["lead_id"].(string), inbound)
	})
	if err != nil {
		t.Fatal(err)
	}
	agreed := waitFor(t, owner, base+"/link-prospects/"+pid, func(m map[string]any) bool { return m["stage"] == "agreed" })
	if agreed["agreed_at"] == nil {
		t.Fatalf("agreed: %v", agreed)
	}
	var labels []string
	if err := e.pool.QueryRow(ctx, `SELECT labels FROM inbound_messages WHERE id = $1`, inbound).Scan(&labels); err != nil || len(labels) != 1 || labels[0] != "link: agreed" {
		t.Fatalf("labels: %v %v", labels, err)
	}
	if k := owner.must(200, "GET", path, nil); k["reply_kind"] != "agreed" || k["replied_at"] == nil {
		t.Fatalf("pitch after reply: %v", k)
	}

	// The live check: not yet; then the page links to the site, so it is
	// live; and when it stops, removed.
	recheck := func(page string) map[string]any {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `UPDATE link_prospects SET page_url = $2 WHERE id = $1`, pid, page); err != nil {
			t.Fatal(err)
		}
		if _, err := e.pool.Exec(ctx, `UPDATE link_page_checks SET checked_at = now() - interval '8 days' WHERE prospect_id = $1`, pid); err != nil {
			t.Fatal(err)
		}
		before := owner.must(200, "GET", base+"/link-prospects/"+pid, nil)["check"].(map[string]any)["checked_at"]
		if err := svc.Schedule(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
		return waitFor(t, owner, base+"/link-prospects/"+pid, func(m map[string]any) bool { return m["check"].(map[string]any)["checked_at"] != before })
	}
	if p := recheck("https://blog.example.org/links"); p["stage"] != "agreed" || p["check"].(map[string]any)["fit"] == nil {
		t.Fatalf("not yet live (the earlier read is kept): %v", p)
	}
	if p := recheck("https://blog.example.org/resources"); p["stage"] != "live" || p["live_at"] == nil {
		t.Fatalf("live: %v", p)
	}
	if p := recheck("https://blog.example.org/links"); p["stage"] != "removed" {
		t.Fatalf("removed: %v", p)
	}

	// An agreed link not live after 14 days is announced once.
	if _, err := e.pool.Exec(ctx, `UPDATE link_prospects SET stage = 'agreed', agreed_at = now() - interval '15 days' WHERE id = $1`, pid); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := svc.Schedule(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	var overdue int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'link_prospect.overdue'`, ws).Scan(&overdue); err != nil || overdue != 1 {
		t.Fatalf("overdue: %d %v", overdue, err)
	}
	if m := owner.must(200, "GET", base+"/link-prospects/"+pid, nil); m["overdue_at"] == nil {
		t.Errorf("overdue_at = %v", m["overdue_at"])
	}

	// Results by method, and live links in the Outcomes report.
	if _, err := e.pool.Exec(ctx, `UPDATE link_prospects SET stage = 'live', live_at = now() WHERE id = $1`, pid); err != nil {
		t.Fatal(err)
	}
	res := owner.must(200, "GET", base+"/link-prospects/results", nil)
	if tot := res["totals"].(map[string]any); tot["pitched"].(float64) != 1 || tot["live"].(float64) != 1 || tot["win_rate"].(float64) != 1 {
		t.Fatalf("results: %v", res)
	}
	rep := owner.must(200, "GET", base+"/outcomes/report?group_by=sequence", nil)
	if rep["totals"].(map[string]any)["live_links"].(float64) != 1 {
		t.Fatalf("report: %v", rep["totals"])
	}

	// Skipping approval: the next pitch goes out once its opener is written.
	owner.must(200, "PATCH", base+"/link-building/sequences/"+seq, map[string]any{"approve_pitches": false})
	q := owner.must(200, "POST", base+"/link-prospects", map[string]any{"site_id": siteID, "prospects": []any{
		map[string]any{"page_url": "https://other.example.net/links", "method": "resource_page"}}})["data"].([]any)[0].(map[string]any)
	owner.must(202, "POST", base+"/link-prospects/"+q["id"].(string)+"/check", nil)
	waitFor(t, owner, base+"/link-prospects/"+q["id"].(string), func(m map[string]any) bool { return len(m["contacts"].([]any)) > 0 })
	auto := owner.must(201, "POST", base+"/link-prospects/"+q["id"].(string)+"/pitches", map[string]any{"sequence_id": seq})
	waitFor(t, owner, base+"/link-pitches/"+auto["id"].(string), func(m map[string]any) bool { return m["status"] == "enrolled" })

	got := map[string]bool{}
	for _, ev := range events() {
		got[ev.Name] = true
	}
	for _, name := range []string{"link_pitch.started", "link_pitch.drafted", "link_pitch.edited", "link_pitch.approved", "link_prospect.pitched",
		"link_prospect.replied", "link_pitch.reply_read", "link_prospect.agreed", "link_prospect.live", "link_prospect.removed", "link_prospect.overdue",
		"link_building.sequence_updated"} {
		if !got[name] {
			t.Errorf("no %s event", name)
		}
	}
}

// blocker is the workspace status's blocker with code, or nil.
func blocker(c *client, base, code string) map[string]any {
	for _, b := range c.must(200, "GET", base+"/status", nil)["blockers"].([]any) {
		if b := b.(map[string]any); b["code"] == code {
			return b
		}
	}
	return nil
}

func TestLinkStarterSequence(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	events := track.Capture(t)
	base := "/v1/workspaces/" + ws

	// The starter is a draft sequence that uses the opener, with approval on.
	q := owner.must(201, "POST", base+"/link-building/sequences", nil)
	if q["name"] != "Link pitches" || q["status"] != "draft" || q["uses_opener"] != true || q["approve_pitches"] != true || q["pitches"] != 0.0 {
		t.Fatalf("starter: %v", q)
	}
	steps := owner.must(200, "GET", base+"/sequences/"+q["sequence_id"].(string), nil)["steps"].([]any)
	if len(steps) != 2 || !strings.Contains(steps[0].(map[string]any)["body"].(string), "{{custom.opener}}") ||
		steps[1].(map[string]any)["delay_days"] != 5.0 || steps[1].(map[string]any)["subject"] != "" {
		t.Fatalf("steps: %v", steps)
	}
	named := owner.must(201, "POST", base+"/link-building/sequences", map[string]any{"name": "  Outreach to bloggers "})
	if named["name"] != "Outreach to bloggers" {
		t.Fatalf("named: %v", named)
	}
	owner.must(400, "POST", base+"/link-building/sequences", map[string]any{"name": strings.Repeat("x", 201)})
	if list := owner.must(200, "GET", base+"/link-building/sequences", nil)["data"].([]any); len(list) != 2 {
		t.Fatalf("listed: %v", list)
	}

	var starters int
	for _, ev := range events() {
		if ev.Name == "sequence.created" && ev.Props["starter"] == "link_pitches" {
			starters++
		}
	}
	if starters != 2 {
		t.Errorf("sequence.created starter events = %d", starters)
	}
}
