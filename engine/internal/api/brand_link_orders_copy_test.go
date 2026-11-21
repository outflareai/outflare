package api_test

import (
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// A broker's file imported into one brand is copied to another, page by
// page, with its metrics and prices; copying again updates, never doubles.
func TestLinkOrderCatalogCopy(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	a := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	b := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Acme", "handle": "acme"})["id"].(string)
	if code, out := owner.raw("POST", "/v1/brands/"+a+"/link-orders/catalog/import", "text/csv", intermediaryCSV); code != 200 || out["created"] != 2.0 {
		t.Fatalf("import %d: %v", code, out)
	}
	copyTo := "/v1/brands/" + b + "/link-orders/catalog/copy"

	first := owner.must(200, "POST", copyTo, map[string]any{"from_brand": a, "limit": 1})
	if first["created"] != 1.0 || first["next_cursor"] == nil {
		t.Fatalf("first page: %v", first)
	}
	second := owner.must(200, "POST", copyTo, map[string]any{"from_brand": a, "cursor": first["next_cursor"]})
	if second["created"] != 1.0 || second["next_cursor"] != nil {
		t.Fatalf("second page: %v", second)
	}
	list := owner.must(200, "GET", "/v1/brands/"+b+"/link-orders/catalog?q=news.example", nil)["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("copied list: %v", list)
	}
	d := list[0].(map[string]any)
	if d["domain_rating"] != 54.0 || d["traffic"] != 12500.0 || d["intermediary"] != "Acme Links" || d["site_since"] != "2012-01-01" ||
		len(d["prices"].([]any)) != 1 || d["prices"].([]any)[0].(map[string]any)["amount_minor"] != 15000.0 {
		t.Fatalf("copied donor: %v", d)
	}
	if m := d["metrics"].(map[string]any); m["referring_domains"] != 1200.0 {
		t.Fatalf("copied metrics: %v", m)
	}
	// Again from the start: updated, not doubled.
	again := owner.must(200, "POST", copyTo, map[string]any{"from_brand": a})
	if again["created"] != 0.0 || again["updated"] != 2.0 {
		t.Fatalf("again: %v", again)
	}
	if n := len(owner.must(200, "GET", "/v1/brands/"+b+"/link-orders/catalog", nil)["data"].([]any)); n != 2 {
		t.Fatalf("target has %d donors", n)
	}
	// The source keeps its own: nothing was moved.
	if n := len(owner.must(200, "GET", "/v1/brands/"+a+"/link-orders/catalog", nil)["data"].([]any)); n != 2 {
		t.Fatalf("source has %d donors", n)
	}

	owner.must(400, "POST", copyTo, map[string]any{"from_brand": b})
	owner.must(400, "POST", copyTo, map[string]any{"from_brand": "not-a-brand"})
	owner.must(400, "POST", copyTo, map[string]any{"from_brand": a, "cursor": "nonsense!"})
	owner.must(404, "POST", copyTo, map[string]any{"from_brand": "00000000-0000-4000-8000-000000000000"})
	// A brand of another workspace is not found, either way round, even
	// for someone who owns both workspaces.
	ows := owner.must(201, "POST", "/v1/workspaces", map[string]string{"name": "Other workspace"})["id"].(string)
	c := owner.must(201, "POST", "/v1/workspaces/"+ows+"/brands", map[string]any{"name": "Elsewhere", "handle": "elsewhere"})["id"].(string)
	owner.must(404, "POST", "/v1/brands/"+c+"/link-orders/catalog/copy", map[string]any{"from_brand": a})
	owner.must(404, "POST", copyTo, map[string]any{"from_brand": c})

	ok := false
	for _, ev := range got() {
		if ev.Name == "link_orders.catalog_copied" && ev.WorkspaceID == ws && ev.Props["created"] == 1 {
			ok = true
		}
	}
	if !ok {
		t.Fatal("no link_orders.catalog_copied event")
	}
}
