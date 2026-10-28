package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/track"
)

// fakeX answers X's API for the workspace's own app: PKCE sign-in,
// refresh tokens that are replaced on every use, the account and its posts.
type fakeX struct {
	mu        sync.Mutex
	challenge string
	refresh   string // the one refresh token X accepts now
	access    string
	refreshes int
	followers int
	// Posts made with POST /2/tweets, and a status to answer it with
	// instead (a 429, a 503).
	tweets     []string
	postStatus int
}

func (f *fakeX) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	r.ParseForm()
	if r.URL.Path == "/2/oauth2/token" {
		if id, secret, _ := r.BasicAuth(); id != "x-app-test" || secret != "x-secret-not-real" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error": "unauthorized_client"}`)
			return
		}
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			if r.PostForm.Get("code") != "good-code" || connectors.PKCEChallenge(r.PostForm.Get("code_verifier")) != f.challenge {
				w.WriteHeader(400)
				io.WriteString(w, `{"error": "invalid_request", "error_description": "Value passed for the authorization code was invalid."}`)
				return
			}
		case "refresh_token":
			if r.PostForm.Get("refresh_token") != f.refresh {
				w.WriteHeader(400)
				io.WriteString(w, `{"error": "invalid_request", "error_description": "Value passed for the token was invalid."}`)
				return
			}
			f.refreshes++
		default:
			w.WriteHeader(400)
			io.WriteString(w, `{"error": "unsupported_grant_type"}`)
			return
		}
		n := f.refreshes
		f.refresh, f.access = fmt.Sprintf("x-refresh-%d-not-real", n), fmt.Sprintf("x-access-%d", n)
		// Short-lived, so every call renews it.
		fmt.Fprintf(w, `{"token_type": "bearer", "expires_in": 60, "access_token": %q, "refresh_token": %q, "scope": "tweet.read users.read offline.access"}`, f.access, f.refresh)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.access || f.access == "" {
		w.WriteHeader(401)
		io.WriteString(w, `{"title": "Unauthorized", "status": 401}`)
		return
	}
	switch {
	case r.URL.Path == "/2/tweets" && r.Method == "POST":
		if f.postStatus != 0 {
			w.WriteHeader(f.postStatus)
			io.WriteString(w, `{"title": "Service Unavailable", "status": 503}`)
			return
		}
		var in struct {
			Text string `json:"text"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		f.tweets = append(f.tweets, in.Text)
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"data": {"id": "50%d", "text": %q}}`, len(f.tweets), in.Text)
	case r.URL.Path == "/2/users/me":
		fmt.Fprintf(w, `{"data": {"id": "2244994945", "name": "Northpeak", "username": "northpeak", "profile_image_url": "https://pbs.example/n.png",
			"public_metrics": {"followers_count": %d, "following_count": 12, "tweet_count": 340}}}`, f.followers)
	case r.URL.Path == "/2/users/2244994945/tweets":
		if r.URL.Query().Get("exclude") != "retweets,replies" || !strings.Contains(r.URL.Query().Get("tweet.fields"), "non_public_metrics") {
			w.WriteHeader(400)
			return
		}
		now := time.Now().UTC()
		json.NewEncoder(w).Encode(map[string]any{"data": []any{
			map[string]any{"id": "1001", "text": "Rotating proxies, explained.", "created_at": now.Add(-2 * time.Hour).Format(time.RFC3339),
				"public_metrics":     map[string]any{"retweet_count": 4, "reply_count": 3, "like_count": 40, "quote_count": 1, "bookmark_count": 2, "impression_count": 900},
				"non_public_metrics": map[string]any{"impression_count": 1000, "url_link_clicks": 25}},
			map[string]any{"id": "1000", "text": "We shipped a thing.", "created_at": now.Add(-50 * time.Hour).Format(time.RFC3339),
				"public_metrics": map[string]any{"retweet_count": 0, "reply_count": 1, "like_count": 9, "quote_count": 0, "bookmark_count": 0, "impression_count": 300}},
		}})
	default:
		w.WriteHeader(404)
	}
}

