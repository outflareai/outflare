package api_test

import (
	"testing"
)

// Brands own their data (plans/brands/README.md): every workspace starts
// with a brand, and /v1/brands/{brand}/ routes let in exactly who may see
// that brand.
func TestBrandRoutes(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws

	// Sign-up made the first brand, named after the workspace.
	list := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("a new workspace has one brand: %v", list)
	}
	first := list[0].(map[string]any)
	if first["name"] != "Acme Outbound" || first["handle"] != "acme-outbound" {
		t.Fatalf("first brand: %v", first)
	}
	acme := first["id"].(string)
	rp := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "website": "repocket.com"})["id"].(string)

	// The owner reads either brand by its id alone.
	if b := owner.must(200, "GET", "/v1/brands/"+rp, nil); b["handle"] != "repocket" {
		t.Fatalf("brand: %v", b)
	}
	owner.must(200, "GET", "/v1/brands/"+acme, nil)
	owner.must(404, "GET", "/v1/brands/nope", nil)
	owner.must(404, "GET", "/v1/brands/00000000-0000-0000-0000-000000000000", nil)

	// A token made for one brand sees only that one; the other is 404.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Repocket app", "scopes": []string{"workspace.read"}, "brand": "repocket"})
	app := &client{t: t, h: h, bearer: tok["token"].(string)}
	app.must(200, "GET", "/v1/brands/"+rp, nil)
	app.must(404, "GET", "/v1/brands/"+acme, nil)

	// A token without the scope is refused for its own brand.
	bare := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Leads only", "scopes": []string{"leads.read"}, "brand": "repocket"})
	(&client{t: t, h: h, bearer: bare["token"].(string)}).must(403, "GET", "/v1/brands/"+rp, nil)

	// Another workspace's token can't see it at all, even with every scope
	// there; its own workspace got its own first brand.
	np := owner.must(201, "POST", "/v1/workspaces", map[string]string{"name": "Northpeak"})["id"].(string)
	if l := owner.must(200, "GET", "/v1/workspaces/"+np+"/brands", nil)["data"].([]any); len(l) != 1 || l[0].(map[string]any)["handle"] != "northpeak" {
		t.Fatalf("second workspace's brands: %v", l)
	}
	ot := owner.must(201, "POST", "/v1/workspaces/"+np+"/api-tokens", map[string]any{"name": "Northpeak", "scopes": []string{"workspace.read"}})
	(&client{t: t, h: h, bearer: ot["token"].(string)}).must(404, "GET", "/v1/brands/"+rp, nil)
}
