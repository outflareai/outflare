package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

// The forecast adds what is won to renewals weighted by health and open
// deals weighted by stage, against the quarter's target, and every line
// says what it adds.
func TestRevenueForecast(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	num := func(m map[string]any, pairs ...any) {
		t.Helper()
		for i := 0; i < len(pairs); i += 2 {
			if m[pairs[i].(string)] != float64(pairs[i+1].(int)) {
				t.Fatalf("%s = %v, want %v (%v)", pairs[i], m[pairs[i].(string)], pairs[i+1], m)
			}
		}
	}
	now := time.Now().UTC()
	quarter := owner.must(200, "GET", base+"/revenue/forecast", nil)
	// Empty: nothing won, nothing expected, no target, the default weights.
	num(quarter, "forecast_minor", 0, "best_case_minor", 0)
	if quarter["target_minor"] != nil || quarter["gap_minor"] != nil || len(quarter["deal_lines"].([]any)) != 0 {
		t.Fatalf("empty: %v", quarter)
	}
	weights := quarter["weights"].(map[string]any)
	num(weights["renewal"].(map[string]any), "high", 95, "medium", 80, "low", 50, "none", 85)
	stages := weights["stages"].([]any)
	if len(stages) < 2 {
		t.Fatalf("stages: %v", stages)
	}
	first := stages[0].(map[string]any)["key"].(string)
	last := stages[len(stages)-1].(map[string]any)["key"].(string)
	firstWeight := int(stages[0].(map[string]any)["weight"].(float64))
	lastWeight := int(stages[len(stages)-1].(map[string]any)["weight"].(float64))
	if firstWeight >= lastWeight || firstWeight <= 0 || lastWeight >= 100 {
		t.Fatalf("stage weights: %v", stages)
	}
	q := quarter["quarter"].(string)

	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme"})["id"].(string)
	north := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Northwind"})["id"].(string)
	// Acme pays 1,000 a month, is in low health and renews tomorrow.
	exec(`INSERT INTO revenue_customers (workspace_id, brand_id, record_id, mrr_minor) VALUES ($1, $2, $3, 100000)`, ws, brand, acme)
	exec(`UPDATE records SET attributes = attributes || '{"scores": {"health": 20, "health_band": "low"}}' WHERE id = $1`, acme)
	owner.must(201, "POST", base+"/catalog/types/account/fields", map[string]any{"label": "Renews on", "value_type": "date"})
	// Yesterday is in the forecast too: late in this quarter, or the last day of the one before.
	owner.must(200, "PATCH", base+"/records/"+acme, map[string]any{"attributes": map[string]any{"renews_on": now.AddDate(0, 0, -1).Format(time.DateOnly)}})

	// One deal won this quarter, one open with Acme (expansion) at the last
	// stage, one open with Northwind (new) at the first, one with no amount.
	exec(`INSERT INTO deals (workspace_id, brand_id, record_id, source, name, stage, state, amount_cents, currency, closed_at) VALUES
		($1, $2, $4, 'manual', 'Northwind pilot', 'won', 'won', 500000, 'USD', now()),
		($1, $2, $3, 'manual', 'Acme seats', $5, 'open', 200000, 'USD', NULL),
		($1, $2, $4, 'manual', 'Northwind rollout', $6, 'open', 1000000, 'USD', NULL),
		($1, $2, $4, 'manual', 'Northwind extra', $6, 'open', NULL, 'USD', NULL)`, ws, brand, acme, north, last, first)

	got := owner.must(200, "GET", base+"/revenue/forecast?quarter="+q, nil)
	renewal := 1200000 * 50 / 100
	seats := 200000 * lastWeight / 100
	rollout := 1000000 * firstWeight / 100
	num(got["won"].(map[string]any), "count", 1, "total_minor", 500000)
	num(got["renewals"].(map[string]any), "count", 1, "total_minor", 1200000, "expected_minor", renewal)
	num(got["expansion"].(map[string]any), "count", 1, "total_minor", 200000, "expected_minor", seats)
	num(got["new_business"].(map[string]any), "count", 1, "total_minor", 1000000, "expected_minor", rollout)
	num(got, "forecast_minor", 500000+renewal+seats+rollout, "best_case_minor", 500000+1200000+200000+1000000)
	num(got["skipped"].(map[string]any), "deals", 1, "renewals", 0)
	line := got["renewal_lines"].([]any)[0].(map[string]any)
	if line["record_id"] != acme || line["health_band"] != "low" || line["weight"] != float64(50) || line["late"] != true {
		t.Fatalf("the renewal: %v", line)
	}
	deals := got["deal_lines"].([]any)
	if len(deals) != 2 {
		t.Fatalf("deals: %v", deals)
	}
	for _, d := range deals {
		d := d.(map[string]any)
		if (d["name"] == "Acme seats") != (d["kind"] == "expansion") {
			t.Fatalf("kind: %v", d)
		}
	}

	// By owner: nobody owns anything yet, so it is all in one line that
	// adds up to the forecast.
	owners := got["owners"].([]any)
	if len(owners) != 1 || owners[0].(map[string]any)["owner_id"] != nil {
		t.Fatalf("owners with none set: %v", owners)
	}
	num(owners[0].(map[string]any), "won_minor", 500000, "forecast_minor", 500000+renewal+seats+rollout, "best_case_minor", 500000+1200000+200000+1000000, "open", 3)
	// Acme gets an owner: its renewal and its deal are theirs, the rest stays unowned and comes last.
	me := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	owner.must(200, "PATCH", base+"/records/"+acme, map[string]any{"attributes": map[string]any{"owner": me}})
	owners = owner.must(200, "GET", base+"/revenue/forecast?quarter="+q, nil)["owners"].([]any)
	if len(owners) != 2 || owners[0].(map[string]any)["owner_id"] != me || owners[1].(map[string]any)["owner_id"] != nil {
		t.Fatalf("owners: %v", owners)
	}
	num(owners[0].(map[string]any), "won_minor", 0, "renewals_expected_minor", renewal, "deals_expected_minor", seats, "forecast_minor", renewal+seats, "open", 2)
	num(owners[1].(map[string]any), "won_minor", 500000, "deals_expected_minor", rollout, "forecast_minor", 500000+rollout, "open", 1)

	// A target, and weights of the brand's own.
	captured := track.Capture(t)
	owner.must(400, "PUT", base+"/revenue/forecast", map[string]any{"quarter": "2019-Q1", "target_minor": 1})
	owner.must(400, "PUT", base+"/revenue/forecast", map[string]any{"renewal_weights": map[string]any{"low": 120}})
	owner.must(400, "PUT", base+"/revenue/forecast", map[string]any{"stage_weights": map[string]any{"no_such_stage": 10}})
	set := owner.must(200, "PUT", base+"/revenue/forecast", map[string]any{"quarter": q, "target_minor": 3000000,
		"renewal_weights": map[string]any{"low": 25}, "stage_weights": map[string]any{first: 5}})
	forecast := 500000 + 1200000*25/100 + seats + 1000000*5/100
	num(set, "target_minor", 3000000, "forecast_minor", forecast, "gap_minor", 3000000-forecast)
	if set["attainment"].(float64) <= 0 {
		t.Fatalf("attainment: %v", set["attainment"])
	}
	found := false
	for _, ev := range captured() {
		found = found || ev.Name == "revenue_forecast.updated"
	}
	if !found {
		t.Fatal("no revenue_forecast.updated event")
	}
	// Only what is sent changes; null goes back to the default.
	again := owner.must(200, "PUT", base+"/revenue/forecast", map[string]any{"quarter": q, "renewal_weights": map[string]any{"low": nil}})
	num(again, "target_minor", 3000000)
	num(again["weights"].(map[string]any)["renewal"].(map[string]any), "low", 50)
	if st := again["weights"].(map[string]any)["stages"].([]any)[0].(map[string]any); st["weight"] != float64(5) || st["custom"] != true {
		t.Fatalf("stage kept: %v", st)
	}
	gone := owner.must(200, "PUT", base+"/revenue/forecast", map[string]any{"quarter": q, "target_minor": nil})
	if gone["target_minor"] != nil || gone["gap_minor"] != nil {
		t.Fatalf("target removed: %v", gone)
	}

	// Next quarter has no undated deals and nothing won.
	next := owner.must(200, "GET", base+"/revenue/forecast?quarter="+nextQuarter(now), nil)
	num(next["won"].(map[string]any), "count", 0)
	if len(next["deal_lines"].([]any)) != 0 {
		t.Fatalf("next quarter: %v", next["deal_lines"])
	}
	owner.must(400, "GET", base+"/revenue/forecast?quarter=soon", nil)

	// Another brand sees none of it.
	other := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Second"})["id"].(string)
	num(owner.must(200, "GET", "/v1/brands/"+other+"/revenue/forecast", nil), "forecast_minor", 0, "best_case_minor", 0)
}

