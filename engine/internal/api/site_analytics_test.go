package api_test

import (
	"testing"
)

// A brand with no analytics tool still has numbers: its own tag's visits,
// day by day, with pages, sources, websites, countries and devices.
func TestSiteAnalytics(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := brandOf(t, e, `SELECT outflare_first_brand($1)::text`, ws)
	bbase := "/v1/brands/" + brand

	empty := owner.must(200, "GET", bbase+"/analytics/tracking", nil)
	if empty["totals"].(map[string]any)["sessions"] != 0.0 || len(empty["series"].([]any)) != 28 {
		t.Fatalf("empty: %v", empty)
	}

	key := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "Site", "origins": []string{"acme.example"}})["key"].(string)
	page := func(origin, anon, path string, ctx map[string]any) {
		t.Helper()
		pg := map[string]any{"path": path, "url": origin + path, "title": "Acme"}
		c := map[string]any{"page": pg}
		for k, v := range ctx {
			c[k] = v
		}
		code, out := sendSite(t, e.h, origin, key, map[string]any{"type": "page", "anonymousId": anon, "context": c})
		if code != 200 || out["accepted"] != 1.0 {
			t.Fatalf("page: %d %v", code, out)
		}
		e.pump(t)
	}
	page("https://acme.example", "sa-1", "/", nil)
	page("https://acme.example", "sa-1", "/pricing", nil)
	page("https://app.acme.example", "sa-2", "/login", nil)

	got := owner.must(200, "GET", bbase+"/analytics/tracking", nil)
	tot := got["totals"].(map[string]any)
	if tot["visitors"] != 2.0 || tot["sessions"] != 2.0 || tot["pageviews"] != 3.0 || tot["bounce_rate"] != 0.5 {
		t.Fatalf("totals: %v", tot)
	}
	series := got["series"].([]any)
	if last := series[len(series)-1].(map[string]any); last["pageviews"] != 3.0 {
		t.Fatalf("today: %v", last)
	}
	keys := func(name string) []string {
		var out []string
		for _, r := range got[name].([]any) {
			out = append(out, r.(map[string]any)["key"].(string))
		}
		return out
	}
	if p := keys("pages"); len(p) != 3 {
		t.Fatalf("pages: %v", p)
	}
	if w := keys("websites"); len(w) != 2 || (w[0] != "acme.example" && w[1] != "acme.example") {
		t.Fatalf("websites: %v", w)
	}
	if s := keys("sources"); len(s) != 1 || s[0] != "(direct)" {
		t.Fatalf("sources: %v", s)
	}
	for _, r := range got["pages"].([]any) {
		p := r.(map[string]any)
		if want := map[string]string{"/": "acme.example", "/pricing": "acme.example", "/login": "app.acme.example"}[p["key"].(string)]; p["website"] != want {
			t.Fatalf("page website: %v", p)
		}
	}

	// One website: only its visits and pages.
	app := owner.must(200, "GET", bbase+"/analytics/tracking?website=APP.acme.example", nil)
	if tot := app["totals"].(map[string]any); tot["visitors"] != 1.0 || tot["pageviews"] != 1.0 {
		t.Fatalf("app totals: %v", tot)
	}
	if p := app["pages"].([]any); len(p) != 1 || p[0].(map[string]any)["key"] != "/login" {
		t.Fatalf("app pages: %v", p)
	}

	owner.must(400, "GET", bbase+"/analytics/tracking?from=2026-02-01&to=2026-01-01", nil)
	owner.must(400, "GET", bbase+"/analytics/tracking?from=2020-01-01&to=2026-01-01", nil)
	owner.must(400, "GET", bbase+"/analytics/tracking?from=nope", nil)
}
