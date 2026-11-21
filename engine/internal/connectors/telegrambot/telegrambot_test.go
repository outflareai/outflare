package telegrambot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// A made-up token in the shape Telegram's are.
const testToken = "123456789:test-not-a-real-bot-token-000000"

var ctx = context.Background()

// fake answers like the Bot API for testToken, and records what was sent.
func fake(t *testing.T, webhook bool) (*c.Client, *[]map[string]any) {
	t.Helper()
	var sent []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		prefix := "/bot" + testToken + "/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"ok": false, "error_code": 401, "description": "Unauthorized"}`))
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		switch strings.TrimPrefix(r.URL.Path, prefix) {
		case "getMe":
			w.Write([]byte(`{"ok": true, "result": {"id": 123456789, "is_bot": true, "first_name": "Acme alerts", "username": "acme_alerts_bot"}}`))
		case "getUpdates":
			if webhook {
				w.WriteHeader(http.StatusConflict)
				w.Write([]byte(`{"ok": false, "error_code": 409, "description": "Conflict: can't use getUpdates method while webhook is active"}`))
				return
			}
			w.Write([]byte(`{"ok": true, "result": [
				{"update_id": 1, "message": {"message_id": 5, "chat": {"id": 4242, "type": "private", "first_name": "Sam", "last_name": "Lee"}, "text": "/start"}},
				{"update_id": 2, "my_chat_member": {"chat": {"id": -1001234567890, "type": "supergroup", "title": "Growth team"}}},
				{"update_id": 3, "channel_post": {"message_id": 9, "chat": {"id": -1009876543210, "type": "channel", "title": "Alerts", "username": "acme_alerts"}}},
				{"update_id": 4, "message": {"message_id": 6, "chat": {"id": 4242, "type": "private", "first_name": "Sam", "last_name": "Lee"}}}]}`))
		case "getChat":
			if body["chat_id"] != float64(-1001234567890) && body["chat_id"] != "@acme_alerts" {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"ok": false, "error_code": 400, "description": "Bad Request: chat not found"}`))
				return
			}
			w.Write([]byte(`{"ok": true, "result": {"id": -1001234567890, "type": "supergroup", "title": "Growth team"}}`))
		case "sendMessage":
			if body["chat_id"] == float64(777) {
				w.WriteHeader(http.StatusForbidden)
				w.Write([]byte(`{"ok": false, "error_code": 403, "description": "Forbidden: bot was kicked from the group chat"}`))
				return
			}
			if body["chat_id"] == float64(429) {
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write([]byte(`{"ok": false, "error_code": 429, "description": "Too Many Requests: retry after 5", "parameters": {"retry_after": 5}}`))
				return
			}
			sent = append(sent, body)
			w.Write([]byte(`{"ok": true, "result": {"message_id": 10}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"ok": false, "error_code": 404, "description": "Not Found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return &c.Client{BaseURLs: map[string]string{ID: srv.URL}}, &sent
}

func TestTelegramBot(t *testing.T) {
	cl, sent := fake(t, false)
	b, creds := Bot{}, c.Creds{"api_key": testToken}
	var _ c.Notifier = b
	var _ c.ChannelFinder = b
	// Its own capability, so a journey's Slack step never picks the bot.
	if d := b.Definition(); !d.Has(c.CapNotifyTelegram) || d.Has(c.CapNotify) || d.ID != "telegram_bot" {
		t.Fatalf("definition: %+v", d)
	}
	acct, err := b.Test(ctx, cl, creds)
	if err != nil || acct.ID != "123456789" || acct.Name != "@acme_alerts_bot" {
		t.Fatalf("test: %+v %v", acct, err)
	}
	// The chats it heard from, each once, by name.
	chats, err := b.Channels(ctx, cl, creds)
	if err != nil || len(chats) != 3 || chats[0].Name != "Alerts" || chats[1] != (c.Channel{ID: "-1001234567890", Name: "Growth team"}) ||
		chats[2] != (c.Channel{ID: "4242", Name: "Sam Lee", Private: true}) {
		t.Fatalf("chats: %+v %v", chats, err)
	}
	// One chat by its number or a channel's @name.
	if ch, err := b.Channel(ctx, cl, creds, "-1001234567890"); err != nil || ch.Name != "Growth team" {
		t.Fatalf("a chat by id: %+v %v", ch, err)
	}
	if _, err := b.Channel(ctx, cl, creds, "@acme_alerts"); err != nil {
		t.Fatalf("a channel by name: %v", err)
	}
	if _, err := b.Channel(ctx, cl, creds, "-100555"); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("a chat the bot can't see: %v", err)
	}
	if _, err := b.Channel(ctx, cl, creds, "growth team"); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("not a chat id: %v", err)
	}

	// A post: the title in bold, the detail, a link, no preview, and names
	// can't be read as markup.
	n := c.Notice{Title: "Hot reply from <Acme> & co", Text: "Reply within the hour", URL: "https://app.example.com/acme/inbox/1?a=1&b=2"}
	if err := b.Post(ctx, cl, creds, "-1001234567890", n); err != nil {
		t.Fatal(err)
	}
	if len(*sent) != 1 {
		t.Fatalf("sent: %v", *sent)
	}
	m := (*sent)[0]
	want := "<b>Hot reply from &lt;Acme&gt; &amp; co</b>\nReply within the hour\n\n<a href=\"https://app.example.com/acme/inbox/1?a=1&amp;b=2\">Open in Outflare</a>"
	if m["chat_id"] != float64(-1001234567890) || m["text"] != want || m["parse_mode"] != "HTML" ||
		m["link_preview_options"].(map[string]any)["is_disabled"] != true {
		t.Fatalf("message: %v", m)
	}
	// A chat the bot was removed from won't fix itself; a rate limit will.
	if err := b.Post(ctx, cl, creds, "777", n); c.KindOf(err) != c.ErrInvalid || !strings.Contains(err.Error(), "kicked") {
		t.Fatalf("removed from the chat: %v", err)
	}
	if err := b.Post(ctx, cl, creds, "429", n); c.KindOf(err) != c.ErrQuota {
		t.Fatalf("rate limited: %v", err)
	}
}

