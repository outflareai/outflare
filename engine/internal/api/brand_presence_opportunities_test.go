package api_test

import (
	"context"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// Opportunities are ranked by leverage (SEO and AI answers together):
// pages competitors are on where the brand isn't, mentions without a
// link, and removed links. Getting there marks them won.
func TestPresenceOpportunities(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "handle": "acme"})["id"].(string)
	root := "/v1/brands/" + brand + "/coverage"
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}

	var site, rival, small string
	if err := e.pool.QueryRow(ctx, `INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, 'northpeak.example', 'https://northpeak.example/', 'tok') RETURNING id`, ws, brand).Scan(&site); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		host string
		id   *string
	}{{"rival.example", &rival}, {"small.example", &small}} {
		if err := e.pool.QueryRow(ctx, `INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, status) VALUES ($1, $2, $3, $4, $4, 'tracked') RETURNING id`, ws, site, brand, c.host).Scan(c.id); err != nil {
			t.Fatal(err)
		}
	}
	// Both competitors are on a strong comparison page; one is on a weak blog.
	exec(`INSERT INTO coverage_competitor_placements (workspace_id,brand_id,competitor_id,url,host,title,kind,domain_rating,domain_traffic,sources) VALUES
		($1,$2,$3,'https://reviews.example/best-tools','reviews.example','Best tools','comparison',80,100000,'{backlinks}'),
		($1,$2,$4,'https://reviews.example/best-tools','reviews.example','Best tools','comparison',80,100000,'{backlinks}'),
		($1,$2,$3,'https://tiny.example/post','tiny.example','A post','blog',5,10,'{web_search}')`, ws, brand, rival, small)
	// AI answers cite the comparison site.
	var cohort, sample string
	if err := e.pool.QueryRow(ctx, `INSERT INTO ai_cohorts(workspace_id,brand_id,name) VALUES($1,$2,'Buyers') RETURNING id`, ws, brand).Scan(&cohort); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `WITH p AS (INSERT INTO ai_prompts(workspace_id,cohort_id,text) VALUES($1,$2,'best tools') RETURNING id),
		r AS (INSERT INTO ai_runs(workspace_id,cohort_id,trigger) VALUES($1,$2,'manual') RETURNING id)
		INSERT INTO ai_samples(workspace_id,run_id,cohort_id,prompt_id,brand_id,provider,model,status,answer) SELECT $1,r.id,$2,p.id,$3,'openai','test','ok','Rival' FROM p,r RETURNING id`, ws, cohort, brand).Scan(&sample); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO ai_citations(sample_id,workspace_id,cohort_id,url,host,title,position,owner,sampled_at) VALUES ($1,$2,$3,'https://reviews.example/best-tools','reviews.example','Best tools',1,'other',now())`, sample, ws, cohort)
	// A placement that names the brand without linking to it.
	company := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Example news", "brand_id": brand})
	publisher := owner.must(201, "POST", root+"/publishers", map[string]any{"record_id": company["id"], "domain": "news.example"})
	placement := owner.must(201, "POST", root+"/placements", map[string]any{"publisher_id": publisher["id"], "title": "Northpeak raises", "url": "https://news.example/northpeak", "kind": "news"})
	exec(`UPDATE coverage_placements SET mention_status='present', link_status='missing' WHERE id=$1`, placement["id"])

	got := owner.must(200, "POST", root+"/opportunities/refresh", nil)
	if got["added"] != 3.0 || got["won"] != 0.0 {
		t.Fatalf("refresh: %v", got)
	}
	list := owner.must(200, "GET", root+"/opportunities", nil)
	rows := list["data"].([]any)
	if list["total"] != 3.0 {
		t.Fatalf("opportunities: %v", list)
	}
	top := rows[0].(map[string]any)
	if top["url"] != "https://reviews.example/best-tools" || top["reason"] != "competitor_gap" || top["action"] != "pitch" || top["score"].(float64) < 60 {
		t.Fatalf("top opportunity: %v", top)
	}
	factors := top["factors"].(map[string]any)
	if factors["competitors"] != 2.0 || factors["ai_citations"] != 1.0 || factors["domain_rating"] != 80.0 {
		t.Fatalf("factors: %v", factors)
	}
	mention := owner.must(200, "GET", root+"/opportunities?action=request_link", nil)
	if mention["total"] != 1.0 || mention["data"].([]any)[0].(map[string]any)["placement_id"] != placement["id"] || mention["data"].([]any)[0].(map[string]any)["publisher_id"] != publisher["id"] {
		t.Fatalf("unlinked mention: %v", mention)
	}
	owner.must(400, "GET", root+"/opportunities?reason=nope", nil)
	owner.must(400, "GET", root+"/opportunities?min_score=101", nil)
	if strong := owner.must(200, "GET", root+"/opportunities?min_score=60", nil); strong["total"] != 1.0 {
		t.Fatalf("min_score: %v", strong)
	}

	// Picking one sets its owner; dismissing keeps it dismissed on refresh.
	id := top["id"].(string)
	var me string
	if err := e.pool.QueryRow(ctx, `SELECT user_id::text FROM memberships WHERE workspace_id=$1 AND role='owner' LIMIT 1`, ws).Scan(&me); err != nil {
		t.Fatal(err)
	}
	picked := owner.must(200, "PATCH", root+"/opportunities/"+id, map[string]any{"state": "picked", "owner_id": me})
	if picked["state"] != "picked" || picked["owner_id"] != me {
		t.Fatalf("picked: %v", picked)
	}
	owner.must(400, "PATCH", root+"/opportunities/"+id, map[string]any{"state": "maybe"})
	owner.must(404, "PATCH", "/v1/brands/"+other+"/coverage/opportunities/"+id, map[string]any{"state": "open"})
	var tiny string
	for _, r := range rows {
		if r.(map[string]any)["host"] == "tiny.example" {
			tiny = r.(map[string]any)["id"].(string)
		}
	}
	owner.must(200, "PATCH", root+"/opportunities/"+tiny, map[string]any{"state": "dismissed"})

	// The brand gets onto the comparison site and the news page links:
	// both are won, and the dismissed one stays dismissed.
	reviews := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Example reviews", "brand_id": brand})
	owner.must(201, "POST", root+"/publishers", map[string]any{"record_id": reviews["id"], "domain": "www.reviews.example"})
	exec(`UPDATE coverage_placements SET link_status='present' WHERE id=$1`, placement["id"])
	if got = owner.must(200, "POST", root+"/opportunities/refresh", nil); got["won"] != 2.0 || got["added"] != 0.0 {
		t.Fatalf("second refresh: %v", got)
	}
	states := map[string]string{}
	for _, r := range owner.must(200, "GET", root+"/opportunities?state=all", nil)["data"].([]any) {
		r := r.(map[string]any)
		states[r["host"].(string)] = r["state"].(string)
	}
	if states["reviews.example"] != "won" || states["news.example"] != "won" || states["tiny.example"] != "dismissed" {
		t.Fatalf("states: %v", states)
	}
	// Two pages on one site and one on a competitor's own site: grouped by
	// domain they are two rows, and competitors' sites can be hidden.
	exec(`INSERT INTO coverage_competitor_placements (workspace_id,brand_id,competitor_id,url,host,title,kind,domain_rating,domain_traffic,sources) VALUES
		($1,$2,$3,'https://blog.example/a','blog.example','A','blog',40,1000,'{backlinks}'),
		($1,$2,$3,'https://blog.example/b','blog.example','B','blog',40,1000,'{backlinks}'),
		($1,$2,$4,'https://rival.example/partners','rival.example','Partners','blog',60,5000,'{backlinks}')`, ws, brand, rival, small)
	if got = owner.must(200, "POST", root+"/opportunities/refresh", nil); got["added"] != 3.0 {
		t.Fatalf("third refresh: %v", got)
	}
	if all := owner.must(200, "GET", root+"/opportunities", nil); all["total"] != 3.0 {
		t.Fatalf("open: %v", all)
	}
	grouped := owner.must(200, "GET", root+"/opportunities?group=domain&sort=domain_rating", nil)
	if grouped["total"] != 2.0 {
		t.Fatalf("grouped: %v", grouped)
	}
	first := grouped["data"].([]any)[0].(map[string]any)
	if first["host"] != "rival.example" || first["competitor_site"] != true || first["site_opportunities"] != 1.0 {
		t.Fatalf("grouped by rating: %v", first)
	}
	if blog := grouped["data"].([]any)[1].(map[string]any); blog["host"] != "blog.example" || blog["site_opportunities"] != 2.0 || blog["competitor_site"] != false {
		t.Fatalf("grouped site: %v", blog)
	}
	if hidden := owner.must(200, "GET", root+"/opportunities?exclude_competitors=true&group=domain", nil); hidden["total"] != 1.0 {
		t.Fatalf("exclude competitors: %v", hidden)
	}
	if one := owner.must(200, "GET", root+"/opportunities?host=www.blog.example", nil); one["total"] != 2.0 {
		t.Fatalf("host: %v", one)
	}
	if strong := owner.must(200, "GET", root+"/opportunities?min_domain_rating=50", nil); strong["total"] != 1.0 {
		t.Fatalf("min_domain_rating: %v", strong)
	}
	if page := owner.must(200, "GET", root+"/opportunities?limit=2", nil); page["next_cursor"] == nil {
		t.Fatalf("no next page: %v", page)
	}
	// A translation of a page is the English page: competitors on /fr/ and
	// on the English page make one opportunity, on the English page.
	exec(`INSERT INTO placement_opportunities (workspace_id,brand_id,url,host,title,kind,reason,action,source,evidence,factors,score)
		VALUES ($1,$2,'https://lists.example/fr/proxies','lists.example','Proxys','blog','competitor_gap','pitch','presence','{}','{}',10)`, ws, brand)
	exec(`INSERT INTO coverage_competitor_placements (workspace_id,brand_id,competitor_id,url,host,title,kind,domain_rating,domain_traffic,sources) VALUES
		($1,$2,$3,'https://lists.example/fr/proxies','lists.example','Proxys','blog',30,100,'{backlinks}'),
		($1,$2,$4,'https://lists.example/proxies','lists.example','Proxies','blog',30,100,'{backlinks}'),
		($1,$2,$4,'https://lists.example/de/proxies','lists.example','Proxys','blog',30,100,'{backlinks}')`, ws, brand, rival, small)
	owner.must(200, "POST", root+"/opportunities/refresh", nil)
	lists := owner.must(200, "GET", root+"/opportunities?host=lists.example", nil)
	if lists["total"] != 1.0 {
		t.Fatalf("translations: %v", lists)
	}
	if l := lists["data"].([]any)[0].(map[string]any); l["url"] != "https://lists.example/proxies" || l["title"] != "Proxies" || l["factors"].(map[string]any)["competitors"] == nil ||
		len(l["evidence"].(map[string]any)["competitor_ids"].([]any)) != 2 || l["evidence"].(map[string]any)["translations"] != true {
		t.Fatalf("english page: %v", l)
	}
	owner.must(400, "GET", root+"/opportunities?access=nope", nil)
	owner.must(200, "GET", root+"/opportunities?access=earned", nil)
	owner.must(400, "GET", root+"/opportunities?sort=nope", nil)
	owner.must(400, "GET", root+"/opportunities?group=site", nil)
	owner.must(400, "GET", root+"/opportunities?exclude_competitors=maybe", nil)

	if got := owner.must(200, "GET", "/v1/brands/"+other+"/coverage/opportunities?state=all", nil); got["total"] != 0.0 {
		t.Fatalf("another brand sees them: %v", got)
	}
	for _, name := range []string{"coverage_opportunities.refreshed", "coverage_opportunity.updated"} {
		if len(named(events(), name)) == 0 {
			t.Fatalf("no %s event", name)
		}
	}
}
