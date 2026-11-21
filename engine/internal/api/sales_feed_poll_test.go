package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/track"
)

const (
	feedBotToken  = "xoxb-test-not-a-real-token"
	feedBotToken2 = "xoxb-test-not-a-real-token-2"
	feedUserToken = "xoxp-test-not-a-real-token"
)

// slackPost is one message in fakeSlackFeeds.
type slackPost struct {
	TS, User, Text string
	Replies        []slackPost
	App            string // posted by an app with this name instead of a person
}

// fakeSlackFeeds is a Slack workspace whose channels hold posts the test
// can change. It counts calls by method, answers the history call numbered
// fail429At with a 429, and answers not_in_channel for channels in out.
type fakeSlackFeeds struct {
	mu        sync.Mutex
	posts     map[string][]slackPost
	calls     map[string]int
	oldest    map[string][]string // each history call's oldest, by channel
	fail429At int
	out       map[string]bool
	// failAt answers that history call with Slack's error failWith.
	failAt   int
	failWith string
	revoked  map[string]bool // tokens Slack no longer accepts
	// search.messages: the matches Slack answers (in its own shape), the
	// queries it got and the HTTP status it answers with (0 is 200).
	matches      []map[string]any
	searchQuery  []string
	searchStatus int
	// Messages the app posted (chat.postMessage, chat.update) and the
	// emojis on each, by ts. noPost answers them as an app without the
	// scopes to post would.
	alerts    []fakeAlert
	reactions map[string][]string
	noPost    bool
}

// fakeAlert is one message the app posted to the fake Slack.
type fakeAlert struct{ Channel, TS, Text string }

func (f *fakeSlackFeeds) posted() []fakeAlert {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeAlert{}, f.alerts...)
}

func (f *fakeSlackFeeds) emojis(ts string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]string{}, f.reactions[ts]...)
	sort.Strings(out)
	return out
}

func newFakeSlackFeeds() *fakeSlackFeeds {
	return &fakeSlackFeeds{posts: map[string][]slackPost{}, calls: map[string]int{}, oldest: map[string][]string{}, out: map[string]bool{}, revoked: map[string]bool{}, reactions: map[string][]string{}}
}

func (f *fakeSlackFeeds) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

func (f *fakeSlackFeeds) set(channel string, posts ...slackPost) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts[channel] = posts
}

