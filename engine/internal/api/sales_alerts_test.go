package api_test

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func (f *feedFixture) runAlert(t *testing.T, lead string) {
	t.Helper()
	if err := f.e.srv.RunSalesAlert(f.ctx, f.ws, f.brand, lead); err != nil {
		t.Fatalf("alert job: %v", err)
	}
}

func (f *feedFixture) putAlerts(t *testing.T, want int, body map[string]any) map[string]any {
	t.Helper()
	return f.owner.must(want, "PUT", f.feedsPath()+"/alerts", body)
}

// Each new conversation is posted once to the Slack channel the brand
// picked, and its message follows the conversation: the owner in its text,
// an emoji for each thing that is true of it.
func TestSalesAlerts(t *testing.T) {
	f := newFeedFixture(t)
	early := slackPost{TS: feedTS(time.Hour, 1), App: "Form Tool", Text: "First name: Old\nWork email: old@acme.example"}
	f.slack.set("C1", early)
	ch := f.channel(t, f.brand, f.integ, "C1", 3)
	f.mustPoll(t, ch)
	f.runItem(t, f.item(t, ch, early.TS))
	leadOf := func(email string) map[string]any {
		t.Helper()
		for _, l := range f.owner.must(200, "GET", f.salesRoot()+"/leads?queue=all", nil)["data"].([]any) {
			if l := l.(map[string]any); l["email"] == email {
				return l
			}
		}
		t.Fatalf("no conversation for %s", email)
		return nil
	}
	old := leadOf("old@acme.example")["id"].(string)

	// The channel must be one the app sees and not one Outflare reads.
	f.putAlerts(t, 400, map[string]any{"enabled": true, "channel_id": "C1", "replies": true, "feed_channels": []string{ch}})
	f.putAlerts(t, 400, map[string]any{"enabled": true, "channel_id": "C9", "replies": true, "feed_channels": []string{ch}})
	f.putAlerts(t, 400, map[string]any{"enabled": true, "channel_id": "", "replies": true, "feed_channels": []string{ch}})
	set := f.putAlerts(t, 200, map[string]any{"enabled": true, "channel_id": "C2", "replies": true, "feed_channels": []string{ch}})
	if set["channel_name"] != "usage-alerts" || set["enabled"] != true || !f.hasEvent("sales_alert.updated") {
		t.Fatalf("settings: %v", set)
	}
	feeds := f.owner.must(200, "GET", f.feedsPath(), nil)
	if feeds["alerts"].(map[string]any)["channel_id"] != "C2" || feeds["channels"].([]any)[0].(map[string]any)["alert"] != true {
		t.Fatalf("feeds: %v", feeds["alerts"])
	}

	// A conversation from before alerts were on is never posted.
	f.runAlert(t, old)
	if n := len(f.slack.posted()); n != 0 {
		t.Fatalf("an old conversation was posted: %d", n)
	}

	// A new one is, once, with who it is, where it came from and its fields.
	form := slackPost{TS: feedTS(0, 2), App: "Form Tool", Text: "First name: Ana\nWork email: ana@acme.example\nExpected monthly traffic: 1TB+ <b>"}
	f.slack.set("C1", early, form)
	f.mustPoll(t, ch)
	f.runItem(t, f.item(t, ch, form.TS))
	l := leadOf("ana@acme.example")
	lead := l["id"].(string)
	if n := f.count(t, `SELECT count(*) FROM jobs WHERE kind='sales.alert' AND payload->>'id'=$1`, lead); n == 0 {
		t.Fatal("arriving queued no alert")
	}
	f.runAlert(t, lead)
	f.runAlert(t, lead)
	posts := f.slack.posted()
	if len(posts) != 1 || posts[0].Channel != "C2" {
		t.Fatalf("posted: %v", posts)
	}
	text, ts := posts[0].Text, posts[0].TS
	for _, want := range []string{"|acme.example>*", "Source: Form Tool", "Expected monthly traffic: 1TB+ &lt;b&gt;", "/crm/sales%3Alead%3A" + lead} {
		if !strings.Contains(text, want) {
			t.Fatalf("message lacks %q: %s", want, text)
		}
	}
	if strings.Contains(text, "ana@acme.example") || strings.Contains(text, "Owner:") || len(f.slack.emojis(ts)) != 0 || !f.hasEvent("sales_alert.posted") {
		t.Fatalf("a new conversation's message: %s %v", text, f.slack.emojis(ts))
	}

	// Seen, assigned and marked: an emoji each, and the owner in the text.
	rep, repID := f.salesUser(t, "rep@outflare.example", "sdr")
	act := func(body map[string]any) {
		t.Helper()
		body["version"] = leadOf("ana@acme.example")["version"]
		rep.must(200, "POST", f.salesRoot()+"/leads/"+lead+"/actions", body)
		f.runAlert(t, lead)
	}
	emojis := func(want ...string) {
		t.Helper()
		slices.Sort(want)
		if got := f.slack.emojis(ts); !slices.Equal(got, want) {
			t.Fatalf("emojis %v, want %v", got, want)
		}
	}
	act(map[string]any{"action": "read"})
	emojis("eyes")
	act(map[string]any{"action": "assign", "assignee_id": repID})
	emojis("eyes", "raising_hand")
	if now := f.slack.posted(); len(now) != 1 || !strings.Contains(now[0].Text, "Owner: Example Rep") {
		t.Fatalf("after assigning: %v", now)
	}
	act(map[string]any{"action": "rate", "quality": "junk"})
	emojis("eyes", "raising_hand", "wastebasket")
	act(map[string]any{"action": "rate", "quality": "good"})
	emojis("eyes", "raising_hand", "+1")
	act(map[string]any{"action": "rate", "quality": ""})
	emojis("eyes", "raising_hand")
	act(map[string]any{"action": "archive"})
	emojis("eyes", "raising_hand", "x")

	// A source the brand left out is not posted.
	f.putAlerts(t, 200, map[string]any{"enabled": true, "channel_id": "C2", "replies": true, "feed_channels": []string{}})
	other := slackPost{TS: feedTS(0, 3), App: "Form Tool", Text: "Work email: ben@globex.example"}
	f.slack.set("C1", early, form, other)
	f.mustPoll(t, ch)
	f.runItem(t, f.item(t, ch, other.TS))
	f.runAlert(t, leadOf("ben@globex.example")["id"].(string))
	if n := len(f.slack.posted()); n != 1 {
		t.Fatalf("a left-out source was posted: %d", n)
	}

	// An app that may not post: the settings say so, and nothing is retried.
	f.putAlerts(t, 200, map[string]any{"enabled": true, "channel_id": "C2", "replies": true, "feed_channels": []string{ch}})
	f.slack.mu.Lock()
	f.slack.noPost = true
	f.slack.mu.Unlock()
	third := slackPost{TS: feedTS(0, 4), App: "Form Tool", Text: "Work email: cy@initech.example"}
	f.slack.set("C1", early, form, other, third)
	f.mustPoll(t, ch)
	f.runItem(t, f.item(t, ch, third.TS))
	f.runAlert(t, leadOf("cy@initech.example")["id"].(string))
	if code := f.owner.must(200, "GET", f.feedsPath(), nil)["alerts"].(map[string]any)["error_code"]; code != "slack_auth" || !f.hasEvent("sales_alert.post_failed") {
		t.Fatalf("refused post: %v", code)
	}
}
