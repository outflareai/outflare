package api_test

import (
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

// dfsLink is a link as DataForSEO's backlinks report gives it.
func dfsLink(from, to, domain string, rank float64, firstSeen time.Time, lost *time.Time) map[string]any {
	l := map[string]any{"url_from": from, "url_to": to, "anchor": "Example", "domain_from": domain, "dofollow": true,
		"domain_from_rank": rank, "backlink_spam_score": 2, "first_seen": firstSeen.Format("2006-01-02 15:04:05 -07:00"),
		"last_seen": time.Now().UTC().Format("2006-01-02 15:04:05 -07:00"), "is_lost": false}
	if lost != nil {
		l["is_lost"], l["last_seen"] = true, lost.Format("2006-01-02 15:04:05 -07:00")
	}
	return l
}

func TestBacklinksLifecycle(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	events := track.Capture(t)

	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})
	id := i["id"].(string)
	site := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.example.com/"})
	siteID := site["id"].(string)

	now := time.Now().UTC()
	ago := func(days int) time.Time { return now.AddDate(0, 0, -days) }
	lostAt := ago(10)
	// An old link is the baseline, a fresh one is new, one lost ten days
	// ago is lost, and the site's own subdomain is not a backlink.
	setDFSLinks(
		dfsLink("https://Old.Example.org/page#top", "https://www.example.com/", "old.example.org", 60, ago(400), nil),
		dfsLink("https://fresh.example.net/post", "https://www.example.com/pricing", "fresh.example.net", 30, ago(5), nil),
		dfsLink("https://gone.example.org/a", "https://www.example.com/", "gone.example.org", 45, ago(200), &lostAt),
		dfsLink("https://blog.example.com/x", "https://www.example.com/", "blog.example.com", 20, ago(3), nil),
	)
	res := owner.must(200, "POST", base+"/seo/"+id+"/sync", nil)
	links := res["links"].([]any)
	if len(links) != 1 {
		t.Fatalf("sync: %v", res)
	}
	run := links[0].(map[string]any)
	if run["site_id"] != siteID || run["links_seen"].(float64) != 3 || run["new"].(float64) != 1 || run["lost"].(float64) != 1 || run["regained"].(float64) != 0 || run["error"] != nil {
		t.Fatalf("first run: %v", run)
	}
	list := owner.must(200, "GET", base+"/backlinks?site_id="+siteID, nil)
	data := list["data"].([]any)
	if list["total"].(float64) != 3 || len(data) != 3 || data[0].(map[string]any)["source_url"] != "https://old.example.org/page" ||
		data[0].(map[string]any)["source_rating"].(float64) != 60 || data[0].(map[string]any)["providers"].(map[string]any)["dataforseo"] == nil {
		t.Fatalf("list: %v", list)
	}
	if lost := owner.must(200, "GET", base+"/backlinks?status=lost", nil)["data"].([]any); len(lost) != 1 || lost[0].(map[string]any)["source_domain"] != "gone.example.org" {
		t.Fatalf("lost: %v", lost)
	}
	if found := owner.must(200, "GET", base+"/backlinks?q=fresh", nil)["data"].([]any); len(found) != 1 {
		t.Fatalf("search: %v", found)
	}

	// The next week the old link is gone and the lost one is back.
	yesterday := ago(1)
	setDFSLinks(
		dfsLink("https://old.example.org/page", "https://www.example.com/", "old.example.org", 60, ago(400), &yesterday),
		dfsLink("https://fresh.example.net/post", "https://www.example.com/pricing", "fresh.example.net", 30, ago(5), nil),
		dfsLink("https://gone.example.org/a", "https://www.example.com/", "gone.example.org", 45, ago(200), nil),
	)
	run = owner.must(200, "POST", base+"/seo/"+id+"/sync?again=1", nil)["links"].([]any)[0].(map[string]any)
	if run["new"].(float64) != 0 || run["lost"].(float64) != 1 || run["regained"].(float64) != 1 {
		t.Fatalf("second run: %v", run)
	}
	feed := owner.must(200, "GET", base+"/backlinks/events?site_id="+siteID, nil)["data"].([]any)
	kinds := map[string]int{}
	for _, ev := range feed {
		kinds[ev.(map[string]any)["kind"].(string)]++
	}
	if len(feed) != 4 || kinds["new"] != 1 || kinds["lost"] != 2 || kinds["regained"] != 1 {
		t.Fatalf("feed: %v", feed)
	}
	if only := owner.must(200, "GET", base+"/backlinks/events?kind=regained", nil)["data"].([]any); len(only) != 1 ||
		only[0].(map[string]any)["backlink"].(map[string]any)["source_domain"] != "gone.example.org" {
		t.Fatalf("regained: %v", only)
	}
	sum := owner.must(200, "GET", base+"/backlinks/summary", nil)["data"].([]any)[0].(map[string]any)
	last := sum["last_30_days"].(map[string]any)
	if sum["live"].(float64) != 2 || sum["lost"].(float64) != 1 || sum["referring_domains"].(float64) != 2 || last["new"].(float64) != 1 ||
		last["lost"].(float64) != 2 || last["regained"].(float64) != 1 || len(sum["weeks"].([]any)) != 12 || len(sum["providers"].([]any)) != 1 {
		t.Fatalf("summary: %v", sum)
	}

	owner.must(400, "GET", base+"/backlinks?status=gone", nil)
	owner.must(400, "GET", base+"/backlinks?site_id=nope", nil)
	owner.must(400, "GET", base+"/backlinks?order=alpha", nil)
	owner.must(400, "GET", base+"/backlinks/events?kind=moved", nil)
	owner.must(400, "PUT", base+"/seo/"+id+"/settings", map[string]any{"link_rows": 5000})
	if set := owner.must(200, "PUT", base+"/seo/"+id+"/settings", map[string]any{"link_rows": 50}); set["link_rows"].(float64) != 50 {
		t.Fatalf("link rows: %v", set)
	}
	n := 0
	for _, ev := range events() {
		if ev.Name == "backlinks.synced" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("backlinks.synced events: %d", n)
	}
}
