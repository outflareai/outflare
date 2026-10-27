package api_test

import (
	"context"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestCompetitorsSuggestTrackAndNumbers(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	events := track.Capture(t)
	t.Cleanup(func() { dfsRank.Store(0); setDFSRivals() })

	id := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})["id"].(string)
	siteID := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.example.com/"})["id"].(string)

	// The connection suggests competitors; the site itself and its own
	// subdomains are never one.
	setDFSRivals(
		map[string]any{"domain": "rival.example", "intersections": 120},
		map[string]any{"domain": "Other.Example", "intersections": 40},
		map[string]any{"domain": "blog.example.com", "intersections": 300},
		map[string]any{"domain": "example.com", "intersections": 900},
	)
	res := owner.must(200, "POST", base+"/seo/"+id+"/sync", nil)
	runs := res["competitors"].([]any)
	if len(runs) != 1 || runs[0].(map[string]any)["suggested"].(float64) != 2 || runs[0].(map[string]any)["error"] != nil {
		t.Fatalf("sync: %v", res["competitors"])
	}
	list := owner.must(200, "GET", base+"/seo-competitors?site_id="+siteID, nil)
	data := list["data"].([]any)
	if list["total"].(float64) != 2 || data[0].(map[string]any)["host"] != "rival.example" || data[0].(map[string]any)["status"] != "suggested" ||
		data[0].(map[string]any)["source"] != "dataforseo" || data[0].(map[string]any)["common_keywords"].(float64) != 120 || data[1].(map[string]any)["host"] != "other.example" {
		t.Fatalf("suggested: %v", list)
	}
	rival := data[0].(map[string]any)["id"].(string)

	// Tracking one and adding one by hand; the site itself and a repeat
	// are refused.
	if c := owner.must(200, "PATCH", base+"/seo-competitors/"+rival, map[string]any{"status": "tracked"}); c["status"] != "tracked" {
		t.Fatalf("track: %v", c)
	}
	third := owner.must(201, "POST", base+"/seo-competitors", map[string]any{"site_id": siteID, "host": "https://www.Third.example/pricing"})
	if third["host"] != "third.example" || third["status"] != "tracked" || third["source"] != "manual" || third["numbers"] != nil {
		t.Fatalf("add: %v", third)
	}
	owner.must(409, "POST", base+"/seo-competitors", map[string]any{"site_id": siteID, "host": "third.example"})
	owner.must(400, "POST", base+"/seo-competitors", map[string]any{"site_id": siteID, "host": "shop.example.com"})
	owner.must(400, "POST", base+"/seo-competitors", map[string]any{"site_id": siteID, "host": "not a domain"})
	owner.must(400, "PATCH", base+"/seo-competitors/"+rival, map[string]any{"status": "maybe"})
	// A name and the other names it goes by; aliases replace the list.
	named := owner.must(200, "PATCH", base+"/seo-competitors/"+third["id"].(string), map[string]any{"name": " Third Co ", "aliases": []string{"Third", "third", " 3rd "}})
	if named["name"] != "Third Co" || named["status"] != "tracked" || len(named["aliases"].([]any)) != 2 || named["aliases"].([]any)[1] != "3rd" {
		t.Fatalf("named: %v", named)
	}
	owner.must(400, "PATCH", base+"/seo-competitors/"+rival, map[string]any{})
	owner.must(400, "PATCH", base+"/seo-competitors/"+rival, map[string]any{"aliases": []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}})
	if c := owner.must(200, "PATCH", base+"/seo-competitors/"+third["id"].(string), map[string]any{"aliases": []string{}}); c["name"] != "Third Co" || len(c["aliases"].([]any)) != 0 {
		t.Fatalf("aliases cleared: %v", c)
	}
	var queued int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE kind = 'crawl.glance'`).Scan(&queued); err != nil || queued != 2 {
		t.Fatalf("glances queued: %d %v", queued, err)
	}

	// Tracked competitors get numbers; the first snapshot is a baseline.
	run := owner.must(200, "POST", base+"/seo/"+id+"/sync?again=1", nil)["competitors"].([]any)[0].(map[string]any)
	if run["checked"].(float64) != 2 || run["events"].(float64) != 0 {
		t.Fatalf("baseline: %v", run)
	}
	// A week later the domain rating moved.
	if _, err := e.pool.Exec(context.Background(), `UPDATE seo_snapshots SET day = day - 7 WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	dfsRank.Store(47)
	run = owner.must(200, "POST", base+"/seo/"+id+"/sync?again=2", nil)["competitors"].([]any)[0].(map[string]any)
	if run["checked"].(float64) != 2 || run["events"].(float64) != 2 {
		t.Fatalf("moved: %v", run)
	}
	feed := owner.must(200, "GET", base+"/seo-competitors/events?site_id="+siteID+"&competitor_id="+rival, nil)["data"].([]any)
	if len(feed) != 1 {
		t.Fatalf("feed: %v", feed)
	}
	ev := feed[0].(map[string]any)
	if ev["kind"] != "rating_changed" || ev["host"] != "rival.example" || ev["connector"] != "dataforseo" ||
		ev["data"].(map[string]any)["before"].(float64) != 40 || ev["data"].(map[string]any)["after"].(float64) != 47 {
		t.Fatalf("event: %v", ev)
	}
	owner.must(400, "GET", base+"/seo-competitors/events?kind=nope", nil)
	tracked := owner.must(200, "GET", base+"/seo-competitors?status=tracked", nil)["data"].([]any)
	if len(tracked) != 2 || tracked[0].(map[string]any)["numbers"].(map[string]any)["metrics"].(map[string]any)["domain_rating"].(float64) != 47 {
		t.Fatalf("numbers: %v", tracked)
	}

	// Checking is for tracked competitors; dismissed ones stay hidden.
	owner.must(202, "POST", base+"/seo-competitors/"+rival+"/check", nil)
	owner.must(200, "PATCH", base+"/seo-competitors/"+rival, map[string]any{"status": "dismissed"})
	owner.must(409, "POST", base+"/seo-competitors/"+rival+"/check", nil)
	setDFSRivals(map[string]any{"domain": "rival.example", "intersections": 130})
	owner.must(200, "POST", base+"/seo/"+id+"/sync?again=3", nil)
	if c := owner.must(200, "GET", base+"/seo-competitors?status=dismissed", nil)["data"].([]any); len(c) != 1 || c[0].(map[string]any)["common_keywords"].(float64) != 130 {
		t.Fatalf("dismissed: %v", c)
	}
	owner.must(204, "DELETE", base+"/seo-competitors/"+third["id"].(string), nil)
	owner.must(404, "DELETE", base+"/seo-competitors/"+third["id"].(string), nil)

	got := map[string]bool{}
	for _, ev := range events() {
		got[ev.Name] = true
	}
	for _, name := range []string{"seo_competitor.added", "seo_competitor.updated", "seo_competitor.removed", "seo_competitor.check_requested", "seo_competitors.synced"} {
		if !got[name] {
			t.Errorf("no %s event", name)
		}
	}
}

