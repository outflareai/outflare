package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/inbox"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestAlerts(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, base, _ := sendingSetup(t, e, &fakeClock{t: time.Now()})
	ws := strings.TrimPrefix(base, "/v1/workspaces/")
	ctx := context.Background()
	mb := mailboxIDs(t, owner, base)["ana@acme.test"]
	if _, err := e.pool.Exec(ctx, `UPDATE users SET email_verified_at = now()`); err != nil {
		t.Fatal(err)
	}

	_, imp := owner.raw("POST", base+"/leads/import?list=Pilot", "text/csv", "email,first_name,last_name,company\nkim@prospect.test,Kim,Lee,Prospect\n")
	listID := imp["list_id"].(string)
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "Pilot", "steps": []map[string]any{{"subject": "Hello", "body": "Hi"}}})["id"].(string)
	owner.must(200, "POST", base+"/sequences/"+seq+"/enrollments", map[string]any{"list_id": listID})

	// A reply the AI is sure is interested: an alert, emailed to who made
	// the sequence.
	hot := func() string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, `
			INSERT INTO inbound_messages (workspace_id, mailbox_id, uidvalidity, uid, from_email, kind, body, lead_id, sequence_id, category, received_at)
			SELECT $1, $2, 0, nextval('inbound_external_uid_seq'), l.email, 'reply', 'Sounds good', l.id, $3, 'interested', now()
			FROM leads l WHERE l.workspace_id = $1 AND l.email = 'kim@prospect.test' RETURNING id`, ws, mb, seq).Scan(&id); err != nil {
			t.Fatal(err)
		}
		cat := "interested"
		if err := inbox.Classified(ctx, e.pool, inbox.Classification{WorkspaceID: ws, InboundID: id, SequenceID: &seq, MailboxID: mb,
			Source: "mailbox", Category: &cat, By: "ai", Confidence: 0.95}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	before := e.outbox.count()
	first := hot()
	for i := 0; e.outbox.count() == before && i < 200; i++ {
		time.Sleep(25 * time.Millisecond)
	}
	if e.outbox.count() != before+1 {
		t.Fatal("no alert email")
	}
	e.outbox.mu.Lock()
	m := e.outbox.sent[len(e.outbox.sent)-1]
	e.outbox.mu.Unlock()
	if m.Subject != "Kim Lee at Prospect is interested" || !strings.Contains(m.Text, "/inbox/"+first) || strings.Contains(m.Text, "Sounds good") {
		t.Fatalf("email: %q %q", m.Subject, m.Text)
	}
	list := owner.must(200, "GET", base+"/alerts", nil)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["kind"] != "reply.hot" || list[0].(map[string]any)["path"] != "inbox/"+first {
		t.Fatalf("alerts: %v", list)
	}
	// The same reply labelled again raises nothing new.
	cat := "meeting_booked"
	if err := inbox.Classified(ctx, e.pool, inbox.Classification{WorkspaceID: ws, InboundID: first, MailboxID: mb, Category: &cat, By: "ai", Confidence: 0.9}); err != nil {
		t.Fatal(err)
	}

	// Settings: off for this kind, then back; and muting it for yourself.
	owner.must(400, "PUT", base+"/alerts/settings", map[string]any{"kind": "reply.hot", "email": "everyone"})
	owner.must(400, "PUT", base+"/alerts/settings", map[string]any{"kind": "nope", "email": "off"})
	owner.must(200, "PUT", base+"/alerts/settings", map[string]any{"kind": "reply.hot", "email": "off"})
	hot()
	owner.must(200, "PUT", base+"/alerts/settings", map[string]any{"kind": "reply.hot", "email": nil})
	owner.must(200, "PUT", base+"/alerts/mutes", map[string]any{"kind": "reply.hot", "muted": true})
	hot()
	set := owner.must(200, "GET", base+"/alerts/settings", nil)
	if mu := set["muted"].([]any); len(mu) != 1 || mu[0] != "reply.hot" || len(set["kinds"].([]any)) < 2 {
		t.Fatalf("settings: %v", set)
	}
	var queued int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM alert_deliveries WHERE workspace_id = $1`, ws).Scan(&queued)
	if queued != 1 {
		t.Fatalf("deliveries: %d", queued)
	}
	if n := len(owner.must(200, "GET", base+"/alerts?kind=reply.hot&limit=2", nil)["data"].([]any)); n != 2 {
		t.Fatalf("page: %d", n)
	}
	owner.must(400, "GET", base+"/alerts?kind=nope", nil)

	// Slack: the workspace's own connection posts each hot reply to the
	// channel picked, with a link and no reply text.
	var posts []map[string]any
	var mu sync.Mutex
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xoxb-test-not-a-real-token" {
			w.Write([]byte(`{"ok": false, "error": "invalid_auth"}`))
			return
		}
		switch r.URL.Path {
		case "/auth.test":
			w.Write([]byte(`{"ok": true, "team": "Acme", "team_id": "T1"}`))
		case "/conversations.list":
			w.Write([]byte(`{"ok": true, "channels": [{"id": "C2", "name": "sales"}, {"id": "C1", "name": "general"}], "response_metadata": {"next_cursor": ""}}`))
		case "/chat.postMessage":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			posts = append(posts, body)
			mu.Unlock()
			w.Write([]byte(`{"ok": true}`))
		}
	}))
	defer slack.Close()
	e.integrations.Client.BaseURLs["slack"] = slack.URL
	sl := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "slack", "credentials": map[string]string{"api_key": "xoxb-test-not-a-real-token"}})["id"].(string)
	ch := owner.must(200, "GET", base+"/alerts/channels?integration_id="+sl, nil)["data"].([]any)
	if len(ch) != 2 || ch[0].(map[string]any)["name"] != "general" {
		t.Fatalf("channels: %v", ch)
	}
	owner.must(400, "PUT", base+"/alerts/settings", map[string]any{"kind": "reply.hot", "slack_integration_id": sl, "slack_channel": "C9"})
	owner.must(400, "PUT", base+"/alerts/settings", map[string]any{"kind": "reply.hot", "slack_integration_id": sl})
	saved := owner.must(200, "PUT", base+"/alerts/settings", map[string]any{"kind": "reply.hot", "slack_integration_id": sl, "slack_channel": "C2"})
	if saved["slack_channel_name"] != "sales" || saved["email"] != nil {
		t.Fatalf("saved: %v", saved)
	}
	posted := hot()
	for i := 0; i < 200; i++ {
		mu.Lock()
		n := len(posts)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	mu.Lock()
	if len(posts) != 1 || posts[0]["channel"] != "C2" || !strings.Contains(fmt.Sprint(posts[0]["blocks"]), "/inbox/"+posted) || strings.Contains(fmt.Sprint(posts[0]), "Sounds good") {
		t.Fatalf("posts: %v", posts)
	}
	mu.Unlock()
	owner.must(200, "PUT", base+"/alerts/settings", map[string]any{"kind": "reply.hot", "slack_integration_id": nil, "slack_channel": nil})

	ev := got()
	if n := len(named(ev, "alert.raised")); n != 4 {
		t.Fatalf("alert.raised: %d", n)
	}
	if n := len(named(ev, "alert.emailed")); n != 1 {
		t.Fatalf("alert.emailed: %d", n)
	}
	if n := len(named(ev, "alert_settings.updated")); n != 4 {
		t.Fatalf("alert_settings.updated: %d", n)
	}
	if n := len(named(ev, "alert.posted")); n != 1 {
		t.Fatalf("alert.posted: %d", n)
	}
	if n := len(named(ev, "alert.muted")); n != 1 {
		t.Fatalf("alert.muted: %d", n)
	}
}
