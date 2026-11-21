package api_test

import (
	"fmt"
	"math"
	"strconv"
	"testing"
	"time"
)

func (f *feedFixture) feedsPath() string { return "/v1/brands/" + f.brand + "/sales/feeds" }

func (f *feedFixture) putFeeds(t *testing.T, want int, channels ...map[string]any) map[string]any {
	t.Helper()
	if channels == nil {
		channels = []map[string]any{}
	}
	return f.owner.must(want, "PUT", f.feedsPath(), map[string]any{"integration_id": f.integ, "channels": channels})
}

func feedRows(t *testing.T, out map[string]any) map[string]map[string]any {
	t.Helper()
	m := map[string]map[string]any{}
	for _, c := range out["channels"].([]any) {
		c := c.(map[string]any)
		m[c["channel_id"].(string)] = c
	}
	return m
}

func TestSalesFeedsRoundTrip(t *testing.T) {
	f := newFeedFixture(t)
	empty := f.owner.must(200, "GET", f.feedsPath(), nil)
	if empty["integration_id"] != f.integ || len(empty["channels"].([]any)) != 0 || empty["search"] != false {
		t.Fatalf("before: %v", empty)
	}
	out := f.putFeeds(t, 200,
		map[string]any{"channel_id": "C1", "kind": "new_person", "read_back_days": 5},
		map[string]any{"channel_id": "C2", "kind": "signal", "signal_type": "upsell", "enabled": false})
	rows := feedRows(t, f.owner.must(200, "GET", f.feedsPath(), nil))
	if len(rows) != 2 {
		t.Fatalf("channels: %v", out)
	}
	c1, c2 := rows["C1"], rows["C2"]
	if c1["kind"] != "new_person" || c1["channel_name"] != "website-leads" || c1["read_back_days"] != float64(5) || c1["enabled"] != true || c1["signal_type"] != "upsell" {
		t.Fatalf("C1: %v", c1)
	}
	if c2["kind"] != "signal" || c2["channel_name"] != "usage-alerts" || c2["enabled"] != false {
		t.Fatalf("C2: %v", c2)
	}
	zero := map[string]any{"read": float64(0), "needs_look": float64(0), "spam": float64(0), "failed": float64(0)}
	for _, c := range rows {
		got := c["counts_7d"].(map[string]any)
		for k, v := range zero {
			if got[k] != v {
				t.Fatalf("counts: %v", got)
			}
		}
	}
	updated := false
	for _, e := range f.events() {
		if e.Name == "sales_feed.updated" {
			updated = true
			if e.Props["channels"] != 2 {
				t.Fatalf("event props: %v", e.Props)
			}
		}
	}
	if !updated {
		t.Fatal("no sales_feed.updated event")
	}
	// Later saves keep read_back_days and read the new kind.
	f.putFeeds(t, 200, map[string]any{"channel_id": "C1", "kind": "handover", "read_back_days": 1})
	c1 = feedRows(t, f.owner.must(200, "GET", f.feedsPath(), nil))["C1"]
	if c1["kind"] != "handover" || c1["read_back_days"] != float64(5) || c1["id"] != rows["C1"]["id"] {
		t.Fatalf("C1 after: %v", c1)
	}
}

func TestSalesFeedsCountsLastWeek(t *testing.T) {
	f := newFeedFixture(t)
	f.putFeeds(t, 200, map[string]any{"channel_id": "C1", "kind": "new_person"})
	var ch string
	if err := f.e.pool.QueryRow(f.ctx, `SELECT id::text FROM sales_feed_channels WHERE workspace_id=$1`, f.ws).Scan(&ch); err != nil {
		t.Fatal(err)
	}
	a, b, old := f.seedIn(t, ch, "one"), f.seedIn(t, ch, "two"), f.seedIn(t, ch, "three")
	for id, st := range map[string]string{a: "spam", b: "spam", old: "failed"} {
		f.e.pool.Exec(f.ctx, `UPDATE sales_feed_items SET status=$2 WHERE id=$1`, id, st)
	}
	f.e.pool.Exec(f.ctx, `UPDATE sales_feed_items SET occurred_at=now()-interval '8 days' WHERE id=$1`, old)
	got := feedRows(t, f.owner.must(200, "GET", f.feedsPath(), nil))["C1"]["counts_7d"].(map[string]any)
	if got["spam"] != float64(2) || got["failed"] != float64(0) || got["read"] != float64(0) {
		t.Fatalf("counts: %v", got)
	}
}

