package api_test

import "testing"

// Best link is a manual label on a catalog donor: set on create, changed
// by PATCH and by a CSV column, and returned on every donor.
func TestLinkOrderCatalogBestLink(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	brand := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	root := "/v1/brands/" + brand + "/link-orders/catalog"

	plain := owner.must(201, "POST", root, map[string]any{"domain": "plain.example"})
	if plain["best_link"] != false {
		t.Fatalf("default best_link: %v", plain["best_link"])
	}
	best := owner.must(201, "POST", root, map[string]any{"domain": "best.example", "best_link": true})
	if best["best_link"] != true {
		t.Fatalf("created best_link: %v", best["best_link"])
	}
	off := owner.must(200, "PATCH", root+"/items/"+best["id"].(string), map[string]any{"best_link": false})
	if off["best_link"] != false || off["pbn"] != false {
		t.Fatalf("patched: %v", off)
	}
	// A PATCH that leaves best_link out keeps it.
	owner.must(200, "PATCH", root+"/items/"+plain["id"].(string), map[string]any{"best_link": true})
	kept := owner.must(200, "PATCH", root+"/items/"+plain["id"].(string), map[string]any{"theme": "Proxies"})
	if kept["best_link"] != true {
		t.Fatalf("kept: %v", kept)
	}
	owner.must200Raw(t, root+"/import", "domain,best link\nbest.example,yes\nplain.example,no\n")
	for _, d := range owner.must(200, "GET", root, nil)["data"].([]any) {
		m := d.(map[string]any)
		if want := m["domain"] == "best.example"; m["best_link"] != want {
			t.Errorf("%v best_link = %v, want %v", m["domain"], m["best_link"], want)
		}
	}
}
