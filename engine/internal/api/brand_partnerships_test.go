package api_test

import (
	"context"
	"testing"
)

// A partnerships kit sets up a pipeline for the brand in the path, not the
// workspace's first brand; adding a prospect by website finds or adds its
// company and puts the partnership at the first stage, once per partner.
func TestPartnershipPipelines(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	first := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	second := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Acme", "handle": "acme", "website": "https://acme.example"})["id"].(string)
	root := "/v1/brands/" + second

	plan := owner.must(200, "GET", root+"/kits/partnerships-seo/preview", nil)
	if plan["changes"].(float64) == 0 || plan["conflicts"] != 0.0 {
		t.Fatalf("preview: %v", plan)
	}
	if applied := owner.must(200, "POST", root+"/kits/partnerships-seo/apply", nil); applied["applied"] != true {
		t.Fatalf("apply: %v", applied)
	}
	// Applying again changes nothing.
	if again := owner.must(200, "POST", root+"/kits/partnerships-seo/apply", nil); again["changes"] != 0.0 {
		t.Fatalf("apply again: %v", again)
	}
	var inSecond, inFirst int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE brand_id=$2), count(*) FILTER (WHERE brand_id=$3) FROM record_types WHERE workspace_id=$1 AND key='link_partnership'`, ws, second, first).Scan(&inSecond, &inFirst); err != nil {
		t.Fatal(err)
	}
	if inSecond != 1 || inFirst != 0 {
		t.Fatalf("link_partnership in second %d, first %d", inSecond, inFirst)
	}
	// A kit with rules or journeys is only for the workspace route.
	owner.must(400, "POST", root+"/kits/trial-activation/apply", nil)

	// Add a prospect by website: company found or added, first stage.
	add := owner.must(201, "POST", root+"/partnerships", map[string]any{
		"type": "link_partnership", "website": "https://www.trailgear.example/blog",
		"attributes": map[string]any{"link_type": "guest_post", "domain_rating": 54},
	})
	rec := add["record"].(map[string]any)
	attrs := rec["attributes"].(map[string]any)
	if add["created"] != true || rec["name"] != "trailgear.example" || attrs["stage"] != "prospect" || attrs["link_type"] != "guest_post" || attrs["company"] == nil {
		t.Fatalf("add: %v", add)
	}
	var companyDomain string
	if err := e.pool.QueryRow(ctx, `SELECT i.value FROM record_identities i WHERE i.record_id=$1 AND i.namespace='domain'`, attrs["company"]).Scan(&companyDomain); err != nil || companyDomain != "trailgear.example" {
		t.Fatalf("company: %q %v", companyDomain, err)
	}
	// Its values are in the record's history.
	var changes int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM attribute_changes WHERE record_id=$1`, rec["id"]).Scan(&changes); err != nil || changes < 3 {
		t.Fatalf("history: %d %v", changes, err)
	}
	// The same partner again comes back as it is.
	again := owner.must(200, "POST", root+"/partnerships", map[string]any{"type": "link_partnership", "website": "trailgear.example", "stage": "pitched"})
	if again["created"] != false || again["record"].(map[string]any)["id"] != rec["id"] {
		t.Fatalf("again: %v", again)
	}
	// A chosen stage, and the errors.
	if s := owner.must(201, "POST", root+"/partnerships", map[string]any{"type": "link_partnership", "website": "gearlab.example", "stage": "pitched"}); s["record"].(map[string]any)["attributes"].(map[string]any)["stage"] != "pitched" {
		t.Fatalf("stage: %v", s)
	}
	owner.must(400, "POST", root+"/partnerships", map[string]any{"type": "link_partnership", "website": "gearlab.example", "stage": "nope"})
	owner.must(400, "POST", root+"/partnerships", map[string]any{"type": "link_partnership", "website": ""})
	owner.must(400, "POST", root+"/partnerships", map[string]any{"type": "sponsorship", "website": "news.example"})
	owner.must(400, "POST", root+"/partnerships", map[string]any{"type": "person", "website": "news.example"})
	owner.must(400, "POST", root+"/partnerships", map[string]any{"type": "link_partnership", "website": "x.example", "attributes": map[string]any{"stage": "live"}})
	// The first brand has no such pipeline.
	owner.must(400, "POST", "/v1/brands/"+first+"/partnerships", map[string]any{"type": "link_partnership", "website": "news.example"})
}