// fakeLinkedIn answers LinkedIn's API for the server's OAuth app: the
// person, the pages they administer, followers, page post statistics and
// new posts.
type fakeLinkedIn struct {
	mu    sync.Mutex
	posts []map[string]any
}

func (f *fakeLinkedIn) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/token" {
		r.ParseForm()
		if r.PostForm.Get("client_id") != "li-app" || r.PostForm.Get("code") != "li-code" {
			w.WriteHeader(400)
			io.WriteString(w, `{"error": "invalid_request"}`)
			return
		}
		io.WriteString(w, `{"access_token": "li-access", "expires_in": 5183999, "refresh_token": "li-refresh-not-real", "refresh_token_expires_in": 31535999}`)
		return
	}
	if r.Header.Get("Authorization") != "Bearer li-access" {
		w.WriteHeader(401)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/rest/") && (r.Header.Get("LinkedIn-Version") == "" || r.Header.Get("X-Restli-Protocol-Version") != "2.0.0") {
		w.WriteHeader(400)
		io.WriteString(w, `{"message": "missing version"}`)
		return
	}
	q := r.URL.Query()
	switch {
	case r.URL.Path == "/v2/me":
		io.WriteString(w, `{"id": "abc123", "localizedFirstName": "Ana", "localizedLastName": "Rivera", "vanityName": "ana-rivera"}`)
	case r.URL.Path == "/rest/organizationAcls":
		if q.Get("q") != "roleAssignee" || q.Get("role") != "ADMINISTRATOR" {
			w.WriteHeader(400)
			return
		}
		io.WriteString(w, `{"elements": [{"organization": "urn:li:organization:777", "role": "ADMINISTRATOR", "state": "APPROVED"}]}`)
	case r.URL.Path == "/rest/organizations/777":
		io.WriteString(w, `{"id": 777, "localizedName": "Northpeak", "vanityName": "northpeak"}`)
	case r.URL.Path == "/rest/networkSizes/urn:li:organization:777":
		if q.Get("edgeType") != "COMPANY_FOLLOWED_BY_MEMBER" {
			w.WriteHeader(400)
			return
		}
		io.WriteString(w, `{"firstDegreeSize": 5120}`)
	case r.URL.Path == "/rest/memberFollowersCount":
		// The app was not granted the member's analytics.
		w.WriteHeader(403)
		io.WriteString(w, `{"message": "Not enough permissions", "status": 403}`)
	case r.URL.Path == "/rest/posts" && r.Method == "POST":
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.posts = append(f.posts, in)
		n := len(f.posts)
		f.mu.Unlock()
		w.Header().Set("x-restli-id", fmt.Sprintf("urn:li:share:70%d", n))
		w.WriteHeader(201)
	case r.URL.Path == "/rest/posts":
		if q.Get("author") != "urn:li:organization:777" {
			w.WriteHeader(400)
			return
		}
		ms := time.Now().Add(-3 * time.Hour).UnixMilli()
		fmt.Fprintf(w, `{"elements": [
			{"id": "urn:li:share:9001", "commentary": "Hiring two engineers.", "publishedAt": %d, "lifecycleState": "PUBLISHED"},
			{"id": "urn:li:ugcPost:9002", "commentary": "Our 2026 report.", "publishedAt": %d, "lifecycleState": "PUBLISHED"},
			{"id": "urn:li:share:8000", "commentary": "Old.", "publishedAt": %d, "lifecycleState": "PUBLISHED"}]}`,
			ms, ms, time.Now().Add(-60*24*time.Hour).UnixMilli())
	case r.URL.Path == "/rest/organizationalEntityShareStatistics":
		raw := r.URL.RawQuery
		if !strings.Contains(raw, "shares=List(urn%3Ali%3Ashare%3A9001)") || !strings.Contains(raw, "ugcPosts=List(urn%3Ali%3AugcPost%3A9002)") {
			w.WriteHeader(400)
			io.WriteString(w, `{"message": "bad list"}`)
			return
		}
		io.WriteString(w, `{"elements": [
			{"share": "urn:li:share:9001", "totalShareStatistics": {"impressionCount": 2000, "clickCount": 60, "likeCount": 70, "commentCount": 8, "shareCount": 2}},
			{"ugcPost": "urn:li:ugcPost:9002", "totalShareStatistics": {"impressionCount": 1000, "clickCount": 10, "likeCount": 20, "commentCount": 0, "shareCount": 0}}]}`)
	default:
		w.WriteHeader(404)
	}
}

