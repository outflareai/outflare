package api_test

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
)

// Hosted and embedded portals use the built React entry. Custom portal hosts
// must reach its shared assets without exposing the dashboard's HTML or API.
func TestPartnerPortalDesignSystem(t *testing.T) {
	web := t.TempDir()
	if err := os.Mkdir(filepath.Join(web, "assets"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"portal.html":      `<!doctype html><html data-portal-base="{{BASE}}"><script type="module" src="/assets/portal.js"></script><div id="root"></div></html>`,
		"index.html":       "dashboard-only-entry",
		"assets/portal.js": "export const portal = true;",
	} {
		if err := os.WriteFile(filepath.Join(web, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	e := freshEnvWith(t, func(o *api.Options) { o.WebDir = web; o.PartnersDomain = "partners.test" })
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	p := owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Northpeak", "kind": "affiliate"})
	id := p["id"].(string)
	owner.must(200, "PUT", base+"/partner-programs/"+id+"/portal", map[string]any{"enabled": true, "subdomain": "northpeak"})
	get := func(url string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRecorder()
		e.h.ServeHTTP(r, httptest.NewRequest("GET", url, nil))
		return r
	}
	for _, path := range []string{"/p/" + id + "/", "/p/" + id + "/embed?view=portal"} {
		r := get(path)
		if r.Code != 200 || !strings.Contains(r.Body.String(), `data-portal-base="/p/`+id+`/"`) || !strings.Contains(r.Body.String(), "/assets/portal.js") {
			t.Fatalf("compiled portal %s: %d %s", path, r.Code, r.Body.String())
		}
		if r.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("compiled portal %s can be cached past a deploy: %q", path, r.Header().Get("Cache-Control"))
		}
		if strings.Contains(path, "embed") && r.Header().Get("X-Frame-Options") != "" {
			t.Fatal("full portal embed cannot be framed")
		}
	}
	root := get("https://northpeak.partners.test/")
	if root.Code != 200 || !strings.Contains(root.Body.String(), `data-portal-base="/"`) {
		t.Fatalf("custom portal root: %d %s", root.Code, root.Body.String())
	}
	asset := get("https://northpeak.partners.test/assets/portal.js")
	if asset.Code != 200 || asset.Body.String() != "export const portal = true;" || asset.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("custom portal asset: %d %s %v", asset.Code, asset.Body.String(), asset.Header())
	}
	// FileServer canonicalizes index.html to the same asset directory.
	redirected := get("https://northpeak.partners.test/assets/index.html")
	if redirected.Code != 301 || redirected.Header().Get("Location") != "./" {
		t.Fatalf("asset index escaped its directory: %d %v", redirected.Code, redirected.Header())
	}
	for _, path := range []string{"assets/missing.js", "v1/setup"} {
		r := get("https://northpeak.partners.test/" + path)
		if r.Code != 404 || strings.Contains(r.Body.String(), "dashboard-only-entry") || strings.Contains(r.Header().Get("Cache-Control"), "immutable") {
			t.Fatalf("portal host exposed app fallback/API on %s: %d %s", path, r.Code, r.Body.String())
		}
	}
}
