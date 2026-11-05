package api_test

import (
	"context"
	"testing"
)

// Has it taken off? Each live placement gets a traction verdict from AI
// answers that cite it and visits it sends, and the report says whether
// placements are growing, and how fast, with share of voice against the
// tracked competitors.
func TestPresenceMomentum(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	root := "/v1/brands/" + brand + "/coverage"
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	company := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Example media", "brand_id": brand})
	publisher := owner.must(201, "POST", root+"/publishers", map[string]any{"record_id": company["id"], "domain": "media.example"})
	place := func(url string, liveDaysAgo int, link string) string {
		t.Helper()
		p := owner.must(201, "POST", root+"/placements", map[string]any{"publisher_id": publisher["id"], "title": url, "url": url, "kind": "news"})
		exec(`UPDATE coverage_placements SET health='up', first_verified_at=now()-make_interval(days=>$2), published_on=NULL, link_status=$3 WHERE id=$1`, p["id"], liveDaysAgo, link)
		return p["id"].(string)
	}
	cited := place("https://media.example/cited", 20, "present")
	visited := place("https://visits.example/post", 15, "missing")
	quiet := place("https://quiet.example/post", 40, "missing")
	fresh := place("https://fresh.example/post", 3, "present")

	// Three AI answers cite one page; one visit came from another site.
	var cohort string
	if err := e.pool.QueryRow(ctx, `INSERT INTO ai_cohorts(workspace_id,brand_id,name) VALUES($1,$2,'Buyers') RETURNING id`, ws, brand).Scan(&cohort); err != nil {
		t.Fatal(err)
	}
	var run string
	if err := e.pool.QueryRow(ctx, `INSERT INTO ai_runs(workspace_id,cohort_id,trigger) VALUES($1,$2,'manual') RETURNING id`, ws, cohort).Scan(&run); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		exec(`WITH p AS (INSERT INTO ai_prompts(workspace_id,cohort_id,text) VALUES($1,$2,'best tools '||$5::int) RETURNING id),
		s AS (INSERT INTO ai_samples(workspace_id,run_id,cohort_id,prompt_id,brand_id,provider,model,status,answer) SELECT $1,$4,$2,p.id,$3,'openai','test','ok','Northpeak' FROM p RETURNING id)
		INSERT INTO ai_citations(sample_id,workspace_id,cohort_id,url,host,title,position,owner,sampled_at) SELECT s.id,$1,$2,'https://media.example/cited/','media.example','Cited',1,'other',now() FROM s`, ws, cohort, brand, run, i)
	}
	exec(`INSERT INTO site_sessions(workspace_id,brand_id,anonymous_id,referrer,referrer_host) VALUES ($1,$2,'a1','https://www.visits.example/post','www.visits.example')`, ws, brand)

	if _, err := e.srv.RefreshPresenceTraction(ctx, ws, brand); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{cited: "taking_off", visited: "steady", quiet: "quiet", fresh: "too_early"}
	for id, verdict := range want {
		p := owner.must(200, "GET", root+"/placements/"+id, nil)
		if p["traction"] != verdict {
			t.Fatalf("%s traction %v, want %s", p["url"], p["traction"], verdict)
		}
	}
	if p := owner.must(200, "GET", root+"/placements/"+cited, nil); p["ai_citations_90d"] != 3.0 || p["traction_at"] == nil {
		t.Fatalf("cited page: %v", p)
	}

	// Three new this period against one the period before, each weighted
	// by its link and traction: accelerating.
	report := owner.must(200, "GET", root+"/reports", nil)
	m := report["momentum"].(map[string]any)
	if m["gained"] != 3.0 || m["previous_gained"] != 1.0 || m["lost"] != 0.0 || m["net"] != 3.0 || m["trend"] != "accelerating" || m["taking_off"] != 1.0 || len(m["weeks"].([]any)) != 12 {
		t.Fatalf("momentum: %v", m)
	}
	// cited 1+1+2, visited 1+1, fresh 1+1; the quiet one 1 the period before.
	if m["weighted_net"] != 8.0 || m["previous_weighted_net"] != 1.0 {
		t.Fatalf("weighted: %v", m)
	}

	// Share of voice: the brand's four live placements against a rival's one.
	var site, rival string
	if err := e.pool.QueryRow(ctx, `INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, 'northpeak.example', 'https://northpeak.example/', 'tok') RETURNING id`, ws, brand).Scan(&site); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, status) VALUES ($1, $2, $3, 'rival.example', 'Rival', 'tracked') RETURNING id`, ws, site, brand).Scan(&rival); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO coverage_competitor_placements (workspace_id,brand_id,competitor_id,url,host,sources) VALUES ($1,$2,$3,'https://reviews.example/a','reviews.example','{backlinks}')`, ws, brand, rival)
	sov := owner.must(200, "GET", root+"/reports", nil)["share_of_voice"].([]any)
	if len(sov) != 2 || sov[0].(map[string]any)["who"] != "brand" || sov[0].(map[string]any)["share"] != 80.0 || sov[1].(map[string]any)["name"] != "Rival" || sov[1].(map[string]any)["share"] != 20.0 {
		t.Fatalf("share of voice: %v", sov)
	}
}
