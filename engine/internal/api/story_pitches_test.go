package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/stories"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestStoryPitches(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	e.runServerJobs(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	out := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Agent", "scopes": []string{"sequences.read", "sequences.manage", "seo.read", "seo.manage"}})
	agent := &client{t: t, h: e.h, bearer: out["token"].(string)}
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})
	owner.must(201, "POST", base+"/pr-experts", map[string]any{"brand_id": "northpeak", "name": "Ana Ruiz", "email": "ana@northpeak.example", "title": "CEO"})

	// A story, its fact and an approved quote.
	embargo := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	sid := owner.must(201, "POST", base+"/stories", map[string]any{"brand_id": "northpeak", "title": "A third of sites block their own customers' bots",
		"release": "Northpeak measured 1,200 sites. A third block their own customers' bots.", "embargo_until": embargo})["id"].(string)
	path := base + "/stories/" + sid
	owner.must(201, "POST", path+"/facts", map[string]any{"statement": "We measured 1,200 sites", "source_kind": "own_data", "source_note": "Crawl, October"})

	// Journalists: one with an address, one without.
	journalist := func(name, email string) string {
		body := map[string]any{"type": "person", "name": name}
		if email != "" {
			body["identities"] = []any{map[string]any{"namespace": "email", "value": email}}
		}
		return owner.must(201, "POST", base+"/records", body)["id"].(string)
	}
	j1, j2 := journalist("Kim Lee", "kim@news.example"), journalist("Sam Cole", "")

	// The sequence: stops on reply, at most one follow-up, uses the opener.
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "Pitches", "steps": []map[string]any{
		{"subject": "A story for you", "body": "Hi {{first_name|there}},\n\n{{custom.opener}}\n\nThanks"}}})["id"].(string)
	long := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "Too long", "steps": []map[string]any{
		{"subject": "a", "body": "{{custom.opener}}"}, {"body": "b", "delay_days": 3}, {"body": "c", "delay_days": 3}}})["id"].(string)

	// Only an approved story is pitched.
	owner.must(409, "POST", path+"/pitches", map[string]any{"sequence_id": seq, "record_ids": []string{j1}})
	owner.must(200, "POST", path+"/approve", nil)
	owner.must(400, "POST", path+"/pitches", map[string]any{"sequence_id": long, "record_ids": []string{j1}})
	owner.must(400, "POST", path+"/pitches", map[string]any{"sequence_id": seq, "record_ids": []string{}})
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})
	res := owner.must(201, "POST", path+"/pitches", map[string]any{"sequence_id": seq, "record_ids": []string{j1, j2, j1}})
	if len(res["data"].([]any)) != 1 || len(res["skipped"].([]any)) != 1 || res["skipped"].([]any)[0].(map[string]any)["reason"] != "no_email" {
		t.Fatalf("started: %v", res)
	}
	pid := res["data"].([]any)[0].(map[string]any)["id"].(string)
	if again := owner.must(201, "POST", path+"/pitches", map[string]any{"sequence_id": seq, "record_ids": []string{j1}}); again["skipped"].([]any)[0].(map[string]any)["reason"] != "already_pitched" {
		t.Fatalf("pitched twice: %v", again)
	}

	// The AI's opener waits for a person; it passes the check.
	pitches := waitFor(t, owner, base+"/story-pitches?story_id="+sid, func(m map[string]any) bool {
		return m["data"].([]any)[0].(map[string]any)["status"] == "draft"
	})["data"].([]any)
	draft := pitches[0].(map[string]any)
	if draft["opener"] == "" || draft["reason"] != "" || draft["journalist_name"] != "Kim Lee" {
		t.Fatalf("draft: %v", draft)
	}
	ppath := base + "/story-pitches/" + pid
	agent.must(403, "POST", ppath+"/approve", nil)
	if c := owner.must(409, "POST", ppath+"/approve", map[string]any{"opener": "52% of sites block bots."})["error"].(map[string]any)["code"]; c != "check_failed" {
		t.Fatalf("an unsourced number: %v", c)
	}
	sent := owner.must(200, "POST", ppath+"/approve", nil)
	if sent["status"] != "enrolled" || sent["lead_id"] == nil {
		t.Fatalf("approved: %v", sent)
	}
	// Held until the embargo lifts.
	var next time.Time
	if err := e.pool.QueryRow(ctx, `SELECT next_send_at FROM enrollments WHERE lead_id = $1 AND sequence_id = $2`, sent["lead_id"], seq).Scan(&next); err != nil ||
		next.Before(time.Now().Add(47*time.Hour)) {
		t.Fatalf("enrollment: %v %v", next, err)
	}

	// The journalist replies; the AI reads it as wanting the data.
	var mb, inbound string
	if err := e.pool.QueryRow(ctx, `INSERT INTO mailboxes (workspace_id, email, display_name, provider, smtp_host, smtp_port, smtp_username, imap_host, imap_port,
		imap_username, credentials, status, daily_limit, source, has_login)
		VALUES ($1, 'pr@example.com', '', 'other', '', 0, '', '', 0, '', '\x', 'active', 10, 'emailbison', false) RETURNING id`, ws).Scan(&mb); err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO inbound_messages (workspace_id, mailbox_id, uidvalidity, uid, from_email, kind, body, lead_id, sequence_id, received_at)
			VALUES ($1, $2, 0, nextval('inbound_external_uid_seq'), 'kim@news.example', 'reply', 'Can you send the spreadsheet?', $3, $4, now())
			RETURNING id`, ws, mb, sent["lead_id"], seq).Scan(&inbound); err != nil {
			return err
		}
		return stories.Replied(ctx, tx, ws, sent["lead_id"].(string), inbound)
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, owner, base+"/story-pitches?story_id="+sid+"&status=enrolled", func(m map[string]any) bool {
		return m["data"].([]any)[0].(map[string]any)["reply_kind"] == "wants_data"
	})
	var labels []string
	var review bool
	if err := e.pool.QueryRow(ctx, `SELECT labels, needs_review FROM inbound_messages WHERE id = $1`, inbound).Scan(&labels, &review); err != nil ||
		len(labels) != 1 || labels[0] != "pr: wants the data" || !review {
		t.Fatalf("reply: %v %v %v", labels, review, err)
	}

	// Kim writes about it: the coverage counts for the story.
	outlet := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "News Example"})["id"].(string)
	var cov string
	if err := e.pool.QueryRow(ctx, `WITH a AS (INSERT INTO news_articles (workspace_id, url, title) VALUES ($1, 'https://news.example/bots', 'Sites block bots')
			RETURNING id), au AS (INSERT INTO news_article_authors (article_id, workspace_id, record_id) SELECT id, $1, $2 FROM a)
		INSERT INTO coverage (workspace_id, brand_id, article_id, record_id, source, linked)
		SELECT $1, (SELECT id FROM brands WHERE handle = 'northpeak'), a.id, $3, 'news_watch', true FROM a RETURNING id`, ws, j1, outlet).Scan(&cov); err != nil {
		t.Fatal(err)
	}
	results := owner.must(200, "GET", base+"/stories/results?brand=northpeak", nil)
	total := results["total"].(map[string]any)
	if total["pitched"] != 1.0 || total["replied"] != 1.0 || total["coverage"] != 1.0 || total["linked"] != 1.0 ||
		total["reply_kinds"].(map[string]any)["wants_data"] != 1.0 || len(results["data"].([]any)) != 1 {
		t.Fatalf("results: %v", results)
	}
	owner.must(400, "GET", base+"/stories/results?days=0", nil)
	if c := owner.must(200, "PATCH", base+"/coverage/"+cov, map[string]any{"story_id": sid}); c["story_id"] != sid {
		t.Fatalf("coverage on a story: %v", c)
	}
	owner.must(400, "PATCH", base+"/coverage/"+cov, map[string]any{"story_id": "00000000-0000-0000-0000-000000000000"})

	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
	}
	if names["story_pitch.started"] != 1 || names["story_pitch.written"] != 1 || names["story_pitch.approved"] != 1 || names["story_pitch.reply_read"] != 1 {
		t.Fatalf("events: %v", names)
	}
}
