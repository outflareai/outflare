package api_test

import (
	"context"
	"testing"
	"time"
)

func dfsKeyword(keyword, url string, volume, position float64) map[string]any {
	return map[string]any{"keyword_data": map[string]any{"keyword": keyword, "keyword_info": map[string]any{"search_volume": volume}},
		"ranked_serp_element": map[string]any{"serp_item": map[string]any{"url": url, "rank_group": position, "etv": 10}}}
}

func TestCompetitorGaps(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
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

	// We rank 4th for "cold email" and 1st for "outflare" (the fake's
	// default keywords) and have a link from news.example.org.
	setDFSLinks(dfsLink("https://news.example.org/a", "https://www.example.com/", "news.example.org", 70, time.Now().AddDate(-1, 0, 0), nil))
	setDFSTarget("rival.example", []any{
		dfsKeyword("cold email", "https://rival.example/cold", 900, 1),
		dfsKeyword("outflare", "https://rival.example/", 50, 2),
		dfsKeyword("email warmup", "https://rival.example/warmup", 700, 5),
		dfsKeyword("deep keyword", "https://rival.example/deep", 5000, 40),
	}, []any{
		map[string]any{"domain": "news.example.org", "rank": 70},
		map[string]any{"domain": "blog.example.net", "rank": 50},
		map[string]any{"domain": "www.dir.example.org", "rank": 20},
	})
	setDFSTarget("other.example", []any{dfsKeyword("email warmup", "https://other.example/w", 700, 8)},
		[]any{map[string]any{"domain": "blog.example.net", "rank": 50}, map[string]any{"domain": "shop.example.com", "rank": 30}})
	owner.must(200, "POST", base+"/seo/"+id+"/sync", nil)

	kw := owner.must(200, "GET", base+"/seo-competitors/gaps?site_id="+siteID, nil)
	data := kw["data"].([]any)
	if kw["kind"] != "keywords" || len(kw["competitors"].([]any)) != 2 || kw["total"].(float64) != 2 || len(data) != 2 {
		t.Fatalf("keywords: %v", kw)
	}
	first, second := data[0].(map[string]any), data[1].(map[string]any)
	if first["keyword"] != "email warmup" || first["kind"] != "missing" || len(first["competitors"].([]any)) != 2 || first["best_position"].(float64) != 5 || first["you"] != nil {
		t.Fatalf("missing: %v", first)
	}
	if second["keyword"] != "cold email" || second["kind"] != "behind" || second["you"].(map[string]any)["position"].(float64) != 4 {
		t.Fatalf("behind: %v", second)
	}

	links := owner.must(200, "GET", base+"/seo-competitors/gaps?site_id="+siteID+"&kind=links", nil)
	ld := links["data"].([]any)
	if links["total"].(float64) != 2 || ld[0].(map[string]any)["domain"] != "blog.example.net" || len(ld[0].(map[string]any)["competitors"].([]any)) != 2 ||
		ld[1].(map[string]any)["domain"] != "dir.example.org" {
		t.Fatalf("links: %v", links)
	}
	owner.must(400, "GET", base+"/seo-competitors/gaps", nil)
	owner.must(400, "GET", base+"/seo-competitors/gaps?site_id="+siteID+"&kind=pages", nil)
	owner.must(404, "GET", base+"/seo-competitors/gaps?site_id=00000000-0000-4000-8000-000000000000", nil)

	// The first read is the baseline: nothing is new yet.
	ctx := context.Background()
	newLinks := func() []map[string]any {
		t.Helper()
		rows, err := e.pool.Query(ctx, `SELECT data FROM audit_events WHERE workspace_id = $1 AND action = 'competitor_link.new' ORDER BY id`, ws)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []map[string]any
		for rows.Next() {
			var d map[string]any
			if err := rows.Scan(&d); err != nil {
				t.Fatal(err)
			}
			out = append(out, d)
		}
		return out
	}
	if n := newLinks(); len(n) != 0 {
		t.Fatalf("new links on the first read: %v", n)
	}
	// A week later the rival has two more linking domains; one of them
	// already links to us, so only the other is news.
	if _, err := e.pool.Exec(ctx, `UPDATE seo_snapshots SET day = day - 7 WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	setDFSTarget("rival.example", []any{dfsKeyword("cold email", "https://rival.example/cold", 900, 1)}, []any{
		map[string]any{"domain": "news.example.org", "rank": 70},
		map[string]any{"domain": "blog.example.net", "rank": 50},
		map[string]any{"domain": "fresh.example.net", "rank": 40},
		map[string]any{"domain": "example.com", "rank": 90},
	})
	owner.must(200, "POST", base+"/seo/"+id+"/sync", nil)
	n := newLinks()
	if len(n) != 1 || n[0]["source_domain"] != "fresh.example.net" || n[0]["competitor"] != "rival.example" || n[0]["site_id"] != siteID ||
		n[0]["competitors"].(float64) != 1 || n[0]["connector"] != "dataforseo" {
		t.Fatalf("competitor_link.new: %v", n)
	}

	// Off, the snapshot reads no gaps.
	if set := owner.must(200, "PUT", base+"/seo/"+id+"/settings", map[string]any{"gap_rows": 0}); set["gap_rows"].(float64) != 0 {
		t.Fatalf("gap rows: %v", set)
	}
}
