package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeEngain answers Engain's API: one project, tasks that publish where
// the test says, and upvote orders that finish.
type fakeEngain struct {
	mu        sync.Mutex
	published string // the permalink a task reports once published
	tasks     int
	sent      []map[string]any
}

func (f *fakeEngain) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("X-API-Key") != "eng_test_key" {
		w.WriteHeader(401)
		io.WriteString(w, `{"error": "invalid api key"}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	var body map[string]any
	if r.Method == http.MethodPost {
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.sent = append(f.sent, map[string]any{"path": r.URL.Path, "body": body})
	}
	switch {
	case r.URL.Path == "/me":
		io.WriteString(w, `{"user": {"id": "u1", "email": "ana@northpeak.example", "name": "Ana"}, "projects": [{"id": "proj-1", "name": "Geonode"}]}`)
	case r.URL.Path == "/balance":
		io.WriteString(w, `{"credits": 480, "freeComments": 2}`)
	case r.URL.Path == "/tasks/comment" || r.URL.Path == "/tasks/reply" || r.URL.Path == "/tasks/post":
		if body["projectId"] != "proj-1" {
			w.WriteHeader(400)
			io.WriteString(w, `{"error": "projectId"}`)
			return
		}
		f.tasks++
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"id": "task-" + string(rune('0'+f.tasks)), "status": "scheduled"}})
	case strings.HasPrefix(r.URL.Path, "/tasks/"):
		if f.published == "" {
			io.WriteString(w, `{"id": "`+strings.TrimPrefix(r.URL.Path, "/tasks/")+`", "status": "scheduled"}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": strings.TrimPrefix(r.URL.Path, "/tasks/"), "status": "Published", "publishedUrl": f.published,
			"account": map[string]any{"username": "u/quiet_otter"}, "metrics": map[string]any{"upvotes": 3, "replyCount": 1}})
	case r.URL.Path == "/orders/upvote":
		io.WriteString(w, `{"id": "order-1", "status": "pending"}`)
	case r.URL.Path == "/orders/bulk":
		io.WriteString(w, `{"data": [{"id": "order-1", "status": "completed", "delivered": 10}]}`)
	case r.URL.Path == "/rule-check/batch":
		io.WriteString(w, `{"results": [{"key": "a", "verdict": "pass", "removalRisk": 12}]}`)
	case r.URL.Path == "/subreddit-stats":
		io.WriteString(w, `{"subreddit": "proxies", "buckets": [{"contentType": "comment", "sampleCount": 120, "removalRate": 0.31}]}`)
	default:
		w.WriteHeader(404)
	}
}

// fakeRedditInfo answers /api/info.json for one comment, live or removed.
type fakeRedditInfo struct {
	mu      sync.Mutex
	removed bool
	reads   int
}

func (f *fakeRedditInfo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	body := `"Geonode's residential pool worked for me"`
	if f.removed {
		body = `"[removed]"`
	}
	io.WriteString(w, `{"kind": "Listing", "data": {"children": [{"kind": "t1", "data": {"author": "quiet_otter", "body": `+body+`, "score": 7}}]}}`)
}

