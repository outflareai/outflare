package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// fakeGeonodePages is Geonode's Scraper API reading a brand's homepage
// (built in the browser, so its links only show with JavaScript on) and
// public X profile pages.
func fakeGeonodePages(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Api-Key") != "gn-key" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"code": "UNAUTHORIZED", "message": "bad key", "retryable": false}`)
			return
		}
		switch r.URL.Path {
		case "/v1/usage/concurrency":
			io.WriteString(w, `{"work_concurrency_in_use": 0, "work_concurrency_limit": 5}`)
			return
		case "/v1/extract":
		default:
			http.NotFound(w, r)
			return
		}
		var req struct {
			URL      string `json:"url"`
			RenderJS bool   `json:"render_js"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		page, status := `<html><body><div id="root"></div></body></html>`, 200
		switch {
		case req.URL == "https://northpeak.example/" && req.RenderJS:
			page = `<html><head><script type="application/ld+json">{"@type":"Organization","sameAs":["https:\/\/www.instagram.com\/northpeak\/"]}</script></head><body>
				<a href="https://twitter.com/northpeak?ref=site">X</a>
				<a href="https://www.linkedin.com/company/northpeak/about/">LinkedIn</a>
				<a href="https://twitter.com/intent/tweet?text=hi">Share</a>
				<a href="https://www.facebook.com/sharer/sharer.php?u=x">Share</a>
				<a href="/pricing">Pricing</a></body></html>`
		case req.URL == "https://x.com/northpeak":
			page = `<html><head><title>Northpeak (@northpeak) / X</title></head><body>
				<img src="https://pbs.twimg.com/profile_images/1/n_400x400.jpg">
				<a href="/northpeak/following"><span class="font-bold">258</span> <span>Following</span></a>
				<a href="/northpeak/verified_followers"><span class="font-bold">12.3K</span> <span>Followers</span></a></body></html>`
		case req.URL == "https://www.youtube.com/@ghostpeaktv" && req.RenderJS:
			page = `<html><head><meta property="og:title" content="Ghostpeak TV"></head><body><span>@ghostpeaktv</span><span>4.2K subscribers</span></body></html>`
		case req.URL == "https://public.api.bsky.app/xrpc/app.bsky.actor.getProfile?actor=ghostpeak.example" && !req.RenderJS:
			page = `{"handle":"ghostpeak.example","displayName":"Ghostpeak","followersCount":845,"followsCount":12,"postsCount":40}`
		case req.URL == "https://x.com/ghostpeak":
			page = `<html><head><title>Log in to X / X</title></head><body>Sign in to see this.</body></html>`
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data":           map[string]any{"html": page},
			"metadata":       map[string]any{"http_status": status, "url": req.URL},
			"tokens_charged": 1,
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBrandLinkDiscoveryAndPublicXFollowers(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	e.integrations.Client.BaseURLs["geonode"] = fakeGeonodePages(t).URL
	jobsOf := func(kind string) int {
		var n int
		e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE workspace_id = $1 AND kind = $2`, ws, kind).Scan(&n)
		return n
	}

	// With no page reader, a new brand is not read and the gaps show.
	b := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})
	bid := b["id"].(string)
	sug := base + "/brands/northpeak/link-suggestions"
	list := owner.must(200, "GET", sug, nil)
	if list["can_discover"] != false || len(list["gaps"].([]any)) != 6 || len(list["data"].([]any)) != 0 {
		t.Fatalf("before connecting: %v", list)
	}
	owner.must(409, "POST", base+"/brands/northpeak/links/discover", nil)
	owner.must(400, "GET", sug+"?status=maybe", nil)
	if n := jobsOf("brand.discover_links"); n != 0 {
		t.Fatalf("discovery jobs with no page reader: %d", n)
	}

	// Connecting Geonode reads the brand's website, and reads X profiles.
	gn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "geonode", "credentials": map[string]string{"api_key": "gn-key"}})["id"].(string)
	if jobsOf("brand.discover_links") != 1 || jobsOf("social.sync") != 1 {
		t.Fatalf("jobs after connecting: discover %d, sync %d", jobsOf("brand.discover_links"), jobsOf("social.sync"))
	}
	if err := e.srv.DiscoverBrandLinks(ctx, ws, bid); err != nil {
		t.Fatal(err)
	}
	list = owner.must(200, "GET", sug, nil)
	byKind := map[string]map[string]any{}
	for _, x := range list["data"].([]any) {
		x := x.(map[string]any)
		byKind[x["kind"].(string)] = x
	}
	if len(byKind) != 3 || byKind["x"]["url"] != "https://x.com/northpeak" || byKind["linkedin"]["url"] != "https://www.linkedin.com/company/northpeak" ||
		byKind["instagram"]["url"] != "https://www.instagram.com/northpeak" || byKind["x"]["found_on"] != "https://northpeak.example/" {
		t.Fatalf("suggestions: %v", list["data"])
	}
	if gaps := list["gaps"].([]any); len(gaps) != 3 || gaps[0] != "youtube" || list["can_discover"] != true {
		t.Fatalf("gaps: %v", list)
	}

	// Dismiss one, add one edited and one as found.
	owner.must(200, "POST", sug+"/"+byKind["instagram"]["id"].(string)+"/dismiss", nil)
	owner.must(409, "POST", sug+"/"+byKind["instagram"]["id"].(string)+"/dismiss", nil)
	owner.must(404, "POST", sug+"/not-an-id/add", nil)
	owner.must(400, "POST", sug+"/"+byKind["linkedin"]["id"].(string)+"/add", map[string]any{"url": "not a link"})
	ed := owner.must(200, "POST", sug+"/"+byKind["linkedin"]["id"].(string)+"/add", map[string]any{"url": "https://www.linkedin.com/company/northpeak-hq"})
	if s, l := ed["suggestion"].(map[string]any), ed["links"].([]any); s["status"] != "added" || s["url"] != "https://www.linkedin.com/company/northpeak" ||
		len(l) != 1 || l[0].(map[string]any)["url"] != "https://www.linkedin.com/company/northpeak-hq" || l[0].(map[string]any)["kind"] != "linkedin" {
		t.Fatalf("edited add: %v", ed)
	}
	syncs := jobsOf("social.sync")
	added := owner.must(200, "POST", sug+"/"+byKind["x"]["id"].(string)+"/add", nil)
	if links := added["links"].([]any); len(links) != 2 {
		t.Fatalf("links after adding: %v", links)
	}
	if jobsOf("social.sync") != syncs+1 {
		t.Fatal("adding an X link should read it now")
	}
	all := owner.must(200, "GET", sug+"?status=all", nil)
	if len(all["data"].([]any)) != 3 || len(owner.must(200, "GET", sug, nil)["data"].([]any)) != 0 {
		t.Fatalf("all: %v", all)
	}
	// Reading the website again suggests nothing new: added links are the
	// brand's and the dismissed one stays dismissed.
	if err := e.srv.DiscoverBrandLinks(ctx, ws, bid); err != nil {
		t.Fatal(err)
	}
	if n := len(owner.must(200, "GET", sug, nil)["data"].([]any)); n != 0 {
		t.Fatalf("suggestions after reading again: %d", n)
	}
	// A token that only reads cannot decide.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Reader", "scopes": []string{"workspace.read"}})["token"].(string)
	bot := &client{t: t, h: e.h, bearer: tok}
	bot.must(200, "GET", sug, nil)
	bot.must(403, "POST", base+"/brands/northpeak/links/discover", nil)

	// The X link's followers come from its public profile page, through
	// Geonode, with no X app. A profile behind a login wall is an error on
	// the account, never zero followers.
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Ghostpeak", "handle": "ghostpeak", "links": []map[string]any{{"url": "https://x.com/ghostpeak"}, {"url": "https://www.youtube.com/@ghostpeaktv"}, {"url": "https://bsky.app/profile/ghostpeak.example"}}})
	if err := e.srv.SyncSocialAccounts(ctx, ws, gn); err != nil {
		t.Fatal(err)
	}
	accts := map[string]map[string]any{}
	for _, a := range owner.must(200, "GET", base+"/social/accounts", nil)["data"].([]any) {
		a := a.(map[string]any)
		accts[a["handle"].(string)] = a
	}
	np, gp := accts["northpeak"], accts["ghostpeak"]
	if np == nil || np["channel"] != "x" || np["followers"] != 12300.0 || np["following"] != 258.0 || np["name"] != "Northpeak" || np["brand_id"] != bid ||
		!strings.HasPrefix(np["avatar_url"].(string), "https://pbs.twimg.com/") || np["last_error"] != nil {
		t.Fatalf("northpeak: %v", np)
	}
	if gp == nil || gp["followers"] != nil || gp["last_error"] == nil {
		t.Fatalf("ghostpeak: %v", gp)
	}
	// Every public profile in the links is read the same way, such as a
	// YouTube channel's subscribers.
	if yt := accts["ghostpeaktv"]; yt == nil || yt["channel"] != "youtube" || yt["followers"] != 4200.0 || yt["name"] != "Ghostpeak TV" || yt["last_error"] != nil {
		t.Fatalf("youtube: %v", yt)
	}
	// Bluesky's public API answers with its counts, read without scripts.
	if bs := accts["ghostpeak.example"]; bs == nil || bs["channel"] != "bluesky" || bs["followers"] != 845.0 || bs["posts"] != 40.0 || bs["name"] != "Ghostpeak" {
		t.Fatalf("bluesky: %v", bs)
	}
	owner.must(200, "GET", base+"/social/accounts?channel=youtube", nil)
	owner.must(400, "GET", base+"/social/accounts?channel=myspace", nil)

	// A page reader in a workspace with linked profiles is queued every
	// hour, so a profile no read has started for (one linked before its
	// channel could be read) starts within the hour, not the next day.
	if _, err := e.srv.QueueDueSocialSyncs(ctx); err != nil {
		t.Fatal(err)
	}
	var hourly int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'social.sync' AND idempotency_key LIKE 'social.sync:' || $1 || ':h%'`, gn).Scan(&hourly)
	if hourly != 1 {
		t.Fatalf("hourly page reader syncs: %d", hourly)
	}

	// Taking the link off the brand stops reading it.
	owner.must(200, "PATCH", base+"/brands/northpeak", map[string]any{"links": []map[string]any{}})
	if err := e.srv.SyncSocialAccounts(ctx, ws, gn); err != nil {
		t.Fatal(err)
	}
	if a := owner.must(200, "GET", base+"/social/accounts/"+np["id"].(string), nil); a["status"] != "disconnected" {
		t.Fatalf("after removing the link: %v", a)
	}

	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
		if ev.Name == "social_account.synced" && ev.Props["source"] != "public_page" {
			t.Fatalf("synced event: %v", ev.Props)
		}
	}
	if names["brand.links_discovered"] != 2 || names["brand.link_added"] != 2 || names["brand.link_dismissed"] != 1 || names["social_account.sync_failed"] < 1 {
		t.Fatalf("events: %v", names)
	}
}