func TestSalesFeedsRejectsWrongConnector(t *testing.T) {
	f := newFeedFixture(t)
	sk := f.owner.must(201, "POST", "/v1/workspaces/"+f.ws+"/integrations", map[string]any{"connector": "slack_knowledge",
		"credentials": map[string]any{"api_key": feedBotToken}})["id"].(string)
	f.owner.must(400, "PUT", f.feedsPath(), map[string]any{"integration_id": sk, "channels": []any{map[string]any{"channel_id": "C1", "kind": "new_person"}}})
	f.owner.must(400, "GET", f.feedsPath()+"/channels?integration_id="+sk, nil)
	f.owner.must(400, "PUT", f.feedsPath(), map[string]any{"integration_id": "nope", "channels": []any{}})
}

func TestSalesFeedsRejectsOtherBrand(t *testing.T) {
	f := newFeedFixture(t)
	other := f.owner.must(201, "POST", "/v1/workspaces/"+f.ws+"/brands", map[string]any{"name": "Northpeak"})["id"].(string)
	theirs := f.owner.must(201, "POST", "/v1/workspaces/"+f.ws+"/integrations", map[string]any{"connector": "slack_feeds",
		"credentials": map[string]any{"api_key": feedBotToken}})["id"].(string)
	if _, err := f.e.pool.Exec(f.ctx, `UPDATE integrations SET brand_id=$2 WHERE id=$1`, theirs, other); err != nil {
		t.Fatal(err)
	}
	f.owner.must(400, "PUT", f.feedsPath(), map[string]any{"integration_id": theirs, "channels": []any{map[string]any{"channel_id": "C1", "kind": "new_person"}}})
	f.owner.must(400, "POST", f.feedsPath()+"/test", map[string]any{"integration_id": theirs, "channel_id": "C1", "kind": "new_person"})
	if f.count(t, `SELECT count(*) FROM sales_feed_channels WHERE workspace_id=$1`, f.ws) != 0 {
		t.Fatal("saved")
	}
}

func TestSalesFeedsUnknownSignalType(t *testing.T) {
	f := newFeedFixture(t)
	f.putFeeds(t, 400, map[string]any{"channel_id": "C2", "kind": "signal", "signal_type": "no_such_type"})
	f.putFeeds(t, 400, map[string]any{"channel_id": "C2", "kind": "nonsense"})
	f.putFeeds(t, 400, map[string]any{"channel_id": "C2", "kind": "signal", "read_back_days": 9})
	f.putFeeds(t, 400, map[string]any{"channel_id": "C1", "kind": "signal"}, map[string]any{"channel_id": "C1", "kind": "context"})
	// The default type is checked like a given one.
	f.putFeeds(t, 200)
	if _, err := f.e.pool.Exec(f.ctx, `UPDATE signal_types SET archived_at=now() WHERE brand_id=$1 AND key='upsell'`, f.brand); err != nil {
		t.Fatal(err)
	}
	f.putFeeds(t, 400, map[string]any{"channel_id": "C2", "kind": "signal"})
	f.putFeeds(t, 200, map[string]any{"channel_id": "C2", "kind": "context"})
	// The settings page sends a type for every row: one that isn't a signal
	// channel saves whatever it gives, since it is never used.
	f.putFeeds(t, 200, map[string]any{"channel_id": "C2", "kind": "context", "signal_type": "upsell"},
		map[string]any{"channel_id": "C1", "kind": "new_person", "signal_type": "no_such_type"})
	f.putFeeds(t, 200)
	if f.count(t, `SELECT count(*) FROM sales_feed_channels WHERE workspace_id=$1 AND removed_at IS NULL`, f.ws) != 0 {
		t.Fatal("saved")
	}
}

