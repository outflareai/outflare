package api_test

import (
	"context"
	"testing"
)

// Found pages come grouped by site with what reading them showed, and
// "about us" or "not us" settles a page or a whole site in one call:
// "about us" makes placements under a publisher it adds with its company.
func TestPresenceFound(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	brand := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	root := "/v1/brands/" + brand + "/coverage"
	if _, err := e.pool.Exec(ctx, `INSERT INTO coverage_candidates(workspace_id,brand_id,url,title,source,source_id,verdict) VALUES
		($1,$2,'https://reviews.example/northpeak','Northpeak review','web_search','a','mentions'),
		($1,$2,'https://reviews.example/northpeak-pricing','Northpeak pricing','web_search','b','links'),
		($1,$2,'https://gis.example/docs','Northpeak GIS docs','web_search','c','name_only'),
		($1,$2,'https://gis.example/install','Install Northpeak GIS','web_search','d','name_only'),
		($1,$2,'https://blog.example/links','Links','backlinks','e','unchecked')`, ws, brand); err != nil {
		t.Fatal(err)
	}
	// A backlink needs no read: it links by definition.
	if err := e.srv.RunCoverageVerify(ctx, ws, brand); err != nil {
		t.Fatal(err)
	}
	about := owner.must(200, "GET", root+"/found", nil)
	sites := about["data"].([]any)
	if len(sites) != 2 || sites[0].(map[string]any)["host"] != "reviews.example" || sites[0].(map[string]any)["pages"] != 2.0 || sites[0].(map[string]any)["best"] != "links" {
		t.Fatalf("about: %v", about)
	}
	counts := about["counts"].(map[string]any)
	if counts["about"] != 3.0 || counts["maybe"] != 2.0 || counts["checking"] != 0.0 {
		t.Fatalf("counts: %v", counts)
	}
	// The other GeoNode-like site is not about the brand: dismissed as a site.
	if r := owner.must(200, "POST", root+"/found/review", map[string]any{"decision": "not_us", "host": "gis.example"}); r["reviewed"] != 2.0 {
		t.Fatalf("not us: %v", r)
	}
	// The review site is: both pages become live placements under one new publisher.
	if r := owner.must(200, "POST", root+"/found/review", map[string]any{"decision": "about_us", "host": "reviews.example"}); r["reviewed"] != 2.0 {
		t.Fatalf("about us: %v", r)
	}
	var placements, publishers, records int
	if err := e.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM coverage_placements WHERE workspace_id=$1 AND brand_id=$2 AND stage='live'),
		(SELECT count(*) FROM coverage_publishers WHERE workspace_id=$1 AND brand_id=$2 AND domain='reviews.example'),
		(SELECT count(*) FROM coverage_publishers p JOIN records r ON r.id=p.record_id WHERE p.workspace_id=$1 AND p.brand_id=$2)`, ws, brand).Scan(&placements, &publishers, &records); err != nil {
		t.Fatal(err)
	}
	if placements != 2 || publishers != 1 || records != 1 {
		t.Fatalf("placements %d publishers %d records %d", placements, publishers, records)
	}
	counts = owner.must(200, "GET", root+"/found?view=not", nil)["counts"].(map[string]any)
	if counts["about"] != 1.0 || counts["not"] != 2.0 || counts["confirmed"] != 2.0 || counts["maybe"] != 0.0 {
		t.Fatalf("after review: %v", counts)
	}
	owner.must(400, "POST", root+"/found/review", map[string]any{"decision": "maybe", "host": "blog.example"})
	owner.must(400, "POST", root+"/found/review", map[string]any{"decision": "not_us"})
}
