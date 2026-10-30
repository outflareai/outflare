package api_test

import "testing"

// TestPartnerPortalLook: the portal's logo and favicon are https images,
// and the portal page gets them with its title and colour.
func TestPartnerPortalLook(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	id := owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate"})["id"].(string)
	portalURL := base + "/partner-programs/" + id + "/portal"
	owner.must(400, "PUT", portalURL, map[string]any{"settings": map[string]any{"favicon_url": "http://northpeak.example/favicon.png"}})
	owner.must(400, "PUT", portalURL, map[string]any{"settings": map[string]any{"favicon_url": "javascript:alert(1)"}})
	saved := owner.must(200, "PUT", portalURL, map[string]any{"enabled": true, "settings": map[string]any{"title": "Northpeak Partners", "color": "#0f766e",
		"logo_url": "https://northpeak.example/logo.svg", "favicon_url": "https://northpeak.example/favicon.png"}})
	if saved["portal"].(map[string]any)["favicon_url"] != "https://northpeak.example/favicon.png" {
		t.Fatalf("portal = %v", saved["portal"])
	}
	portal := &portalClient{t: t, h: e.h, base: "https://outflare.test/p/" + id + "/"}
	info := portal.must(200, "GET", "api/program", nil)
	if info["favicon_url"] != "https://northpeak.example/favicon.png" || info["logo_url"] != "https://northpeak.example/logo.svg" ||
		info["title"] != "Northpeak Partners" || info["color"] != "#0f766e" {
		t.Fatalf("info = %v", info)
	}
}
