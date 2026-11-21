package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/alerts"
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
	// Every alert email says where to switch it off, as a link.
	if !strings.Contains(m.Text, "Manage notifications:\n") || !strings.Contains(m.HTML, ">Manage notifications</a>") || !strings.Contains(m.HTML, "/alerts\"") {
		t.Fatalf("no link to manage notifications: %q", m.Text)
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
	// A mute is the brand's: muting it for the first brand stops these.
	var first0 string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&first0); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "PUT", "/v1/brands/"+first0+"/alerts/mutes", map[string]any{"kind": "reply.hot", "muted": true})
	hot()
	set := owner.must(200, "GET", base+"/alerts/settings", nil)
	if mu := set["muted"].([]any); len(mu) != 1 || mu[0] != "reply.hot" || len(set["kinds"].([]any)) < 2 {
		t.Fatalf("settings: %v", set)
	}
	if b := owner.must(200, "GET", "/v1/brands/"+first0+"/alerts/settings", nil); len(b["muted"].([]any)) != 1 {
		t.Fatalf("brand settings: %v", b)
	}
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	if b := owner.must(200, "GET", "/v1/brands/"+other+"/alerts/settings", nil); len(b["muted"].([]any)) != 0 {
		t.Fatalf("another brand has the mute: %v", b)
	}
	owner.must(200, "PUT", base+"/alerts/mutes?brand=northpeak", map[string]any{"kind": "reply.hot", "muted": true})
	if b := owner.must(200, "GET", "/v1/brands/"+other+"/alerts/settings", nil); len(b["muted"].([]any)) != 1 {
		t.Fatalf("northpeak mutes: %v", b)
	}
	if mu := owner.must(200, "GET", base+"/alerts/settings", nil)["muted"].([]any); len(mu) != 1 || mu[0] != "reply.hot" {
		t.Fatalf("the first brand's mutes changed: %v", mu)
	}
	// Many kinds at once: all or none, and only the ones that changed count.
	owner.must(400, "PUT", "/v1/brands/"+other+"/alerts/mutes", map[string]any{"kinds": []string{"reply.hot", "nope"}, "muted": true})
	owner.must(400, "PUT", "/v1/brands/"+other+"/alerts/mutes", map[string]any{"kind": "reply.hot", "kinds": []string{"visitor.hot"}, "muted": true})
	owner.must(400, "PUT", "/v1/brands/"+other+"/alerts/mutes", map[string]any{"kinds": []string{}, "muted": true})
	if b := owner.must(200, "GET", "/v1/brands/"+other+"/alerts/settings", nil); len(b["muted"].([]any)) != 1 {
		t.Fatalf("a refused bulk mute changed something: %v", b)
	}
	bulk := owner.must(200, "PUT", "/v1/brands/"+other+"/alerts/mutes", map[string]any{"kinds": []string{"reply.hot", "visitor.hot", "visitor.hot"}, "muted": true})
	if bulk["changed"] != float64(1) || len(bulk["kinds"].([]any)) != 2 {
		t.Fatalf("bulk mute: %v", bulk)
	}
	if mu := owner.must(200, "GET", "/v1/brands/"+other+"/alerts/settings", nil)["muted"].([]any); len(mu) != 2 {
		t.Fatalf("northpeak mutes after bulk: %v", mu)
	}
	if un := owner.must(200, "PUT", "/v1/brands/"+other+"/alerts/mutes", map[string]any{"kinds": []string{"reply.hot", "visitor.hot"}, "muted": false}); un["changed"] != float64(2) {
		t.Fatalf("bulk unmute: %v", un)
	}
	if mu := owner.must(200, "GET", "/v1/brands/"+other+"/alerts/settings", nil)["muted"].([]any); len(mu) != 0 {
		t.Fatalf("northpeak mutes after bulk unmute: %v", mu)
	}
	if mu := owner.must(200, "GET", base+"/alerts/settings", nil)["muted"].([]any); len(mu) != 1 {
		t.Fatalf("the first brand's mutes changed: %v", mu)
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

	// Telegram: the workspace's own bot posts each hot reply to the chat
	// picked, beside Slack, with a link and no reply text.
	const botToken = "123456789:test-not-a-real-bot-token-000000"
	var sent []map[string]any
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, ok := strings.CutPrefix(r.URL.Path, "/bot"+botToken+"/")
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"ok": false, "error_code": 401, "description": "Unauthorized"}`))
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		switch method {
		case "getMe":
			w.Write([]byte(`{"ok": true, "result": {"id": 123456789, "first_name": "Acme alerts", "username": "acme_alerts_bot"}}`))
		case "getUpdates":
			w.Write([]byte(`{"ok": true, "result": [{"update_id": 1, "my_chat_member": {"chat": {"id": -1001234567890, "type": "supergroup", "title": "Growth team"}}}]}`))
		case "getChat":
			if body["chat_id"] != "@acme_alerts" {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"ok": false, "error_code": 400, "description": "Bad Request: chat not found"}`))
				return
			}
			w.Write([]byte(`{"ok": true, "result": {"id": -1009876543210, "type": "channel", "title": "Acme alerts", "username": "acme_alerts"}}`))
		case "sendMessage":
			mu.Lock()
			sent = append(sent, body)
			mu.Unlock()
			w.Write([]byte(`{"ok": true, "result": {"message_id": 1}}`))
		}
	}))
	defer telegram.Close()
	e.integrations.Client.BaseURLs["telegram_bot"] = telegram.URL
	owner.must(422, "POST", base+"/integrations", map[string]any{"connector": "telegram_bot", "credentials": map[string]string{"api_key": "not a token"}})
	tg := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "telegram_bot", "credentials": map[string]string{"api_key": botToken}})["id"].(string)
	chats := owner.must(200, "GET", base+"/alerts/channels?integration_id="+tg, nil)["data"].([]any)
	if len(chats) != 1 || chats[0].(map[string]any)["name"] != "Growth team" || chats[0].(map[string]any)["id"] != "-1001234567890" {
		t.Fatalf("chats: %v", chats)
	}
	// A chat the bot can't see, half a setting, and each connection sent
	// as the other's are refused.
	owner.must(400, "PUT", base+"/alerts/settings", map[string]any{"kind": "reply.hot", "telegram_integration_id": tg, "telegram_chat": "-100555"})
	owner.must(400, "PUT", base+"/alerts/settings", map[string]any{"kind": "reply.hot", "telegram_integration_id": tg, "telegram_chat": "growth team"})
	owner.must(400, "PUT", base+"/alerts/settings", map[string]any{"kind": "reply.hot", "telegram_integration_id": tg})
	owner.must(400, "PUT", base+"/alerts/settings", map[string]any{"kind": "reply.hot", "telegram_integration_id": sl, "telegram_chat": "-1001234567890"})
	owner.must(400, "PUT", base+"/alerts/settings", map[string]any{"kind": "reply.hot", "slack_integration_id": tg, "slack_channel": "-1001234567890"})
	// A public channel by its @name is saved by its number, with its name.
	saved = owner.must(200, "PUT", base+"/alerts/settings", map[string]any{"kind": "reply.hot", "telegram_integration_id": tg, "telegram_chat": "@acme_alerts"})
	if saved["telegram_chat"] != "-1009876543210" || saved["telegram_chat_name"] != "Acme alerts" {
		t.Fatalf("a channel by name: %v", saved)
	}
	// A chat from the list, with Slack on again beside it.
	owner.must(200, "PUT", base+"/alerts/settings", map[string]any{"kind": "reply.hot", "slack_integration_id": sl, "slack_channel": "C1"})
	saved = owner.must(200, "PUT", base+"/alerts/settings", map[string]any{"kind": "reply.hot", "telegram_integration_id": tg, "telegram_chat": "-1001234567890"})
	if saved["telegram_chat_name"] != "Growth team" || saved["telegram_integration_id"] != tg || saved["slack_channel_name"] != "general" {
		t.Fatalf("saved: %v", saved)
	}
	posted = hot()
	for i := 0; i < 200; i++ {
		mu.Lock()
		n, m := len(sent), len(posts)
		mu.Unlock()
		if n > 0 && m > 1 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	mu.Lock()
	if len(sent) != 1 || sent[0]["chat_id"] != float64(-1001234567890) || !strings.Contains(fmt.Sprint(sent[0]["text"]), "/inbox/"+posted) ||
		strings.Contains(fmt.Sprint(sent[0]), "Sounds good") || len(posts) != 2 {
		t.Fatalf("telegram: %v; slack posts: %d", sent, len(posts))
	}
	mu.Unlock()
	var channels []string
	if err := e.pool.QueryRow(ctx, `SELECT array_agg(channel ORDER BY channel) FROM alert_deliveries WHERE workspace_id = $1 AND alert_id =
		(SELECT id FROM alerts WHERE workspace_id = $1 AND target_id = $2) AND status = 'sent'`, ws, posted).Scan(&channels); err != nil || !slices.Equal(channels, []string{"slack", "telegram"}) {
		t.Fatalf("deliveries: %v %v", channels, err)
	}
	// Telegram off, then Slack: nothing is chosen any more.
	if off := owner.must(200, "PUT", base+"/alerts/settings", map[string]any{"kind": "reply.hot", "telegram_integration_id": nil, "telegram_chat": nil}); off["telegram_chat"] != nil || off["slack_channel"] != "C1" {
		t.Fatalf("telegram off: %v", off)
	}
	if off := owner.must(200, "PUT", base+"/alerts/settings", map[string]any{"kind": "reply.hot", "slack_integration_id": nil, "slack_channel": nil}); off["cleared"] != true {
		t.Fatalf("all off: %v", off)
	}

	ev := got()
	if n := len(named(ev, "alert.raised")); n != 5 {
		t.Fatalf("alert.raised: %d", n)
	}
	if n := len(named(ev, "alert.emailed")); n != 1 {
		t.Fatalf("alert.emailed: %d", n)
	}
	if n := len(named(ev, "alert_settings.updated")); n != 9 {
		t.Fatalf("alert_settings.updated: %d", n)
	}
	var where []string
	for _, p := range named(ev, "alert.posted") {
		where = append(where, fmt.Sprint(p.Props["channel"]))
	}
	slices.Sort(where)
	if !slices.Equal(where, []string{"slack", "slack", "telegram"}) {
		t.Fatalf("alert.posted: %v", where)
	}
	// Two single mutes and the one kind the bulk mute changed.
	muted := named(ev, "alert.muted")
	if len(muted) != 3 {
		t.Fatalf("alert.muted: %v", muted)
	}
}

