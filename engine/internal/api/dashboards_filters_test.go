package api_test

import (
	"context"
	"testing"
	"time"
)

// A dashboard's filters go on every event part of every tile, on the
// share link too, and bad ones are refused.
func TestDashboardFilters(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	at := time.Now().UTC().Add(-48 * time.Hour)
	if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, brand_id, kind, name, anonymous_id, occurred_at, properties)
		VALUES ($1, $2, 'track', 'signed_up', 'a', $3, '{"plan": "pro"}'), ($1, $2, 'track', 'signed_up', 'b', $3, '{"plan": "free"}'),
		($1, $2, 'track', 'signed_up', 'c', $3, '{"plan": "free"}')`, ws, brand, at); err != nil {
		t.Fatal(err)
	}
	trend := owner.must(201, "POST", base+"/insights", map[string]any{"kind": "trend", "name": "Sign-ups",
		"spec": map[string]any{"date_range": map[string]any{"preset": "last_7_days"}, "series": []map[string]any{{"kind": "event", "event": "signed_up"}}, "display": "number"}})["id"].(string)
	did := owner.must(201, "POST", base+"/dashboards", map[string]any{"name": "Pro", "settings": map[string]any{"date_range": map[string]any{"preset": "last_30_days"}}})["id"].(string)
	tile := owner.must(201, "POST", base+"/dashboards/"+did+"/tiles", map[string]any{"insight_id": trend})["tile_list"].([]any)[0].(map[string]any)["id"].(string)

	owner.must(400, "PATCH", base+"/dashboards/"+did, map[string]any{"settings": map[string]any{"filters": []map[string]any{{"property": "plan", "op": "nearly", "value": "pro"}}}})
	owner.must(400, "PATCH", base+"/dashboards/"+did, map[string]any{"settings": map[string]any{"filters": []map[string]any{{"property": "", "op": "eq", "value": "pro"}}}})
	many := []map[string]any{}
	for range 6 {
		many = append(many, map[string]any{"property": "plan", "op": "exists"})
	}
	owner.must(400, "PATCH", base+"/dashboards/"+did, map[string]any{"settings": map[string]any{"filters": many}})
	got := owner.must(200, "PATCH", base+"/dashboards/"+did, map[string]any{"settings": map[string]any{"date_range": map[string]any{"preset": "last_30_days"}, "filters": []map[string]any{{"property": "plan", "op": "eq", "value": "pro"}}}})
	if fs := got["settings"].(map[string]any)["filters"].([]any); len(fs) != 1 {
		t.Fatalf("settings: %v", got["settings"])
	}

	token := owner.must(201, "POST", base+"/dashboards/"+did+"/share", map[string]any{})["token"].(string)
	anon := &client{t: t, h: e.h}
	res := anon.must(200, "POST", "/v1/shared/dashboards/"+token+"/tiles/"+tile, map[string]any{})
	if total := res["series"].([]any)[0].(map[string]any)["total"]; total != float64(1) {
		t.Fatalf("filtered shared tile total = %v, want 1", total)
	}
	owner.must(200, "PATCH", base+"/dashboards/"+did, map[string]any{"settings": map[string]any{"date_range": map[string]any{"preset": "last_30_days"}}})
	res = anon.must(200, "POST", "/v1/shared/dashboards/"+token+"/tiles/"+tile, map[string]any{})
	if total := res["series"].([]any)[0].(map[string]any)["total"]; total != float64(3) {
		t.Fatalf("unfiltered shared tile total = %v, want 3", total)
	}
}