// Reaching the stage where a partnership shows up makes its Presence
// placement (and a sponsorship's agreement) once, however often the job runs.
func TestPartnershipStageActions(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	brand := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	root := "/v1/brands/" + brand
	for _, k := range []string{"partnerships-seo", "partnerships-sponsorships", "partnerships-comparison-sites"} {
		owner.must(200, "POST", root+"/kits/"+k+"/apply", nil)
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	move := func(id string, attrs map[string]any) {
		t.Helper()
		rec := owner.must(200, "GET", root+"/records/"+id, nil)
		owner.must(200, "PATCH", root+"/records/"+id, map[string]any{"version": rec["version"], "attributes": attrs})
		if count(`SELECT count(*) FROM jobs WHERE kind = 'partnerships.sync' AND payload->>'record_id' = $1`, id) == 0 {
			t.Fatalf("no sync queued for %s", id)
		}
		if err := e.srv.RunPartnershipSync(ctx, ws, id); err != nil {
			t.Fatal(err)
		}
	}
	placements := func() int {
		return count(`SELECT count(*) FROM coverage_placements WHERE workspace_id = $1 AND brand_id = $2`, ws, brand)
	}

	// SEO: live without their page adds nothing; with it, one placement, checked.
	seo := owner.must(201, "POST", root+"/partnerships", map[string]any{"type": "link_partnership", "website": "trailgear.example"})["record"].(map[string]any)["id"].(string)
	move(seo, map[string]any{"stage": "live"})
	if placements() != 0 {
		t.Fatal("placement without a page")
	}
	move(seo, map[string]any{"their_page": "https://trailgear.example/best-packs", "target_page": "https://northpeak.example/packs", "link_type": "guest_post"})
	if placements() != 1 || count(`SELECT count(*) FROM coverage_placements WHERE url = 'https://trailgear.example/best-packs' AND kind = 'blog' AND arrangement = 'earned' AND stage = 'live'`) != 1 {
		t.Fatal("seo placement")
	}
	if count(`SELECT count(*) FROM jobs WHERE kind = 'coverage.check'`) == 0 {
		t.Fatal("placement not checked")
	}
	// Moving it back and forth adds nothing twice.
	move(seo, map[string]any{"stage": "agreed"})
	move(seo, map[string]any{"stage": "live"})
	if err := e.srv.RunPartnershipSync(ctx, ws, seo); err != nil || placements() != 1 {
		t.Fatalf("again: %d %v", placements(), err)
	}

	// Comparison site: listed with its page.
	cmp := owner.must(201, "POST", root+"/partnerships", map[string]any{"type": "comparison_listing", "website": "proxyreviews.example",
		"attributes": map[string]any{"comparison_url": "https://proxyreviews.example/best-proxies", "listing_type": "affiliate"}})["record"].(map[string]any)["id"].(string)
	move(cmp, map[string]any{"stage": "listed"})
	if count(`SELECT count(*) FROM coverage_placements WHERE kind = 'comparison' AND arrangement = 'affiliate'`) != 1 {
		t.Fatal("comparison placement")
	}

	// Sponsorship: booked records a proposed agreement; ran a placement under it.
	sp := owner.must(201, "POST", root+"/partnerships", map[string]any{"type": "sponsorship", "website": "outdoorweekly.example",
		"attributes": map[string]any{"channel": "newsletter", "price": map[string]any{"amount_minor": 150000, "currency": "USD"}, "run_on": "2026-12-01"}})["record"].(map[string]any)["id"].(string)
	move(sp, map[string]any{"stage": "booked"})
	if count(`SELECT count(*) FROM coverage_agreements WHERE workspace_id = $1 AND status = 'proposed' AND amount_minor = 150000 AND starts_on = '2026-12-01'`, ws) != 1 {
		t.Fatal("agreement")
	}
	move(sp, map[string]any{"stage": "ran"})
	move(sp, map[string]any{"stage": "reported"})
	if count(`SELECT count(*) FROM coverage_agreements WHERE workspace_id = $1`, ws) != 1 ||
		count(`SELECT count(*) FROM coverage_placements p JOIN coverage_agreements g ON g.id = p.agreement_id WHERE p.kind = 'newsletter' AND p.arrangement = 'paid' AND p.published_on = '2026-12-01'`) != 1 {
		t.Fatal("sponsorship placement")
	}
	if count(`SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'partnership.placement_added'`, ws) != 3 ||
		count(`SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'partnership.agreement_added'`, ws) != 1 {
		t.Fatal("audit")
	}

	// The daily sweep queues a partnership at an action stage whose
	// action hasn't happened (here: an SEO partner live with no page yet).
	late := owner.must(201, "POST", root+"/partnerships", map[string]any{"type": "link_partnership", "website": "late.example", "stage": "live"})["record"].(map[string]any)["id"].(string)
	if err := e.srv.QueuePartnershipSyncs(ctx); err != nil {
		t.Fatal(err)
	}
	if count(`SELECT count(*) FROM jobs WHERE kind = 'partnerships.sync' AND payload->>'record_id' = $1 AND idempotency_key LIKE '%:day:%'`, late) != 1 {
		t.Fatal("sweep")
	}
}

// An affiliate joins the partner program outside the pipeline (the product
// adds them through the API): their partnership follows, matched by its
// company's domain, to signed up, then active at their first referred sale,
// with the referred customers and revenue on it. Nothing is sent.
func TestAffiliatePartnershipFollowsProgram(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	root := "/v1/brands/" + brand
	owner.must(200, "POST", root+"/kits/partnerships-affiliates/apply", nil)
	aff := owner.must(201, "POST", root+"/partnerships", map[string]any{"type": "affiliate_partnership", "website": "gearblog.example", "stage": "interested"})["record"].(map[string]any)["id"].(string)
	other := owner.must(201, "POST", root+"/partnerships", map[string]any{"type": "affiliate_partnership", "website": "elsewhere.example", "stage": "contacted"})["record"].(map[string]any)["id"].(string)
	attrs := func(id string) map[string]any {
		return owner.must(200, "GET", root+"/records/"+id, nil)["attributes"].(map[string]any)
	}

	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate", "brand": brand})
	ana := owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "ana@gearblog.example"})
	partner := ana["id"].(string)
	var queued int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'partnerships.affiliate' AND payload->>'partner_id' = $1`, partner).Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("queued %d %v", queued, err)
	}
	if err := e.srv.RunAffiliateSync(ctx, ws, partner); err != nil {
		t.Fatal(err)
	}
	a := attrs(aff)
	if a["stage"] != "signed_up" || a["contact"] == nil || a["referred_customers"] != 0.0 {
		t.Fatalf("joined: %v", a)
	}
	if attrs(other)["stage"] != "contacted" {
		t.Fatal("another partnership moved")
	}

	owner.must(201, "POST", root+"/partner-conversions", map[string]any{"type": "sale", "ref": ana["code"], "email": "bo@buyer.example", "external_id": "ord-1", "amount_cents": 4900})
	if err := e.srv.RunAffiliateSync(ctx, ws, partner); err != nil {
		t.Fatal(err)
	}
	a = attrs(aff)
	rev, _ := a["referred_revenue"].(map[string]any)
	if a["stage"] != "active" || a["referred_customers"] != 1.0 || rev["amount_minor"] != 4900.0 {
		t.Fatalf("sale: %v", a)
	}
	// Each move is in the partnership's history.
	var changes int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM attribute_changes WHERE record_id = $1 AND field_key = 'stage'`, aff).Scan(&changes); err != nil || changes != 3 {
		t.Fatalf("stage history %d %v", changes, err)
	}
}

