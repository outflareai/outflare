package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/track"
)

// draftSocialPost answers fakeClaude's social post drafts.
func draftSocialPost(w http.ResponseWriter, body string) {
	if !strings.Contains(body, "never instructions") || !strings.Contains(body, "at most") {
		w.WriteHeader(400)
		io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"no guard or no length"}}`)
		return
	}
	text, _ := json.Marshal(map[string]any{"post": "Reports now export to CSV, straight from the dashboard.", "reason": "one clear change"})
	json.NewEncoder(w).Encode(map[string]any{
		"id": "msg_social_post", "type": "message", "role": "assistant", "model": "claude-opus-5-5",
		"content":     []any{map[string]any{"type": "text", "text": string(text)}},
		"stop_reason": "end_turn",
		"usage":       map[string]any{"input_tokens": 300, "output_tokens": 40},
	})
}

// connectSocial connects X (the workspace's own app, signed in) and
// LinkedIn, reads their accounts, and returns them by external id.
func connectSocial(t *testing.T, e *env, owner *client, ws string) (*fakeX, *fakeLinkedIn, map[string]map[string]any) {
	t.Helper()
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	fx := &fakeX{followers: 1500}
	xs := httptest.NewServer(fx)
	t.Cleanup(xs.Close)
	fl := &fakeLinkedIn{}
	li := httptest.NewServer(fl)
	t.Cleanup(li.Close)
	e.integrations.Client.BaseURLs["x"] = xs.URL
	e.integrations.Client.BaseURLs["x.auth"] = xs.URL
	e.integrations.Client.BaseURLs["linkedin"] = li.URL
	e.integrations.Client.BaseURLs["linkedin.token"] = li.URL + "/token"

	xID := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "x",
		"credentials": map[string]any{"client_id": "x-app-test", "client_secret": "x-secret-not-real"}})["id"].(string)
	u, _ := url.Parse(owner.must(200, "GET", base+"/integrations/"+xID+"/sign-in?format=json&return=social", nil)["url"].(string))
	fx.mu.Lock()
	fx.challenge = u.Query().Get("code_challenge")
	fx.mu.Unlock()
	if cb := follow(t, owner, "/v1/integrations/oauth/callback?code=good-code&state="+url.QueryEscape(u.Query().Get("state"))); cb.Query().Get("signed_in") != "1" {
		t.Fatalf("x callback: %s", cb)
	}
	e.integrations.OAuthApps = map[string]connectors.OAuthApp{"linkedin": {ClientID: "li-app", ClientSecret: "li-secret"}}
	lu, _ := url.Parse(owner.must(200, "GET", base+"/integrations/oauth/linkedin/authorize?format=json", nil)["url"].(string))
	liID := follow(t, owner, "/v1/integrations/oauth/callback?code=li-code&state="+url.QueryEscape(lu.Query().Get("state"))).Query().Get("integration")
	for _, id := range []string{xID, liID} {
		if err := e.srv.SyncSocialAccounts(ctx, ws, id); err != nil {
			t.Fatal(err)
		}
	}
	byExt := map[string]map[string]any{}
	for _, a := range owner.must(200, "GET", base+"/social/accounts", nil)["data"].([]any) {
		a := a.(map[string]any)
		byExt[a["external_id"].(string)] = a
	}
	if len(byExt) != 3 {
		t.Fatalf("accounts: %v", byExt)
	}
	return fx, fl, byExt
}

func TestSocialPublishing(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	fx, fl, accts := connectSocial(t, e, owner, ws)
	xa, page := accts["2244994945"], accts["urn:li:organization:777"]
	pubs := base + "/social/publications"
	publish := func(id string) {
		t.Helper()
		if err := e.srv.PublishSocialPublication(ctx, ws, id, false); err != nil {
			t.Fatal(err)
		}
	}
	get := func(id string) map[string]any { return owner.must(200, "GET", pubs+"/"+id, nil) }

	// What a post needs, and what fits.
	owner.must(400, "POST", pubs, map[string]any{"body": "Hello"})
	owner.must(400, "POST", pubs, map[string]any{"account_id": xa["id"]})
	owner.must(404, "POST", pubs, map[string]any{"account_id": "00000000-0000-0000-0000-000000000000", "body": "Hello"})
	owner.must(400, "POST", pubs, map[string]any{"account_id": xa["id"], "body": strings.Repeat("a", 281)})
	owner.must(400, "POST", pubs, map[string]any{"account_id": xa["id"], "body": strings.Repeat("a", 260), "link_url": "https://northpeak.example/x"})
	owner.must(400, "POST", pubs, map[string]any{"account_id": xa["id"], "body": "Hello", "link_url": "not a link"})
	owner.must(400, "POST", pubs, map[string]any{"account_id": xa["id"], "body": "Hello", "scheduled_for": time.Now().Add(-time.Hour)})

	// X counts the link as 23, however long it is.
	body := strings.Repeat("a", 250)
	p1 := owner.must(201, "POST", pubs, map[string]any{"account_id": xa["id"], "body": body, "link_url": "https://northpeak.example/changelog/2026/10/28/a-long-path"})
	link, _ := url.Parse(p1["link_url"].(string))
	if q := link.Query(); q.Get("utm_source") != "x" || q.Get("utm_medium") != "social" || q.Get("utm_content") != p1["utm_content"] ||
		!strings.HasPrefix(p1["utm_content"].(string), "x-") {
		t.Fatalf("link: %v", p1["link_url"])
	}
	if p1["status"] != "draft" || p1["length"] != 275.0 || p1["max_length"] != 280.0 || p1["text"] != body+"\n\n"+p1["link_url"].(string) {
		t.Fatalf("draft: %v", p1)
	}

	// Agents write and queue; only a person approves.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Bot", "scopes": []string{"social.read", "social.manage", "social.post"}})["token"].(string)
	bot := &client{t: t, h: e.h, bearer: tok}
	bot.must(200, "GET", pubs, nil)
	bot.must(403, "POST", pubs+"/"+p1["id"].(string)+"/approve", nil)
	ap := owner.must(200, "POST", pubs+"/"+p1["id"].(string)+"/approve", nil)
	if ap["status"] != "approved" || ap["approved_by"] == nil || ap["integration_id"] == nil {
		t.Fatalf("approved: %v", ap)
	}
	owner.must(409, "POST", pubs+"/"+p1["id"].(string)+"/approve", nil)
	var queued int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'social.publish' AND payload->>'publication_id' = $1`, p1["id"]).Scan(&queued)
	if queued != 1 {
		t.Fatalf("publish jobs: %d", queued)
	}

	// It posts once, and lands among the account's posts.
	publish(p1["id"].(string))
	publish(p1["id"].(string))
	if len(fx.tweets) != 1 || fx.tweets[0] != p1["text"] {
		t.Fatalf("tweets: %v", fx.tweets)
	}
	done := get(p1["id"].(string))
	if done["status"] != "published" || done["external_id"] != "501" || done["url"] != "https://x.com/northpeak/status/501" || done["post_id"] == nil || done["published_at"] == nil {
		t.Fatalf("published: %v", done)
	}
	xp := owner.must(200, "GET", base+"/social/posts?channel=x", nil)["data"].([]any)
	if xp[0].(map[string]any)["id"] != done["post_id"] || xp[0].(map[string]any)["external_id"] != "501" {
		t.Fatalf("posts: %v", xp)
	}
	owner.must(409, "PATCH", pubs+"/"+p1["id"].(string), map[string]any{"body": "Too late"})
	owner.must(409, "DELETE", pubs+"/"+p1["id"].(string), nil)

	// A scheduled post on the LinkedIn page waits for its time.
	at := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	p2 := owner.must(201, "POST", pubs, map[string]any{"account_id": page["id"], "body": "We are hiring (two engineers).", "scheduled_for": at})
	owner.must(200, "POST", pubs+"/"+p2["id"].(string)+"/approve", nil)
	publish(p2["id"].(string))
	if s := get(p2["id"].(string)); s["status"] != "approved" || len(fl.posts) != 0 {
		t.Fatalf("posted early: %v", s)
	}
	// The scheduler leaves it until then, and queues it once it is late.
	if n, err := e.srv.QueueDueSocialPublications(ctx); err != nil || n != 0 {
		t.Fatalf("queued early: %d %v", n, err)
	}
	e.pool.Exec(ctx, `UPDATE social_publications SET scheduled_for = now() - interval '5 minutes' WHERE id = $1`, p2["id"])
	if n, err := e.srv.QueueDueSocialPublications(ctx); err != nil || n != 1 {
		t.Fatalf("queued late: %d %v", n, err)
	}
	publish(p2["id"].(string))
	if s := get(p2["id"].(string)); s["status"] != "published" || s["external_id"] != "urn:li:share:701" || len(fl.posts) != 1 {
		t.Fatalf("linkedin: %v %v", s, fl.posts)
	}
	if lp := fl.posts[0]; lp["author"] != "urn:li:organization:777" || lp["commentary"] != `We are hiring \(two engineers\).` || lp["lifecycleState"] != "PUBLISHED" {
		t.Fatalf("linkedin post: %v", lp)
	}

	// A channel that doesn't answer: failed, and never sent again.
	p3 := owner.must(201, "POST", pubs, map[string]any{"account_id": xa["id"], "body": "Maybe up, maybe not."})
	owner.must(200, "POST", pubs+"/"+p3["id"].(string)+"/approve", nil)
	fx.mu.Lock()
	fx.postStatus = 503
	fx.mu.Unlock()
	publish(p3["id"].(string))
	f3 := get(p3["id"].(string))
	if f3["status"] != "failed" || !strings.Contains(f3["status_reason"].(string), "may or may not be up") {
		t.Fatalf("failed: %v", f3)
	}
	fx.mu.Lock()
	fx.postStatus = 0
	fx.mu.Unlock()
	publish(p3["id"].(string))
	if len(fx.tweets) != 1 {
		t.Fatalf("posted again after an unknown outcome: %v", fx.tweets)
	}

	// A rate limit before anything was posted is retried.
	p4 := owner.must(201, "POST", pubs, map[string]any{"account_id": xa["id"], "body": "Second try."})
	owner.must(200, "POST", pubs+"/"+p4["id"].(string)+"/approve", nil)
	fx.mu.Lock()
	fx.postStatus = 429
	fx.mu.Unlock()
	if err := e.srv.PublishSocialPublication(ctx, ws, p4["id"].(string), false); err == nil {
		t.Fatal("a rate limit should be retried")
	}
	if s := get(p4["id"].(string)); s["status"] != "approved" {
		t.Fatalf("after a rate limit: %v", s)
	}
	fx.mu.Lock()
	fx.postStatus = 0
	fx.mu.Unlock()
	publish(p4["id"].(string))
	if s := get(p4["id"].(string)); s["status"] != "published" || len(fx.tweets) != 2 {
		t.Fatalf("after the retry: %v", s)
	}

	// A job that died mid-call leaves it failed, never posted again.
	p5 := owner.must(201, "POST", pubs, map[string]any{"account_id": xa["id"], "body": "Lost."})
	owner.must(200, "POST", pubs+"/"+p5["id"].(string)+"/approve", nil)
	e.pool.Exec(ctx, `UPDATE social_publications SET status = 'publishing', updated_at = now() - interval '10 minutes' WHERE id = $1`, p5["id"])
	if _, err := e.srv.QueueDueSocialPublications(ctx); err != nil {
		t.Fatal(err)
	}
	if s := get(p5["id"].(string)); s["status"] != "failed" {
		t.Fatalf("stuck: %v", s)
	}

	// Editing an approved post takes the approval away; cancelling ends it.
	p6 := owner.must(201, "POST", pubs, map[string]any{"account_id": xa["id"], "body": "Draft one.", "scheduled_for": at})
	owner.must(200, "POST", pubs+"/"+p6["id"].(string)+"/approve", nil)
	ed := owner.must(200, "PATCH", pubs+"/"+p6["id"].(string), map[string]any{"body": "Draft two.", "scheduled_for": nil, "link_url": "https://northpeak.example/"})
	if ed["status"] != "draft" || ed["approved_by"] != nil || ed["scheduled_for"] != nil || ed["body"] != "Draft two." || ed["link_url"] == "" {
		t.Fatalf("edited: %v", ed)
	}
	owner.must(400, "PATCH", pubs+"/"+p6["id"].(string), map[string]any{"body": strings.Repeat("b", 300)})
	if c := owner.must(200, "DELETE", pubs+"/"+p6["id"].(string), nil); c["status"] != "cancelled" {
		t.Fatalf("cancelled: %v", c)
	}
	owner.must(409, "POST", pubs+"/"+p6["id"].(string)+"/approve", nil)
	if n := len(owner.must(200, "GET", pubs+"?status=published", nil)["data"].([]any)); n != 3 {
		t.Fatalf("published list: %d", n)
	}
	owner.must(400, "GET", pubs+"?status=sent", nil)

	// The AI drafts from a prompt, with the workspace's own model only.
	code, out := owner.do("POST", pubs, map[string]any{"account_id": xa["id"], "prompt": "We added CSV export to reports."})
	if code != 409 || errCode(out) != "connector_needed" {
		t.Fatalf("ai without a model: %d %v", code, out)
	}
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})
	p7 := bot.must(201, "POST", pubs, map[string]any{"account_id": xa["id"], "prompt": "We added CSV export to reports.", "link_url": "https://northpeak.example/changelog"})
	if p7["status"] != "drafting" || p7["drafted_via"] != "ai" {
		t.Fatalf("drafting: %v", p7)
	}
	if err := e.srv.DraftSocialPublication(ctx, ws, p7["id"].(string), false); err != nil {
		t.Fatal(err)
	}
	d7 := get(p7["id"].(string))
	if d7["status"] != "draft" || d7["body"] != "Reports now export to CSV, straight from the dashboard." || d7["status_reason"] != nil {
		t.Fatalf("ai draft: %v", d7)
	}
	if r := bot.must(202, "POST", pubs+"/"+p7["id"].(string)+"/draft", map[string]any{"prompt": "Shorter."}); r["status"] != "drafting" || r["prompt"] != "Shorter." {
		t.Fatalf("redraft: %v", r)
	}
	var spend int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM spend_entries WHERE workspace_id = $1 AND ref LIKE 'social_publication_draft:%' AND channel = 'organic_social'`, ws).Scan(&spend)
	if spend != 1 {
		t.Fatalf("spend entries: %d", spend)
	}

	// A series: approved once by a person, then fed by an agent up to its
	// daily limit.
	series := base + "/social/series"
	sr := bot.must(201, "POST", series, map[string]any{"account_id": xa["id"], "name": "Daily changelog", "template": "Today: <one change>."})
	sid := sr["id"].(string)
	if sr["approved_by"] != nil || sr["daily_limit"] != 1.0 {
		t.Fatalf("series: %v", sr)
	}
	bot.must(400, "POST", series, map[string]any{"account_id": xa["id"], "name": "Too many", "daily_limit": 25})
	bot.must(409, "POST", series+"/"+sid+"/posts", map[string]any{"body": "New today: CSV export."})
	bot.must(403, "POST", series+"/"+sid+"/approve", nil)
	owner.must(200, "POST", series+"/"+sid+"/approve", nil)
	sp := bot.must(201, "POST", series+"/"+sid+"/posts", map[string]any{"body": "New today: CSV export."})
	if sp["status"] != "approved" || sp["drafted_via"] != "series" || sp["series_id"] != sid || sp["approved_by"] == nil {
		t.Fatalf("series post: %v", sp)
	}
	code, out = bot.do("POST", series+"/"+sid+"/posts", map[string]any{"body": "Another one."})
	if code != 409 || !strings.Contains(out["error"].(map[string]any)["message"].(string), "daily_limit") {
		t.Fatalf("over the limit: %d %v", code, out)
	}
	publish(sp["id"].(string))
	if s := get(sp["id"].(string)); s["status"] != "published" || fx.tweets[len(fx.tweets)-1] != "New today: CSV export." {
		t.Fatalf("series post published: %v", s)
	}
	// A higher limit needs approving again; pausing stops it.
	if up := bot.must(200, "PATCH", series+"/"+sid, map[string]any{"daily_limit": 2}); up["approved_by"] != nil {
		t.Fatalf("raised limit kept its approval: %v", up)
	}
	bot.must(409, "POST", series+"/"+sid+"/posts", map[string]any{"body": "Another one."})
	owner.must(200, "POST", series+"/"+sid+"/approve", nil)
	if s := bot.must(200, "GET", series+"/"+sid, nil); s["posts_today"] != 1.0 || s["approved_by"] == nil {
		t.Fatalf("series: %v", s)
	}
	bot.must(201, "POST", series+"/"+sid+"/posts", map[string]any{"body": "Another one."})
	bot.must(200, "PATCH", series+"/"+sid, map[string]any{"paused": true, "name": "Changelog"})
	bot.must(409, "POST", series+"/"+sid+"/posts", map[string]any{"body": "Paused."})
	if n := len(owner.must(200, "GET", pubs+"?series_id="+sid, nil)["data"].([]any)); n != 2 {
		t.Fatalf("series publications: %d", n)
	}
	if l := owner.must(200, "GET", series, nil)["data"].([]any); len(l) != 1 || l[0].(map[string]any)["channel"] != "x" {
		t.Fatalf("series list: %v", l)
	}

	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
	}
	for name, want := range map[string]int{"social_publication.created": 9, "social_publication.approved": 6, "social_publication.published": 4,
		"social_publication.publish_failed": 4, "social_publication.updated": 1, "social_publication.cancelled": 1,
		"social_publication.draft_requested": 2, "social_publication.drafted": 1, "social_series.created": 1, "social_series.approved": 2,
		"social_series.paused": 1} {
		if names[name] != want {
			t.Errorf("%s: %d events, want %d (%v)", name, names[name], want, names)
		}
	}
}