func TestSalesFeedsUnknownChannel(t *testing.T) {
	f := newFeedFixture(t)
	f.putFeeds(t, 400, map[string]any{"channel_id": "C1", "kind": "new_person"}, map[string]any{"channel_id": "C9", "kind": "context"})
	if f.count(t, `SELECT count(*) FROM sales_feed_channels WHERE workspace_id=$1`, f.ws) != 0 {
		t.Fatal("saved a channel Slack does not list")
	}
}

func TestSalesFeedsMaxFifty(t *testing.T) {
	f := newFeedFixture(t)
	var list []map[string]any
	for i := 0; i < 51; i++ {
		list = append(list, map[string]any{"channel_id": fmt.Sprintf("C%d", i+1), "kind": "context"})
	}
	f.putFeeds(t, 400, list...)
}

func TestSalesFeedsRemoveChannel(t *testing.T) {
	f := newFeedFixture(t)
	f.putFeeds(t, 200, map[string]any{"channel_id": "C1", "kind": "new_person"}, map[string]any{"channel_id": "C2", "kind": "context"})
	var ch string
	if err := f.e.pool.QueryRow(f.ctx, `SELECT id::text FROM sales_feed_channels WHERE workspace_id=$1 AND channel_id='C1'`, f.ws).Scan(&ch); err != nil {
		t.Fatal(err)
	}
	item := f.seedIn(t, ch, "Contact: ana@acme.example wants a demo")
	f.runItem(t, item)
	if f.itemRow(t, item).LeadID == nil {
		t.Fatal("no conversation made")
	}
	out := f.putFeeds(t, 200, map[string]any{"channel_id": "C2", "kind": "context"})
	if rows := feedRows(t, out); len(rows) != 1 || rows["C2"] == nil {
		t.Fatalf("after: %v", out)
	}
	// Removed: no longer listed or read, and its row and posts stay.
	if f.count(t, `SELECT count(*) FROM sales_feed_channels WHERE id=$1 AND removed_at IS NOT NULL AND NOT enabled`, ch) != 1 || f.count(t, `SELECT count(*) FROM sales_feed_items WHERE id=$1`, item) != 1 {
		t.Fatal("the channel was not kept as removed, or its posts went")
	}
	lead := *f.itemRow(t, item).LeadID
	detail := f.owner.must(200, "GET", f.salesRoot()+"/leads/"+lead, nil)
	if ids := listedIDs(detail["feed_items"].([]any)); len(ids) != 1 || ids[0] != item {
		t.Fatalf("the conversation lost its post: %v", ids)
	}
	if detail["lead"].(map[string]any)["feed_channel_name"] != "website-leads" {
		t.Fatalf("the conversation lost its channel's name: %v", detail["lead"])
	}
	if f.count(t, `SELECT count(*) FROM sales_leads WHERE workspace_id=$1`, f.ws) != 1 || f.person(t, "ana@acme.example") == "" {
		t.Fatal("the conversation or person went with the channel")
	}
	if n, err := f.e.srv.EnqueueDueSalesFeeds(f.ctx); err != nil || n != 1 {
		t.Fatalf("due: %d %v (only C2 is read)", n, err)
	}
	f.putFeeds(t, 200)
	if len(feedRows(t, f.owner.must(200, "GET", f.feedsPath(), nil))) != 0 {
		t.Fatal("an empty list keeps channels listed")
	}
	if f.count(t, `SELECT count(*) FROM sales_feed_channels WHERE workspace_id=$1 AND removed_at IS NULL`, f.ws) != 0 {
		t.Fatal("an empty list keeps channels")
	}
}

