package api_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// The overview, reports, outcomes, install health, email and attribution
// reads are each one brand's on its own route, the same as the workspace
// route asked about that brand.
func TestBrandReportRoutes(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	rp := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "website": "repocket.com"})["id"].(string)
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode", "website": "geonode.com"})
	bb := "/v1/brands/" + rp

	// Fields that hold the time of the call differ between two calls, and a
	// brand route notes that costs no brand owns are left out.
	clean := func(m map[string]any) string {
		for _, k := range []string{"checked_at", "generated_at", "as_of", "now", "from", "to", "notes"} {
			delete(m, k)
		}
		b, _ := json.Marshal(m)
		return string(b)
	}
	for _, p := range []string{
		"/stats",
		"/reports",
		"/outcomes/report",
		"/install-health",
		"/email-overview",
		"/email-recipients/ana@acme.example",
		"/attribution/customers",
		"/attribution/cohorts",
		"/attribution/accounts",
		"/attribution/deals",
	} {
		sep := "?"
		if strings.Contains(p, "?") {
			sep = "&"
		}
		own := owner.must(200, "GET", bb+p, nil)
		ws := owner.must(200, "GET", base+p+sep+"brand="+rp, nil)
		if clean(own) != clean(ws) {
			t.Errorf("%s: brand route %v, workspace route %v", p, own, ws)
		}
		// The path's brand wins: ?brand= can't point a brand route at another.
		owner.must(200, "GET", bb+p+sep+"brand=geonode", nil)
	}
	if code, _ := owner.raw("GET", bb+"/outcomes/export?dataset=report", "", ""); code != 200 {
		t.Fatalf("export: %d", code)
	}
	owner.must(404, "GET", "/v1/brands/00000000-0000-0000-0000-000000000000/stats", nil)
}