// The token sits in the URL's path, so no error may carry the URL.
func TestTelegramBotKeepsTheTokenOutOfErrors(t *testing.T) {
	cl, _ := fake(t, false)
	b := Bot{}
	// A wrong token Telegram refuses.
	other := "987654321:test-not-a-real-bot-token-111111"
	_, err := b.Test(ctx, cl, c.Creds{"api_key": other})
	if c.KindOf(err) != c.ErrAuth || strings.Contains(err.Error(), other) {
		t.Fatalf("a refused token: %v", err)
	}
	// Something that isn't a token never reaches the URL.
	for _, bad := range []string{"", "nope", "123:abc/../../x", testToken + "/getMe?x="} {
		if _, err := b.Test(ctx, cl, c.Creds{"api_key": bad}); c.KindOf(err) != c.ErrAuth {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	// Telegram unreachable: the cause, never the address.
	down := &c.Client{BaseURLs: map[string]string{ID: "http://127.0.0.1:1"}}
	_, err = b.Test(ctx, down, c.Creds{"api_key": testToken})
	if c.KindOf(err) != c.ErrUnavailable || strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "/bot") {
		t.Fatalf("unreachable: %v", err)
	}
}

// A bot whose updates go to a webhook lists no chats, without failing.
func TestTelegramBotWithAWebhook(t *testing.T) {
	cl, _ := fake(t, true)
	chats, err := Bot{}.Channels(ctx, cl, c.Creds{"api_key": testToken})
	if err != nil || len(chats) != 0 {
		t.Fatalf("chats: %+v %v", chats, err)
	}
}
