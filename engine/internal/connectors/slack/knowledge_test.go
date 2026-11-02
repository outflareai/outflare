package slack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// fakeSlack answers the Web API calls the knowledge connector makes.
func fakeSlack(t *testing.T, calls *[]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer xoxb-test-not-a-real-token" {
			io.WriteString(w, `{"ok":false,"error":"invalid_auth"}`)
			return
		}
		switch r.URL.Path {
		case "/conversations.join":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["channel"] != "C1" {
				io.WriteString(w, `{"ok":false,"error":"channel_not_found"}`)
				return
			}
			io.WriteString(w, `{"ok":true}`)
		case "/conversations.history":
			if r.URL.Query().Get("channel") != "C1" || r.URL.Query().Get("oldest") == "" {
				t.Errorf("history query: %s", r.URL.RawQuery)
			}
			io.WriteString(w, `{"ok":true,"messages":[
				{"ts":"1767225600.000100","user":"U1","text":"How do we reset two factor for a customer?","reply_count":1},
				{"ts":"1767225500.000100","subtype":"channel_join","user":"U2","text":"joined"},
				{"ts":"1767225400.000100","bot_id":"B1","text":"Deploy finished"}]}`)
		case "/conversations.replies":
			io.WriteString(w, `{"ok":true,"messages":[
				{"ts":"1767225600.000100","user":"U1","text":"How do we reset two factor for a customer?"},
				{"ts":"1767225700.000100","user":"U3","text":"Settings, Security, Reset two factor. They get an email to set it up again."}]}`)
		case "/chat.getPermalink":
			io.WriteString(w, `{"ok":true,"permalink":"https://acme.slack.test/archives/C1/p1767225600000100"}`)
		default:
			t.Errorf("unexpected call %s", r.URL.Path)
			io.WriteString(w, `{"ok":false,"error":"unknown_method"}`)
		}
	}))
}

func TestKnowledgeReadsChosenChannels(t *testing.T) {
	var calls []string
	srv := fakeSlack(t, &calls)
	defer srv.Close()
	cl := &c.Client{BaseURLs: map[string]string{"slack": srv.URL}}
	creds := c.Creds{"api_key": "xoxb-test-not-a-real-token"}
	k := Knowledge{}

	d := k.Definition()
	if d.ID != "slack_knowledge" || d.Category != c.CategoryKnowledge || !slices.Contains(d.Capabilities, c.CapReadMessages) {
		t.Fatalf("definition: %+v", d)
	}
	// It reads, and never posts.
	for _, sc := range d.OAuth.Scopes {
		if strings.HasPrefix(sc, "chat:write") {
			t.Fatalf("knowledge connector asks to post: %v", d.OAuth.Scopes)
		}
	}
	var _ c.MessageReader = k

	if err := k.Join(context.Background(), cl, creds, "C1"); err != nil {
		t.Fatal(err)
	}
	if err := k.Join(context.Background(), cl, creds, "C9"); c.KindOf(err) != "invalid" {
		t.Fatalf("join missing channel: %v", err)
	}
	msgs, err := k.Recent(context.Background(), cl, creds, "C1", time.Now().Add(-24*time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	// Joins are left out; bots are kept but marked.
	if len(msgs) != 2 || msgs[0].Text != "How do we reset two factor for a customer?" || msgs[0].Author != "U1" || msgs[0].Bot ||
		len(msgs[0].Replies) != 1 || !strings.HasPrefix(msgs[0].Replies[0].Text, "Settings, Security") || !msgs[1].Bot {
		t.Fatalf("messages: %+v", msgs)
	}
	if got := msgs[0].At.UTC().Format(time.RFC3339); got != "2026-01-01T00:00:00Z" {
		t.Fatalf("time: %s", got)
	}
	link, err := k.Permalink(context.Background(), cl, creds, "C1", msgs[0].ID)
	if err != nil || !strings.Contains(link, "/archives/C1/") {
		t.Fatalf("permalink: %q %v", link, err)
	}
	if _, err := k.Recent(context.Background(), cl, c.Creds{"api_key": "xoxb-wrong"}, "C1", time.Now(), 10); c.KindOf(err) != "auth" {
		t.Fatalf("bad token: %v", err)
	}
}
