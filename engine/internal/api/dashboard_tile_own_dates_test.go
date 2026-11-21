package api_test

import (
	"context"
	"testing"
	"time"
)

// A tile follows the dashboard's dates unless it keeps its insight's own.
func TestDashboardTileOwnDates(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	now := time.Now().UTC()
	if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, brand_id, kind, name, anonymous_id, occurred_at, properties)
		VALUES ($1, $2, 'track', 'signed_up', 'a', $3, '{}'), ($1, $2, 'track', 'signed_up', 'b', $4, '{}')`,
		ws, brand, now.Add(-48*time.Hour), now.Add(-20*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	trend := owner.must(201, "POST", base+"/insights", map[string]any{"kind": "trend", "name": "Sign-ups",
		"spec": map[string]any{"date_range": map[string]any{"preset": "last_7_days"}, "series": []map[string]any{{"kind": "event", "event": "signed_up"}}, "display": "number"}})["id"].(string)
	did := owner.must(201, "POST", base+"/dashboards", map[string]any{"name": "Growth", "settings": map[string]any{"date_range": map[string]any{"preset": "last_30_days"}}})["id"].(string)
	first := owner.must(201, "POST", base+"/dashboards/"+did+"/tiles", map[string]any{"insight_id": trend})["tile_list"].([]any)[0].(map[string]any)
	tile := first["id"].(string)
	if first["own_dates"] != false {
		t.Fatalf("a new tile follows the dashboard: %v", first["own_dates"])
	}

	token := owner.must(201, "POST", base+"/dashboards/"+did+"/share", map[string]any{})["token"].(string)
	anon := &client{t: t, h: e.h}
	total := func() any {
		return anon.must(200, "POST", "/v1/shared/dashboards/"+token+"/tiles/"+tile, map[string]any{})["series"].([]any)[0].(map[string]any)["total"]
	}
	if got := total(); got != float64(2) {
		t.Fatalf("over the dashboard's 30 days = %v, want 2", got)
	}

	got := owner.must(200, "PATCH", base+"/dashboards/"+did+"/tiles/"+tile, map[string]any{"own_dates": true})["tile_list"].([]any)[0].(map[string]any)
	if got["own_dates"] != true {
		t.Fatalf("own_dates not kept: %v", got)
	}
	if got := total(); got != float64(1) {
		t.Fatalf("over the trend's own 7 days = %v, want 1", got)
	}
	// Moving the tile leaves the choice alone.
	got = owner.must(200, "PATCH", base+"/dashboards/"+did+"/tiles/"+tile, map[string]any{"w": 4})["tile_list"].([]any)[0].(map[string]any)
	if got["own_dates"] != true || got["w"] != float64(4) {
		t.Fatalf("after a move: %v", got)
	}
	owner.must(200, "PATCH", base+"/dashboards/"+did+"/tiles/"+tile, map[string]any{"own_dates": false})
	if got := total(); got != float64(2) {
		t.Fatalf("back on the dashboard's dates = %v, want 2", got)
	}

	// A note has no dates to keep.
	owner.must(400, "POST", base+"/dashboards/"+did+"/tiles", map[string]any{"note": "Read me", "own_dates": true})
	// A tile can be added keeping its own dates.
	list := owner.must(201, "POST", base+"/dashboards/"+did+"/tiles", map[string]any{"insight_id": trend, "own_dates": true})["tile_list"].([]any)
	if list[len(list)-1].(map[string]any)["own_dates"] != true {
		t.Fatalf("added with own_dates: %v", list[len(list)-1])
	}
}
