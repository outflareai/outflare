package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

// Presence discovery runs every day by itself: it brings in finds from the
// brand's social watches and from the pages AI answers cite when they name
// the brand, sets each find's type from its address, adds a web search for
// the brand's name, and runs it on the workspace's own connection.
func TestPresenceDailyDiscovery(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "handle": "acme"})["id"].(string)
	root := "/v1/brands/" + brand + "/coverage"

	// A Reddit thread the brand's social watch found.
	var watch string
	if err := e.pool.QueryRow(ctx, `INSERT INTO social_watches(workspace_id,brand_id,channel,name,keywords,disclosure) VALUES($1,$2,'reddit','Mentions','{northpeak}','I work at Northpeak.') RETURNING id`, ws, brand).Scan(&watch); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO social_items(workspace_id,brand_id,channel,watch_id,external_id,title,url,posted_at) VALUES($1,$2,'reddit',$3,'t3_1','Anyone used Northpeak?','https://www.reddit.com/r/example/comments/1/thread/',now())`, ws, brand, watch); err != nil {
		t.Fatal(err)
	}
	// An AI answer that names the brand and cites a YouTube review.
	var cohort, sample string
	if err := e.pool.QueryRow(ctx, `INSERT INTO ai_cohorts(workspace_id,brand_id,name) VALUES($1,$2,'Buyers') RETURNING id`, ws, brand).Scan(&cohort); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `WITH p AS (INSERT INTO ai_prompts(workspace_id,cohort_id,text) VALUES($1,$2,'best crm') RETURNING id),
		r AS (INSERT INTO ai_runs(workspace_id,cohort_id,trigger) VALUES($1,$2,'manual') RETURNING id)
		INSERT INTO ai_samples(workspace_id,run_id,cohort_id,prompt_id,brand_id,provider,model,status,answer) SELECT $1,r.id,$2,p.id,$3,'openai','test','ok','Northpeak is good' FROM p,r RETURNING id`, ws, cohort, brand).Scan(&sample); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO ai_mentions(sample_id,workspace_id,cohort_id,entity,name,position,count,sampled_at) VALUES($1,$2,$3,'brand','Northpeak',1,1,now())`, sample, ws, cohort); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO ai_citations(sample_id,workspace_id,cohort_id,url,host,title,position,owner,sampled_at) VALUES
		($1,$2,$3,'https://www.youtube.com/watch?v=abc','youtube.com','Northpeak review',1,'other',now()),
		($1,$2,$3,'https://northpeak.example/pricing','northpeak.example','Pricing',2,'brand',now())`, sample, ws, cohort); err != nil {
		t.Fatal(err)
	}

	// The daily round queues discovery for every active brand once a day.
	if _, err := e.srv.QueueDueCoverage(ctx); err != nil {
		t.Fatal(err)
	}
	var queued, brands int
	if err := e.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM jobs WHERE kind='coverage.discover' AND workspace_id=$1),(SELECT count(*) FROM brands WHERE workspace_id=$1)`, ws).Scan(&queued, &brands); err != nil || queued != brands {
		t.Fatalf("discovery queued %d for %d brands, %v", queued, brands, err)
	}
	if _, err := e.srv.QueueDueCoverage(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind='coverage.discover' AND workspace_id=$1`, ws).Scan(&queued); err != nil || queued != brands {
		t.Fatalf("discovery queued twice in a day: %d, %v", queued, err)
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
	wait("finds", func() bool { return owner.must(200, "GET", root+"/candidates", nil)["total"] == 2.0 })
	kinds := map[string]string{}
	for _, c := range owner.must(200, "GET", root+"/candidates", nil)["data"].([]any) {
		c := c.(map[string]any)
		kinds[c["source"].(string)] = c["kind"].(string)
	}
	if kinds["social"] != "forum" || kinds["ai_citations"] != "youtube" {
		t.Fatalf("finds and their types: %v", kinds)
	}
	if got := owner.must(200, "GET", "/v1/brands/"+other+"/coverage/candidates", nil); got["total"] != 0.0 {
		t.Fatalf("another brand's finds: %v", got)
	}

	// The first run added a search for the brand's name. Without a search
	// results connection it waits and says so.
	var search map[string]any
	wait("seeded search", func() bool {
		l := owner.must(200, "GET", root+"/searches", nil)
		if l["total"] != 1.0 {
			return false
		}
		search = l["data"].([]any)[0].(map[string]any)
		return true
	})
	if search["query"] != `"Northpeak"` || search["every_days"] != 1.0 || search["enabled"] != true {
		t.Fatalf("seeded search: %v", search)
	}
	id := search["id"].(string)
	owner.must(202, "POST", root+"/searches/"+id+"/run", nil)
	wait("connector_needed", func() bool {
		return owner.must(200, "GET", root+"/searches/"+id, nil)["last_error"] == "connector_needed"
	})

	// With the workspace's own connection it runs and adds results that
	// aren't the brand's own pages.
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})
	if _, err := e.pool.Exec(ctx, `UPDATE coverage_searches SET next_run_at=now() WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.QueueDueCoverage(ctx); err != nil {
		t.Fatal(err)
	}
	var ran map[string]any
	wait("search run", func() bool {
		ran = owner.must(200, "GET", root+"/searches/"+id, nil)
		return ran["last_run_at"] != nil && ran["last_error"] == ""
	})
	var web int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM coverage_candidates WHERE workspace_id=$1 AND brand_id=$2 AND source='web_search'`, ws, brand).Scan(&web); err != nil {
		t.Fatal(err)
	}
	if web == 0 || ran["last_found"] != float64(web) {
		t.Fatalf("web search finds: %d, %v", web, ran)
	}
	runs := 0
	for _, ev := range named(events(), "coverage_search.ran") {
		if ev.Props["brand_id"] == brand {
			runs++
		}
	}
	if runs != 1 {
		n := map[string]int{}
		for _, ev := range events() {
			n[ev.Name]++
		}
		t.Fatalf("a search run records coverage_search.ran: %v", n)
	}

	// Searches are the brand's own, and checked.
	owner.must(400, "POST", root+"/searches", map[string]any{"query": "x", "every_days": 0})
	owner.must(400, "POST", root+"/searches", map[string]any{"query": "x", "depth": 10.5})
	owner.must(404, "GET", "/v1/brands/"+other+"/coverage/searches/"+id, nil)
	owner.must(404, "POST", "/v1/brands/"+other+"/coverage/searches/"+id+"/run", nil)

	// Confirming a find makes a placement of its type.
	company := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Example forum", "brand_id": brand})
	publisher := owner.must(201, "POST", root+"/publishers", map[string]any{"record_id": company["id"], "domain": "reddit.com"})
	var forum string
	if err := e.pool.QueryRow(ctx, `SELECT id FROM coverage_candidates WHERE workspace_id=$1 AND brand_id=$2 AND source='social'`, ws, brand).Scan(&forum); err != nil {
		t.Fatal(err)
	}
	placed := owner.must(200, "POST", root+"/candidates/"+forum+"/confirm", map[string]any{"publisher_id": publisher["id"]})
	if placed["kind"] != "forum" {
		t.Fatalf("confirmed placement: %v", placed)
	}
}
