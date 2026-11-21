package slack

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const botKey = "xoxb-test-not-a-real-token"
const searchKey = "xoxp-test-not-a-real-token"

func feedsClient(t *testing.T, h http.HandlerFunc) (*c.Client, c.Creds) {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &c.Client{BaseURLs: map[string]string{"slack": srv.URL}}, c.Creds{"api_key": botKey, "search_token": searchKey}
}

func TestFeedsDefinition(t *testing.T) {
	d := Feeds{}.Definition()
	if d.ID != "slack_feeds" || d.OAuth != nil || d.Category != c.CategoryKnowledge || d.Name != "Slack (sales channels)" {
		t.Fatalf("definition: %+v", d)
	}
	if !slices.Equal(d.Capabilities, []string{c.CapReadFeeds, c.CapSearchMessages, c.CapPostFeedAlerts}) || d.Capabilities[0] != "feeds.read" || d.Capabilities[1] != "messages.search" || d.Capabilities[2] != "feeds.alert" {
		t.Fatalf("capabilities: %v", d.Capabilities)
	}
	if d.Auth != c.AuthAPIKey || !slices.Equal(d.Methods(), []string{c.AuthAPIKey}) {
		t.Fatalf("auth: %q %v", d.Auth, d.Methods())
	}
	if len(d.Fields) != 2 || d.Fields[0].Name != "api_key" || !d.Fields[0].Secret || d.Fields[0].Placeholder != "xoxb-…" ||
		d.Fields[1].Name != "search_token" || !d.Fields[1].Secret || !d.Fields[1].Optional || d.Fields[1].Placeholder != "xoxp-…" {
		t.Fatalf("fields: %+v", d.Fields)
	}
	var _ c.Connector = Feeds{}
	var _ c.FeedReader = Feeds{}
	var _ c.MessageSearcher = Feeds{}
}

func TestFeedsHistoryFlattensText(t *testing.T) {
	cl, creds := feedsClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true,"messages":[{"ts":"1763380000.000100","bot_id":"B1","text":"",
		"attachments":[{"title":"New submission","fields":[{"title":"Work email","value":"ana@acme.example"}],"text":"We need 500GB"}],
		"blocks":[{"type":"section","text":{"type":"mrkdwn","text":"*Company Website* <https://acme.example|acme.example>"}}]}]}`)
	})
	got, err := Feeds{}.History(context.Background(), cl, creds, "C1", "0", "")
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	want := "New submission\nWork email: ana@acme.example\nWe need 500GB\nCompany Website https://acme.example"
	if got[0].Text != want {
		t.Fatalf("text %q want %q", got[0].Text, want)
	}
}

func TestFeedsHistoryOldestFirstAcrossPages(t *testing.T) {
	var queries []string
	cl, creds := feedsClient(t, func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		if r.URL.Query().Get("cursor") == "" {
			io.WriteString(w, `{"ok":true,"messages":[{"ts":"1763380300.000100","user":"U1","text":"c"},{"ts":"1763380200.000100","user":"U1","text":"b"}],"response_metadata":{"next_cursor":"p2"}}`)
			return
		}
		io.WriteString(w, `{"ok":true,"messages":[{"ts":"1763380100.000100","user":"U1","text":"a"}]}`)
	})
	got, err := Feeds{}.History(context.Background(), cl, creds, "C1", "1763380000.000000", "")
	if err != nil || len(got) != 3 {
		t.Fatalf("%v %v", got, err)
	}
	if got[0].Text != "a" || got[1].Text != "b" || got[2].Text != "c" {
		t.Fatalf("order: %+v", got)
	}
	q := queries[0]
	for _, w := range []string{"limit=200", "oldest=1763380000.000000", "inclusive=false", "channel=C1"} {
		if !strings.Contains(q, w) {
			t.Errorf("query %q lacks %q", q, w)
		}
	}
}

func TestFeedsHistorySkipsJoinsKeepsBots(t *testing.T) {
	cl, creds := feedsClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true,"messages":[
		{"ts":"1763380002.000100","subtype":"channel_join","user":"U2","text":"joined"},
		{"ts":"1763380001.000100","subtype":"bot_message","bot_id":"B1","text":"form"}]}`)
	})
	got, err := Feeds{}.History(context.Background(), cl, creds, "C1", "0", "")
	if err != nil || len(got) != 1 || !got[0].Bot || got[0].Text != "form" {
		t.Fatalf("%+v %v", got, err)
	}
}