// feedChannelID is the brand's channel row for a Slack channel id.
func (f *feedFixture) feedChannelID(t *testing.T, slackID string) string {
	t.Helper()
	var id string
	if err := f.e.pool.QueryRow(f.ctx, `SELECT id::text FROM sales_feed_channels WHERE workspace_id=$1 AND brand_id=$2 AND channel_id=$3`, f.ws, f.brand, slackID).Scan(&id); err != nil {
		t.Fatalf("channel %s: %v", slackID, err)
	}
	return id
}

// Deleting the Slack connection keeps every channel and post: the channels
// say their connection is gone and are not read, and saving them with a new
// connection moves them to it.
func TestSalesFeedsDisconnectKeepsPosts(t *testing.T) {
	f := newFeedFixture(t)
	f.putFeeds(t, 200, map[string]any{"channel_id": "C1", "kind": "new_person"})
	ch := f.feedChannelID(t, "C1")
	item := f.seedIn(t, ch, "Contact: ana@acme.example wants a demo")
	f.runItem(t, item)
	lead := *f.itemRow(t, item).LeadID

	f.owner.must(204, "DELETE", "/v1/workspaces/"+f.ws+"/integrations/"+f.integ, nil)
	if f.count(t, `SELECT count(*) FROM sales_feed_channels WHERE id=$1 AND integration_id IS NULL`, ch) != 1 || f.count(t, `SELECT count(*) FROM sales_feed_items WHERE id=$1`, item) != 1 {
		t.Fatal("the channel or its post went with the connection")
	}
	if ids := listedIDs(f.owner.must(200, "GET", f.salesRoot()+"/leads/"+lead, nil)["feed_items"].([]any)); len(ids) != 1 || ids[0] != item {
		t.Fatalf("the conversation lost its post: %v", ids)
	}
	out := f.owner.must(200, "GET", f.feedsPath(), nil)
	if c := feedRows(t, out)["C1"]; c == nil || c["connection_removed"] != true || c["id"] != ch {
		t.Fatalf("settings: %v", out)
	}
	if n, err := f.e.srv.EnqueueDueSalesFeeds(f.ctx); err != nil || n != 0 {
		t.Fatalf("due without a connection: %d %v", n, err)
	}
	before := f.slack.count("conversations.history")
	f.mustPoll(t, ch)
	if f.slack.count("conversations.history") != before {
		t.Fatal("read a channel with no connection")
	}

	f.integ = f.owner.must(201, "POST", "/v1/workspaces/"+f.ws+"/integrations", map[string]any{"connector": "slack_feeds",
		"credentials": map[string]any{"api_key": feedBotToken2}})["id"].(string)
	c := feedRows(t, f.putFeeds(t, 200, map[string]any{"channel_id": "C1", "kind": "new_person"}))["C1"]
	if c["id"] != ch || c["connection_removed"] != false {
		t.Fatalf("after reconnecting: %v", c)
	}
	if f.count(t, `SELECT count(*) FROM sales_feed_channels WHERE id=$1 AND integration_id=$2`, ch, f.integ) != 1 || f.count(t, `SELECT count(*) FROM sales_feed_items WHERE id=$1`, item) != 1 {
		t.Fatal("not moved to the new connection, or the post went")
	}
}

