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
	if c["version"] != 1.0 || len(f["industries"].([]any)) != 1 || len(f["titles"].([]any)) != 0 {
		t.Fatalf("profile = %v", c)
	}
	owner.must(400, "POST", base+"/ideal-customer-profiles", map[string]any{"name": "series a fintech, au"})
	owner.must(400, "POST", base+"/ideal-customer-profiles", map[string]any{"name": "Bad", "filters": map[string]any{"colour": []string{"red"}}})
	owner.must(400, "POST", base+"/ideal-customer-profiles", map[string]any{"name": ""})

	// A second profile; editing its filters bumps its version.
	own := owner.must(201, "POST", base+"/ideal-customer-profiles", map[string]any{"name": "AU CFOs", "filters": map[string]any{"titles": []string{"CFO"}}})["id"].(string)
	owner.must(200, "PATCH", base+"/ideal-customer-profiles/"+own, map[string]any{"filters": map[string]any{"titles": []string{"CFO", "VP Finance"}}})
	if v := owner.must(200, "GET", base+"/ideal-customer-profiles/"+own, nil); len(v["filters"].(map[string]any)["titles"].([]any)) != 2 || v["version"] != 2.0 || v["project_ids"] != nil {
		t.Fatalf("after edit = %v", v)
	}
	// Saving the same filters changes nothing.
	cid := c["id"].(string)
	owner.must(200, "PATCH", base+"/ideal-customer-profiles/"+cid, map[string]any{"filters": map[string]any{"locations": []string{"Australia", "New Zealand"}}})
	same := owner.must(200, "PATCH", base+"/ideal-customer-profiles/"+cid, map[string]any{"filters": map[string]any{"locations": []string{"Australia", "New Zealand"}}})
	if same["version"] != 2.0 {
		t.Fatalf("no-op = %v", same)
	}

	// A brand's profile.
	owner.must(200, "PATCH", base+"/ideal-customer-profiles/"+own, map[string]any{"brand_id": "geonode"})
	if l := owner.must(200, "GET", base+"/ideal-customer-profiles?brand=geonode", nil)["data"].([]any); len(l) != 1 || l[0].(map[string]any)["brand_id"] != geo {
		t.Fatalf("by brand = %v", l)
	}

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