// A reply also sent to the channel is a thread reply: Slack repeats it in
// the channel's history, but it is not a post of its own.
func TestFeedsSkipThreadBroadcasts(t *testing.T) {
	cl, creds := feedsClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true,"messages":[
		{"ts":"1763380002.000100","subtype":"thread_broadcast","thread_ts":"1763380001.000100","user":"U2","text":"also sent to the channel"},
		{"ts":"1763380001.000100","user":"U1","text":"parent","reply_count":1,"latest_reply":"1763380002.000100"}]}`)
	})
	got, err := Feeds{}.History(context.Background(), cl, creds, "C1", "0", "")
	if err != nil || len(got) != 1 || got[0].Text != "parent" {
		t.Fatalf("history: %+v %v", got, err)
	}
	got, err = Feeds{}.Latest(context.Background(), cl, creds, "C1", 5)
	if err != nil || len(got) != 1 || got[0].Text != "parent" {
		t.Fatalf("latest: %+v %v", got, err)
	}
}

func TestFeedsHistoryEditedAndReplies(t *testing.T) {
	cl, creds := feedsClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true,"messages":[{"ts":"1763380000.000100","user":"U1","text":"hi",
		"edited":{"user":"U1","ts":"1763380000.000100"},"reply_count":2,"latest_reply":"1763380900.000200"}]}`)
	})
	got, _ := Feeds{}.History(context.Background(), cl, creds, "C1", "0", "1763390000.000000")
	if len(got) != 1 || got[0].EditedAt == nil || got[0].EditedAt.Unix() != 1763380000 || got[0].ReplyCount != 2 || got[0].LatestReply != "1763380900.000200" {
		t.Fatalf("%+v", got)
	}
}

func TestFeedsThreadAllReplies(t *testing.T) {
	cl, creds := feedsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ts") != "1763380000.000100" {
			t.Errorf("ts: %s", r.URL.RawQuery)
		}
		page2 := r.URL.Query().Get("cursor") != ""
		out := `{"ok":true,"messages":[`
		start, end := 0, 100
		if page2 {
			start, end = 100, 120
		} else {
			out += `{"ts":"1763380000.000100","user":"U1","text":"parent"},`
		}
		for i := start; i < end; i++ {
			if i > start {
				out += ","
			}
			out += fmt.Sprintf(`{"ts":"17633801%02d.%06d","user":"U2","text":"r%d"}`, i%100, i+1, i)
		}
		out += `]`
		if !page2 {
			out += `,"response_metadata":{"next_cursor":"p2"}`
		}
		io.WriteString(w, out+"}")
	})
	got, err := Feeds{}.Thread(context.Background(), cl, creds, "C1", "1763380000.000100")
	if err != nil || len(got) != 120 {
		t.Fatalf("%d %v", len(got), err)
	}
	for _, m := range got {
		if m.Text == "parent" {
			t.Fatal("parent included")
		}
	}
}

func TestFeedsSearchUsesSearchToken(t *testing.T) {
	cl, creds := feedsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+searchKey {
			t.Errorf("auth %q", r.Header.Get("Authorization"))
		}
		q := r.URL.Query()
		if r.URL.Path != "/search.messages" || q.Get("query") != "ana@acme.example" || q.Get("count") != "5" || q.Get("sort") != "timestamp" {
			t.Errorf("request %s", r.URL.String())
		}
		io.WriteString(w, `{"ok":true,"messages":{"matches":[{"channel":{"id":"C1","name":"leads","is_private":false},"user":"U1","text":"ana@acme.example wrote","ts":"1763380000.000100","permalink":"https://acme.slack.com/archives/C1/p1763380000000100"}]}}`)
	})
	hits, err := Feeds{}.Search(context.Background(), cl, creds, "ana@acme.example", 5)
	if err != nil || len(hits) != 1 || hits[0].Channel.ID != "C1" || hits[0].Channel.Name != "leads" || hits[0].Link == "" || hits[0].At.Unix() != 1763380000 {
		t.Fatalf("%+v %v", hits, err)
	}
}