// A brand-specific connection that replaces the workspace's moves the
// channels to it, posts and all.
func TestSalesFeedsSwitchConnectionKeepsItems(t *testing.T) {
	f := newFeedFixture(t)
	f.putFeeds(t, 200, map[string]any{"channel_id": "C1", "kind": "new_person"}, map[string]any{"channel_id": "C2", "kind": "context"})
	ch := f.feedChannelID(t, "C1")
	a, b := f.seedIn(t, ch, "Contact: ana@acme.example"), f.seedIn(t, ch, "Contact: bo@acme.example")
	f.runItem(t, a)
	mine := f.owner.must(201, "POST", "/v1/workspaces/"+f.ws+"/integrations", map[string]any{"connector": "slack_feeds",
		"credentials": map[string]any{"api_key": feedBotToken2}})["id"].(string)
	f.exec(t, `UPDATE integrations SET brand_id=$2 WHERE id=$1`, mine, f.brand)
	f.integ = mine
	rows := feedRows(t, f.putFeeds(t, 200, map[string]any{"channel_id": "C1", "kind": "new_person"}, map[string]any{"channel_id": "C2", "kind": "context"}))
	if rows["C1"]["id"] != ch {
		t.Fatalf("C1 is a new row: %v", rows["C1"])
	}
	if f.count(t, `SELECT count(*) FROM sales_feed_channels WHERE workspace_id=$1`, f.ws) != 2 || f.count(t, `SELECT count(*) FROM sales_feed_channels WHERE workspace_id=$1 AND integration_id=$2`, f.ws, mine) != 2 {
		t.Fatal("the channels did not move to the brand's connection")
	}
	if f.count(t, `SELECT count(*) FROM sales_feed_items WHERE feed_channel_id=$1 AND id IN ($2,$3)`, ch, a, b) != 2 {
		t.Fatal("the posts went")
	}
}

// A manager saving a page loaded before another added a channel removes it,
// and its posts stay.
func TestSalesFeedsStaleSaveKeepsPosts(t *testing.T) {
	f := newFeedFixture(t)
	f.putFeeds(t, 200, map[string]any{"channel_id": "C1", "kind": "new_person"})
	// Another manager adds C2, and a post arrives in it.
	f.putFeeds(t, 200, map[string]any{"channel_id": "C1", "kind": "new_person"}, map[string]any{"channel_id": "C2", "kind": "handover"})
	ch := f.feedChannelID(t, "C2")
	item := f.seedIn(t, ch, "Hand over: ana@acme.example")
	f.runItem(t, item)
	// The first manager's page still has C1 only.
	f.putFeeds(t, 200, map[string]any{"channel_id": "C1", "kind": "new_person"})
	if f.count(t, `SELECT count(*) FROM sales_feed_items WHERE id=$1 AND feed_channel_id=$2`, item, ch) != 1 {
		t.Fatal("the other manager's channel's post went")
	}
	lead := *f.itemRow(t, item).LeadID
	if ids := listedIDs(f.owner.must(200, "GET", f.salesRoot()+"/leads/"+lead, nil)["feed_items"].([]any)); len(ids) != 1 || ids[0] != item {
		t.Fatalf("conversation entries: %v", ids)
	}
}

// Removing a channel and adding it back is the same channel: its posts are
// not stored or read twice, and reading starts again read_back_days back.
func TestSalesFeedsReAddRevives(t *testing.T) {
	f := newFeedFixture(t)
	post := slackPost{TS: feedTS(time.Hour, 1), User: "B1", Text: "Contact: ana@acme.example wants a demo"}
	f.slack.set("C1", post)
	f.putFeeds(t, 200, map[string]any{"channel_id": "C1", "kind": "new_person"})
	ch := f.feedChannelID(t, "C1")
	f.mustPoll(t, ch)
	item := f.item(t, ch, post.TS)
	f.runItem(t, item)
	f.putFeeds(t, 200)
	f.mustPoll(t, ch) // removed: not read
	added := feedRows(t, f.putFeeds(t, 200, map[string]any{"channel_id": "C1", "kind": "new_person", "read_back_days": 2}))["C1"]
	if added["id"] != ch || added["read_back_days"] != float64(2) {
		t.Fatalf("added back: %v", added)
	}
	var cursor string
	if err := f.e.pool.QueryRow(f.ctx, `SELECT cursor_ts FROM sales_feed_channels WHERE id=$1`, ch).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	got, _ := strconv.ParseFloat(cursor, 64)
	if want := float64(time.Now().Add(-48 * time.Hour).Unix()); math.Abs(got-want) > 120 {
		t.Fatalf("cursor %s, want about %.0f", cursor, want)
	}
	f.sweepDue(t, ch)
	f.mustPoll(t, ch)
	if f.items(t, ch) != 1 || f.item(t, ch, post.TS) != item {
		t.Fatalf("posts after adding back: %d", f.items(t, ch))
	}
	if f.count(t, `SELECT count(*) FROM sales_leads WHERE workspace_id=$1`, f.ws) != 1 {
		t.Fatal("a second conversation")
	}
}

