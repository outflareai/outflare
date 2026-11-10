package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/quota"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Weighted destinations and daily click caps on a redirect link
// (plans/ad-tracker.md, T4a).
func TestLinkDestinations(t *testing.T) {
	events := track.Capture(t)
	q := &fakeQuota{refuse: map[string]bool{}}
	e := freshEnvWith(t, func(o *api.Options) { o.Quota = q })
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{
		"name": "Offers spring", "channel": "paid_ads", "source": "acme", "redirect_links": true,
	})
	links := base + "/tracking-campaigns/" + c["code"].(string) + "/links"
	newLink := func(page string) (id, code string) {
		t.Helper()
		l := owner.must(201, "POST", links, map[string]any{"url": page})
		return l["id"].(string), regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(l["redirect_url"].(string))[1]
	}
	const person = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15"
	follow := func(code, ua string) *url.URL {
		t.Helper()
		req := httptest.NewRequest("GET", "/l/"+code, nil)
		req.Header.Set("User-Agent", ua)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("redirect = %d", rec.Code)
		}
		loc, _ := url.Parse(rec.Header().Get("Location"))
		return loc
	}
	flush := func() {
		t.Helper()
		if err := e.srv.FlushClicks(ctx); err != nil {
			t.Fatal(err)
		}
	}

	// A capped offer, one stopped by a cap of 0 and one by a weight of 0;
	// the link's own page is the fallback.
	linkID, code := newLink("https://northpeak.example/fallback")
	dests := links + "/" + linkID + "/destinations"
	got := owner.must(200, "PUT", dests, map[string]any{"destinations": []any{
		map[string]any{"name": "Offer A", "url": "https://northpeak.example/a?c={click_id}", "daily_click_cap": 3},
		map[string]any{"name": "Offer B", "url": "https://northpeak.example/b", "daily_click_cap": 0},
		map[string]any{"name": "Offer C", "url": "https://northpeak.example/c", "weight": 0},
	}})
	ds := got["destinations"].([]any)
	if len(ds) != 3 || got["fallback_url"] != "https://northpeak.example/fallback" || ds[0].(map[string]any)["weight"] != float64(100) ||
		ds[0].(map[string]any)["open"] != true || ds[1].(map[string]any)["open"] != false || ds[2].(map[string]any)["open"] != false {
		t.Fatalf("destinations: %v", got)
	}
	offerA := ds[0].(map[string]any)["id"].(string)

	// A link scanner goes where a person would but doesn't use up the cap.
	if loc := follow(code, "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"); loc.Path != "/a" {
		t.Fatalf("bot went to %s", loc)
	}
	var pages []string
	for range 5 {
		loc := follow(code, person)
		pages = append(pages, loc.Path)
		if loc.Path == "/a" && loc.Query().Get("c") != loc.Query().Get("ofclk") {
			t.Fatalf("token not filled: %s", loc)
		}
	}
	if strings.Join(pages, ",") != "/a,/a,/a,/fallback,/fallback" {
		t.Fatalf("pages = %v", pages)
	}
	flush()
	got = owner.must(200, "GET", dests, nil)
	if a := got["destinations"].([]any)[0].(map[string]any); a["clicks_today"] != float64(3) || a["open"] != false {
		t.Fatalf("after the cap: %v", a)
	}
	var toA, toFallback int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE destination_id = $2::uuid), count(*) FILTER (WHERE destination_id IS NULL)
		FROM tracking_link_clicks WHERE workspace_id = $1 AND NOT likely_bot`, ws, offerA).Scan(&toA, &toFallback); err != nil || toA != 3 || toFallback != 2 {
		t.Fatalf("clicks: %d to A, %d to the fallback, %v", toA, toFallback, err)
	}

	// Raising the cap opens it again, keeping its count; leaving C out takes
	// it off the link.
	got = owner.must(200, "PUT", dests, map[string]any{"destinations": []any{
		map[string]any{"id": offerA, "name": "Offer A", "url": "https://northpeak.example/a", "daily_click_cap": 4},
		map[string]any{"id": ds[1].(map[string]any)["id"], "url": "https://northpeak.example/b", "daily_click_cap": 0},
	}})
	if len(got["destinations"].([]any)) != 2 || got["destinations"].([]any)[0].(map[string]any)["clicks_today"] != float64(3) {
		t.Fatalf("updated: %v", got)
	}
	if loc := follow(code, person); loc.Path != "/a" {
		t.Fatalf("after raising the cap: %s", loc)
	}
	if loc := follow(code, person); loc.Path != "/fallback" {
		t.Fatalf("after the new cap: %s", loc)
	}
	var removed int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM tracking_link_destinations WHERE link_id = $1 AND removed_at IS NOT NULL`, linkID).Scan(&removed); err != nil || removed != 1 {
		t.Fatalf("removed = %d, %v", removed, err)
	}

	// Weights split the clicks.
	link2, code2 := newLink("https://northpeak.example/home")
	owner.must(200, "PUT", links+"/"+link2+"/destinations", map[string]any{"destinations": []any{
		map[string]any{"url": "https://northpeak.example/one", "weight": 1},
		map[string]any{"url": "https://northpeak.example/three", "weight": 3},
	}})
	three := 0
	for range 400 {
		if follow(code2, person).Path == "/three" {
			three++
		}
	}
	if three < 240 || three > 360 {
		t.Fatalf("weight 3 of 4 got %d of 400", three)
	}

	// Clicks over the workspace's click quota aren't stored and don't use
	// up a cap.
	link3, code3 := newLink("https://northpeak.example/fallback3")
	owner.must(200, "PUT", links+"/"+link3+"/destinations", map[string]any{"destinations": []any{
		map[string]any{"url": "https://northpeak.example/only", "daily_click_cap": 1},
	}})
	flush()
	q.set(false, quota.TrackerClicks)
	if loc := follow(code3, person); loc.Path != "/only" {
		t.Fatalf("over quota went to %s", loc)
	}
	flush()
	q.set(false)
	if loc := follow(code3, person); loc.Path != "/only" {
		t.Fatalf("a refused click used up the cap: %s", loc)
	}

	// Bad lists.
	for name, body := range map[string][]any{
		"weight":     {map[string]any{"url": "https://northpeak.example/a", "weight": -1}},
		"cap":        {map[string]any{"url": "https://northpeak.example/a", "daily_click_cap": -2}},
		"url":        {map[string]any{"url": "northpeak"}},
		"token":      {map[string]any{"url": "https://northpeak.example/?x={nope}"}},
		"unknown id": {map[string]any{"id": "00000000-0000-4000-8000-000000000009", "url": "https://northpeak.example/a"}},
		"twenty-one": func() []any {
			var l []any
			for range 21 {
				l = append(l, map[string]any{"url": "https://northpeak.example/a"})
			}
			return l
		}(),
	} {
		if status, out := owner.do("PUT", dests, map[string]any{"destinations": body}); status != 400 {
			t.Errorf("%s: %d %v", name, status, out)
		}
	}
	plain := owner.must(201, "POST", links, map[string]any{"url": "https://northpeak.example/plain", "content": "no-redirect", "redirect": false})
	owner.must(400, "PUT", links+"/"+plain["id"].(string)+"/destinations", map[string]any{"destinations": []any{map[string]any{"url": "https://northpeak.example/a"}}})
	other := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Repocket"})
	owner.must(404, "GET", "/v1/brands/"+other["id"].(string)+"/tracking-campaigns/"+c["code"].(string)+"/links/"+linkID+"/destinations", nil)

	found := false
	for _, ev := range events() {
		found = found || ev.Name == "tracking_link.destinations_updated"
	}
	if !found {
		t.Error("no tracking_link.destinations_updated event")
	}
}
