package api_test

import (
	"context"
	"testing"
	"time"
)

// TestRetention covers cohorts by the day people first visited, how many
// came back on each later day, the average curve, a different return
// step, saved retention questions and bad specs.
func TestRetention(t *testing.T) {
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
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Ana visits 20, 13 and 6 days ago; Bo 20 and 6 days ago (twice that
	// day); an anonymous browser 6 days ago. Bo pays 6 days ago.
	for _, v := range []struct {
		anon   string
		record any
		at     time.Time
	}{
		{"a1", ana, d(20)}, {"a2", ana, d(13)}, {"a3", ana, d(6)}, {"b1", bo, d(20)}, {"b2", bo, d(6)}, {"b3", bo, d(6).Add(time.Hour)}, {"c1", nil, d(6)},
	} {
		exec(`INSERT INTO site_sessions (workspace_id, brand_id, anonymous_id, record_id, started_at, last_seen_at, landing_path)
			VALUES ($1, $2, $3, $4, $5, $5, '/')`, ws, brand, v.anon, v.record, v.at)
	}
	exec(`INSERT INTO transactions (workspace_id, brand_id, record_id, kind, amount_minor, brand_amount_minor, source, source_id, occurred_at)
		VALUES ($1, $2, $3, 'payment', 4900, 4900, 'stripe', 'in_1', $4)`, ws, brand, bo, d(6))

	from := today.Add(-20 * day).Format(time.DateOnly)
	to := today.Format(time.DateOnly)
	spec := map[string]any{"start": map[string]any{"kind": "visit"}, "date_range": map[string]any{"from": from, "to": to}, "interval": "day", "periods": 14}
	res := owner.must(200, "POST", base+"/insights/retention", spec)
	if res["interval"] != "day" || res["periods"] != float64(14) || res["return_label"] != "Did it again" {
		t.Fatalf("header: %v", res)
	}
	cohorts := res["cohorts"].([]any)
	if len(cohorts) != 21 {
		t.Fatalf("cohorts: %d", len(cohorts))
	}
	first := cohorts[0].(map[string]any)
	counts := first["counts"].([]any)
	if first["size"] != float64(2) || counts[0] != float64(2) || counts[7] != float64(1) || counts[14] != float64(2) || counts[1] != float64(0) {
		t.Fatalf("first cohort: %v", first)
	}
	// The browser that visited 6 days ago is its own cohort; the days
	// after today haven't begun.
	sixth := cohorts[14].(map[string]any)
	sc := sixth["counts"].([]any)
	if sixth["size"] != float64(1) || sc[0] != float64(1) || sc[6] != float64(0) || sc[7] != nil {
		t.Fatalf("sixth cohort: %v", sixth)
	}
	avg := res["average"].([]any)
	if avg[0] != float64(1) || avg[7] != float64(0.5) || avg[14] != float64(1) {
		t.Fatalf("average: %v", avg)
	}

	// Coming back to pay: only Bo, on day 14 of the first cohort.
	spec["return"] = map[string]any{"kind": "payment"}
	res = owner.must(200, "POST", base+"/insights/retention", spec)
	first = res["cohorts"].([]any)[0].(map[string]any)
	counts = first["counts"].([]any)
	if res["return_label"] != "Paid" || counts[0] != float64(0) || counts[14] != float64(1) {
		t.Fatalf("payment return: %v", first)
	}

	// Defaults: weeks, eight periods.
	res = owner.must(200, "POST", base+"/insights/retention", map[string]any{"start": map[string]any{"kind": "event", "event": "signed_up"}})
	if res["interval"] != "week" || res["periods"] != float64(8) {
		t.Fatalf("defaults: %v", res)
	}

	// Bad specs.
	owner.must(400, "POST", base+"/insights/retention", map[string]any{"start": map[string]any{"kind": "visit", "within": map[string]any{"value": 1, "unit": "day"}}})
	owner.must(400, "POST", base+"/insights/retention", map[string]any{"start": map[string]any{"kind": "visit"}, "periods": 99})
	owner.must(400, "POST", base+"/insights/retention", map[string]any{"start": map[string]any{"kind": "nope"}})

	// Saved.
	saved := owner.must(201, "POST", base+"/insights", map[string]any{"kind": "retention", "name": "Weekly visits", "spec": map[string]any{"start": map[string]any{"kind": "visit"}, "date_range": map[string]any{"preset": "last_90_days"}}})
	if saved["kind"] != "retention" {
		t.Fatalf("saved: %v", saved)
	}
	list := owner.must(200, "GET", base+"/insights?kind=retention", nil)
	if len(list["data"].([]any)) != 1 {
		t.Fatalf("list: %v", list)
	}
	owner.must(400, "POST", base+"/insights", map[string]any{"kind": "retention", "name": "Bad", "spec": map[string]any{"start": map[string]any{"kind": "visit"}, "periods": 0, "nope": 1}})
}
