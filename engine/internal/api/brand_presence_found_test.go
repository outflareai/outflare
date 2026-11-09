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
	// Each site carries its numbers; the table sorts and filters on them,
	// and a tracked competitor's own site can be hidden.
	var site, rival string
	if err := e.pool.QueryRow(ctx, `INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, 'northpeak.example', 'https://northpeak.example/', 'tok') RETURNING id`, ws, brand).Scan(&site); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, status) VALUES ($1, $2, $3, 'blog.example', 'Blog', 'tracked') RETURNING id`, ws, site, brand).Scan(&rival); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO coverage_competitor_placements (workspace_id,brand_id,competitor_id,url,host,title,kind,domain_rating,domain_traffic,sources) VALUES
		($1,$2,$3,'https://blog.example/x','blog.example','X','blog',70,9000,'{backlinks}')`, ws, brand, rival); err != nil {
		t.Fatal(err)
	}
	byRating := owner.must(200, "GET", root+"/found?sort=domain_rating", nil)["data"].([]any)
	if top := byRating[0].(map[string]any); top["host"] != "blog.example" || top["domain_rating"] != 70.0 || top["competitors"] != 1.0 || top["competitor_site"] != true {
		t.Fatalf("by rating: %v", top)
	}
	if strong := owner.must(200, "GET", root+"/found?min_domain_rating=50", nil); strong["sites_total"] != 1.0 {
		t.Fatalf("min rating: %v", strong)
	}
	if hidden := owner.must(200, "GET", root+"/found?exclude_competitors=true", nil); hidden["sites_total"] != 1.0 || hidden["data"].([]any)[0].(map[string]any)["host"] != "reviews.example" {
		t.Fatalf("exclude competitors: %v", hidden)
	}
	if backlinks := owner.must(200, "GET", root+"/found?source=backlinks", nil); backlinks["sites_total"] != 1.0 || backlinks["pages_total"] != 1.0 {
		t.Fatalf("source: %v", backlinks)
	}
	owner.must(400, "GET", root+"/found?sort=nope", nil)

	// A site whose links to the brand the SEO tool scores as spam carries
	// the score and why, as a hint; nothing is hidden by it.
	if _, err := e.pool.Exec(ctx, `INSERT INTO backlinks (workspace_id, brand_id, site_id, source_url, target_url, source_domain, spam_score, spam_factors)
		VALUES ($1, $2, $3, 'https://reviews.example/northpeak', 'https://northpeak.example/', 'reviews.example', 72, '[{"code":"provider_spam","points":72},{"code":"spam_tld","points":15}]')`,
		ws, brand, site); err != nil {
		t.Fatal(err)
	}
	var spammy map[string]any
	for _, x := range owner.must(200, "GET", root+"/found", nil)["data"].([]any) {
		if x.(map[string]any)["host"] == "reviews.example" {
			spammy = x.(map[string]any)
		}
	}
	if spammy == nil || spammy["spam_score"] != 72.0 || len(spammy["spam_reasons"].([]any)) != 2 {
		t.Fatalf("spam: %v", spammy)
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

// Spam is a site's: marking one page marks every page of its site, they
// leave the review list, and a page found on that site later arrives as
// spam. "Check again" hides a page for a week, then it is read again and
// comes back with what that read found.
func TestPresenceFoundSpamAndRecheck(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	brand := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	root := "/v1/brands/" + brand + "/coverage"
	var spamPage, offline string
	if err := e.pool.QueryRow(ctx, `INSERT INTO coverage_candidates(workspace_id,brand_id,url,title,source,source_id,verdict) VALUES
		($1,$2,'https://spammy.example/a','Northpeak deals','web_search','a','links'),
		($1,$2,'https://spammy.example/b','Northpeak coupons','web_search','b','mentions'),
		($1,$2,'https://reviews.example/northpeak','Northpeak review','web_search','c','links') RETURNING id::text`, ws, brand).Scan(&spamPage); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT id::text FROM coverage_candidates WHERE workspace_id=$1 AND url='https://spammy.example/a'`, ws).Scan(&spamPage); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO coverage_candidates(workspace_id,brand_id,url,title,source,source_id,verdict,checked_at) VALUES
		($1,$2,'https://down.example/post','Northpeak post','web_search','d','unreachable',now()) RETURNING id::text`, ws, brand).Scan(&offline); err != nil {
		t.Fatal(err)
	}

	// One page marked spam takes its whole site with it.
	if r := owner.must(200, "POST", root+"/found/review", map[string]any{"decision": "spam", "ids": []string{spamPage}}); r["reviewed"] != 2.0 {
		t.Fatalf("spam: %v", r)
	}
	not := owner.must(200, "GET", root+"/found?view=not", nil)
	if c := not["counts"].(map[string]any); c["about"] != 1.0 || c["not"] != 2.0 {
		t.Fatalf("after spam: %v", c)
	}
	if item := not["data"].([]any)[0].(map[string]any)["items"].([]any)[0].(map[string]any); item["status"] != "spam" {
		t.Fatalf("spam item: %v", item)
	}

	// A later find on the spam site arrives as spam.
	var site string
	if err := e.pool.QueryRow(ctx, `INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, 'northpeak.example', 'https://northpeak.example/', 'tok') RETURNING id`, ws, brand).Scan(&site); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO backlinks(workspace_id,brand_id,site_id,source_url,target_url,source_domain) VALUES
		($1,$2,$3,'https://spammy.example/c','https://northpeak.example','spammy.example'),
		($1,$2,$3,'https://fresh.example/c','https://northpeak.example','fresh.example')`, ws, brand, site); err != nil {
		t.Fatal(err)
	}
	if r := owner.must(200, "POST", root+"/discovery/reconcile", nil); r["added"] != 1.0 {
		t.Fatalf("reconcile: %v", r)
	}
	var status string
	if err := e.pool.QueryRow(ctx, `SELECT status FROM coverage_candidates WHERE workspace_id=$1 AND url='https://spammy.example/c'`, ws).Scan(&status); err != nil || status != "spam" {
		t.Fatalf("new find on a spam site: %q %v", status, err)
	}

	// An offline page is checked again in a week: hidden until then.
	if r := owner.must(200, "POST", root+"/found/review", map[string]any{"decision": "recheck", "ids": []string{offline}}); r["reviewed"] != 1.0 {
		t.Fatalf("recheck: %v", r)
	}
	later := owner.must(200, "GET", root+"/found?view=later", nil)
	// The new backlink on fresh.example is the only page left to read.
	if c := later["counts"].(map[string]any); c["later"] != 1.0 || c["maybe"] != 1.0 || c["checking"] != 1.0 {
		t.Fatalf("after recheck: %v", c)
	}
	if item := later["data"].([]any)[0].(map[string]any)["items"].([]any)[0].(map[string]any); item["recheck_at"] == nil || item["verdict"] != "unchecked" {
		t.Fatalf("later item: %v", item)
	}
	// Not read before it is due.
	if err := e.srv.RunCoverageVerify(ctx, ws, brand); err != nil {
		t.Fatal(err)
	}
	var verdict string
	if err := e.pool.QueryRow(ctx, `SELECT verdict FROM coverage_candidates WHERE id=$1`, offline).Scan(&verdict); err != nil || verdict != "unchecked" {
		t.Fatalf("read early: %q %v", verdict, err)
	}
	// Once due, it is read again and leaves the later view.
	if _, err := e.pool.Exec(ctx, `UPDATE coverage_candidates SET recheck_at=now()-interval '1 minute' WHERE id=$1`, offline); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.RunCoverageVerify(ctx, ws, brand); err != nil {
		t.Fatal(err)
	}
	var recheck *string
	if err := e.pool.QueryRow(ctx, `SELECT verdict, recheck_at::text FROM coverage_candidates WHERE id=$1`, offline).Scan(&verdict, &recheck); err != nil || verdict == "unchecked" || recheck != nil {
		t.Fatalf("after due read: %q %v %v", verdict, recheck, err)
	}
	if c := owner.must(200, "GET", root+"/found", nil)["counts"].(map[string]any); c["later"] != 0.0 {
		t.Fatalf("later after read: %v", c)
	}
	owner.must(400, "GET", root+"/found?view=soon", nil)
}
