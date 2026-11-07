package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/track"
)

// fakeGeonodePages is Geonode's Scraper API reading a brand's homepage
// (built in the browser, so its links only show with JavaScript on) and
// public X profile pages.
func fakeGeonodePages(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(geonodeJobs(nil, func(w http.ResponseWriter, r *http.Request) {
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

// geonodeJobs adds Geonode's page jobs (processing_mode async, then GET
// /v1/extract/{id}) to a fake that answers sync reads: a job ends with
// what the sync read answered, completed for a 200 and failed with its
// code otherwise. A job for an address in pending runs forever.
func geonodeJobs(pending map[string]bool, answer http.HandlerFunc) http.Handler {
	var mu sync.Mutex
	jobs := map[string]map[string]any{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/extract/") {
			job, ok := jobs[strings.TrimPrefix(r.URL.Path, "/v1/extract/")]
			w.Header().Set("Content-Type", "application/json")
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, `{"code": "NOT_FOUND"}`)
				return
			}
			json.NewEncoder(w).Encode(job)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(raw, &req)
		if r.URL.Path != "/v1/extract" || req["processing_mode"] != "async" {
			r.Body = io.NopCloser(bytes.NewReader(raw))
			answer(w, r)
			return
		}
		url, _ := req["url"].(string)
		id := fmt.Sprintf("job-%d", len(jobs)+1)
		job := map[string]any{"status": "processing"}
		if !pending[url] {
			req["processing_mode"] = "sync"
			body, _ := json.Marshal(req)
			rec := httptest.NewRecorder()
			sr := r.Clone(r.Context())
			sr.Body = io.NopCloser(bytes.NewReader(body))
			answer(rec, sr)
			json.Unmarshal(rec.Body.Bytes(), &job)
			if rec.Code == http.StatusOK {
				job["status"] = "completed"
			} else {
				code, _ := job["code"].(string)
				if e, ok := job["error"].(map[string]any); ok {
					code, _ = e["code"].(string)
				}
				job = map[string]any{"status": "failed", "error": map[string]any{"code": code}, "tokens_charged": 0}
			}
		}
		jobs[id] = job
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]any{"job_id": id, "status": "queued"})
	})
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
	for _, brand := range []string{bid, "ghostpeak"} {
		for _, a := range owner.must(200, "GET", base+"/social/accounts?brand="+brand, nil)["data"].([]any) {
			a := a.(map[string]any)
			accts[a["handle"].(string)] = a
		}
	}
	np, gp := accts["northpeak"], accts["ghostpeak"]
	if np == nil || np["channel"] != "x" || np["followers"] != 12300.0 || np["following"] != 258.0 || np["name"] != "Northpeak" || np["brand_id"] != bid ||
		!strings.HasPrefix(np["avatar_url"].(string), "https://pbs.twimg.com/") || np["last_error"] != nil {
		t.Fatalf("northpeak: %v", np)
	}
	if gp == nil || gp["followers"] != nil || gp["last_error"] == nil {
		t.Fatalf("ghostpeak: %v", gp)
	}
	// It says why, and is tried again in 15 minutes, not tomorrow, so
	// today's count still comes in.
	next, _ := time.Parse(time.RFC3339Nano, gp["next_sync_at"].(string))
	if gp["last_error_code"] != "no_follower_count" || gp["failures"] != 1.0 || gp["last_failed_at"] == nil ||
		time.Until(next) < 10*time.Minute || time.Until(next) > 20*time.Minute {
		t.Fatalf("ghostpeak's retry: %v", gp)
	}
	// Read now reads it again at once, as does Try again on the account.
	owner.must(202, "POST", base+"/social/accounts/sync?brand=ghostpeak", nil)
	var due bool
	e.pool.QueryRow(ctx, `SELECT next_sync_at <= now() FROM social_accounts WHERE id = $1`, gp["id"]).Scan(&due)
	if !due {
		t.Fatal("read now should read the failed account again")
	}
	owner.must(202, "POST", base+"/social/accounts/"+gp["id"].(string)+"/sync", nil)
	owner.must(404, "POST", base+"/social/accounts/00000000-0000-0000-0000-000000000000/sync", nil)
	if err := e.srv.SyncSocialAccounts(ctx, ws, gn); err != nil {
		t.Fatal(err)
	}
	if again := owner.must(200, "GET", base+"/social/accounts/"+gp["id"].(string), nil); again["failures"] != 2.0 {
		t.Fatalf("second try: %v", again)
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

// A website that shows the page reader a bot check, or loads too slowly,
// is the site's own refusal: the read with JavaScript on is still tried,
// the failure gets its own code (blocked, slow_page) and the job is not
// retried, and the Links panel says why nothing was found.
func TestBrandLinkDiscoveryBlockedOrSlow(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	reads := map[string]int{}
	defer api.SetLinkJobWait(time.Millisecond, 50*time.Millisecond)()
	srv := httptest.NewServer(geonodeJobs(map[string]bool{"https://slowest.example/": true}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/usage/concurrency" {
			io.WriteString(w, `{"work_concurrency_in_use": 0, "work_concurrency_limit": 5}`)
			return
		}
		var req struct {
			URL      string `json:"url"`
			RenderJS bool   `json:"render_js"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		reads[req.URL]++
		switch {
		case req.URL == "https://blocked.example/":
			w.WriteHeader(http.StatusUnprocessableEntity)
			io.WriteString(w, `{"error": {"code": "UNPROCESSABLE_CONTENT", "message": "Cloudflare challenge"}}`)
		case req.URL == "https://slow.example/" && !req.RenderJS:
			w.WriteHeader(http.StatusGatewayTimeout)
			io.WriteString(w, `{"code": "TIMEOUT"}`)
		case req.URL == "https://slow.example/":
			json.NewEncoder(w).Encode(map[string]any{
				"data":           map[string]any{"html": `<a href="https://www.linkedin.com/company/slowpeak/">LinkedIn</a>`},
				"metadata":       map[string]any{"http_status": 200, "url": req.URL},
				"tokens_charged": 1,
			})
		case req.URL == "https://slower.example/", req.URL == "https://slowest.example/":
			w.WriteHeader(http.StatusGatewayTimeout)
			io.WriteString(w, `{"code": "TIMEOUT"}`)
		case req.URL == "https://challenge.example/" && req.RenderJS:
			json.NewEncoder(w).Encode(map[string]any{
				"data":     map[string]any{"html": `<html><title>Just a moment...</title></html>`},
				"metadata": map[string]any{"http_status": 403, "url": req.URL}, "tokens_charged": 1,
			})
		case req.URL == "https://challenge.example/":
			w.WriteHeader(http.StatusUnprocessableEntity)
			io.WriteString(w, `{"error": {"code": "CAPTCHA_CHALLENGE"}}`)
		case req.URL == "https://broke.example/":
			w.WriteHeader(http.StatusPaymentRequired)
			io.WriteString(w, `{"code": "PAYMENT_REQUIRED", "message": "out of credits"}`)
		case req.URL == "https://down.example/":
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"code": "INTERNAL"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	e.integrations.Client.BaseURLs["geonode"] = srv.URL
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "geonode", "credentials": map[string]string{"api_key": "gn-key"}})

	brand := func(name, site string) (string, string) {
		b := owner.must(201, "POST", base+"/brands", map[string]any{"name": name, "handle": strings.ToLower(name), "website": site})
		return b["id"].(string), base + "/brands/" + strings.ToLower(name) + "/link-suggestions"
	}

	// A bot check on both reads: blocked, not an outage, and not retried.
	bid, sug := brand("Blockpeak", "https://blocked.example")
	if list := owner.must(200, "GET", sug, nil); list["last_read_at"] != nil || list["last_read_error"] != "" {
		t.Fatalf("before reading: %v", list)
	}
	if err := e.srv.DiscoverBrandLinks(ctx, ws, bid); err != nil {
		t.Fatalf("a blocked site should not be retried: %v", err)
	}
	if reads["https://blocked.example/"] != 2 {
		t.Fatalf("reads of the blocked site: %d, want one without and one with JavaScript", reads["https://blocked.example/"])
	}
	if list := owner.must(200, "GET", sug, nil); list["last_read_at"] == nil || list["last_read_error"] != "blocked" || len(list["data"].([]any)) != 0 {
		t.Fatalf("after a bot check: %v", list)
	}

	// Too slow without JavaScript, read with it: the links are found and
	// the panel shows no error.
	bid, sug = brand("Slowpeak", "https://slow.example")
	if err := e.srv.DiscoverBrandLinks(ctx, ws, bid); err != nil {
		t.Fatal(err)
	}
	if list := owner.must(200, "GET", sug, nil); list["last_read_error"] != "" || len(list["data"].([]any)) != 1 {
		t.Fatalf("slow then read: %v", list)
	}

	// Too slow both times: slow_page.
	bid, sug = brand("Slowerpeak", "https://slower.example")
	if err := e.srv.DiscoverBrandLinks(ctx, ws, bid); err != nil {
		t.Fatalf("a slow site should not be retried: %v", err)
	}
	if list := owner.must(200, "GET", sug, nil); list["last_read_error"] != "slow_page" {
		t.Fatalf("slow both times: %v", list)
	}

	// Still reading the page with JavaScript when the job's wait runs out:
	// slow_page, never an outage.
	bid, sug = brand("Slowestpeak", "https://slowest.example")
	if err := e.srv.DiscoverBrandLinks(ctx, ws, bid); err != nil {
		t.Fatalf("a page still being read should not be retried: %v", err)
	}
	if list := owner.must(200, "GET", sug, nil); list["last_read_error"] != "slow_page" {
		t.Fatalf("still reading: %v", list)
	}

	// The read with JavaScript answered with the bot check itself (403):
	// blocked, not a page with no links.
	bid, sug = brand("Challengepeak", "https://challenge.example")
	if err := e.srv.DiscoverBrandLinks(ctx, ws, bid); err != nil {
		t.Fatal(err)
	}
	if list := owner.must(200, "GET", sug, nil); list["last_read_error"] != "blocked" {
		t.Fatalf("challenge page: %v", list)
	}

	codes := map[string]int{}
	for _, ev := range got() {
		if ev.Name == "brand.links_discovery_failed" {
			codes[ev.ErrorCode]++
		}
	}
	if codes["blocked"] < 1 || codes["slow_page"] < 1 || codes["connector_unavailable"] != 0 {
		t.Fatalf("failure codes: %v", codes)
	}

	// The page reader out of credits, or down: the panel says so, and the
	// job is tried again, its events naming the connector's code instead of
	// a generic job error.
	for site, code := range map[string]string{"https://broke.example": "connector_quota", "https://down.example": "connector_unavailable"} {
		name := map[string]string{"connector_quota": "Brokepeak", "connector_unavailable": "Downpeak"}[code]
		bid, sug := brand(name, site)
		err := e.srv.DiscoverBrandLinks(ctx, ws, bid)
		var coded interface{ ErrorCode() string }
		if !errors.As(err, &coded) || coded.ErrorCode() != code {
			t.Fatalf("%s: error %v, want one retried with code %s", site, err, code)
		}
		if list := owner.must(200, "GET", sug, nil); list["last_read_error"] != code {
			t.Fatalf("%s: %v", site, list)
		}
	}
}