func (f *fakeSlackFeeds) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if r.URL.Path == "/search.messages" {
		f.calls["search.messages"]++
		f.searchQuery = append(f.searchQuery, r.URL.Query().Get("query"))
		if tok != feedUserToken {
			io.WriteString(w, `{"ok":false,"error":"not_allowed_token_type"}`)
			return
		}
		if f.searchStatus != 0 {
			w.WriteHeader(f.searchStatus)
			io.WriteString(w, `{"ok":false,"error":"fatal_error"}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "messages": map[string]any{"matches": f.matches}})
		return
	}
	if (tok != feedBotToken && tok != feedBotToken2) || f.revoked[tok] {
		io.WriteString(w, `{"ok":false,"error":"invalid_auth"}`)
		return
	}
	method := strings.TrimPrefix(r.URL.Path, "/")
	f.calls[method]++
	q := r.URL.Query()
	ch := q.Get("channel")
	msg := func(p slackPost) map[string]any {
		m := map[string]any{"ts": p.TS, "user": p.User, "text": p.Text}
		if p.App != "" {
			m = map[string]any{"ts": p.TS, "bot_id": "B9", "username": "Ana Diaz", "bot_profile": map[string]any{"name": p.App}, "text": p.Text}
		}
		if len(p.Replies) > 0 {
			m["reply_count"] = len(p.Replies)
			m["latest_reply"] = p.Replies[len(p.Replies)-1].TS
		}
		return m
	}
	switch method {
	case "auth.test":
		io.WriteString(w, `{"ok":true,"team":"Acme","team_id":"T1","url":"https://acme.slack.example/"}`)
	case "conversations.list":
		io.WriteString(w, `{"ok":true,"channels":[{"id":"C1","name":"website-leads"},{"id":"C2","name":"usage-alerts"}]}`)
	case "conversations.history":
		if f.calls[method] == f.fail429At {
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"ok":false,"error":"ratelimited"}`)
			return
		}
		if f.calls[method] == f.failAt {
			io.WriteString(w, `{"ok":false,"error":"`+f.failWith+`"}`)
			return
		}
		if f.out[ch] {
			io.WriteString(w, `{"ok":false,"error":"not_in_channel"}`)
			return
		}
		f.oldest[ch] = append(f.oldest[ch], q.Get("oldest"))
		oldest, _ := strconv.ParseFloat(q.Get("oldest"), 64)
		latest := 1e18
		if l := q.Get("latest"); l != "" {
			latest, _ = strconv.ParseFloat(l, 64)
		}
		var out []any
		for _, p := range f.posts[ch] {
			if ts, _ := strconv.ParseFloat(p.TS, 64); ts > oldest && ts < latest {
				out = append(out, msg(p))
			}
		}
		// Slack answers newest first.
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].(map[string]any)["ts"].(string) > out[j].(map[string]any)["ts"].(string)
		})
		if n, _ := strconv.Atoi(q.Get("limit")); n > 0 && len(out) > n {
			out = out[:n]
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "messages": out})
	case "conversations.replies":
		for _, p := range f.posts[ch] {
			if p.TS == q.Get("ts") {
				out := []any{msg(p)}
				for _, rp := range p.Replies {
					out = append(out, msg(rp))
				}
				json.NewEncoder(w).Encode(map[string]any{"ok": true, "messages": out})
				return
			}
		}
		io.WriteString(w, `{"ok":false,"error":"thread_not_found"}`)
	case "chat.postMessage", "chat.update", "reactions.add", "reactions.remove":
		var body struct {
			Channel, Text, TS, Timestamp, Name string
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch {
		case f.noPost:
			io.WriteString(w, `{"ok":false,"error":"missing_scope"}`)
		case f.out[body.Channel]:
			io.WriteString(w, `{"ok":false,"error":"not_in_channel"}`)
		case method == "chat.postMessage":
			ts := fmt.Sprintf("1900000000.%06d", len(f.alerts)+1)
			f.alerts = append(f.alerts, fakeAlert{body.Channel, ts, body.Text})
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": ts})
		case method == "chat.update":
			for i := range f.alerts {
				if f.alerts[i].TS == body.TS {
					f.alerts[i].Text = body.Text
				}
			}
			io.WriteString(w, `{"ok":true}`)
		case method == "reactions.add":
			if slices.Contains(f.reactions[body.Timestamp], body.Name) {
				io.WriteString(w, `{"ok":false,"error":"already_reacted"}`)
				return
			}
			f.reactions[body.Timestamp] = append(f.reactions[body.Timestamp], body.Name)
			io.WriteString(w, `{"ok":true}`)
		default:
			if !slices.Contains(f.reactions[body.Timestamp], body.Name) {
				io.WriteString(w, `{"ok":false,"error":"no_reaction"}`)
				return
			}
			f.reactions[body.Timestamp] = slices.DeleteFunc(f.reactions[body.Timestamp], func(v string) bool { return v == body.Name })
			io.WriteString(w, `{"ok":true}`)
		}
	default:
		io.WriteString(w, `{"ok":false,"error":"unknown_method"}`)
	}
}

// feedTS is a Slack timestamp ago before now; n keeps two posts in the same
// second apart.
func feedTS(ago time.Duration, n int) string {
	return fmt.Sprintf("%d.%06d", time.Now().Add(-ago).Unix(), n)
}

type feedFixture struct {
	e      *env
	owner  *client
	ws     string
	brand  string
	integ  string
	slack  *fakeSlackFeeds
	ctx    context.Context
	events func() []track.Event
}

// newFeedFixture connects a fake Slack (sales channels) to a fresh
// workspace's first brand.
func newFeedFixture(t *testing.T) *feedFixture {
	t.Helper()
	f := &feedFixture{events: track.Capture(t), ctx: context.Background()}
	f.e = freshEnv(t)
	f.owner, f.ws = setupOwner(t, f.e.h)
	f.slack = newFakeSlackFeeds()
	srv := httptest.NewServer(f.slack)
	t.Cleanup(srv.Close)
	f.e.integrations.Client.BaseURLs["slack"] = srv.URL
	f.integ = f.owner.must(201, "POST", "/v1/workspaces/"+f.ws+"/integrations", map[string]any{"connector": "slack_feeds",
		"credentials": map[string]any{"api_key": feedBotToken}})["id"].(string)
	if err := f.e.pool.QueryRow(f.ctx, `SELECT outflare_first_brand($1)::text`, f.ws).Scan(&f.brand); err != nil {
		t.Fatal(err)
	}
	return f
}

