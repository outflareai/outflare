package api_test

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestLinkOpportunities(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	t.Cleanup(func() {
		setDFSLinks()
		setDFSTarget("rival.example", nil, nil)
		setDFSTarget("other.example", nil, nil)
	})

	id := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})["id"].(string)
	owner.must(200, "PUT", base+"/seo/"+id+"/settings", map[string]any{"domains": []string{"example.com"}})
	siteID := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.example.com/"})["id"].(string)
	owner.must(201, "POST", base+"/seo-competitors", map[string]any{"site_id": siteID, "host": "rival.example"})
	owner.must(201, "POST", base+"/seo-competitors", map[string]any{"site_id": siteID, "host": "other.example"})

	// The latest complete crawl found /old-guide gone.
	var run string
	if err := e.pool.QueryRow(ctx, `INSERT INTO crawl_runs (workspace_id, site_id, host, trigger, max_pages, delay_ms, obey_robots, status, finished_at)
		VALUES ($1, $2, 'www.example.com', 'manual', 100, 0, true, 'done', now()) RETURNING id::text`, ws, siteID).Scan(&run); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO crawl_pages (workspace_id, run_id, url, depth, source, status, http_status)
		VALUES ($1, $2, 'https://www.example.com/old-guide', 1, 'link', 'fetched', 404), ($1, $2, 'https://www.example.com/', 0, 'start', 'fetched', 200)`, ws, run); err != nil {
		t.Fatal(err)
	}

	old, lost := time.Now().AddDate(-1, 0, 0), time.Now().AddDate(0, 0, -10)
	farm := dfsLink("https://links.example.xyz/1", "https://www.example.com/", "links.example.xyz", 3, old, nil)
	farm["anchor"], farm["page_from_external_links"] = "cheap casino bonus", 900
	setDFSLinks(
		dfsLink("https://news.example.org/a", "https://www.example.com/", "news.example.org", 70, old, nil),
		dfsLink("https://mag.example.org/review", "https://www.example.com/", "mag.example.org", 60, old, &lost),
		dfsLink("https://weak.example.net/x", "https://www.example.com/", "weak.example.net", 5, old, &lost),
		dfsLink("https://wiki.example.org/tools", "https://www.example.com/old-guide", "wiki.example.org", 50, old, nil),
		farm,
	)
	setDFSTarget("rival.example", nil, []any{map[string]any{"domain": "blog.example.net", "rank": 50}, map[string]any{"domain": "news.example.org", "rank": 70}})
	setDFSTarget("other.example", nil, []any{map[string]any{"domain": "blog.example.net", "rank": 50}, map[string]any{"domain": "tiny.example.net", "rank": 10}})
	owner.must(200, "POST", base+"/seo/"+id+"/sync", nil)

	list := owner.must(200, "GET", base+"/fixes?source=seo_links", nil)["data"].([]any)
	got := map[string]map[string]any{}
	for _, f := range list {
		f := f.(map[string]any)
		got[f["rule"].(map[string]any)["id"].(string)+" "+f["group"].(string)] = f
	}
	if len(got) != 4 {
		t.Fatalf("fixes: %v", list)
	}
	for key, count := range map[string]float64{
		"link_reclaim mag.example.org":                         1, // the weak one is not worth winning back
		"link_broken_target https://www.example.com/old-guide": 1,
		"link_gap_domain blog.example.net":                     2, // news.example.org already links to us
		"toxic_links ":                                         1,
	} {
		f := got[key]
		if f == nil || f["count"].(float64) != count || f["status"] != "open" || f["source"] != "seo_links" || f["subject"].(map[string]any)["id"] != siteID {
			t.Fatalf("%s: %v", key, f)
		}
	}
	if r := got["link_reclaim mag.example.org"]["rule"].(map[string]any); r["title"] != "Strong link lost" || r["category"] != "links" {
		t.Fatalf("rule: %v", r)
	}

	// The prompt lists the links and keeps outreach with the person.
	p := owner.must(200, "GET", base+"/fixes/"+got["link_reclaim mag.example.org"]["id"].(string)+"/prompt", nil)
	if prompt := p["prompt"].(string); !strings.Contains(prompt, "https://mag.example.org/review") || !strings.Contains(prompt, "Never contact a site") {
		t.Fatalf("prompt: %s", prompt)
	}
	p = owner.must(200, "GET", base+"/fixes/"+got["link_broken_target https://www.example.com/old-guide"]["id"].(string)+"/prompt", nil)
	if prompt := p["prompt"].(string); !strings.Contains(prompt, "https://wiki.example.org/tools") || !strings.Contains(prompt, "permanent redirect") {
		t.Fatalf("broken prompt: %s", prompt)
	}
	owner.must(409, "POST", base+"/fixes/"+got["toxic_links "]["id"].(string)+"/verify", nil)

	// Reviewing the toxic link confirms that fix straight away.
	farmID := owner.must(200, "GET", base+"/backlinks?spam=toxic", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	owner.must(200, "PATCH", base+"/backlinks/"+farmID, map[string]any{"review": "disavow"})
	toxic := owner.must(200, "GET", base+"/fixes/"+got["toxic_links "]["id"].(string), nil)
	if toxic["status"] != "done" || toxic["confirmed_by"] == nil {
		t.Fatalf("toxic after review: %v", toxic)
	}

	// Marked done by hand, a gap the next read still finds opens again; a
	// lost link that came back is confirmed.
	gap := got["link_gap_domain blog.example.net"]["id"].(string)
	owner.must(200, "PATCH", base+"/fixes/"+gap, map[string]any{"status": "done"})
	setDFSLinks(
		dfsLink("https://news.example.org/a", "https://www.example.com/", "news.example.org", 70, old, nil),
		dfsLink("https://mag.example.org/review", "https://www.example.com/", "mag.example.org", 60, old, nil),
		dfsLink("https://wiki.example.org/tools", "https://www.example.com/old-guide", "wiki.example.org", 50, old, nil),
		farm,
	)
	owner.must(200, "POST", base+"/seo/"+id+"/sync", nil)
	if f := owner.must(200, "GET", base+"/fixes/"+gap, nil); f["status"] != "open" {
		t.Fatalf("gap: %v", f)
	}
	if f := owner.must(200, "GET", base+"/fixes/"+got["link_reclaim mag.example.org"]["id"].(string), nil); f["status"] != "done" || f["confirmed_by"] == nil {
		t.Fatalf("reclaimed: %v", f)
	}

	// Another workspace sees none of it.
	ob := "/v1/workspaces/" + owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Other"})["id"].(string)
	if l := owner.must(200, "GET", ob+"/fixes?source=seo_links", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("other workspace: %v", l)
	}
	owner.must(404, "GET", ob+"/fixes/"+gap, nil)
}
