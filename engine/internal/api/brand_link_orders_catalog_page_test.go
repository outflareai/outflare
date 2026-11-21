package api_test

import "testing"

// The catalog lists up to 1,000 rows a page, so the Catalog page can load
// a whole intermediary database in a few requests.
func TestLinkOrderCatalogPageSize(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	brand := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	root := "/v1/brands/" + brand + "/link-orders/catalog"
	owner.must200Raw(t, root+"/import", "domain\na.example\nb.example\nc.example\n")
	got := owner.must(200, "GET", root+"?limit=1000", nil)
	if n := len(got["data"].([]any)); n != 3 || got["next_cursor"] != nil {
		t.Fatalf("limit=1000: %d rows, next %v", n, got["next_cursor"])
	}
	page := owner.must(200, "GET", root+"?limit=2", nil)
	if len(page["data"].([]any)) != 2 || page["next_cursor"] == nil {
		t.Fatalf("limit=2: %v", page)
	}
	owner.must(400, "GET", root+"?limit=1001", nil)
}