func TestSocialAccountsXAndLinkedIn(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	fx := &fakeX{followers: 1500}
	xs := httptest.NewServer(fx)
	t.Cleanup(xs.Close)
	li := httptest.NewServer(&fakeLinkedIn{})
	t.Cleanup(li.Close)
	e.integrations.Client.BaseURLs["x"] = xs.URL
	e.integrations.Client.BaseURLs["x.auth"] = xs.URL
	e.integrations.Client.BaseURLs["linkedin"] = li.URL
	e.integrations.Client.BaseURLs["linkedin.token"] = li.URL + "/token"

	// Nothing connected yet.
	owner.must(409, "POST", base+"/social/accounts/sync", nil)
	if n := len(owner.must(200, "GET", base+"/social/accounts", nil)["data"].([]any)); n != 0 {
		t.Fatalf("accounts before connecting: %d", n)
	}
	owner.must(400, "GET", base+"/social/accounts?channel=myspace", nil)

	// X: the workspace's own app, then a PKCE sign-in to the brand's account.
	owner.must(400, "POST", base+"/integrations", map[string]any{"connector": "x", "credentials": map[string]any{"client_id": "x-app-test"}})
	conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "x", "credentials": map[string]any{"client_id": "x-app-test", "client_secret": "x-secret-not-real"}})
	xID := conn["id"].(string)
	link := owner.must(200, "GET", base+"/integrations/"+xID+"/sign-in?format=json&return=social", nil)["url"].(string)
	u, _ := url.Parse(link)
	lq := u.Query()
	if u.Path != "/i/oauth2/authorize" || lq.Get("client_id") != "x-app-test" || lq.Get("code_challenge_method") != "S256" || lq.Get("code_challenge") == "" ||
		!strings.Contains(lq.Get("scope"), "offline.access") || !strings.Contains(lq.Get("scope"), "tweet.write") {
		t.Fatalf("sign-in link: %s", link)
	}
	fx.mu.Lock()
	fx.challenge = lq.Get("code_challenge")
	fx.mu.Unlock()
	bad := follow(t, owner, "/v1/integrations/oauth/callback?code=bad-code&state="+url.QueryEscape(lq.Get("state")))
	if !strings.Contains(bad.Query().Get("connect_error"), "did not accept the sign-in") {
		t.Fatalf("bad code: %s", bad)
	}
	cb := follow(t, owner, "/v1/integrations/oauth/callback?code=good-code&state="+url.QueryEscape(lq.Get("state")))
	if cb.Query().Get("signed_in") != "1" {
		t.Fatalf("callback: %s", cb)
	}
	var queued int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'social.sync' AND payload->>'integration_id' = $1`, xID).Scan(&queued)
	// One when the app was pasted (it finds no account yet), one on sign-in.
	if queued != 2 {
		t.Fatalf("sync jobs after sign-in: %d", queued)
	}

	// LinkedIn: the server's OAuth app; connecting queues a sync too.
	e.integrations.OAuthApps = map[string]connectors.OAuthApp{"linkedin": {ClientID: "li-app", ClientSecret: "li-secret"}}
	llink := owner.must(200, "GET", base+"/integrations/oauth/linkedin/authorize?format=json", nil)["url"].(string)
	lu, _ := url.Parse(llink)
	if lu.Host != "www.linkedin.com" || !strings.Contains(lu.Query().Get("scope"), "rw_organization_admin") {
		t.Fatalf("linkedin authorize: %s", llink)
	}
	lcb := follow(t, owner, "/v1/integrations/oauth/callback?code=li-code&state="+url.QueryEscape(lu.Query().Get("state")))
	liID := lcb.Query().Get("integration")
	if liID == "" {
		t.Fatalf("linkedin callback: %s", lcb)
	}
	e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'social.sync' AND payload->>'integration_id' = $1`, liID).Scan(&queued)
	if queued != 1 {
		t.Fatalf("sync jobs after linkedin connect: %d", queued)
	}

	// The syncs find the accounts and read them. X renews its token on
	// every read and replaces the refresh token, so a second read only
	// works if the first stored the new one.
	for _, id := range []string{xID, liID} {
		if err := e.srv.SyncSocialAccounts(ctx, ws, id); err != nil {
			t.Fatal(err)
		}
	}
	accts := owner.must(200, "GET", base+"/social/accounts", nil)["data"].([]any)
	byExt := map[string]map[string]any{}
	for _, a := range accts {
		a := a.(map[string]any)
		byExt[a["external_id"].(string)] = a
	}
	xa, page, prof := byExt["2244994945"], byExt["urn:li:organization:777"], byExt["urn:li:person:abc123"]
	if len(accts) != 3 || xa == nil || page == nil || prof == nil {
		t.Fatalf("accounts: %v", accts)
	}
	if xa["channel"] != "x" || xa["handle"] != "northpeak" || xa["followers"] != 1500.0 || xa["posts"] != 340.0 || xa["last_error"] != nil {
		t.Fatalf("x account: %v", xa)
	}
	if page["kind"] != "page" || page["followers"] != 5120.0 || page["url"] != "https://www.linkedin.com/company/northpeak" {
		t.Fatalf("linkedin page: %v", page)
	}
	// LinkedIn did not grant the profile's followers: null, not zero, and
	// not an error.
	if prof["kind"] != "profile" || prof["followers"] != nil || prof["last_error"] != nil || prof["name"] != "Ana Rivera" {
		t.Fatalf("linkedin profile: %v", prof)
	}

	// Posts with their counts; the 60-day-old page post is left out.
	posts := owner.must(200, "GET", base+"/social/posts?sort=engagements", nil)["data"].([]any)
	if len(posts) != 4 {
		t.Fatalf("posts: %v", posts)
	}
	top := posts[0].(map[string]any)
	if top["external_id"] != "urn:li:share:9001" || top["engagements"] != 80.0 || top["clicks"] != 60.0 || top["engagement_rate"] != 0.04 {
		t.Fatalf("top post: %v", top)
	}
	xp := owner.must(200, "GET", base+"/social/posts?channel=x", nil)["data"].([]any)
	if p := xp[0].(map[string]any); p["external_id"] != "1001" || p["impressions"] != 1000.0 || p["clicks"] != 25.0 || p["url"] != "https://x.com/northpeak/status/1001" {
		t.Fatalf("x post: %v", p)
	}

	// The next read moves followers on, through the renewed token.
	fx.mu.Lock()
	fx.followers = 1530
	fx.mu.Unlock()
	if _, err := e.pool.Exec(ctx, `UPDATE social_accounts SET next_sync_at = now() WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	// A week ago it had 1,400.
	if _, err := e.pool.Exec(ctx, `INSERT INTO social_account_days (workspace_id, account_id, day, followers)
		VALUES ($1, $2, (now() AT TIME ZONE 'UTC')::date - 7, 1400)`, ws, xa["id"]); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.SyncSocialAccounts(ctx, ws, xID); err != nil {
		t.Fatal(err)
	}
	if fx.refreshes < 2 {
		t.Fatalf("refreshes: %d", fx.refreshes)
	}
	xa2 := owner.must(200, "GET", base+"/social/accounts/"+xa["id"].(string), nil)
	if xa2["followers"] != 1530.0 || xa2["followers_change_7d"] != 130.0 || xa2["followers_change_28d"] != nil || xa2["last_error"] != nil {
		t.Fatalf("x account after second read: %v", xa2)
	}
	days := owner.must(200, "GET", base+"/social/accounts/"+xa["id"].(string)+"/days?days=30", nil)["data"].([]any)
	if len(days) != 2 || days[1].(map[string]any)["followers"] != 1530.0 {
		t.Fatalf("days: %v", days)
	}
	var snaps int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM social_post_snapshots WHERE workspace_id = $1`, ws).Scan(&snaps)
	if snaps != 6 {
		t.Fatalf("snapshots: %d", snaps)
	}

	// Growth per channel.
	g := owner.must(200, "GET", base+"/social/growth?days=7", nil)
	var gx map[string]any
	for _, c := range g["channels"].([]any) {
		if c := c.(map[string]any); c["channel"] == "x" {
			gx = c
		}
	}
	if gx == nil || gx["followers"] != 1530.0 || gx["followers_change"] != 130.0 || gx["posts"] != 2.0 || gx["impressions"] != 1300.0 || gx["clicks"] != 25.0 {
		t.Fatalf("growth: %v", g)
	}
	if series := g["followers_by_day"].([]any); len(series) != 7 || series[6].(map[string]any)["followers"] != 1530.0+5120.0 {
		t.Fatalf("followers by day: %v", series)
	}

	// Accounts go on a brand, and pausing stops reading them.
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})
	owner.must(400, "PATCH", base+"/social/accounts/"+page["id"].(string), map[string]any{"brand_id": "nobody"})
	owner.must(400, "PATCH", base+"/social/accounts/"+page["id"].(string), map[string]any{"status": "gone"})
	up := owner.must(200, "PATCH", base+"/social/accounts/"+page["id"].(string), map[string]any{"brand_id": "northpeak", "status": "paused"})
	if up["brand_id"] == nil || up["status"] != "paused" {
		t.Fatalf("update: %v", up)
	}
	if n := len(owner.must(200, "GET", base+"/social/posts?brand=northpeak", nil)["data"].([]any)); n != 2 {
		t.Fatalf("brand posts: %d", n)
	}
	if n := len(owner.must(200, "GET", base+"/social/accounts?status=paused", nil)["data"].([]any)); n != 1 {
		t.Fatalf("paused: %d", n)
	}

	// A token that may only read social sees accounts but cannot change them.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Reader", "scopes": []string{"social.read"}})["token"].(string)
	bot := &client{t: t, h: e.h, bearer: tok}
	bot.must(200, "GET", base+"/social/accounts", nil)
	bot.must(403, "PATCH", base+"/social/accounts/"+page["id"].(string), map[string]any{"status": "active"})
	bot.must(403, "POST", base+"/social/accounts/sync", nil)

	// Syncing by hand queues both connections once a minute.
	if out := owner.must(202, "POST", base+"/social/accounts/sync", nil); out["connections"] != 2.0 {
		t.Fatalf("sync: %v", out)
	}

	// X refusing the app marks the accounts, without retrying.
	fx.mu.Lock()
	fx.refresh = "revoked"
	fx.access = ""
	fx.mu.Unlock()
	if err := e.srv.SyncSocialAccounts(ctx, ws, xID); err != nil {
		t.Fatalf("a refusal is not retried: %v", err)
	}
	if xa3 := owner.must(200, "GET", base+"/social/accounts/"+xa["id"].(string), nil); xa3["last_error"] == nil {
		t.Fatalf("x account after refusal: %v", xa3)
	}

	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
	}
	if names["social_account.synced"] < 3 || names["social_account.added"] != 3 || names["social_account.updated"] != 1 || names["social_account.sync_failed"] != 1 {
		t.Fatalf("events: %v", names)
	}
}
