package api_test

import (
	"context"
	"testing"
	"time"
)

// A brand's own team and test accounts are left out of its reports while
// the setting is on, by email domain or address, and counted again when it
// is off. Strangers are never left out.
func TestInternalPeopleLeftOutOfReports(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	person := func(name, email string) string {
		return owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": name, "identities": []map[string]any{{"namespace": "email", "value": email}}})["id"].(string)
	}
	customer := person("Ana", "ana@acme.example")
	staff := person("Bo", "Bo@Northpeak.example")
	tester := person("Cy", "qa+1@tests.example")

	day := 24 * time.Hour
	t0 := time.Now().UTC().Truncate(day).Add(-3*day + time.Hour)
	event := func(name string, record, anon any, at time.Time) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, brand_id, record_id, anonymous_id, kind, name, occurred_at, properties)
			VALUES ($1, $2, $3, $4, 'track', $5, $6, '{}')`, ws, brand, record, anon, name, at); err != nil {
			t.Fatal(err)
		}
	}
	for _, who := range []string{customer, staff, tester} {
		event("signed_up", who, nil, t0)
		event("plan_picked", who, nil, t0.Add(time.Hour))
	}
	event("signed_up", nil, "b-stranger", t0)

	dates := map[string]any{"from": t0.Add(-day).Format(time.DateOnly), "to": time.Now().UTC().Format(time.DateOnly)}
	funnel := func() (float64, float64) {
		t.Helper()
		res := owner.must(200, "POST", base+"/insights/funnel", map[string]any{"date_range": dates,
			"steps": []map[string]any{{"kind": "event", "event": "signed_up"}, {"kind": "event", "event": "plan_picked"}}})
		steps := res["steps"].([]any)
		return steps[0].(map[string]any)["count"].(float64), steps[1].(map[string]any)["count"].(float64)
	}
	trend := func() float64 {
		t.Helper()
		res := owner.must(200, "POST", base+"/insights/trend", map[string]any{"date_range": dates, "interval": "day",
			"series": []map[string]any{{"kind": "event", "event": "signed_up", "measure": "people"}}})
		return res["series"].([]any)[0].(map[string]any)["total"].(float64)
	}

	// Nothing set: everyone counts.
	if got := owner.must(200, "GET", base+"/internal-people", nil); got["enabled"] != false || got["matched"] != 0.0 || got["updated_at"] != nil {
		t.Fatalf("default: %v", got)
	}
	if a, b := funnel(); a != 4 || b != 3 {
		t.Fatalf("before: %v %v", a, b)
	}

	for _, bad := range []map[string]any{
		{"enabled": true},
		{"enabled": true, "domains": []string{"not a domain"}, "emails": []string{}},
		{"enabled": true, "domains": []string{}, "emails": []string{"nope"}},
	} {
		owner.must(400, "PUT", base+"/internal-people", bad)
	}
	set := owner.must(200, "PUT", base+"/internal-people", map[string]any{"enabled": true,
		"domains": []string{"Northpeak.example", "northpeak.example"}, "emails": []string{" QA+1@tests.example "}})
	if set["matched"] != 2.0 || len(set["domains"].([]any)) != 1 || set["emails"].([]any)[0] != "qa+1@tests.example" {
		t.Fatalf("set: %v", set)
	}
	// The team member and the test account are out; the customer and the stranger stay.
	if a, b := funnel(); a != 2 || b != 1 {
		t.Fatalf("on: %v %v", a, b)
	}
	if n := trend(); n != 2 {
		t.Fatalf("trend on: %v", n)
	}
	paths := owner.must(200, "POST", base+"/insights/paths", map[string]any{"date_range": dates, "start": map[string]any{"kind": "event", "name": "signed_up"}})
	if paths["people"] != 2.0 {
		t.Fatalf("paths on: %v", paths["people"])
	}

	// Off again: they count, and the list is kept.
	off := owner.must(200, "PUT", base+"/internal-people", map[string]any{"enabled": false, "domains": []string{"northpeak.example"}, "emails": []string{"qa+1@tests.example"}})
	if off["enabled"] != false || off["matched"] != 2.0 {
		t.Fatalf("off: %v", off)
	}
	if a, b := funnel(); a != 4 || b != 3 {
		t.Fatalf("off: %v %v", a, b)
	}
}