// Brands share no alert settings: a workspace row saved with no brand
// (before brands were separate) counts for no brand, and a setting saved
// on the workspace route is the first brand's.
func TestAlertSettingsAreTheBrands(t *testing.T) {
	e := freshEnv(t)
	owner, base, _ := sendingSetup(t, e, &fakeClock{t: time.Now()})
	ws := strings.TrimPrefix(base, "/v1/workspaces/")
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `INSERT INTO alert_settings (workspace_id, kind, email) VALUES ($1, 'reply.hot', 'off')`, ws); err != nil {
		t.Fatal(err)
	}
	if l := owner.must(200, "GET", base+"/alerts/settings", nil)["settings"].([]any); len(l) != 0 {
		t.Fatalf("the workspace's row counts for a brand: %v", l)
	}
	v := owner.must(200, "PUT", base+"/alerts/settings", map[string]any{"kind": "reply.hot", "email": "admins"})
	var first string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if v["brand_id"] != first {
		t.Fatalf("saved for %v, want the first brand %s", v["brand_id"], first)
	}
	if to, err := alerts.EmailSetting(ctx, e.pool, ws, &first, "reply.hot"); err != nil || to != "admins" {
		t.Fatalf("the brand's email = %q %v", to, err)
	}
}

// Alerts of one kind waiting for the same person go as one email that
// lists them all, and the rest are marked grouped.
func TestAlertEmailsGroup(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `UPDATE users SET email_verified_at = now()`); err != nil {
		t.Fatal(err)
	}
	var brand, me string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT user_id::text FROM memberships WHERE workspace_id = $1 AND role = 'owner'`, ws).Scan(&me); err != nil {
		t.Fatal(err)
	}
	var landers []string
	for _, name := range []string{"Essential", "Travel kit", "Pocket"} {
		landers = append(landers, owner.must(201, "POST", "/v1/brands/"+brand+"/landers",
			map[string]any{"name": name, "url": "https://pages.northpeak.example/" + strings.ToLower(strings.ReplaceAll(name, " ", "-"))})["id"].(string))
	}
	// Raised in one transaction, so every delivery waits when the first
	// job runs.
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range landers {
		if _, err := alerts.Raise(ctx, tx, alerts.Alert{WorkspaceID: ws, BrandID: &brand, Kind: "tracker.link_broken", OwnerID: &me,
			DedupeKey: id, TargetType: "tracker_lander", TargetID: id}); err != nil {
			t.Fatal(err)
		}
	}
	before := e.outbox.count()
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	e.runJobs(t)
	var sent, grouped int
	for i := 0; i < 200; i++ {
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'sent'), count(*) FILTER (WHERE reason = 'grouped')
			FROM alert_deliveries WHERE workspace_id = $1 AND channel = 'email'`, ws).Scan(&sent, &grouped); err != nil {
			t.Fatal(err)
		}
		if sent+grouped == 3 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if sent != 1 || grouped != 2 || e.outbox.count() != before+1 {
		t.Fatalf("sent %d, grouped %d, emails %d: want one email for three alerts", sent, grouped, e.outbox.count()-before)
	}
	e.outbox.mu.Lock()
	m := e.outbox.sent[len(e.outbox.sent)-1]
	e.outbox.mu.Unlock()
	if m.Subject != "Tracker page not answering: 3 alerts" {
		t.Fatalf("subject: %q", m.Subject)
	}
	for _, name := range []string{"Essential", "Travel kit", "Pocket"} {
		if !strings.Contains(m.Text, name) {
			t.Fatalf("the email doesn't list %s: %s", name, m.Text)
		}
	}
}
