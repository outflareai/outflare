package api_test

import (
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

// dfsLink is a link as DataForSEO's backlinks report gives it.
func dfsLink(from, to, domain string, rank float64, firstSeen time.Time, lost *time.Time) map[string]any {
	l := map[string]any{"url_from": from, "url_to": to, "anchor": "Example", "domain_from": domain, "dofollow": true,
		"domain_from_rank": rank, "backlink_spam_score": 2, "item_type": "anchor", "first_seen": firstSeen.Format("2006-01-02 15:04:05 -07:00"),
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

func TestBacklinkFiltersAndGroups(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})
	id := i["id"].(string)
	siteID := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.example.com/"})["id"].(string)

	now := time.Now().UTC()
	ago := func(days int) time.Time { return now.AddDate(0, 0, -days) }
	lostAt := ago(3)
	// Three links from one domain (one an image in the content, one to a
	// redirecting page), a UGC link, and one lost three days ago.
	a := dfsLink("https://news.example.org/a", "https://www.example.com/", "news.example.org", 70, ago(400), nil)
	a["page_from_rank"], a["item_type"], a["semantic_location"] = 20, "image", "article"
	b := dfsLink("https://news.example.org/b", "https://www.example.com/old", "news.example.org", 70, ago(300), nil)
	b["page_from_rank"], b["url_to_redirect_target"], b["url_to_status_code"] = 35, "https://www.example.com/new", 301
	c := dfsLink("https://news.example.org/c", "https://www.example.com/", "news.example.org", 70, ago(5), nil)
	c["page_from_rank"], c["dofollow"] = 50, false
	d := dfsLink("https://forum.example.net/t/1", "https://www.example.com/pricing", "forum.example.net", 25, ago(200), nil)
	d["attributes"], d["anchor"] = []any{"ugc"}, "Cheap proxies here"
	gone := dfsLink("https://gone.example.org/x", "https://www.example.com/", "gone.example.org", 40, ago(100), &lostAt)
	setDFSLinks(a, b, c, d, gone)
	owner.must(200, "POST", base+"/seo/"+id+"/sync", nil)

	list := func(query string) []map[string]any {
		t.Helper()
		var out []map[string]any
		for _, x := range owner.must(200, "GET", base+"/backlinks?site_id="+siteID+"&"+query, nil)["data"].([]any) {
			out = append(out, x.(map[string]any))
		}
		return out
	}
	// One link per domain: news.example.org stands as one row of three, by
	// its best link: followed and in the page's main content.
	got := list("group=domain&since=" + ago(30).Format(time.DateOnly))
	if len(got) != 3 {
		t.Fatalf("grouped: %v", got)
	}
	for _, l := range got {
		if l["source_domain"] == "news.example.org" && (l["group_links"].(float64) != 3 || l["source_url"] != "https://news.example.org/a") {
			t.Fatalf("news group: %v", l)
		}
	}
	if all := list(""); len(all) != 5 || all[0]["group_links"].(float64) != 1 {
		t.Fatalf("all: %v", all)
	}
	// Live only without history; the lost link shows with a history range.
	if live := list("status=live"); len(live) != 4 {
		t.Fatalf("live: %v", live)
	}
	if lost := list("change=lost&since=" + ago(7).Format(time.DateOnly)); len(lost) != 1 || lost[0]["source_domain"] != "gone.example.org" || lost[0]["lost_at"] == nil {
		t.Fatalf("lost: %v", lost)
	}
	if fresh := list("change=new"); len(fresh) != 1 || fresh[0]["source_url"] != "https://news.example.org/c" {
		t.Fatalf("new: %v", fresh)
	}
	if ugc := list("follow=ugc"); len(ugc) != 1 || ugc[0]["source_domain"] != "forum.example.net" {
		t.Fatalf("ugc: %v", ugc)
	}
	if nf := list("follow=nofollow"); len(nf) != 1 {
		t.Fatalf("nofollow: %v", nf)
	}
	if img := list("link_type=image"); len(img) != 1 || img[0]["url_rating"].(float64) != 20 || img[0]["in_content"] != true {
		t.Fatalf("image: %v", img)
	}
	if strong := list("dr_min=50&order=url_rating"); len(strong) != 3 || strong[0]["source_url"] != "https://news.example.org/c" {
		t.Fatalf("dr: %v", strong)
	}
	hop := list("q=old&q_in=target_url")
	if len(hop) != 1 {
		t.Fatalf("target search: %v", hop)
	}
	if hops := hop[0]["hops"].([]any); len(hops) != 1 || hops[0].(map[string]any)["kind"] != "301" || hops[0].(map[string]any)["url"] != "https://www.example.com/new" {
		t.Fatalf("hops: %v", hop[0])
	}
	if anchor := list("q=cheap&q_in=anchor"); len(anchor) != 1 {
		t.Fatalf("anchor search: %v", anchor)
	}
	if dom := list("source_domain=news.example.org"); len(dom) != 3 {
		t.Fatalf("domain: %v", dom)
	}
	sum := owner.must(200, "GET", base+"/backlinks/summary?site_id="+siteID, nil)["data"].([]any)[0].(map[string]any)
	if lt := sum["link_types"].([]any); len(lt) != 2 || sum["traffic"] != false {
		t.Fatalf("summary facets: %v", sum)
	}

	for _, bad := range []string{"group=site", "change=moved", "since=yesterday", "follow=maybe", "link_type=pixel", "dr_min=-1", "q=x&q_in=body", "page_type=a/b"} {
		owner.must(400, "GET", base+"/backlinks?"+bad, nil)
	}
	// Traffic is an Ahrefs setting.
	owner.must(400, "PUT", base+"/seo/"+id+"/settings", map[string]any{"link_traffic": true})
}