// A channel turned on again reads from read_back_days before now: what was
// posted earlier while it was off is not read, by the poll or the sweep.
func TestSalesFeedsTurnOnRestartsReading(t *testing.T) {
	f := newFeedFixture(t)
	day := 24 * time.Hour
	f.putFeeds(t, 200, map[string]any{"channel_id": "C1", "kind": "new_person", "read_back_days": 1})
	ch := f.feedChannelID(t, "C1")
	f.mustPoll(t, ch)
	f.putFeeds(t, 200, map[string]any{"channel_id": "C1", "kind": "new_person", "enabled": false})
	// Off for three days.
	f.exec(t, `UPDATE sales_feed_channels SET cursor_ts=$2,created_at=now()-interval '4 days',read_since=now()-interval '4 days' WHERE id=$1`, ch, feedTS(3*day, 0))
	whileOff := slackPost{TS: feedTS(2*day, 1), User: "B1", Text: "Contact: old@acme.example"}
	recent := slackPost{TS: feedTS(time.Hour, 2), User: "B1", Text: "Contact: new@acme.example"}
	f.slack.set("C1", whileOff, recent)
	f.putFeeds(t, 200, map[string]any{"channel_id": "C1", "kind": "new_person", "enabled": true})
	f.mustPoll(t, ch)
	f.sweepDue(t, ch)
	f.mustPoll(t, ch)
	if f.items(t, ch) != 1 {
		t.Fatalf("posts: %d, want only the one within a day", f.items(t, ch))
	}
	f.item(t, ch, recent.TS)
}

func TestSalesFeedsTurnOnQueuesWaitingPosts(t *testing.T) {
	f := newFeedFixture(t)
	f.putFeeds(t, 200, map[string]any{"channel_id": "C1", "kind": "new_person", "enabled": false})
	var ch string
	if err := f.e.pool.QueryRow(f.ctx, `SELECT id::text FROM sales_feed_channels WHERE workspace_id=$1`, f.ws).Scan(&ch); err != nil {
		t.Fatal(err)
	}
	f.seedIn(t, ch, "Contact: ana@acme.example")
	f.seedIn(t, ch, "Contact: bo@acme.example")
	before := f.itemJobs(t)
	// Saved again, still off: nothing is queued.
	f.putFeeds(t, 200, map[string]any{"channel_id": "C1", "kind": "new_person", "enabled": false})
	if f.itemJobs(t) != before {
		t.Fatal("queued while off")
	}
	f.putFeeds(t, 200, map[string]any{"channel_id": "C1", "kind": "new_person", "enabled": true})
	if got := f.itemJobs(t); got != before+2 {
		t.Fatalf("jobs after turning on: %d, want %d", got, before+2)
	}
	// Left on, a later save queues nothing more.
	f.putFeeds(t, 200, map[string]any{"channel_id": "C1", "kind": "new_person"})
	if got := f.itemJobs(t); got != before+2 {
		t.Fatalf("jobs after another save: %d", got)
	}
}

