package api_test

import (
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestIdealCustomerProfiles(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	geo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode", "handle": "geonode"})["id"].(string)
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "handle": "repocket"})

	// A profile on its own.
	c := owner.must(201, "POST", base+"/ideal-customer-profiles", map[string]any{
		"name": "Series A fintech, AU", "description": "Fintechs that just raised and need pipeline.",
		"filters": map[string]any{"industries": []string{"Financial Services", " financial services "}, "locations": []string{"Australia"}},
	})
	f := c["filters"].(map[string]any)
	if c["version"] != 1.0 || len(f["industries"].([]any)) != 1 || len(f["titles"].([]any)) != 0 || len(c["project_ids"].([]any)) != 0 {
		t.Fatalf("profile = %v", c)
	}
	owner.must(400, "POST", base+"/ideal-customer-profiles", map[string]any{"name": "series a fintech, au"})
	owner.must(400, "POST", base+"/ideal-customer-profiles", map[string]any{"name": "Bad", "filters": map[string]any{"colour": []string{"red"}}})
	owner.must(400, "POST", base+"/ideal-customer-profiles", map[string]any{"name": ""})

	// A project given an ICP gets a profile of its own, named after it.
	p := owner.must(201, "POST", base+"/projects", map[string]any{"name": "AU CFOs", "icp": map[string]any{"titles": []string{"CFO"}}})
	own := p["profile_id"].(string)
	if v := owner.must(200, "GET", base+"/ideal-customer-profiles/"+own, nil); v["name"] != "AU CFOs" || v["project_ids"].([]any)[0] != p["id"] {
		t.Fatalf("project profile = %v", v)
	}
	// Without an ICP, none.
	if q := owner.must(201, "POST", base+"/projects", map[string]any{"name": "Later"}); q["profile_id"] != nil {
		t.Fatalf("empty project = %v", q)
	}

	// Editing the project's ICP edits its profile.
	p = owner.must(200, "PATCH", base+"/projects/"+p["id"].(string), map[string]any{"icp": map[string]any{"titles": []string{"CFO", "VP Finance"}}})
	if v := owner.must(200, "GET", base+"/ideal-customer-profiles/"+own, nil); len(v["filters"].(map[string]any)["titles"].([]any)) != 2 || v["version"] != 2.0 {
		t.Fatalf("after project edit = %v", v)
	}

	// Linking another profile takes its filters, and editing that profile
	// moves the project's ICP and brief version.
	pid, cid := p["id"].(string), c["id"].(string)
	p = owner.must(200, "PATCH", base+"/projects/"+pid, map[string]any{"profile_id": cid})
	if p["profile_id"] != cid || len(p["icp"].(map[string]any)["locations"].([]any)) != 1 || len(p["icp"].(map[string]any)["titles"].([]any)) != 0 {
		t.Fatalf("linked = %v", p)
	}
	version := p["brief_version"].(float64)
	owner.must(200, "PATCH", base+"/ideal-customer-profiles/"+cid, map[string]any{"filters": map[string]any{"locations": []string{"Australia", "New Zealand"}}})
	p = owner.must(200, "GET", base+"/projects/"+pid, nil)
	if len(p["icp"].(map[string]any)["locations"].([]any)) != 2 || p["brief_version"].(float64) != version+1 {
		t.Fatalf("synced = %v", p)
	}
	// Saving the same filters changes nothing.
	same := owner.must(200, "PATCH", base+"/ideal-customer-profiles/"+cid, map[string]any{"filters": map[string]any{"locations": []string{"Australia", "New Zealand"}}})
	if same["version"] != 2.0 || owner.must(200, "GET", base+"/projects/"+pid, nil)["brief_version"].(float64) != version+1 {
		t.Fatalf("no-op = %v", same)
	}

	// A brand's profile only fits that brand's projects.
	owner.must(200, "PATCH", base+"/ideal-customer-profiles/"+own, map[string]any{"brand_id": "geonode"})
	owner.must(400, "PATCH", base+"/projects/"+pid, map[string]any{"profile_id": own})
	owner.must(200, "PATCH", base+"/projects/"+pid, map[string]any{"brand_id": "geonode", "profile_id": own})
	owner.must(400, "PATCH", base+"/ideal-customer-profiles/"+own, map[string]any{"brand_id": "repocket"})
	if l := owner.must(200, "GET", base+"/ideal-customer-profiles?brand=geonode", nil)["data"].([]any); len(l) != 1 || l[0].(map[string]any)["brand_id"] != geo {
		t.Fatalf("by brand = %v", l)
	}
	// Unlinking keeps the ICP.
	if p = owner.must(200, "PATCH", base+"/projects/"+pid, map[string]any{"profile_id": nil}); p["profile_id"] != nil || len(p["icp"].(map[string]any)["titles"].([]any)) != 2 {
		t.Fatalf("unlinked = %v", p)
	}
	owner.must(400, "PATCH", base+"/projects/"+pid, map[string]any{"profile_id": "not-a-profile"})

	// Archived profiles leave the default list.
	owner.must(200, "PATCH", base+"/ideal-customer-profiles/"+cid, map[string]any{"status": "archived"})
	if n := len(owner.must(200, "GET", base+"/ideal-customer-profiles", nil)["data"].([]any)); n != 1 {
		t.Fatalf("active = %d", n)
	}
	if n := len(owner.must(200, "GET", base+"/ideal-customer-profiles?status=all", nil)["data"].([]any)); n != 2 {
		t.Fatalf("all = %d", n)
	}
	owner.must(404, "GET", base+"/ideal-customer-profiles/00000000-0000-0000-0000-000000000000", nil)

	seen := map[string]bool{}
	for _, ev := range got() {
		seen[ev.Name] = true
	}
	for _, name := range []string{"ideal_customer_profile.created", "ideal_customer_profile.updated", "ideal_customer_profile.archived"} {
		if !seen[name] {
			t.Fatalf("no %s event", name)
		}
	}
}
