package api_test

import (
	"context"
	"testing"
	"time"
)

// TestTrendMeasuresAndTwoBreakdowns checks the spread and per-person
// measures, rolling active people, exact totals for people across
// groups, and splitting by two fields.
func TestTrendMeasuresAndTwoBreakdowns(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana"})["id"].(string)
	bo := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Bo"})["id"].(string)

	day := 24 * time.Hour
	today := time.Now().UTC().Truncate(day)
	d := func(n int) time.Time { return today.Add(-time.Duration(n)*day + time.Hour) }
	// Ana uses the app three times on day 5 (pro, in PT) and once on day
	// 2 (pro, in ES); Bo once on day 5 (free, in PT). Seats 1, 2, 3, 4, 10.
	for _, v := range []struct {
		record  string
		at      time.Time
		plan    string
		country string
		seats   int
	}{
		{ana, d(5), "pro", "PT", 1}, {ana, d(5), "pro", "PT", 2}, {ana, d(5), "pro", "PT", 3}, {ana, d(2), "pro", "ES", 4}, {bo, d(5), "free", "PT", 10},
	} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, brand_id, record_id, kind, name, occurred_at, properties)
			VALUES ($1, $2, $3, 'track', 'used_app', $4, jsonb_build_object('plan', $5::text, 'country', $6::text, 'seats', $7::int))`,
			ws, brand, v.record, v.at, v.plan, v.country, v.seats); err != nil {
			t.Fatal(err)
		}
	}
	run := func(spec map[string]any) map[string]any {
		t.Helper()
		spec["date_range"] = map[string]any{"from": today.Add(-9 * day).Format(time.DateOnly), "to": today.Format(time.DateOnly)}
		spec["interval"] = "day"
		return owner.must(200, "POST", base+"/insights/trend", spec)
	}
	totals := func(res map[string]any) []float64 {
		var out []float64
		for _, s := range res["series"].([]any) {
			v, _ := s.(map[string]any)["total"].(float64)
			out = append(out, v)
		}
		return out
	}
	ev := func(measure, property string) map[string]any {
		s := map[string]any{"kind": "event", "event": "used_app", "measure": measure}
		if property != "" {
			s["property"] = "property:seats"
		}
		return s
	}
	res := run(map[string]any{"series": []map[string]any{
		ev("median", "seats"), ev("min", "seats"), ev("max", "seats"), ev("per_person_average", ""), ev("per_person_max", ""), ev("people", ""),
	}})
	got := totals(res)
	// Median of 1,2,3,4,10 is 3; per person over the range: Ana 4, Bo 1.
	want := []float64{3, 1, 10, 2.5, 4, 2}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("totals: got %v want %v", got, want)
		}
	}
	// Day 5's point: per person average is (3 + 1) / 2.
	pts := res["series"].([]any)[3].(map[string]any)["points"].([]any)
	if v := pts[4].(map[string]any)["value"]; v != 2.0 {
		t.Fatalf("per person on day 5: %v", pts[4])
	}

	// Weekly active: on day 2 both were active in the 7 days before.
	res = run(map[string]any{"series": []map[string]any{ev("weekly_active", "")}})
	pts = res["series"].([]any)[0].(map[string]any)["points"].([]any)
	if v := pts[7].(map[string]any)["value"]; v != 2.0 {
		t.Fatalf("weekly active on day 2: %v", pts)
	}
	if v := pts[0].(map[string]any)["value"]; v != nil && v != 0.0 {
		t.Fatalf("weekly active on day 9: %v", pts[0])
	}

	// People broken down by country: the total stays 2 people, though
	// Ana is in both groups.
	res = run(map[string]any{"series": []map[string]any{ev("people", "")}, "breakdown": "property:country"})
	if got := totals(res); got[0] != 2 {
		t.Fatalf("people total with a breakdown: %v", got)
	}
	if n := len(res["breakdown"].([]any)); n != 2 {
		t.Fatalf("groups: %d", n)
	}

	// Two breakdowns: country then plan.
	res = run(map[string]any{"series": []map[string]any{ev("count", "")}, "breakdown": "property:country", "breakdown_2": "property:plan"})
	groups := res["breakdown"].([]any)
	if len(groups) != 3 {
		t.Fatalf("two breakdowns: %v", groups)
	}
	first := groups[0].(map[string]any)
	if first["value"] != "PT / pro" || first["series"].([]any)[0].(map[string]any)["total"] != 3.0 {
		t.Fatalf("first group: %v", first)
	}
	if vs := first["values"].([]any); len(vs) != 2 || vs[0] != "PT" || vs[1] != "pro" {
		t.Fatalf("values: %v", vs)
	}

	for _, bad := range []map[string]any{
		{"series": []map[string]any{ev("count", "")}, "breakdown_2": "property:plan"},
		{"series": []map[string]any{ev("count", "")}, "breakdown": "property:plan", "breakdown_2": "property:plan"},
		{"series": []map[string]any{ev("median", "")}},
		{"series": []map[string]any{ev("count", "")}, "display": "map", "breakdown": "property:plan"},
		{"series": []map[string]any{ev("count", "")}, "smoothing": 5},
		{"series": []map[string]any{ev("count", "")}, "right_axis": []string{"A"}},
	} {
		owner.must(400, "POST", base+"/insights/trend", bad)
	}
	owner.must(200, "POST", base+"/insights/trend", map[string]any{"series": []map[string]any{ev("count", ""), ev("people", "")}, "display": "stacked_bar", "cumulative": true, "smoothing": 7, "right_axis": []string{"B"}})
}
