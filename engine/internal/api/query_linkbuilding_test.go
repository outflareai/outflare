package api_test

import (
	"context"
	"testing"
)

// Link building in Query: backlinks and link prospects, for callers with
// seo.read.
func TestQueryLinkBuilding(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	siteID := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.example.com/"})["id"].(string)
	owner.must(200, "POST", base+"/link-prospects", map[string]any{"site_id": siteID, "prospects": []any{
		map[string]any{"page_url": "https://blog.example.org/tools", "method": "broken_link"},
		map[string]any{"page_url": "https://list.example.net/resources", "method": "resource_page"},
	}})
	if _, err := e.pool.Exec(ctx, `INSERT INTO backlinks (workspace_id, site_id, source_url, target_url, source_domain, source_rating, status, brand_id)
		VALUES ($1, $2, 'https://linked.example/a', 'https://example.com/', 'linked.example', 70, 'live', outflare_first_brand($1::uuid)),
		       ($1, $2, 'https://gone.example/b', 'https://example.com/', 'gone.example', 20, 'lost', outflare_first_brand($1::uuid))`, ws, siteID); err != nil {
		t.Fatal(err)
	}

	out := owner.must(200, "POST", base+"/query?brand=all", map[string]any{"sql": "select method, stage from link_prospects order by method"})
	if rows := queryRows(t, out); len(rows) != 2 || rows[0][0] != "broken_link" || rows[0][1] != "prospect" || rows[1][0] != "resource_page" {
		t.Fatalf("link prospects: %v", out)
	}
	out = owner.must(200, "POST", base+"/query?brand=all", map[string]any{"sql": "select status, count(*), max(source_rating) from backlinks group by 1 order by 1"})
	if rows := queryRows(t, out); len(rows) != 2 || rows[0][0] != "live" || rows[0][2] != 70.0 || rows[1][0] != "lost" {
		t.Fatalf("backlinks: %v", out)
	}

	// Without seo.read the tables aren't readable.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "CRM agent", "scopes": []string{"customers.read"}})
	agent := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	tables := agent.must(200, "GET", base+"/query/tables", nil)["data"].([]any)
	for _, tb := range tables {
		m := tb.(map[string]any)
		if (m["name"] == "link_prospects" || m["name"] == "backlinks") && m["available"] != false {
			t.Fatalf("%s available without seo.read: %v", m["name"], m)
		}
	}
	if st, _ := agent.do("POST", base+"/query", map[string]any{"sql": "select count(*) from link_prospects"}); st == 200 {
		out := agent.must(200, "POST", base+"/query", map[string]any{"sql": "select count(*) from link_prospects"})
		if rows := queryRows(t, out); rows[0][0] != 0.0 {
			t.Fatalf("link prospects without seo.read: %v", out)
		}
	}
}