func TestRedditCampaigns(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	fe := &fakeEngain{}
	es := httptest.NewServer(fe)
	t.Cleanup(es.Close)
	fr := &fakeRedditInfo{}
	rs := httptest.NewServer(fr)
	t.Cleanup(rs.Close)
	e.integrations.Client.BaseURLs["engain"] = es.URL
	e.integrations.Client.BaseURLs["reddit.public"] = rs.URL

	geo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode", "handle": "geonode"})["id"].(string)
	thread := "https://www.reddit.com/r/proxies/comments/1abc2d/best_residential_proxies/"
	comment := map[string]any{"brand": "geonode", "kind": "comment", "target_url": thread, "content": "Geonode's residential pool worked for me",
		"brand_mentioned": true, "strategy": "top_comment_on_ranking_thread"}

	// Nothing goes out without an Engain connection: the action stays a draft.
	owner.must(200, "PUT", base+"/reddit/settings/geonode", map[string]any{"monthly_credit_limit": 30, "credit_price_cents": 10})
	noConn := owner.must(201, "POST", base+"/reddit/actions", comment)
	if noConn["status"] != "approved" {
		t.Fatalf("queued: %v", noConn)
	}
	if err := e.srv.SendRedditAction(ctx, ws, noConn["id"].(string)); err != nil {
		t.Fatal(err)
	}
	if a := owner.must(200, "GET", base+"/reddit/actions/"+noConn["id"].(string), nil); a["status"] != "failed" || !strings.Contains(a["status_reason"].(string), "Connectors") {
		t.Fatalf("no connection: %v", a)
	}

	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "engain", "credentials": map[string]any{"api_key": "eng_test_key"}})
	if b := owner.must(200, "GET", base+"/reddit/budget?brand=geonode", nil); b["engain_credits"] != 480.0 || b["monthly_credit_limit"] != 30.0 {
		t.Fatalf("budget: %v", b)
	}

	// An experiment, then a comment in it: sent, published, live.
	exp := owner.must(201, "POST", base+"/reddit/experiments", map[string]any{"brand": "geonode", "name": "Top comment, no link",
		"hypothesis": "A plain recommendation survives in r/proxies", "strategy": "top_comment_on_ranking_thread"})
	comment["experiment_id"] = exp["id"]
	a1 := owner.must(201, "POST", base+"/reddit/actions", comment)
	id := a1["id"].(string)
	if a1["status"] != "approved" || a1["subreddit"] != "proxies" || a1["target_kind"] != "thread" || a1["estimated_credits"] != 5.0 {
		t.Fatalf("created: %v", a1)
	}
	if err := e.srv.SendRedditAction(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	a1 = owner.must(200, "GET", base+"/reddit/actions/"+id, nil)
	if a1["status"] != "submitted" || len(a1["engain_ids"].([]any)) != 1 {
		t.Fatalf("submitted: %v", a1)
	}
	if err := e.srv.CheckRedditAction(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	if a := owner.must(200, "GET", base+"/reddit/actions/"+id, nil); a["status"] != "submitted" {
		t.Fatalf("not published yet: %v", a)
	}
	fe.mu.Lock()
	fe.published = thread + "k9xyz12/"
	fe.mu.Unlock()
	if err := e.srv.CheckRedditAction(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	a1 = owner.must(200, "GET", base+"/reddit/actions/"+id, nil)
	if a1["status"] != "live" || a1["reddit_fullname"] != "t1_k9xyz12" || a1["author"] != "quiet_otter" || a1["score"] != 7.0 || a1["live_at"] == nil ||
		len(a1["snapshots"].([]any)) != 1 {
		t.Fatalf("live: %v", a1)
	}
	r := owner.must(200, "GET", base+"/reddit/report?brand=geonode&group_by=subreddit", nil)
	tot := r["totals"].(map[string]any)
	if tot["mentions_live"] != 1.0 || tot["mentions_placed"] != 1.0 || tot["credits"] != 5.0 || tot["cost_cents"] != 50.0 || len(r["groups"].([]any)) != 1 {
		t.Fatalf("report live: %v", r)
	}

	// Removed by a moderator: it ends, and the report says so.
	fr.mu.Lock()
	fr.removed = true
	fr.mu.Unlock()
	if err := e.srv.CheckRedditAction(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	a1 = owner.must(200, "GET", base+"/reddit/actions/"+id, nil)
	kinds := []string{}
	for _, ev := range a1["events"].([]any) {
		kinds = append(kinds, ev.(map[string]any)["kind"].(string))
	}
	if a1["status"] != "removed" || a1["removal_category"] != "removed" || a1["ended_at"] == nil || a1["next_check_at"] == nil ||
		strings.Join(kinds, ",") != "created,approved,submitted,went_live,removed" {
		t.Fatalf("removed: %v %v", a1, kinds)
	}
	tot = owner.must(200, "GET", base+"/reddit/report?brand=geonode", nil)["totals"].(map[string]any)
	if tot["mentions_live"] != 0.0 || tot["mentions_placed"] != 1.0 || tot["removed"] != 1.0 {
		t.Fatalf("report removed: %v", tot)
	}

	// Limits: a day's actions in one subreddit, then the month's credits.
	owner.must(200, "PUT", base+"/reddit/settings/geonode", map[string]any{"max_actions_per_subreddit_day": 1})
	code, out := owner.do("POST", base+"/reddit/actions", comment)
	if code != 409 || out["error"].(map[string]any)["code"] != "reddit_limit_reached" {
		t.Fatalf("subreddit limit: %d %v", code, out)
	}
	owner.must(200, "PUT", base+"/reddit/settings/geonode", map[string]any{"max_actions_per_subreddit_day": 5, "monthly_credit_limit": 6})
	code, out = owner.do("POST", base+"/reddit/actions", comment)
	if code != 409 || out["error"].(map[string]any)["code"] != "reddit_budget_reached" {
		t.Fatalf("budget: %d %v", code, out)
	}
	drafts := owner.must(200, "GET", base+"/reddit/actions?status=draft", nil)["data"].([]any)
	if len(drafts) != 2 {
		t.Fatalf("refused actions are kept as drafts: %v", drafts)
	}
	owner.must(200, "PUT", base+"/reddit/settings/geonode", map[string]any{"no_monthly_limit": true})
	owner.must(200, "POST", base+"/reddit/actions/"+drafts[0].(map[string]any)["id"].(string)+"/approve", nil)
	owner.must(200, "POST", base+"/reddit/actions/"+drafts[1].(map[string]any)["id"].(string)+"/cancel", nil)

	// Upvotes on a comment: an order that finishes.
	votes := owner.must(201, "POST", base+"/reddit/actions", map[string]any{"brand": geo, "kind": "upvotes", "target_url": thread + "k9xyz12/", "upvotes_goal": 10})
	if err := e.srv.SendRedditAction(ctx, ws, votes["id"].(string)); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.CheckRedditAction(ctx, ws, votes["id"].(string)); err != nil {
		t.Fatal(err)
	}
	if v := owner.must(200, "GET", base+"/reddit/actions/"+votes["id"].(string), nil); v["status"] != "done" || v["upvotes_delivered"] != 10.0 {
		t.Fatalf("votes: %v", v)
	}
	code, out = owner.do("POST", base+"/reddit/actions", map[string]any{"brand": geo, "kind": "upvotes", "target_url": thread + "k9xyz12/", "upvotes_goal": 45})
	if code != 409 || out["error"].(map[string]any)["code"] != "reddit_limit_reached" {
		t.Fatalf("upvotes per item: %d %v", code, out)
	}

	// Links stay clean.
	owner.must(400, "POST", base+"/reddit/actions", map[string]any{"brand": "geonode", "kind": "comment", "target_url": thread, "content": "x",
		"link_url": "https://geonode.example/?utm_source=reddit"})

	// Through to Engain: preflight and subreddit stats.
	pf := owner.must(200, "POST", base+"/reddit/preflight", map[string]any{"brand": "geonode", "subreddit": "r/proxies",
		"items": []any{map[string]any{"key": "a", "taskType": "comment", "content": "Geonode worked for me"}}})
	if pf["results"] == nil {
		t.Fatalf("preflight: %v", pf)
	}
	if st := owner.must(200, "GET", base+"/reddit/subreddits/proxies?brand=geonode", nil); st["engain"] == nil || st["ours"].(map[string]any)["removed"] != 1.0 {
		t.Fatalf("subreddit: %v", st)
	}

	// Agents work within what a person set: they can't change limits, and
	// a brand that needs approval keeps their actions as drafts.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Reddit agent", "scopes": []string{"social.read", "social.manage", "social.post"}})["token"].(string)
	bot := &client{t: t, h: e.h, bearer: tok}
	bot.must(403, "PUT", base+"/reddit/settings/geonode", map[string]any{"monthly_credit_limit": 1000})
	bot.must(200, "PUT", base+"/reddit/settings/geonode", map[string]any{"engain_project_id": "proj-1"})
	owner.must(200, "PUT", base+"/reddit/settings/geonode", map[string]any{"require_approval": true})
	reply := bot.must(201, "POST", base+"/reddit/actions", map[string]any{"brand": "geonode", "kind": "reply", "target_url": thread + "k9xyz12/",
		"content": "Same here, it held up for scraping", "brand_mentioned": false})
	if reply["status"] != "draft" {
		t.Fatalf("agent action with approval on: %v", reply)
	}
	bot.must(403, "POST", base+"/reddit/actions/"+reply["id"].(string)+"/approve", nil)
	owner.must(200, "POST", base+"/reddit/actions/"+reply["id"].(string)+"/approve", nil)

	// Closing the experiment keeps its conclusion.
	closed := owner.must(200, "PATCH", base+"/reddit/experiments/"+exp["id"].(string), map[string]any{"status": "closed",
		"conclusion": "Removed within a day: r/proxies removes plain recommendations."})
	if closed["status"] != "closed" || closed["closed_at"] == nil || closed["actions"] != 3.0 {
		t.Fatalf("closed: %v", closed)
	}
	owner.must(409, "POST", base+"/reddit/actions", comment)
}
