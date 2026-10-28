package api_test

import (
	"context"
	"testing"
)

// TestBrandLimitedPlacesSites: someone kept to one brand puts a site in
// their brand when it is theirs or nobody's yet, and never touches another
// brand's site or a shared one.
func TestBrandLimitedPlacesSites(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	geo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode", "website": "geonode.com"})["id"].(string)
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "website": "repocket.com"})
	ga := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "name": "Company Plausible", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})["id"].(string)
	ctx := context.Background()
	for site, host := range map[string]string{"s/geo": "geonode.com", "s/rp": "repocket.com", "s/new": "games.example", "s/shared": "blog.example"} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO analytics_syncs (integration_id, workspace_id, site, host, site_name) VALUES ($1, $2, $3, $4, $3)`, ga, ws, site, host); err != nil {
			t.Fatal(err)
		}
	}
	owner.must(200, "PUT", base+"/analytics/"+ga+"/site-brand", map[string]any{"site": "s/shared", "brand": "none"})

	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Geonode agent", "scopes": []string{"integrations.manage", "analytics.read"}, "brand": "geonode"})["token"].(string)
	agent := &client{t: t, h: e.h, bearer: tok}
	put := func(code int, site, brand string) map[string]any {
		t.Helper()
		return agent.must(code, "PUT", base+"/analytics/"+ga+"/site-brand", map[string]any{"site": site, "brand": brand})
	}
	// A site nobody has placed goes into their own brand.
	if sb := put(200, "s/new", "geonode"); sb["brand_id"] != geo {
		t.Fatalf("placed: %v", sb)
	}
	put(200, "s/geo", "geonode")
	put(403, "s/new", "repocket") // not their brand
	put(403, "s/new", "none")     // shared changes every brand
	put(403, "s/geo", "auto")
	put(403, "s/rp", "geonode")                                                                       // another brand's site
	put(403, "s/shared", "geonode")                                                                   // a shared site
	if n := len(agent.must(200, "GET", base+"/analytics/site-brands", nil)["data"].([]any)); n != 3 { // theirs and the shared one
		t.Fatalf("geonode sites: %d", n)
	}

	// A brand's own connection to another brand is out of reach.
	own := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "name": "Repocket Plausible", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}, "brand_id": "repocket"})["id"].(string)
	agent.must(404, "PUT", base+"/analytics/"+own+"/site-brand", map[string]any{"site": "x", "brand": "geonode"})
}
