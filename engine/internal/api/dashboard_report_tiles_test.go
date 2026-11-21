package api_test

import (
	"context"
	"testing"
)

// A saved report on a dashboard (plans/ad-tracker-ui.md, B7): a shared
// report view is pinned as a tile, stays shared while it is pinned, takes
// its tile with it when deleted, and is named but never run on a share
// link.
func TestDashboardReportTiles(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	spec := map[string]any{"source": "tracker", "group_by": []string{"tracking_campaign", "country"}, "sort": "-profit",
		"date_range": map[string]any{"preset": "last_7_days"}}
	view := owner.must(201, "POST", base+"/report-views", map[string]any{"name": "Profit by campaign", "spec": spec})["id"].(string)
	did := owner.must(201, "POST", base+"/dashboards", map[string]any{"name": "Tracker"})["id"].(string)
	tiles := base + "/dashboards/" + did + "/tiles"

	// A private report can't be pinned: everyone who opens the dashboard
	// would have to see it.
	owner.must(400, "POST", tiles, map[string]any{"report_view_id": view})
	owner.must(200, "PATCH", base+"/report-views/"+view, map[string]any{"shared": true})
	owner.must(400, "POST", tiles, map[string]any{"report_view_id": "00000000-0000-0000-0000-000000000000"})
	owner.must(400, "POST", tiles, map[string]any{"report_view_id": view, "note": "why"})
	other := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Summitly"})["id"].(string)
	otherDash := owner.must(201, "POST", "/v1/brands/"+other+"/dashboards", map[string]any{"name": "Other"})["id"].(string)
	owner.must(400, "POST", "/v1/brands/"+other+"/dashboards/"+otherDash+"/tiles", map[string]any{"report_view_id": view})

	d := owner.must(201, "POST", tiles, map[string]any{"report_view_id": view})
	list := d["tile_list"].([]any)
	if len(list) != 1 {
		t.Fatalf("tiles: %v", list)
	}
	tile := list[0].(map[string]any)
	rv, _ := tile["report_view"].(map[string]any)
	if tile["report_view_id"] != view || tile["insight_id"] != nil || tile["note"] != "" || rv == nil || rv["name"] != "Profit by campaign" ||
		rv["source"] != "tracker" || len(rv["group_by"].([]any)) != 2 || tile["w"] != 6.0 || tile["h"] != 5.0 {
		t.Fatalf("report tile: %v", tile)
	}
	id := tile["id"].(string)
	// It moves like any tile, and what it shows can't be swapped.
	moved := owner.must(200, "PATCH", tiles+"/"+id, map[string]any{"w": 12})["tile_list"].([]any)[0].(map[string]any)
	if moved["w"] != 12.0 || moved["report_view_id"] != view {
		t.Fatalf("moved: %v", moved)
	}
	owner.must(400, "PATCH", tiles+"/"+id, map[string]any{"report_view_id": view})
	owner.must(400, "PATCH", tiles+"/"+id, map[string]any{"note": "a note"})
	// The app runs it with the report itself.
	if r := owner.must(200, "GET", base+"/drilldown?view="+view, nil); r["source"] != "tracker" {
		t.Fatalf("the tile's report: %v", r)
	}
	// While it is on a dashboard it stays shared.
	owner.must(409, "PATCH", base+"/report-views/"+view, map[string]any{"shared": false})
	owner.must(200, "PATCH", base+"/report-views/"+view, map[string]any{"name": "Profit by campaign and country"})

	// On a share link the tile is named, and its numbers never leave.
	token := owner.must(201, "POST", base+"/dashboards/"+did+"/share", map[string]any{})["token"].(string)
	anon := &client{t: t, h: e.h}
	shared := anon.must(200, "POST", "/v1/shared/dashboards/"+token, map[string]any{})["tile_list"].([]any)[0].(map[string]any)
	if shared["kind"] != "report" || shared["name"] != "Profit by campaign and country" {
		t.Fatalf("shared tile: %v", shared)
	}
	anon.must(404, "POST", "/v1/shared/dashboards/"+token+"/tiles/"+id, map[string]any{})

	// Deleting the report takes its tile along; then it can be private.
	second := owner.must(201, "POST", base+"/report-views", map[string]any{"name": "Second", "spec": spec, "shared": true})["id"].(string)
	owner.must(201, "POST", tiles, map[string]any{"report_view_id": second})
	owner.must(204, "DELETE", base+"/report-views/"+second, nil)
	if n := len(owner.must(200, "GET", base+"/dashboards/"+did, nil)["tile_list"].([]any)); n != 1 {
		t.Fatalf("tiles after the report was deleted: %d", n)
	}
	owner.must(204, "DELETE", tiles+"/"+id, nil)
	owner.must(200, "PATCH", base+"/report-views/"+view, map[string]any{"shared": false})
}