// channel adds a feed channel to brand on integ and returns its id.
func (f *feedFixture) channel(t *testing.T, brand, integ, slackID string, days int) string {
	t.Helper()
	var id string
	if err := f.e.pool.QueryRow(f.ctx, `INSERT INTO sales_feed_channels(workspace_id,brand_id,integration_id,channel_id,channel_name,kind,read_back_days)
		VALUES($1,$2,$3,$4,$4,'new_person',$5) RETURNING id::text`, f.ws, brand, integ, slackID, days).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *feedFixture) poll(t *testing.T, channel string) error {
	t.Helper()
	return f.e.srv.PollSalesFeed(f.ctx, f.ws, f.brand, channel)
}

func (f *feedFixture) mustPoll(t *testing.T, channel string) {
	t.Helper()
	if err := f.poll(t, channel); err != nil {
		t.Fatal(err)
	}
}

func (f *feedFixture) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := f.e.pool.QueryRow(f.ctx, sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *feedFixture) items(t *testing.T, channel string) int {
	return f.count(t, `SELECT count(*) FROM sales_feed_items WHERE workspace_id=$1 AND feed_channel_id=$2`, f.ws, channel)
}

func (f *feedFixture) itemJobs(t *testing.T) int {
	return f.count(t, `SELECT count(*) FROM jobs WHERE workspace_id=$1 AND kind='sales.feed_item'`, f.ws)
}

func (f *feedFixture) item(t *testing.T, channel, ts string) string {
	t.Helper()
	var id string
	if err := f.e.pool.QueryRow(f.ctx, `SELECT id::text FROM sales_feed_items WHERE workspace_id=$1 AND feed_channel_id=$2 AND slack_ts=$3`, f.ws, channel, ts).Scan(&id); err != nil {
		t.Fatalf("item %s: %v", ts, err)
	}
	return id
}

func (f *feedFixture) sweepDue(t *testing.T, channel string) {
	t.Helper()
	if _, err := f.e.pool.Exec(f.ctx, `UPDATE sales_feed_channels SET last_swept_at=now()-interval '2 hours' WHERE id=$1`, channel); err != nil {
		t.Fatal(err)
	}
}