func TestSalesFeedsOmittedEnabledKeepsIt(t *testing.T) {
	f := newFeedFixture(t)
	f.putFeeds(t, 200, map[string]any{"channel_id": "C1", "kind": "new_person", "enabled": false})
	var ch string
	if err := f.e.pool.QueryRow(f.ctx, `SELECT id::text FROM sales_feed_channels WHERE workspace_id=$1`, f.ws).Scan(&ch); err != nil {
		t.Fatal(err)
	}
	f.seedIn(t, ch, "Contact: ana@acme.example")
	before := f.itemJobs(t)
	out := f.putFeeds(t, 200, map[string]any{"channel_id": "C1", "kind": "handover"})
	if feedRows(t, out)["C1"]["enabled"] != false || f.itemJobs(t) != before {
		t.Fatalf("omitted enabled turned the channel on: %v", out)
	}
	// A new channel without it is on.
	out = f.putFeeds(t, 200, map[string]any{"channel_id": "C1", "kind": "handover"}, map[string]any{"channel_id": "C2", "kind": "context"})
	if feedRows(t, out)["C2"]["enabled"] != true {
		t.Fatalf("new channel: %v", out)
	}
}

func TestSalesFeedTestBooksEverySpend(t *testing.T) {
	f := newFeedFixture(t)
	answer := spamAnswer(0.1)
	f.feedAIAnswer(t, &answer)
	f.slack.set("C1",
		slackPost{TS: feedTS(2*time.Hour, 1), User: "B1", Text: "Contact: a@acme.example"},
		slackPost{TS: feedTS(time.Hour, 2), User: "B1", Text: "Contact: b@acme.example"})
	calls := func() int { return f.count(t, `SELECT count(*) FROM ai_calls WHERE workspace_id=$1`, f.ws) }
	for round := 1; round <= 2; round++ {
		f.owner.must(200, "POST", f.feedsPath()+"/test", map[string]any{"integration_id": f.integ, "channel_id": "C1", "kind": "new_person"})
		if got := calls(); got != 2*round {
			t.Fatalf("after preview %d: %d AI calls booked, want %d", round, got, 2*round)
		}
	}
}

func TestSalesFeedsNeedsManage(t *testing.T) {
	f := newFeedFixture(t)
	inv := f.owner.must(201, "POST", "/v1/workspaces/"+f.ws+"/invites", map[string]string{"email": "sam@acme.example", "role": "operator"})
	sdr := &client{t: t, h: f.e.h}
	sdr.must(200, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "name": "Sam", "password": "a long enough password"})
	sdr.must(200, "GET", f.feedsPath(), nil)
	sdr.must(403, "PUT", f.feedsPath(), map[string]any{"integration_id": f.integ, "channels": []any{}})
	sdr.must(403, "GET", f.feedsPath()+"/channels?integration_id="+f.integ, nil)
	sdr.must(403, "POST", f.feedsPath()+"/test", map[string]any{"integration_id": f.integ, "channel_id": "C1", "kind": "context"})
}

