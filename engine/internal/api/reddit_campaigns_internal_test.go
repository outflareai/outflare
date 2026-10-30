package api

import (
	"testing"
	"time"
)

func TestRedditTarget(t *testing.T) {
	for _, tc := range []struct{ url, sub, kind, fullname string }{
		{"https://www.reddit.com/r/webscraping", "webscraping", "subreddit", ""},
		{"https://www.reddit.com/r/webscraping/", "webscraping", "subreddit", ""},
		{"https://old.reddit.com/r/proxies/comments/1abc2d/best_residential_proxies/", "proxies", "thread", "t3_1abc2d"},
		{"https://www.reddit.com/r/proxies/comments/1abc2d/best_residential_proxies/k9xyz12/", "proxies", "comment", "t1_k9xyz12"},
		{"https://reddit.com/r/proxies/comments/1abc2d/best/k9xyz12/?context=3", "proxies", "comment", "t1_k9xyz12"},
	} {
		sub, kind, ok := redditTarget(tc.url)
		if !ok || sub != tc.sub || kind != tc.kind || redditFullname(tc.url) != tc.fullname {
			t.Errorf("%s: %s %s %v %s", tc.url, sub, kind, ok, redditFullname(tc.url))
		}
	}
	for _, bad := range []string{"https://example.com/r/x", "http://www.reddit.com/r/proxies", "https://www.reddit.com/user/ana", ""} {
		if _, _, ok := redditTarget(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	if p := redditPermalink("/r/proxies/comments/1abc2d/x/k9xyz12/"); p != "https://www.reddit.com/r/proxies/comments/1abc2d/x/k9xyz12/" {
		t.Errorf("relative permalink: %s", p)
	}
	if p := redditPermalink("https://old.reddit.com/r/proxies/comments/1abc2d/x/?utm_source=share"); p != "https://www.reddit.com/r/proxies/comments/1abc2d/x/" {
		t.Errorf("absolute permalink: %s", p)
	}
}

func TestParseRedditInfo(t *testing.T) {
	info := func(data string) []byte {
		return []byte(`{"kind": "Listing", "data": {"children": [{"kind": "t1", "data": ` + data + `}]}}`)
	}
	for _, tc := range []struct {
		name, body, state, category string
	}{
		{"live", `{"author": "ana", "body": "Try Geonode", "score": 4, "removed_by_category": null}`, "live", ""},
		{"mod removed", `{"author": "ana", "body": "[removed]", "score": 1}`, "removed", "removed"},
		{"automod", `{"author": "ana", "selftext": "x", "removed_by_category": "automod_filtered", "num_comments": 3}`, "removed", "automod_filtered"},
		{"deleted", `{"author": "[deleted]", "body": "[deleted]"}`, "gone", "deleted"},
		{"post deleted", `{"author": "[deleted]", "selftext": "", "removed_by_category": "deleted"}`, "gone", "deleted"},
		{"locked", `{"author": "ana", "body": "hi", "locked": true}`, "frozen", ""},
	} {
		v, err := parseRedditInfo(info(tc.body))
		if err != nil || v.State != tc.state || v.RemovalCategory != tc.category {
			t.Errorf("%s: %+v %v", tc.name, v, err)
		}
	}
	if v, err := parseRedditInfo([]byte(`{"kind": "Listing", "data": {"children": []}}`)); err != nil || v.State != "gone" {
		t.Errorf("missing: %+v %v", v, err)
	}
	if v, err := parseRedditInfo([]byte(`<html><pre>` + string(info(`{"author": "ana", "body": "hi", "score": 9}`)) + `</pre></html>`)); err != nil ||
		v.State != "live" || v.Score == nil || *v.Score != 9 {
		t.Errorf("wrapped: %+v %v", v, err)
	}
	if _, err := parseRedditInfo([]byte(`<html>blocked</html>`)); err == nil {
		t.Error("html accepted")
	}
}

func TestEngainState(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "scheduled": "", "Published": "published", "posted": "published", "FAILED": "failed", "rejected": "failed",
		"completed": "done", "removed": "removed", "in_progress": "",
	} {
		if got := engainState(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestRedditNextCheck(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { x := now.Add(-d); return &x }
	in := func(next *time.Time) time.Duration {
		if next == nil {
			return -1
		}
		return next.Sub(now)
	}
	for _, tc := range []struct {
		name, kind, status string
		live, ended        *time.Time
		want               time.Duration
	}{
		{"waiting", "comment", "submitted", nil, nil, 15 * time.Minute},
		{"votes", "upvotes", "submitted", nil, nil, time.Hour},
		{"new", "comment", "live", ago(time.Hour), nil, 10 * time.Minute},
		{"day", "comment", "live", ago(5 * time.Hour), nil, time.Hour},
		{"week", "post", "frozen", ago(3 * 24 * time.Hour), nil, 6 * time.Hour},
		{"month", "reply", "live", ago(20 * 24 * time.Hour), nil, 24 * time.Hour},
		{"old", "reply", "live", ago(90 * 24 * time.Hour), nil, 7 * 24 * time.Hour},
		{"ancient", "reply", "live", ago(200 * 24 * time.Hour), nil, -1},
		{"just removed", "comment", "removed", ago(time.Hour), ago(time.Hour), 24 * time.Hour},
		{"long removed", "comment", "removed", ago(30 * 24 * time.Hour), ago(8 * 24 * time.Hour), -1},
		{"failed", "comment", "failed", nil, nil, -1},
		{"done", "upvotes", "done", nil, nil, -1},
	} {
		if got := in(redditNextCheck(tc.kind, tc.status, tc.live, tc.ended, now)); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRedditActionValidate(t *testing.T) {
	thread := "https://www.reddit.com/r/proxies/comments/1abc2d/best/"
	comment := thread + "k9xyz12/"
	for _, tc := range []struct {
		name  string
		req   redditActionRequest
		param string
	}{
		{"comment", redditActionRequest{Kind: "comment", TargetURL: thread, Content: "Geonode works well"}, ""},
		{"comment on subreddit", redditActionRequest{Kind: "comment", TargetURL: "https://www.reddit.com/r/proxies", Content: "x"}, "target_url"},
		{"reply", redditActionRequest{Kind: "reply", TargetURL: comment, Content: "+1"}, ""},
		{"reply on thread", redditActionRequest{Kind: "reply", TargetURL: thread, Content: "+1"}, "target_url"},
		{"post without title", redditActionRequest{Kind: "post", TargetURL: "https://www.reddit.com/r/proxies", Content: "x"}, "title"},
		{"empty", redditActionRequest{Kind: "comment", TargetURL: thread}, "content"},
		{"utm", redditActionRequest{Kind: "comment", TargetURL: thread, Content: "x", LinkURL: "https://geonode.com/?utm_source=reddit"}, "link_url"},
		{"votes", redditActionRequest{Kind: "upvotes", TargetURL: comment, UpvotesGoal: 10}, ""},
		{"votes on subreddit", redditActionRequest{Kind: "upvotes", TargetURL: "https://www.reddit.com/r/proxies", UpvotesGoal: 10}, "target_url"},
		{"no votes", redditActionRequest{Kind: "upvotes", TargetURL: comment}, "upvotes_goal"},
		{"boost without speed", redditActionRequest{Kind: "comment", TargetURL: thread, Content: "x", UpvotesGoal: 10}, "upvotes_per_day"},
		{"thread", redditActionRequest{Kind: "thread", TargetURL: thread, Content: "Which proxy?", Children: []redditChild{{Account: 2, Content: "Geonode"}}}, ""},
		{"thread bad account", redditActionRequest{Kind: "thread", TargetURL: thread, Content: "x", Children: []redditChild{{Account: 11, Content: "y"}}}, "children[0].account"},
		{"bad kind", redditActionRequest{Kind: "downvotes", TargetURL: thread}, "kind"},
	} {
		_, _, param, msg := tc.req.validate()
		if param != tc.param {
			t.Errorf("%s: %q (%s), want %q", tc.name, param, msg, tc.param)
		}
	}
	r := redditActionRequest{Kind: "comment", TargetURL: thread, Content: "x", LinkURL: "https://geonode.com"}
	if sub, kind, _, _ := r.validate(); sub != "proxies" || kind != "thread" || r.LinkType != "other" {
		t.Errorf("derived: %s %s %s", sub, kind, r.LinkType)
	}
	if got := redditEstimate("thread", 3, 10); got != 10+15+1 {
		t.Errorf("estimate: %v", got)
	}
}