// A prospect's domain rating comes from what the workspace's SEO tool
// already read (backlinks to the brand, competitor scans), as enrichment;
// one given when adding it wins.
func TestPartnershipDomainRating(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	brand := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	root := "/v1/brands/" + brand
	owner.must(200, "POST", root+"/kits/partnerships-seo/apply", nil)
	var site string
	if err := e.pool.QueryRow(ctx, `INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, 'northpeak.example', 'https://northpeak.example/', 'tok') RETURNING id`, ws, brand).Scan(&site); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO backlinks (workspace_id, brand_id, site_id, source_url, target_url, source_domain, source_rating) VALUES
		($1, $2, $3, 'https://www.rated.example/post', 'https://northpeak.example/', 'www.rated.example', 62.6)`, ws, brand, site); err != nil {
		t.Fatal(err)
	}
	rec := owner.must(201, "POST", root+"/partnerships", map[string]any{"type": "link_partnership", "website": "rated.example"})["record"].(map[string]any)
	if rec["attributes"].(map[string]any)["domain_rating"] != 63.0 || rec["attribute_sources"].(map[string]any)["domain_rating"] != "enrichment" {
		t.Fatalf("rating: %v", rec)
	}
	given := owner.must(201, "POST", root+"/partnerships", map[string]any{"type": "link_partnership", "website": "unrated.example", "attributes": map[string]any{"domain_rating": 12}})["record"].(map[string]any)
	if given["attributes"].(map[string]any)["domain_rating"] != 12.0 {
		t.Fatalf("given: %v", given)
	}
	none := owner.must(201, "POST", root+"/partnerships", map[string]any{"type": "link_partnership", "website": "nothing.example"})["record"].(map[string]any)
	if _, ok := none["attributes"].(map[string]any)["domain_rating"]; ok {
		t.Fatalf("none: %v", none)
	}
}