func TestSalesFeedChannelsFromSlack(t *testing.T) {
	f := newFeedFixture(t)
	list := f.owner.must(200, "GET", f.feedsPath()+"/channels?integration_id="+f.integ, nil)["data"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["id"] != "C2" || list[1].(map[string]any)["name"] != "website-leads" {
		t.Fatalf("channels: %v", list)
	}
	f.slack.revoked[feedBotToken] = true
	f.owner.must(422, "GET", f.feedsPath()+"/channels?integration_id="+f.integ, nil)
	if !f.hasEvent("sales_feed.slack_failed") {
		t.Fatal("no failure event")
	}
}

func TestSalesFeedTestSavesNothing(t *testing.T) {
	f := newFeedFixture(t)
	var posts []slackPost
	for i := 1; i <= 7; i++ {
		text := fmt.Sprintf("Contact: p%d@acme.example wants a demo", i)
		if i == 2 {
			text = "Someone said hello with no address"
		}
		posts = append(posts, slackPost{TS: feedTS(time.Duration(10-i)*time.Hour, i), User: "B1", Text: text})
	}
	f.slack.set("C1", posts...)
	records := f.count(t, `SELECT count(*) FROM records WHERE workspace_id=$1`, f.ws)
	before := f.slack.count("conversations.history")
	out := f.owner.must(200, "POST", f.feedsPath()+"/test", map[string]any{"integration_id": f.integ, "channel_id": "C1", "kind": "new_person"})["data"].([]any)
	if len(out) != 5 {
		t.Fatalf("previews: %d", len(out))
	}
	if got := f.slack.count("conversations.history") - before; got != 1 {
		t.Fatalf("history calls for a preview: %d", got)
	}
	first := out[0].(map[string]any)
	if first["text"] != "Contact: p7@acme.example wants a demo" || first["would_make"] != "conversation" || first["candidates"].([]any)[0] != "p7@acme.example" {
		t.Fatalf("newest: %v", first)
	}
	if len(first["fields"].([]any)) == 0 {
		t.Fatalf("fields: %v", first)
	}
	if got := out[len(out)-1].(map[string]any); got["text"] != "Contact: p3@acme.example wants a demo" {
		t.Fatalf("oldest of five: %v", got)
	}
	if f.count(t, `SELECT count(*) FROM sales_feed_items`) != 0 || f.count(t, `SELECT count(*) FROM sales_feed_channels`) != 0 ||
		f.count(t, `SELECT count(*) FROM records WHERE workspace_id=$1`, f.ws) != records || f.count(t, `SELECT count(*) FROM sales_leads`) != 0 {
		t.Fatal("the preview saved something")
	}
	kind := func(kind string) string {
		o := f.owner.must(200, "POST", f.feedsPath()+"/test", map[string]any{"integration_id": f.integ, "channel_id": "C1", "kind": kind})["data"].([]any)
		return o[0].(map[string]any)["would_make"].(string)
	}
	if kind("signal") != "signal" || kind("context") != "context" || kind("handover") != "conversation" {
		t.Fatal("would_make does not follow the kind")
	}
	for _, e := range f.events() {
		if e.Name == "sales_feed.tested" {
			if e.Props["posts"] != 5 {
				t.Fatalf("event: %v", e.Props)
			}
			for _, v := range e.Props {
				if s, ok := v.(string); ok && (s == first["text"]) {
					t.Fatal("post text in an event")
				}
			}
			return
		}
	}
	t.Fatal("no sales_feed.tested event")
}

func TestSalesFeedTestWithAISpam(t *testing.T) {
	f := newFeedFixture(t)
	answer := spamAnswer(0.97)
	f.feedAIAnswer(t, &answer)
	f.slack.set("C1", slackPost{TS: feedTS(time.Hour, 1), User: "B1", Text: "Contact: ana@acme.example buy cheap links"})
	out := f.owner.must(200, "POST", f.feedsPath()+"/test", map[string]any{"integration_id": f.integ, "channel_id": "C1", "kind": "new_person"})["data"].([]any)
	if len(out) != 1 || out[0].(map[string]any)["would_make"] != "spam" || out[0].(map[string]any)["summary"] != "A pitch." {
		t.Fatalf("previews: %v", out)
	}
	if f.count(t, `SELECT count(*) FROM sales_feed_items`) != 0 {
		t.Fatal("stored")
	}
}

func TestSalesFeedsAIState(t *testing.T) {
	f := newFeedFixture(t)
	if got := f.owner.must(200, "GET", f.feedsPath(), nil)["ai"]; got != "connector_needed" {
		t.Fatalf("ai without a connection: %v", got)
	}
	answer := spamAnswer(0.1)
	f.feedAIAnswer(t, &answer)
	if got := f.owner.must(200, "GET", f.feedsPath(), nil)["ai"]; got != "on" {
		t.Fatalf("ai with a connection: %v", got)
	}
}