func TestFeedsSearchCapsCount(t *testing.T) {
	cl, creds := feedsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("count") != "100" {
			t.Errorf("count %q", r.URL.Query().Get("count"))
		}
		io.WriteString(w, `{"ok":true,"messages":{"matches":[]}}`)
	})
	if _, err := (Feeds{}).Search(context.Background(), cl, creds, "x", 500); err != nil {
		t.Fatal(err)
	}
}

func TestFeedsSearchWithoutTokenIsInvalid(t *testing.T) {
	cl, creds := feedsClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("called Slack") })
	delete(creds, "search_token")
	_, err := Feeds{}.Search(context.Background(), cl, creds, "x", 5)
	if c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("err %v", err)
	}
}

func TestFeedsRateLimited(t *testing.T) {
	cl, creds := feedsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := Feeds{}.History(context.Background(), cl, creds, "C1", "0", "")
	if c.KindOf(err) != c.ErrQuota {
		t.Fatalf("err %v", err)
	}
}

func TestFeedsBaseURL(t *testing.T) {
	cl, creds := feedsClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true,"url":"https://acme.slack.com/","team":"Acme","team_id":"T1"}`)
	})
	u, err := Feeds{}.BaseURL(context.Background(), cl, creds)
	if err != nil || u != "https://acme.slack.com/" {
		t.Fatalf("%q %v", u, err)
	}
}

func TestLink(t *testing.T) {
	if got := Link("https://acme.slack.com/", "C1", "1763380000.000100"); got != "https://acme.slack.com/archives/C1/p1763380000000100" {
		t.Fatal(got)
	}
}

func TestFeedsLatestOneRequestNewestFirst(t *testing.T) {
	var queries []string
	cl, creds := feedsClient(t, func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		io.WriteString(w, `{"ok":true,"messages":[
		{"ts":"1763380003.000100","user":"U1","text":"c"},
		{"ts":"1763380002.000100","subtype":"channel_join","user":"U2","text":"joined"},
		{"ts":"1763380001.000100","subtype":"bot_message","bot_id":"B1","text":"a"}],"response_metadata":{"next_cursor":"more"}}`)
	})
	got, err := Feeds{}.Latest(context.Background(), cl, creds, "C1", 2)
	if err != nil || len(got) != 2 || got[0].Text != "c" || got[1].Text != "a" {
		t.Fatalf("%+v %v", got, err)
	}
	if len(queries) != 1 || !strings.Contains(queries[0], "limit=2") || strings.Contains(queries[0], "oldest") {
		t.Fatalf("queries: %v", queries)
	}
}

func TestFeedsLatestOneMorePageAtMost(t *testing.T) {
	var queries []string
	cl, creds := feedsClient(t, func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		io.WriteString(w, `{"ok":true,"messages":[{"ts":"1763380002.000100","subtype":"channel_join","user":"U2","text":"joined"}],"response_metadata":{"next_cursor":"more"}}`)
	})
	got, err := Feeds{}.Latest(context.Background(), cl, creds, "C1", 5)
	if err != nil || len(got) != 0 || len(queries) != 2 || !strings.Contains(queries[1], "cursor=more") {
		t.Fatalf("%+v %v %v", got, err, queries)
	}
}

func TestFeedsSearchMarksDirectAndGroupMessagesPrivate(t *testing.T) {
	cl, creds := feedsClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true,"messages":{"matches":[
			{"channel":{"id":"C1","name":"leads"},"user":"U1","text":"a","ts":"1763380000.000100"},
			{"channel":{"id":"D1","name":"U2","is_im":true},"user":"U2","text":"b","ts":"1763380001.000100"},
			{"channel":{"id":"G1","name":"mpdm-a--b","is_mpim":true},"user":"U2","text":"c","ts":"1763380002.000100"},
			{"channel":{"id":"G2","name":"old","is_group":true},"user":"U2","text":"d","ts":"1763380003.000100"},
			{"channel":{"id":"C2","name":"secret","is_private":true},"user":"U2","text":"e","ts":"1763380004.000100"}]}}`)
	})
	hits, err := Feeds{}.Search(context.Background(), cl, creds, "x", 5)
	if err != nil || len(hits) != 5 {
		t.Fatalf("%+v %v", hits, err)
	}
	for i, private := range []bool{false, true, true, true, true} {
		if hits[i].Channel.Private != private {
			t.Errorf("hit %d private %v", i, hits[i].Channel.Private)
		}
	}
}