func nextQuarter(d time.Time) string {
	first := time.Date(d.Year(), time.Month((int(d.Month())-1)/3*3+1), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 3, 0)
	return first.Format("2006") + "-Q" + string(rune('1'+(int(first.Month())-1)/3))
}

// The forecast week by week: a reading is kept once a day per brand, the
// last one of a week stands for it, and each week says how far it moved.
func TestForecastHistory(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	now := time.Now().UTC()
	day := now.Truncate(24 * time.Hour)
	monday := day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
	quarter := owner.must(200, "GET", base+"/revenue/forecast", nil)["quarter"].(string)
	keep := func(at time.Time, want int) {
		t.Helper()
		n, err := e.srv.KeepForecasts(ctx, at)
		if err != nil || n != want {
			t.Fatalf("kept %d, want %d: %v", n, want, err)
		}
	}
	history := func() []any {
		t.Helper()
		return owner.must(200, "GET", base+"/revenue/forecast", nil)["history"].([]any)
	}

	// A brand with no deals and no target has nothing to keep.
	keep(now, 0)
	if h := history(); len(h) != 0 {
		t.Fatalf("empty: %v", h)
	}

	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme"})["id"].(string)
	if _, err := e.pool.Exec(ctx, `INSERT INTO deals (workspace_id, brand_id, record_id, source, name, stage, state, amount_cents, currency, closed_at)
		VALUES ($1, $2, $3, 'manual', 'Acme pilot', 'won', 'won', 500000, 'USD', now())`, ws, brand, acme); err != nil {
		t.Fatal(err)
	}
	keep(now, 1)
	// The same day again: it already has its reading.
	keep(now, 0)
	h := history()
	if len(h) != 1 {
		t.Fatalf("one week: %v", h)
	}
	first := h[0].(map[string]any)
	if first["forecast_minor"] != float64(500000) || first["won_minor"] != float64(500000) || first["change_minor"] != nil || first["target_minor"] != nil {
		t.Fatalf("first week: %v", first)
	}
	if first["week"] != monday.Format(time.DateOnly) {
		t.Fatalf("week: %v", first["week"])
	}

	// Last week's reading was lower: this week shows how far it moved.
	if _, err := e.pool.Exec(ctx, `INSERT INTO revenue_forecast_history (workspace_id, brand_id, quarter, week, currency, won_minor,
			renewals_expected_minor, deals_expected_minor, forecast_minor, best_case_minor, taken_at)
		VALUES ($1, $2, $3, $4, 'USD', 100000, 0, 0, 100000, 100000, now() - interval '7 days')`,
		ws, brand, quarter, monday.AddDate(0, 0, -7)); err != nil {
		t.Fatal(err)
	}
	h = history()
	if len(h) != 2 || h[1].(map[string]any)["change_minor"] != float64(400000) {
		t.Fatalf("two weeks: %v", h)
	}

	// A day later the reading is replaced, not added: still one row for the week.
	if _, err := e.pool.Exec(ctx, `UPDATE revenue_forecast_history SET taken_at = taken_at - interval '1 day' WHERE brand_id = $1`, brand); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "PUT", base+"/revenue/forecast", map[string]any{"quarter": quarter, "target_minor": 900000})
	keep(now, 1)
	h = history()
	if len(h) != 2 || h[1].(map[string]any)["target_minor"] != float64(900000) {
		t.Fatalf("replaced: %v", h)
	}
}
