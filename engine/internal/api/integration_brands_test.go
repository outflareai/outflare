package api_test

import (
	"context"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// TestIntegrationsSharedWithBrands: a workspace connection serves every
// brand or the ones it is shared with, and a brand's own connection to the
// same tool replaces it for that brand alone.
func TestIntegrationsSharedWithBrands(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	geo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "website": "northpeak.example"})["id"].(string)
	rp := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Summitly", "website": "summitly.example"})["id"].(string)
	games := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Sudokumax", "website": "sudokumax.com"})["id"].(string)

	plausible := func(name string, extra map[string]any) map[string]any {
		t.Helper()
		body := map[string]any{"connector": "plausible", "name": name, "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}}
		for k, v := range extra {
			body[k] = v
		}
		return owner.must(201, "POST", base+"/integrations", body)
	}
	names := func(q string) map[string]bool {
		t.Helper()
		out := map[string]bool{}
		for _, i := range owner.must(200, "GET", base+"/integrations"+q, nil)["data"].([]any) {
			out[i.(map[string]any)["name"].(string)] = true
		}
		return out
	}

	shared := plausible("Company Plausible", nil)
	if shared["brand_id"] != nil || len(shared["brand_ids"].([]any)) != 0 {
		t.Fatalf("workspace connection: %v", shared)
	}
	zb := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "zerobounce", "credentials": map[string]string{"api_key": "good-key"}, "brand_ids": []string{"northpeak", "summitly"}})
	if ids := zb["brand_ids"].([]any); len(ids) != 2 {
		t.Fatalf("shared with two brands: %v", zb)
	}
	own := plausible("Summitly Plausible", map[string]any{"brand_id": "summitly"})
	if own["brand_id"] != rp {
		t.Fatalf("brand's own: %v", own)
	}
	owner.must(400, "POST", base+"/integrations", map[string]any{"connector": "plausible", "name": "Both", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}, "brand_id": "northpeak", "brand_ids": []string{"summitly"}})
	owner.must(400, "POST", base+"/integrations", map[string]any{"connector": "plausible", "name": "Nobody", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}, "brand_id": "nope"})

	if n := names(""); len(n) != 3 {
		t.Fatalf("all: %v", n)
	}
	// Northpeak uses the company's Plausible and ZeroBounce.
	if n := names("?brand=northpeak"); len(n) != 2 || !n["Company Plausible"] {
		t.Fatalf("northpeak: %v", n)
	}
	// Summitly's own Plausible replaces the company's for Summitly.
	if n := names("?brand=summitly"); len(n) != 2 || !n["Summitly Plausible"] || n["Company Plausible"] {
		t.Fatalf("summitly: %v", n)
	}
	// ZeroBounce is not shared with the games brand.
	if n := names("?brand=" + games); len(n) != 1 || !n["Company Plausible"] {
		t.Fatalf("games: %v", n)
	}
	if n := names("?brand=none"); len(n) != 1 || !n["Company Plausible"] {
		t.Fatalf("none: %v", n)
	}
	if n := names("?brand=summitly&connector=plausible"); len(n) != 1 || !n["Summitly Plausible"] {
		t.Fatalf("summitly plausible: %v", n)
	}

	// Sharing with every brand again, and moving a connection to a brand.
	if p := owner.must(200, "PATCH", base+"/integrations/"+zb["id"].(string), map[string]any{"brand_ids": []string{}}); len(p["brand_ids"].([]any)) != 0 {
		t.Fatalf("shared with all: %v", p)
	}
	if n := names("?brand=" + games); len(n) != 2 {
		t.Fatalf("games after sharing: %v", n)
	}
	if p := owner.must(200, "PATCH", base+"/integrations/"+zb["id"].(string), map[string]any{"brand_id": "northpeak"}); p["brand_id"] != geo {
		t.Fatalf("moved: %v", p)
	}
	owner.must(400, "PATCH", base+"/integrations/"+zb["id"].(string), map[string]any{"brand_ids": []string{"summitly"}})
	if p := owner.must(200, "PATCH", base+"/integrations/"+zb["id"].(string), map[string]any{"brand_id": nil, "brand_ids": []string{"summitly"}}); p["brand_id"] != nil || len(p["brand_ids"].([]any)) != 1 {
		t.Fatalf("back to the workspace: %v", p)
	}
	if len(named(got(), "integration.updated")) != 3 {
		t.Fatal("no integration.updated event")
	}

	// A token kept to Northpeak sees only what Northpeak uses.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Northpeak agent", "scopes": []string{"integrations.read"}, "brand": "northpeak"})["token"].(string)
	agent := &client{t: t, h: e.h, bearer: tok}
	if list := agent.must(200, "GET", base+"/integrations", nil)["data"].([]any); len(list) != 1 || list[0].(map[string]any)["name"] != "Company Plausible" {
		t.Fatalf("northpeak token: %v", list)
	}
	agent.must(404, "GET", base+"/integrations/"+own["id"].(string), nil)
	agent.must(200, "GET", base+"/integrations/"+shared["id"].(string), nil)

	// A site read through Summitly's own connection reports for Summitly,
	// whatever its domain.
	ctx := context.Background()
	var b *string
	if err := e.pool.QueryRow(ctx, `SELECT analytics_site_brand($1, $2, 'x', 'northpeak.example')::text`, ws, own["id"]).Scan(&b); err != nil || b == nil || *b != rp {
		t.Fatalf("own site brand: %v %v", b, err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT analytics_site_brand($1, $2, 'x', 'northpeak.example')::text`, ws, shared["id"]).Scan(&b); err != nil || b == nil || *b != geo {
		t.Fatalf("shared site brand: %v %v", b, err)
	}
}