func TestCompetitorGlance(t *testing.T) {
	e := freshEnv(t)
	owner, _ := setupOwner(t, e.h)
	ws := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Sandbox", "test": true})["id"].(string)
	base := "/v1/workspaces/" + ws
	siteID := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "www.example.com"})["id"].(string)
	e.runCrawls(t)

	// The first look is a baseline: the home page and the sitemap's pages.
	owner.must(201, "POST", base+"/seo-competitors", map[string]any{"site_id": siteID, "host": "rival.example"})
	path := base + "/seo-competitors?site_id=" + siteID
	looked := func(m map[string]any) bool { return m["data"].([]any)[0].(map[string]any)["checked_at"] != nil }
	c := waitFor(t, owner, path, looked)["data"].([]any)[0].(map[string]any)
	if c["title"] != "Home" || c["pages"].(float64) != 3 || c["check_error"] != nil {
		t.Fatalf("first look: %v", c)
	}
	if feed := owner.must(200, "GET", base+"/seo-competitors/events", nil)["data"].([]any); len(feed) != 0 {
		t.Fatalf("baseline made events: %v", feed)
	}

	// Next time a page is new, one is gone and the title changed.
	ctx := context.Background()
	for _, q := range []string{
		`DELETE FROM competitor_pages WHERE url LIKE '%/pricing'`,
		`INSERT INTO competitor_pages (competitor_id, workspace_id, url) SELECT id, workspace_id, 'https://rival.example/old' FROM seo_competitors`,
		`UPDATE seo_competitors SET title = 'Old home', checked_at = now() - interval '8 days'`,
		`DELETE FROM jobs WHERE kind = 'crawl.glance'`,
	} {
		if _, err := e.pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	owner.must(202, "POST", base+"/seo-competitors/"+c["id"].(string)+"/check", nil)
	waitFor(t, owner, path, func(m map[string]any) bool { return m["data"].([]any)[0].(map[string]any)["title"] == "Home" })
	feed := owner.must(200, "GET", base+"/seo-competitors/events", nil)["data"].([]any)
	kinds := map[string]map[string]any{}
	for _, ev := range feed {
		ev := ev.(map[string]any)
		kinds[ev["kind"].(string)] = ev["data"].(map[string]any)
	}
	if len(kinds) != 3 || kinds["pages_added"]["count"].(float64) != 1 || kinds["pages_removed"]["urls"].([]any)[0] != "https://rival.example/old" ||
		kinds["title_changed"]["before"] != "Old home" {
		t.Fatalf("events: %v", feed)
	}
}