func TestFeedPollStoresPostsOnce(t *testing.T) {
	f := newFeedFixture(t)
	posts := []slackPost{
		{TS: feedTS(time.Hour, 1), User: "B1", Text: "New form: ana@acme.example wants a demo"},
		{TS: feedTS(30*time.Minute, 2), User: "B1", Text: "New form: ben@globex.example asks about pricing"},
		{TS: feedTS(10*time.Minute, 3), User: "B1", Text: "New form: cy@initech.example needs 10 seats"},
	}
	f.slack.set("C1", posts...)
	ch := f.channel(t, f.brand, f.integ, "C1", 3)

	if n, err := f.e.srv.EnqueueDueSalesFeeds(f.ctx); err != nil || n != 1 {
		t.Fatalf("due: %d %v", n, err)
	}
	if n := f.count(t, `SELECT count(*) FROM jobs WHERE workspace_id=$1 AND kind='sales.feed_poll' AND payload->>'id'=$2 AND payload->>'brand_id'=$3`, f.ws, ch, f.brand); n != 1 {
		t.Fatalf("poll jobs: %d", n)
	}
	f.mustPoll(t, ch)
	f.mustPoll(t, ch)
	if n := f.items(t, ch); n != 3 {
		t.Fatalf("items: %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM sales_feed_items WHERE feed_channel_id=$1 AND status='pending' AND brand_id=$2 AND kind='new_person'`, ch, f.brand); n != 3 {
		t.Fatalf("pending: %d", n)
	}
	if n := f.itemJobs(t); n != 3 {
		t.Fatalf("item jobs: %d", n)
	}
	id := f.item(t, ch, posts[0].TS)
	c, err := f.e.srv.SalesFeedContent(f.ctx, f.ws, id)
	if err != nil {
		t.Fatal(err)
	}
	if c.Text != posts[0].Text || c.Author != "B1" || c.Link != "https://acme.slack.example/archives/C1/p"+strings.Replace(posts[0].TS, ".", "", 1) {
		t.Fatalf("content: %+v", c)
	}
	var code string
	var read *time.Time
	if err := f.e.pool.QueryRow(f.ctx, `SELECT error_code,last_read_at FROM sales_feed_channels WHERE id=$1`, ch).Scan(&code, &read); err != nil || code != "" || read == nil {
		t.Fatalf("channel: %q %v %v", code, read, err)
	}
	if n, err := f.e.srv.EnqueueDueSalesFeeds(f.ctx); err != nil || n != 0 {
		t.Fatalf("due after a read: %d %v", n, err)
	}
}

func TestFeedPollReadBackDays(t *testing.T) {
	f := newFeedFixture(t)
	day := 24 * time.Hour
	posts := []slackPost{
		{TS: feedTS(9*day, 1), User: "B1", Text: "nine days"},
		{TS: feedTS(4*day, 2), User: "B1", Text: "four days"},
		{TS: feedTS(day, 3), User: "B1", Text: "one day"},
	}
	f.slack.set("C1", posts...)
	f.slack.set("C2", posts...)
	three := f.channel(t, f.brand, f.integ, "C1", 3)
	none := f.channel(t, f.brand, f.integ, "C2", 0)
	f.mustPoll(t, three)
	f.mustPoll(t, none)
	if n := f.items(t, three); n != 1 {
		t.Fatalf("3 days: %d", n)
	}
	f.item(t, three, posts[2].TS)
	if n := f.items(t, none); n != 0 {
		t.Fatalf("0 days: %d", n)
	}
}

func TestFeedPollCursorSurvivesFailure(t *testing.T) {
	f := newFeedFixture(t)
	day := 24 * time.Hour
	posts := []slackPost{
		{TS: feedTS(60*time.Hour, 1), User: "B1", Text: "first"},
		{TS: feedTS(58*time.Hour, 2), User: "B1", Text: "second"},
		{TS: feedTS(day/2, 3), User: "B1", Text: "third"},
	}
	f.slack.set("C1", posts...)
	ch := f.channel(t, f.brand, f.integ, "C1", 3)
	f.slack.fail429At = 2
	err := f.poll(t, ch)
	var later interface{ RetryIn() time.Duration }
	if !errors.As(err, &later) || later.RetryIn() != 60*time.Second || connectors.KindOf(err) != connectors.ErrQuota {
		t.Fatalf("429: %v", err)
	}
	if n := f.items(t, ch); n != 2 {
		t.Fatalf("page one: %d", n)
	}
	var cursor, code string
	if err := f.e.pool.QueryRow(f.ctx, `SELECT cursor_ts,error_code FROM sales_feed_channels WHERE id=$1`, ch).Scan(&cursor, &code); err != nil || cursor < posts[1].TS || cursor >= posts[2].TS || code != "" {
		t.Fatalf("cursor %q code %q %v", cursor, code, err)
	}
	// The sweep isn't due, so every call of the next run reads new posts.
	if _, err := f.e.pool.Exec(f.ctx, `UPDATE sales_feed_channels SET last_swept_at=now() WHERE id=$1`, ch); err != nil {
		t.Fatal(err)
	}
	f.slack.mu.Lock()
	f.slack.fail429At = 0
	f.slack.oldest["C1"] = nil
	f.slack.mu.Unlock()
	f.mustPoll(t, ch)
	if n := f.items(t, ch); n != 3 {
		t.Fatalf("after retry: %d", n)
	}
	if n := f.itemJobs(t); n != 3 {
		t.Fatalf("item jobs: %d", n)
	}
	f.slack.mu.Lock()
	defer f.slack.mu.Unlock()
	if o := f.slack.oldest["C1"]; len(o) == 0 || o[0] != cursor {
		t.Fatalf("read again from %v, not the cursor %s", o, cursor)
	}
}

func TestFeedPollNotInChannel(t *testing.T) {
	f := newFeedFixture(t)
	f.slack.set("C1", slackPost{TS: feedTS(time.Hour, 1), User: "B1", Text: "hidden"})
	f.slack.set("C2", slackPost{TS: feedTS(time.Hour, 2), User: "B1", Text: "usage alert for acme.example"})
	f.slack.out["C1"] = true
	out := f.channel(t, f.brand, f.integ, "C1", 3)
	in := f.channel(t, f.brand, f.integ, "C2", 3)
	if _, err := f.e.pool.Exec(f.ctx, `UPDATE sales_feed_channels SET error_code='slack_auth' WHERE id=$1`, in); err != nil {
		t.Fatal(err)
	}
	f.mustPoll(t, out)
	f.mustPoll(t, in)
	// Still refused two minutes later: the code stays, with no new event.
	f.mustPoll(t, out)
	codes := map[string]string{}
	rows, _ := f.e.pool.Query(f.ctx, `SELECT id::text,error_code FROM sales_feed_channels WHERE workspace_id=$1`, f.ws)
	for rows.Next() {
		var id, c string
		_ = rows.Scan(&id, &c)
		codes[id] = c
	}
	if codes[out] != "slack_channel" || codes[in] != "" {
		t.Fatalf("codes: %v", codes)
	}
	if f.items(t, out) != 0 || f.items(t, in) != 1 {
		t.Fatalf("items: %d %d", f.items(t, out), f.items(t, in))
	}
	var failed []track.Event
	for _, e := range f.events() {
		if e.Name == "sales_feed.read_failed" {
			failed = append(failed, e)
		}
	}
	if len(failed) != 1 || failed[0].ErrorCode != "slack_channel" || failed[0].Surface != "job" || failed[0].WorkspaceID != f.ws || failed[0].Props["feed_channel_id"] != out {
		t.Fatalf("events: %+v", failed)
	}
	for k, v := range failed[0].Props {
		if s, ok := v.(string); ok && strings.Contains(s, "not_in_channel") {
			t.Fatalf("event carries the provider's message in %s", k)
		}
	}
}

func TestFeedSweepEdit(t *testing.T) {
	f := newFeedFixture(t)
	p := slackPost{TS: feedTS(time.Hour, 1), User: "B1", Text: "ana@acme.example wants a demo"}
	f.slack.set("C1", p)
	ch := f.channel(t, f.brand, f.integ, "C1", 3)
	f.mustPoll(t, ch)
	id := f.item(t, ch, p.TS)
	var hash string
	_ = f.e.pool.QueryRow(f.ctx, `SELECT content_hash FROM sales_feed_items WHERE id=$1`, id).Scan(&hash)

	p.Text = "ana@acme.example wants a demo for 12 seats"
	f.slack.set("C1", p)
	f.mustPoll(t, ch) // the sweep isn't due yet
	if n := f.count(t, `SELECT count(*) FROM sales_feed_items WHERE id=$1 AND content_hash=$2 AND edited_at IS NULL`, id, hash); n != 1 {
		t.Fatal("changed before the sweep was due")
	}
	f.sweepDue(t, ch)
	f.mustPoll(t, ch)
	var newHash string
	var edited *time.Time
	if err := f.e.pool.QueryRow(f.ctx, `SELECT content_hash,edited_at FROM sales_feed_items WHERE id=$1`, id).Scan(&newHash, &edited); err != nil || newHash == hash || edited == nil {
		t.Fatalf("edit: %s %v %v", newHash, edited, err)
	}
	if f.items(t, ch) != 1 || f.itemJobs(t) != 2 {
		t.Fatalf("items %d jobs %d", f.items(t, ch), f.itemJobs(t))
	}
	if c, err := f.e.srv.SalesFeedContent(f.ctx, f.ws, id); err != nil || c.Text != p.Text {
		t.Fatalf("content: %+v %v", c, err)
	}
}

func TestFeedSweepDeleted(t *testing.T) {
	f := newFeedFixture(t)
	keep := slackPost{TS: feedTS(2*time.Hour, 1), User: "B1", Text: "stays"}
	gone := slackPost{TS: feedTS(time.Hour, 2), User: "B1", Text: "goes"}
	f.slack.set("C1", keep, gone)
	ch := f.channel(t, f.brand, f.integ, "C1", 3)
	f.mustPoll(t, ch)
	f.slack.set("C1", keep)
	f.sweepDue(t, ch)
	f.mustPoll(t, ch)
	if n := f.count(t, `SELECT count(*) FROM sales_feed_items WHERE feed_channel_id=$1 AND deleted_in_slack`, ch); n != 1 {
		t.Fatalf("deleted: %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM sales_feed_items WHERE id=$1 AND deleted_in_slack`, f.item(t, ch, gone.TS)); n != 1 {
		t.Fatal("the wrong post is marked")
	}
	if f.items(t, ch) != 2 {
		t.Fatal("rows kept")
	}
}

func TestFeedSweepReplies(t *testing.T) {
	f := newFeedFixture(t)
	p := slackPost{TS: feedTS(time.Hour, 1), User: "B1", Text: "ana@acme.example wants a demo"}
	f.slack.set("C1", p)
	ch := f.channel(t, f.brand, f.integ, "C1", 3)
	f.mustPoll(t, ch)
	p.Replies = []slackPost{{TS: feedTS(30*time.Minute, 2), User: "U1", Text: "I'll take it"}, {TS: feedTS(20*time.Minute, 3), User: "U2", Text: "thanks"}}
	f.slack.set("C1", p)
	f.sweepDue(t, ch)
	f.mustPoll(t, ch)
	id := f.item(t, ch, p.TS)
	c, err := f.e.srv.SalesFeedContent(f.ctx, f.ws, id)
	if err != nil || len(c.Replies) != 2 || c.Replies[0].Text != "I'll take it" || c.Replies[1].Author != "U2" || c.Text != p.Text {
		t.Fatalf("replies: %+v %v", c, err)
	}
	if n := f.count(t, `SELECT count(*) FROM sales_feed_items WHERE id=$1 AND latest_reply=$2`, id, p.Replies[1].TS); n != 1 {
		t.Fatal("latest_reply")
	}
	if f.itemJobs(t) != 1 {
		t.Fatalf("re-queued: %d", f.itemJobs(t))
	}
	// Nothing new in the thread: no second fetch.
	before := f.slack.count("conversations.replies")
	f.sweepDue(t, ch)
	f.mustPoll(t, ch)
	if f.slack.count("conversations.replies") != before {
		t.Fatal("fetched an unchanged thread")
	}
}

func TestFeedPollOtherBrandUntouched(t *testing.T) {
	f := newFeedFixture(t)
	other := f.owner.must(201, "POST", "/v1/workspaces/"+f.ws+"/brands", map[string]any{"name": "Northpeak"})["id"].(string)
	theirs := f.owner.must(201, "POST", "/v1/workspaces/"+f.ws+"/integrations", map[string]any{"connector": "slack_feeds",
		"credentials": map[string]any{"api_key": feedBotToken}})["id"].(string)
	if _, err := f.e.pool.Exec(f.ctx, `UPDATE integrations SET brand_id=$2 WHERE id=$1`, theirs, other); err != nil {
		t.Fatal(err)
	}
	f.slack.set("C1", slackPost{TS: feedTS(time.Hour, 1), User: "B1", Text: "Northpeak's own"})
	f.slack.set("C2", slackPost{TS: feedTS(time.Hour, 2), User: "B1", Text: "not ours to read"})
	theirChannel := f.channel(t, other, theirs, "C1", 3)
	borrowed := f.channel(t, f.brand, theirs, "C2", 3)
	before := f.slack.count("conversations.history")

	// This brand's job given the other brand's channel finds nothing.
	f.mustPoll(t, theirChannel)
	// A channel of this brand on the other brand's connection is not read.
	f.mustPoll(t, borrowed)
	if f.slack.count("conversations.history") != before {
		t.Fatal("read Slack for another brand's connection")
	}
	if f.items(t, theirChannel) != 0 || f.items(t, borrowed) != 0 {
		t.Fatal("stored")
	}
	var read *time.Time
	if err := f.e.pool.QueryRow(f.ctx, `SELECT last_read_at FROM sales_feed_channels WHERE id=$1`, theirChannel).Scan(&read); err != nil || read != nil {
		t.Fatalf("other brand's channel touched: %v %v", read, err)
	}
	var code string
	if err := f.e.pool.QueryRow(f.ctx, `SELECT error_code FROM sales_feed_channels WHERE id=$1`, borrowed).Scan(&code); err != nil || code != "slack_auth" {
		t.Fatalf("borrowed: %q %v", code, err)
	}
}

func (f *feedFixture) code(t *testing.T, channel string) string {
	t.Helper()
	var c string
	if err := f.e.pool.QueryRow(f.ctx, `SELECT error_code FROM sales_feed_channels WHERE id=$1`, channel).Scan(&c); err != nil {
		t.Fatal(err)
	}
	return c
}

// A post Slack shows only after the cursor passed its timestamp (a clock
// ahead of Slack's, a late Slack Connect post) is stored by the next sweep;
// one older than the channel's read-back days is not.
func TestFeedSweepStoresMissedPost(t *testing.T) {
	f := newFeedFixture(t)
	day := 24 * time.Hour
	first := slackPost{TS: feedTS(time.Hour, 1), User: "B1", Text: "first"}
	f.slack.set("C1", first)
	ch := f.channel(t, f.brand, f.integ, "C1", 3)
	f.mustPoll(t, ch)
	late := slackPost{TS: feedTS(10*time.Minute, 2), User: "B1", Text: "ana@acme.example arrived late"}
	old := slackPost{TS: feedTS(5*day, 3), User: "B1", Text: "older than the read-back days"}
	f.slack.set("C1", old, first, late)
	f.mustPoll(t, ch) // the sweep isn't due: the cursor is past the late post
	if f.items(t, ch) != 1 {
		t.Fatalf("stored before the sweep: %d", f.items(t, ch))
	}
	f.sweepDue(t, ch)
	f.mustPoll(t, ch)
	f.sweepDue(t, ch)
	f.mustPoll(t, ch)
	if f.items(t, ch) != 2 || f.itemJobs(t) != 2 {
		t.Fatalf("items %d jobs %d", f.items(t, ch), f.itemJobs(t))
	}
	id := f.item(t, ch, late.TS)
	if c, err := f.e.srv.SalesFeedContent(f.ctx, f.ws, id); err != nil || c.Text != late.Text || !strings.HasPrefix(c.Link, "https://acme.slack.example/archives/C1/p") {
		t.Fatalf("content: %+v %v", c, err)
	}
}

// A sweep whose read fails marks nothing deleted and waits before trying
// again, instead of running on every poll.
func TestFeedSweepFailureMarksNothing(t *testing.T) {
	f := newFeedFixture(t)
	keep := slackPost{TS: feedTS(2*time.Hour, 1), User: "B1", Text: "stays"}
	gone := slackPost{TS: feedTS(time.Hour, 2), User: "B1", Text: "goes"}
	f.slack.set("C1", keep, gone)
	ch := f.channel(t, f.brand, f.integ, "C1", 3)
	f.mustPoll(t, ch)
	f.slack.set("C1", keep)
	f.sweepDue(t, ch)
	f.slack.mu.Lock()
	f.slack.failAt, f.slack.failWith = f.slack.calls["conversations.history"]+2, "internal_error" // the poll's read, then the sweep's
	f.slack.mu.Unlock()
	if err := f.poll(t, ch); err == nil {
		t.Fatal("the sweep's failure was not reported")
	}
	if n := f.count(t, `SELECT count(*) FROM sales_feed_items WHERE feed_channel_id=$1 AND deleted_in_slack`, ch); n != 0 {
		t.Fatalf("marked deleted: %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM sales_feed_channels WHERE id=$1 AND last_swept_at > now()-interval '1 hour'+interval '9 minutes'`, ch); n != 1 {
		t.Fatal("the next sweep isn't put off")
	}
	before := f.slack.count("conversations.history")
	f.mustPoll(t, ch)
	if got := f.slack.count("conversations.history") - before; got != 1 {
		t.Fatalf("history calls on the next poll: %d (the sweep ran again)", got)
	}
	if n := f.count(t, `SELECT count(*) FROM sales_feed_items WHERE feed_channel_id=$1 AND deleted_in_slack`, ch); n != 0 {
		t.Fatalf("marked deleted: %d", n)
	}
}

func TestFeedPollRevokedToken(t *testing.T) {
	f := newFeedFixture(t)
	other := f.owner.must(201, "POST", "/v1/workspaces/"+f.ws+"/integrations", map[string]any{"connector": "slack_feeds",
		"credentials": map[string]any{"api_key": feedBotToken2}})["id"].(string)
	f.slack.set("C1", slackPost{TS: feedTS(time.Hour, 1), User: "B1", Text: "behind a revoked token"})
	f.slack.set("C2", slackPost{TS: feedTS(time.Hour, 2), User: "B1", Text: "readable"})
	revoked := f.channel(t, f.brand, f.integ, "C1", 3)
	fine := f.channel(t, f.brand, other, "C2", 3)
	f.slack.mu.Lock()
	f.slack.revoked[feedBotToken] = true
	f.slack.mu.Unlock()
	f.mustPoll(t, revoked)
	f.mustPoll(t, fine)
	if f.code(t, revoked) != "slack_auth" || f.code(t, fine) != "" || f.items(t, revoked) != 0 || f.items(t, fine) != 1 {
		t.Fatalf("codes %q %q items %d %d", f.code(t, revoked), f.code(t, fine), f.items(t, revoked), f.items(t, fine))
	}
	var failed []track.Event
	for _, e := range f.events() {
		if e.Name == "sales_feed.read_failed" {
			failed = append(failed, e)
		}
	}
	if len(failed) != 1 || failed[0].ErrorCode != "slack_auth" || failed[0].Props["feed_channel_id"] != revoked {
		t.Fatalf("events: %+v", failed)
	}
}

func TestFeedPollUnavailable(t *testing.T) {
	f := newFeedFixture(t)
	f.slack.set("C1", slackPost{TS: feedTS(time.Hour, 1), User: "B1", Text: "later"})
	ch := f.channel(t, f.brand, f.integ, "C1", 3)
	f.slack.failAt, f.slack.failWith = 1, "internal_error"
	if err := f.poll(t, ch); err == nil || connectors.KindOf(err) != connectors.ErrUnavailable || strings.Contains(err.Error(), "internal_error") {
		t.Fatalf("unavailable: %v", err)
	}
	if f.code(t, ch) != "slack_unavailable" || f.items(t, ch) != 0 {
		t.Fatalf("code %q items %d", f.code(t, ch), f.items(t, ch))
	}
	f.mustPoll(t, ch)
	if f.code(t, ch) != "" || f.items(t, ch) != 1 {
		t.Fatalf("after a clean read: code %q items %d", f.code(t, ch), f.items(t, ch))
	}
}

// Slack shows a deleted parent that has replies as "This message was
// deleted.": the post keeps what it said, stays marked deleted through
// every sweep, its replies still follow, and it is not read again.
func TestFeedSweepDeletedParentKeepsText(t *testing.T) {
	f := newFeedFixture(t)
	p := slackPost{TS: feedTS(time.Hour, 1), User: "B1", Text: "Contact: ana@acme.example wants a demo",
		Replies: []slackPost{{TS: feedTS(50*time.Minute, 2), User: "U1", Text: "mine"}}}
	f.slack.set("C1", p)
	ch := f.channel(t, f.brand, f.integ, "C1", 3)
	f.mustPoll(t, ch)
	id := f.item(t, ch, p.TS)
	jobs := f.itemJobs(t)
	p.Text = "This message was deleted."
	p.Replies = append(p.Replies, slackPost{TS: feedTS(40*time.Minute, 3), User: "U2", Text: "it went"})
	f.slack.set("C1", p)
	for round := 1; round <= 2; round++ {
		f.sweepDue(t, ch)
		f.mustPoll(t, ch)
		c, err := f.e.srv.SalesFeedContent(f.ctx, f.ws, id)
		if err != nil || c.Text != "Contact: ana@acme.example wants a demo" || len(c.Replies) != 2 {
			t.Fatalf("sweep %d: content %+v %v", round, c, err)
		}
		if r := f.itemRow(t, id); !r.Deleted {
			t.Fatalf("sweep %d: not marked deleted", round)
		}
	}
	if f.itemJobs(t) != jobs {
		t.Fatalf("read again: %d jobs, want %d", f.itemJobs(t), jobs)
	}
}

// A first read that covers whole days ends on a window that reaches now. The
// cursor it leaves still stops the overlap short of now, so a post in the
// same second as that read is stored by the next one.
func TestFeedFirstReadKeepsTheOverlap(t *testing.T) {
	f := newFeedFixture(t)
	f.slack.set("C1", slackPost{TS: feedTS(time.Hour, 1), User: "B1", Text: "first"})
	ch := f.channel(t, f.brand, f.integ, "C1", 3)
	before := time.Now()
	f.mustPoll(t, ch)
	var cursor string
	if err := f.e.pool.QueryRow(f.ctx, `SELECT cursor_ts FROM sales_feed_channels WHERE id=$1`, ch).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	if limit := fmt.Sprintf("%d.000000", before.Add(-30*time.Second).Unix()); cursor >= limit {
		t.Fatalf("the cursor %s is within 30 seconds of now", cursor)
	}
	// Stamped a moment before the read ended, as Slack does for a post it
	// shows a moment late.
	same := slackPost{TS: fmt.Sprintf("%d.000000", before.Unix()), User: "B1", Text: "ana@acme.example"}
	f.slack.set("C1", same)
	f.mustPoll(t, ch)
	f.item(t, ch, same.TS)
}
