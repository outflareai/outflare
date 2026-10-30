package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestRedirectLinks(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	follow := func(path, ua string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("User-Agent", ua)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	const person = "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 Safari/605.1.15"
	short := regexp.MustCompile(`/l/([a-z0-9]{8})$`)

	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{
		"name": "Podcast spring", "channel": "referral", "source": "podcast", "landing_url": "https://northpeak.example/listen",
	})
	if c["redirect_links"] != false || c["clicks"] != float64(0) {
		t.Fatalf("campaign = %v", c)
	}
	code := c["code"].(string)
	links := base + "/tracking-campaigns/" + code + "/links"

	// The script stays the default: no redirect link unless asked.
	plain := owner.must(201, "POST", links, map[string]any{"content": "show-notes"})
	if plain["redirect_url"] != nil || plain["clicks"] != float64(0) {
		t.Fatalf("plain = %v", plain)
	}
	// Asking again for the same link adds one, once.
	added := owner.must(200, "POST", links, map[string]any{"content": "show-notes", "redirect": true})
	ru, _ := added["redirect_url"].(string)
	m := short.FindStringSubmatch(ru)
	if added["id"] != plain["id"] || m == nil {
		t.Fatalf("added = %v", added)
	}
	if again := owner.must(200, "POST", links, map[string]any{"content": "show-notes", "redirect": false}); again["redirect_url"] != ru {
		t.Fatalf("a redirect link stays: %v", again)
	}

	// A person's click is counted and goes on to the tagged page with ofclk.
	rec := follow("/l/"+m[1], person)
	if rec.Code != http.StatusFound || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("redirect = %d %v", rec.Code, rec.Header())
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || !strings.HasPrefix(loc.String(), plain["tagged_url"].(string)+"&ofclk=") || !regexp.MustCompile(`^[a-z0-9]{16}$`).MatchString(loc.Query().Get("ofclk")) {
		t.Fatalf("location = %v", loc)
	}
	// A link scanner is kept but not counted; an unknown code is a 404.
	if rec := follow("/l/"+m[1], "Mozilla/5.0 (compatible; Slackbot-LinkExpanding 1.0)"); rec.Code != http.StatusFound {
		t.Fatalf("bot = %d", rec.Code)
	}
	for _, bad := range []string{"/l/zzzzzzzz", "/l/NOT-A-CODE"} {
		if rec := follow(bad, person); rec.Code != http.StatusNotFound {
			t.Fatalf("%s = %d", bad, rec.Code)
		}
	}
	var stored, bots int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*), count(*) FILTER (WHERE likely_bot) FROM tracking_link_clicks WHERE workspace_id = $1`, ws).Scan(&stored, &bots); err != nil || stored != 2 || bots != 1 {
		t.Fatalf("clicks = %d, bots = %d, %v", stored, bots, err)
	}
	list := owner.must(200, "GET", links, nil)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["clicks"] != float64(1) || list[0].(map[string]any)["redirect_url"] != ru ||
		list[0].(map[string]any)["landed"] != float64(0) {
		t.Fatalf("links = %v", list)
	}

	// The landing page's script sends ofclk, and the visit claims the
	// click once: a reload, a track event or a made-up key changes nothing.
	sk := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "Northpeak", "origins": []string{"https://northpeak.example"}})
	clk := loc.Query().Get("ofclk")
	landing := func(anon, kind, key string) {
		t.Helper()
		ev := map[string]any{"type": kind, "anonymousId": anon, "properties": map[string]any{"ofclk": key},
			"context": map[string]any{"page": map[string]any{"url": loc.String(), "path": loc.Path},
				"campaign": map[string]any{"source": "podcast", "medium": "referral", "name": c["slug"]}}}
		if kind == "track" {
			ev["event"] = "Signed up"
		}
		if code, out := sendSite(t, e.h, "https://northpeak.example", sk["key"].(string), ev); code != 200 {
			t.Fatalf("site event = %d %v", code, out)
		}
	}
	landing("v-1", "track", clk)
	landing("v-2", "page", "zzzzzzzzzzzzzzzz")
	if l := owner.must(200, "GET", links, nil)["data"].([]any)[0].(map[string]any); l["landed"] != float64(0) {
		t.Fatalf("claimed without a landing: %v", l)
	}
	landing("v-1", "page", clk)
	landing("v-1", "page", clk)
	landing("v-3", "page", clk)
	var claimed int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM tracking_link_clicks k JOIN site_sessions s ON s.id = k.session_id
		WHERE k.workspace_id = $1 AND k.click_key = $2 AND s.anonymous_id = 'v-1'`, ws, clk).Scan(&claimed); err != nil || claimed != 1 {
		t.Fatalf("claimed = %d, %v", claimed, err)
	}
	if l := owner.must(200, "GET", links, nil)["data"].([]any)[0].(map[string]any); l["landed"] != float64(1) || l["clicks"] != float64(1) {
		t.Fatalf("landed = %v", l)
	}
	if v := owner.must(200, "GET", base+"/tracking-campaigns/"+code, nil); v["landed"] != float64(1) {
		t.Fatalf("campaign landed = %v", v)
	}

	// The campaign's default gives every new link one; a link can say no.
	if v := owner.must(200, "PATCH", base+"/tracking-campaigns/"+code, map[string]any{"redirect_links": true}); v["redirect_links"] != true || v["clicks"] != float64(1) {
		t.Fatalf("patch = %v", v)
	}
	if l := owner.must(201, "POST", links, map[string]any{"content": "qr"}); !short.MatchString(l["redirect_url"].(string)) {
		t.Fatalf("default = %v", l)
	}
	if l := owner.must(201, "POST", links, map[string]any{"content": "bio", "redirect": false}); l["redirect_url"] != nil {
		t.Fatalf("opt out = %v", l)
	}
	if c2 := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Partners", "channel": "affiliates", "redirect_links": true}); c2["redirect_links"] != true {
		t.Fatalf("create = %v", c2)
	}

	// An archived campaign's redirect links keep working, since they may be printed.
	owner.must(200, "PATCH", base+"/tracking-campaigns/"+code, map[string]any{"status": "archived"})
	if rec := follow("/l/"+m[1], person); rec.Code != http.StatusFound {
		t.Fatalf("archived = %d", rec.Code)
	}

	seen := map[string]bool{}
	for _, ev := range got() {
		seen[ev.Name] = true
	}
	for _, name := range []string{"tracking_link.created", "tracking_link.redirect_added"} {
		if !seen[name] {
			t.Fatalf("no %s event", name)
		}
	}
}