func TestFeedsSearchPublicIsPositive(t *testing.T) {
	flag := func(name string) string {
		return `{"channel":{"id":"C1","name":"n","is_channel":true,"` + name + `":true},"text":"x","ts":"1763380000.000100"}`
	}
	matches := []string{
		`{"channel":{"id":"C1","name":"open","is_channel":true},"text":"x","ts":"1763380000.000100"}`, // public
		`{"text":"no channel","ts":"1763380000.000100"}`,
		`{"channel":{"id":"","name":"x","is_channel":true},"text":"empty id","ts":"1763380000.000100"}`,
		`{"channel":{"id":"C2","name":"x"},"text":"no is_channel","ts":"1763380000.000100"}`,
	}
	flags := []string{"is_private", "is_im", "is_mpim", "is_group", "is_ext_shared", "is_org_shared", "is_shared", "is_pending_ext_shared"}
	for _, f := range flags {
		matches = append(matches, flag(f))
	}
	cl, creds := feedsClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true,"messages":{"matches":[`+strings.Join(matches, ",")+`]}}`)
	})
	hits, err := Feeds{}.Search(context.Background(), cl, creds, "x", 50)
	if err != nil || len(hits) != len(matches) {
		t.Fatalf("%d %v", len(hits), err)
	}
	if !hits[0].Public {
		t.Error("an open channel is public")
	}
	for i, h := range hits[1:] {
		if h.Public {
			t.Errorf("match %d (%s) must not be public", i+1, matches[i+1])
		}
	}
}

// A form tool's post: each answer is a bold question with the answer under
// it. The app's name is the post's source; a bot's username is not.
func TestFeedsFormAnswersBecomeFieldsAndSource(t *testing.T) {
	cl, creds := feedsClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true,"messages":[
		{"ts":"1763380000.000100","bot_id":"B1","username":"Ana Diaz","bot_profile":{"name":" Form  Tool "},"text":"",
		"blocks":[{"type":"section","text":{"type":"mrkdwn","text":"*First name*\nAna"}},
		{"type":"section","fields":[{"type":"mrkdwn","text":"*Work email:*\n<mailto:ana@acme.example|ana@acme.example>"},{"type":"mrkdwn","text":"*What is your goal?*\nWe need 500GB\nfor a crawler"}]},
		{"type":"section","text":{"type":"mrkdwn","text":"*Heading*\n*Another heading*"}}]},
		{"ts":"1763380100.000100","user":"U1","text":"*Note*\nby a person"}]}`)
	})
	got, err := Feeds{}.History(context.Background(), cl, creds, "C1", "0", "")
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	want := "First name: Ana\nWork email: ana@acme.example\nWhat is your goal?: We need 500GB\nfor a crawler\nHeading\nAnother heading"
	if got[0].Text != want {
		t.Fatalf("text %q want %q", got[0].Text, want)
	}
	if got[0].Source != "Form Tool" {
		t.Fatalf("source %q", got[0].Source)
	}
	if got[1].Source != "" || got[1].Text != "Note: by a person" {
		t.Fatalf("person post %+v", got[1])
	}
}
