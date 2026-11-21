package api_test

import (
	"context"
	"testing"
)

// TestBrandLimitedPlacesSites: someone kept to one brand puts a site in
// their brand when it is theirs or nobody has placed it yet (it counts for
// the first brand meanwhile), and never touches another brand's site.
// There is no shared site.
func TestBrandLimitedPlacesSites(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	geo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "website": "northpeak.example"})["id"].(string)
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Summitly", "website": "summitly.example"})
	ga := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "name": "Company Plausible", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})["id"].(string)
	ctx := context.Background()
	for site, host := range map[string]string{"s/geo": "northpeak.example", "s/rp": "summitly.example", "s/new": "games.example", "s/shared": "blog.example"} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO analytics_syncs (integration_id, workspace_id, site, host, site_name) VALUES ($1, $2, $3, $4, $3)`, ga, ws, site, host); err != nil {
			t.Fatal(err)
		}
	}
	owner.must(400, "PUT", base+"/analytics/"+ga+"/site-brand", map[string]any{"site": "s/shared", "brand": "none"})
	// Summitly's site is Summitly's by its domain; assigning it makes that
	// a pick.
	owner.must(200, "PUT", base+"/analytics/"+ga+"/site-brand", map[string]any{"site": "s/rp", "brand": "summitly"})

	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Northpeak agent", "scopes": []string{"integrations.manage", "analytics.read"}, "brand": "northpeak"})["token"].(string)
	agent := &client{t: t, h: e.h, bearer: tok}
	put := func(code int, site, brand string) map[string]any {
		t.Helper()
		return agent.must(code, "PUT", base+"/analytics/"+ga+"/site-brand", map[string]any{"site": site, "brand": brand})
	}
	// A site nobody has placed goes into their own brand.
	if sb := put(200, "s/new", "northpeak"); sb["brand_id"] != geo {
		t.Fatalf("placed: %v", sb)
	}
	put(200, "s/geo", "northpeak")
	put(403, "s/new", "summitly") // not their brand
	put(400, "s/new", "none")     // no site is shared
	put(403, "s/geo", "auto")
	put(403, "s/rp", "northpeak")                                                                     // another brand's site
	if n := len(agent.must(200, "GET", base+"/analytics/site-brands", nil)["data"].([]any)); n != 2 { // theirs only: the others are Summitly's or the first brand's
		t.Fatalf("northpeak sites: %d", n)
	}
	// A site that counts for the first brand only by default is still
	// nobody's pick: someone kept to another brand may place it.
	rtok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Summitly agent", "scopes": []string{"integrations.manage", "analytics.read"}, "brand": "summitly"})["token"].(string)
	ragent := &client{t: t, h: e.h, bearer: rtok}
	if sb := ragent.must(200, "PUT", base+"/analytics/"+ga+"/site-brand", map[string]any{"site": "s/shared", "brand": "summitly"}); sb["source"] != "set" {
		t.Fatalf("placed by default: %v", sb)
	}
	ragent.must(403, "PUT", base+"/analytics/"+ga+"/site-brand", map[string]any{"site": "s/new", "brand": "summitly"}) // Northpeak placed it

	// A brand's own connection to another brand is out of reach.
	own := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "name": "Summitly Plausible", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}, "brand_id": "summitly"})["id"].(string)
	agent.must(404, "PUT", base+"/analytics/"+own+"/site-brand", map[string]any{"site": "x", "brand": "northpeak"})
}
