package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

// Where competitors appear: tracked competitors are scanned on the
// brand's cadence (top ones weekly, the rest monthly, overridable), on the
// workspace's own connections, and each page is kept with its type, the
// site's numbers and "likely paid" only with evidence.
func TestPresenceCompetitors(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "handle": "acme"})["id"].(string)
	root := "/v1/brands/" + brand + "/coverage"
	t.Cleanup(func() { setDFSLinks() })

	var site, rival, small string
	if err := e.pool.QueryRow(ctx, `INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, 'northpeak.example', 'https://northpeak.example/', 'tok') RETURNING id`, ws, brand).Scan(&site); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, status, common_keywords) VALUES ($1, $2, $3, 'rival.example', 'Rival', 'tracked', 500) RETURNING id`, ws, site, brand).Scan(&rival); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, status, common_keywords) VALUES ($1, $2, $3, 'small.example', 'Small', 'tracked', 5) RETURNING id`, ws, site, brand).Scan(&small); err != nil {
		t.Fatal(err)
	}

	// With one top competitor, the first is weekly and the second monthly.
	owner.must(400, "PATCH", root+"/competitors/settings", map[string]any{"top_every_days": 0})
	set := owner.must(200, "PATCH", root+"/competitors/settings", map[string]any{"top_competitors": 1})
	if set["top_competitors"] != 1.0 || set["top_every_days"] != 7.0 || set["rest_every_days"] != 30.0 {
		t.Fatalf("settings: %v", set)
	}
	list := owner.must(200, "GET", root+"/competitors", nil)
	rows := list["data"].([]any)
	if len(rows) != 2 || rows[0].(map[string]any)["id"] != rival || rows[0].(map[string]any)["every_days"] != 7.0 || rows[0].(map[string]any)["top"] != true ||
		rows[1].(map[string]any)["every_days"] != 30.0 || rows[1].(map[string]any)["top"] != false {
		t.Fatalf("competitors: %v", list)
	}
	// One competitor can have its own cadence, and go back to the brand's.
	if got := owner.must(200, "PATCH", root+"/competitors/"+small, map[string]any{"every_days": 7}); got["every_days"] != 7.0 || got["own_every_days"] != 7.0 {
		t.Fatalf("own cadence: %v", got)
	}
	owner.must(400, "PATCH", root+"/competitors/"+small, map[string]any{"every_days": 0})
	if got := owner.must(200, "PATCH", root+"/competitors/"+small, map[string]any{"every_days": nil}); got["every_days"] != 30.0 || got["own_every_days"] != nil {
		t.Fatalf("brand cadence: %v", got)
	}
	owner.must(404, "PATCH", "/v1/brands/"+other+"/coverage/competitors/"+small, map[string]any{"every_days": 7})
	owner.must(404, "POST", "/v1/brands/"+other+"/coverage/competitors/"+rival+"/scan", nil)

	// An AI answer that names the rival cites a comparison page.
	var cohort, sample string
	if err := e.pool.QueryRow(ctx, `INSERT INTO ai_cohorts(workspace_id,brand_id,name) VALUES($1,$2,'Buyers') RETURNING id`, ws, brand).Scan(&cohort); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `WITH p AS (INSERT INTO ai_prompts(workspace_id,cohort_id,text) VALUES($1,$2,'best crm') RETURNING id),
		r AS (INSERT INTO ai_runs(workspace_id,cohort_id,trigger) VALUES($1,$2,'manual') RETURNING id)
		INSERT INTO ai_samples(workspace_id,run_id,cohort_id,prompt_id,brand_id,provider,model,status,answer) SELECT $1,r.id,$2,p.id,$3,'openai','test','ok','Rival is good' FROM p,r RETURNING id`, ws, cohort, brand).Scan(&sample); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO ai_mentions(sample_id,workspace_id,cohort_id,entity,name,position,count,sampled_at) VALUES($1,$2,$3,'competitor','Rival',1,1,now())`, sample, ws, cohort); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO ai_citations(sample_id,workspace_id,cohort_id,url,host,title,position,owner,sampled_at) VALUES
		($1,$2,$3,'https://reviews.example/rival-vs-other','reviews.example','Rival vs Other',1,'other',now())`, sample, ws, cohort); err != nil {
		t.Fatal(err)
	}

	// Without connections, the daily round scans both and says which
	// connection each source needs; AI citations need none.
	if _, err := e.srv.QueueDueCoverage(ctx); err != nil {
		t.Fatal(err)
	}
	var queued int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind='coverage.competitor_scan' AND workspace_id=$1`, ws).Scan(&queued); err != nil || queued != 2 {
		t.Fatalf("scans queued: %d, %v", queued, err)
	}
	if _, err := e.srv.QueueDueCoverage(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind='coverage.competitor_scan' AND workspace_id=$1`, ws).Scan(&queued); err != nil || queued != 2 {
		t.Fatalf("scans queued again before they're due: %d, %v", queued, err)
	}
	e.runServerAndMailJobs(t)
	wait := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); !ok(); {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	var scanned map[string]any
	wait("first scan", func() bool {
		scanned = owner.must(200, "GET", root+"/competitors", nil)["data"].([]any)[0].(map[string]any)
		return scanned["scanned_at"] != nil
	})
	outcome := scanned["outcome"].(map[string]any)
	if outcome["web_search"] != "connector_needed" || outcome["backlinks"] != "connector_needed" || outcome["ai_citations"] != "" {
		t.Fatalf("outcome without connections: %v", scanned)
	}
	found := owner.must(200, "GET", root+"/competitor_placements?competitor_id="+rival, nil)
	if found["total"] != 1.0 {
		t.Fatalf("AI citations: %v", found)
	}
	cited := found["data"].([]any)[0].(map[string]any)
	if cited["kind"] != "comparison" || cited["arrangement"] != "unknown" || cited["competitor_host"] != "rival.example" || cited["brand_present"] != false {
		t.Fatalf("cited page: %v", cited)
	}

	// With the workspace's own SEO connection, a scan reads who links to
	// the rival: an affiliate link looks paid, and the site's numbers stay.
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})
	ago := time.Now().UTC().AddDate(0, -2, 0)
	setDFSLinks(
		dfsLink("https://blog.example.org/tools", "https://rival.example/?via=blogger", "blog.example.org", 70, ago, nil),
		dfsLink("https://news.example.net/sponsored/rival-launch", "https://rival.example/", "news.example.net", 50, ago, nil),
		dfsLink("https://northpeak.example/blog", "https://rival.example/", "northpeak.example", 10, ago, nil),
	)
	owner.must(202, "POST", root+"/competitors/"+rival+"/scan", nil)
	wait("connected scan", func() bool {
		r := owner.must(200, "GET", root+"/competitors", nil)["data"].([]any)[0].(map[string]any)
		o, _ := r["outcome"].(map[string]any)
		return o["backlinks"] == ""
	})
	all := owner.must(200, "GET", root+"/competitor_placements?competitor_id="+rival, nil)
	if all["total"] != 3.0 {
		t.Fatalf("placements: %v", all)
	}
	byHost := map[string]map[string]any{}
	for _, p := range all["data"].([]any) {
		p := p.(map[string]any)
		byHost[p["host"].(string)] = p
	}
	blog, news := byHost["blog.example.org"], byHost["news.example.net"]
	if blog == nil || blog["arrangement"] != "likely_paid" || blog["evidence"].([]any)[0] != "link_affiliate" || blog["domain_rating"] != 70.0 || blog["links_to_them"] != true {
		t.Fatalf("affiliate link: %v", blog)
	}
	if news == nil || news["arrangement"] != "likely_paid" || news["evidence"].([]any)[0] != "address_sponsored" {
		t.Fatalf("sponsored page: %v", news)
	}
	if byHost["northpeak.example"] != nil {
		t.Fatalf("the brand's own site is not a competitor placement: %v", all)
	}
	if paid := owner.must(200, "GET", root+"/competitor_placements?arrangement=likely_paid", nil); paid["total"] != 2.0 {
		t.Fatalf("likely paid filter: %v", paid)
	}

	// Gaps are sites where the brand has no publisher yet.
	company := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Example blog", "brand_id": brand})
	owner.must(201, "POST", root+"/publishers", map[string]any{"record_id": company["id"], "domain": "www.blog.example.org"})
	gaps := owner.must(200, "GET", root+"/competitor_placements?gap=true", nil)
	if gaps["total"] != 2.0 {
		t.Fatalf("gaps: %v", gaps)
	}
	if here := owner.must(200, "GET", root+"/competitor_placements?gap=false", nil); here["total"] != 1.0 || here["data"].([]any)[0].(map[string]any)["brand_present"] != true {
		t.Fatalf("brand present: %v", here)
	}
	owner.must(400, "GET", root+"/competitor_placements?gap=maybe", nil)
	if got := owner.must(200, "GET", "/v1/brands/"+other+"/coverage/competitor_placements", nil); got["total"] != 0.0 {
		t.Fatalf("another brand sees them: %v", got)
	}

	for _, name := range []string{"coverage_competitor_settings.updated", "coverage_competitor.updated", "coverage_competitor.scan_queued", "coverage_competitor.scanned"} {
		if len(named(events(), name)) == 0 {
			t.Fatalf("no %s event", name)
		}
	}
}
