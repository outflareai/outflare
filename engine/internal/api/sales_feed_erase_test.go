package api_test

import (
	"testing"
	"time"
)

// An erased person's Slack post is never stored again: not by the poll after
// its cursor is reset (turning the channel on again reads days back), and
// not by the sweep, which looks for posts it never stored. Other posts in
// the channel are still read.
func TestFeedPostOfErasedPersonNotReadAgain(t *testing.T) {
	f := newFeedFixture(t)
	post := slackPost{TS: feedTS(time.Hour, 1), User: "B1", Text: "New form: ana@acme.example wants a demo"}
	f.slack.set("C1", post)
	ch := f.channel(t, f.brand, f.integ, "C1", 3)
	f.mustPoll(t, ch)
	f.runItem(t, f.item(t, ch, post.TS))
	person := f.person(t, "ana@acme.example")
	if person == "" {
		t.Fatal("the post made no person")
	}
	f.owner.must(200, "DELETE", "/v1/workspaces/"+f.ws+"/records/"+person, nil)
	if f.items(t, ch) != 0 {
		t.Fatal("the post stayed")
	}
	jobs := f.itemJobs(t)

	// The poll from the start, with the sweep not due.
	if _, err := f.e.pool.Exec(f.ctx, `UPDATE sales_feed_channels SET cursor_ts='',last_swept_at=now() WHERE id=$1`, ch); err != nil {
		t.Fatal(err)
	}
	f.mustPoll(t, ch)
	// The sweep over the same history.
	f.sweepDue(t, ch)
	f.mustPoll(t, ch)
	if n := f.items(t, ch); n != 0 {
		t.Fatalf("the erased post was stored again: %d", n)
	}
	if f.itemJobs(t) != jobs {
		t.Fatalf("read again: %d jobs, want %d", f.itemJobs(t), jobs)
	}
	if f.person(t, "ana@acme.example") != "" {
		t.Fatal("the person came back")
	}
	if n := f.count(t, `SELECT count(*) FROM sales_feed_erased WHERE workspace_id=$1 AND brand_id=$2 AND feed_channel_id=$3 AND slack_ts=$4`, f.ws, f.brand, ch, post.TS); n != 1 {
		t.Fatalf("erased markers: %d", n)
	}

	// A new post in the channel is still read.
	next := slackPost{TS: feedTS(time.Minute*5, 2), User: "B1", Text: "New form: ben@globex.example asks about pricing"}
	f.slack.set("C1", post, next)
	f.sweepDue(t, ch)
	f.mustPoll(t, ch)
	if n := f.items(t, ch); n != 1 {
		t.Fatalf("items: %d", n)
	}
	f.runItem(t, f.item(t, ch, next.TS))
	if f.person(t, "ben@globex.example") == "" || f.person(t, "ana@acme.example") != "" {
		t.Fatal("wrong people after the new post")
	}
}
