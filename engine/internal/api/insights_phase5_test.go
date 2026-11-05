package api_test

import (
	"context"
	"testing"
	"time"
)

// TestInsightsPhase5 covers lifecycle (new, returning, resurrected,
// dormant), funnels in any order, correlation on people's properties and
// the saved lifecycle kind.
func TestInsightsPhase5(t *testing.T) {
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
	cy := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Cy"})["id"].(string)
	di := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Di"})["id"].(string)

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	day := 24 * time.Hour
	today := time.Now().UTC().Truncate(day)
	d := func(n int) time.Time { return today.Add(-time.Duration(n)*day + time.Hour) }
	event := func(name string, record any, anon any, at time.Time) {
		exec(`INSERT INTO customer_events (workspace_id, brand_id, record_id, anonymous_id, kind, name, occurred_at)
			VALUES ($1, $2, $3, $4, 'track', $5, $6)`, ws, brand, record, anon, name, at)
	}

	// Lifecycle by day over the last five days. Ana is active every day
	// (new on day 4, then returning). Bo was active 30 days ago and again
	// on day 3 (resurrected), then not on day 2 (dormant on day 2). Cy is
	// new on day 2 only (dormant on day 1). Di never does it.
	for n := 4; n >= 0; n-- {
		event("opened_app", ana, nil, d(n))
	}
	event("opened_app", bo, nil, d(30))
	event("opened_app", bo, nil, d(3))
	event("opened_app", cy, nil, d(2))
	event("opened_app", cy, nil, d(2).Add(time.Hour))

	spec := map[string]any{"step": map[string]any{"kind": "event", "event": "opened_app"}, "interval": "day",
		"date_range": map[string]any{"from": today.Add(-4 * day).Format(time.DateOnly), "to": today.Format(time.DateOnly)}}
	res := owner.must(200, "POST", base+"/insights/lifecycle", spec)
	if res["interval"] != "day" || res["label"] != "opened_app" {
		t.Fatalf("header: %v", res)
	}
	periods := res["periods"].([]any)
	if len(periods) != 5 {
		t.Fatalf("periods: %d", len(periods))
	}
	want := [][4]float64{
		{1, 0, 0, 0}, // day 4: Ana new
		{0, 1, 1, 0}, // day 3: Ana returning, Bo resurrected
		{1, 1, 0, 1}, // day 2: Cy new, Ana returning, Bo dormant
		{0, 1, 0, 1}, // day 1: Ana returning, Cy dormant
		{0, 1, 0, 0}, // today: Ana returning
	}
	for i, w := range want {
		p := periods[i].(map[string]any)
		got := [4]float64{p["new"].(float64), p["returning"].(float64), p["resurrected"].(float64), p["dormant"].(float64)}
		if got != w {
			t.Fatalf("period %d: got %v want %v (%v)", i, got, w, p)
		}
	}
	// Defaults to weeks; bad specs.
	res = owner.must(200, "POST", base+"/insights/lifecycle", map[string]any{"step": map[string]any{"kind": "visit"}})
	if res["interval"] != "week" {
		t.Fatalf("defaults: %v", res)
	}
	owner.must(400, "POST", base+"/insights/lifecycle", map[string]any{"step": map[string]any{"kind": "event", "event": "opened_app"}, "interval": "hour"})
	owner.must(400, "POST", base+"/insights/lifecycle", map[string]any{"step": map[string]any{"kind": "event", "event": "opened_app", "within": map[string]any{"value": 1, "unit": "day"}}})

	// Saved lifecycle question, run with the dashboard's dates.
	saved := owner.must(201, "POST", base+"/insights", map[string]any{"kind": "lifecycle", "name": "Weekly actives", "spec": map[string]any{"step": map[string]any{"kind": "event", "event": "opened_app"}, "date_range": map[string]any{"preset": "last_90_days"}}})
	if saved["kind"] != "lifecycle" {
		t.Fatalf("saved: %v", saved)
	}
	did := owner.must(201, "POST", base+"/dashboards", map[string]any{"name": "Actives"})["id"].(string)
	tile := owner.must(201, "POST", base+"/dashboards/"+did+"/tiles", map[string]any{"insight_id": saved["id"]})["tile_list"].([]any)[0].(map[string]any)["id"].(string)
	token := owner.must(201, "POST", base+"/dashboards/"+did+"/share", map[string]any{})["token"].(string)
	run := owner.must(200, "POST", "/v1/shared/dashboards/"+token+"/tiles/"+tile, map[string]any{"date_range": map[string]any{"preset": "last_30_days"}})
	if run["interval"] != "week" || len(run["periods"].([]any)) == 0 {
		t.Fatalf("run: %v", run)
	}
	owner.must(400, "POST", base+"/insights", map[string]any{"kind": "lifecycle", "name": "Bad", "spec": map[string]any{"step": map[string]any{"kind": "visit"}, "nope": 1}})

	// Any order: Ana does A then B, Bo does B then A, Cy only B, Di only
	// A. In strict order, three did A and only Ana did B after it; in any
	// order, all four did one step and Ana and Bo did both.
	event("a", ana, nil, d(6))
	event("b", ana, nil, d(6).Add(time.Hour))
	event("b", bo, nil, d(6))
	event("a", bo, nil, d(6).Add(time.Hour))
	event("b", cy, nil, d(6))
	event("a", di, nil, d(6))
	funnel := map[string]any{
		"date_range": map[string]any{"from": today.Add(-7 * day).Format(time.DateOnly), "to": today.Format(time.DateOnly)},
		"steps":      []map[string]any{{"kind": "event", "event": "a"}, {"kind": "event", "event": "b"}},
	}
	count := func(res map[string]any, i int) float64 {
		return res["steps"].([]any)[i].(map[string]any)["count"].(float64)
	}
	label := func(res map[string]any, i int) string {
		return res["steps"].([]any)[i].(map[string]any)["label"].(string)
	}
	res = owner.must(200, "POST", base+"/insights/funnel", funnel)
	if count(res, 0) != 3 || count(res, 1) != 1 {
		t.Fatalf("strict: %v", res["steps"])
	}
	funnel["order"] = "any"
	res = owner.must(200, "POST", base+"/insights/funnel", funnel)
	if count(res, 0) != 4 || count(res, 1) != 2 || label(res, 0) != "1 step" || label(res, 1) != "All 2 steps" {
		t.Fatalf("any order: %v", res["steps"])
	}
	// The people who did both, and who dropped off after one.
	people := owner.must(200, "POST", base+"/insights/funnel/people", map[string]any{"funnel": funnel, "step": 2})
	if len(people["data"].([]any)) != 2 {
		t.Fatalf("any-order people: %v", people)
	}
	people = owner.must(200, "POST", base+"/insights/funnel/people", map[string]any{"funnel": funnel, "step": 2, "dropped": true})
	if len(people["data"].([]any)) != 2 {
		t.Fatalf("any-order dropped: %v", people)
	}
	// No exclusions or per-step limits in any order; a bad order.
	owner.must(400, "POST", base+"/insights/funnel", map[string]any{"date_range": funnel["date_range"], "order": "any",
		"steps": []map[string]any{{"kind": "event", "event": "a"}, {"kind": "event", "event": "b", "within": map[string]any{"value": 1, "unit": "day"}}}})
	owner.must(400, "POST", base+"/insights/funnel", map[string]any{"date_range": funnel["date_range"], "order": "any", "steps": funnel["steps"],
		"exclusions": []map[string]any{{"kind": "event", "event": "c", "from": 1, "to": 2}}})
	owner.must(400, "POST", base+"/insights/funnel", map[string]any{"date_range": funnel["date_range"], "order": "sideways", "steps": funnel["steps"]})

	// Correlation on properties: the pro plan went with doing both steps,
	// free with dropping off (three people is the floor, so Ed and Fa join
	// the free ones). Emails are never compared, nor long text, nor a
	// value only one person has.
	ed := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ed"})["id"].(string)
	fa := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Fa"})["id"].(string)
	event("a", ed, nil, d(5))
	event("b", fa, nil, d(5))
	exec(`UPDATE records SET attributes = attributes || '{"plan": "free"}' WHERE id = $1 OR id = $2`, ed, fa)
	exec(`UPDATE records SET attributes = attributes || '{"plan": "pro", "email": "ana@acme.example", "bio": "A long story about Ana that goes on for well over one hundred characters so that it reads as text and not as a group of people."}' WHERE id = $1`, ana)
	exec(`UPDATE records SET attributes = attributes || '{"plan": "pro", "email": "bo@acme.example"}' WHERE id = $1`, bo)
	exec(`UPDATE records SET attributes = attributes || '{"plan": "pro", "email": "cy@acme.example"}' WHERE id = $1`, cy)
	exec(`UPDATE records SET attributes = attributes || '{"plan": "free", "seats": 3}' WHERE id = $1`, di)
	corr := owner.must(200, "POST", base+"/insights/funnel/correlation", map[string]any{"funnel": funnel})
	if corr["converted"] != 2.0 || corr["dropped"] != 4.0 {
		t.Fatalf("totals: %v", corr)
	}
	names := map[string]string{}
	for _, side := range []string{"positive", "negative"} {
		for _, it := range corr[side].([]any) {
			m := it.(map[string]any)
			names[m["name"].(string)] = side + ":" + m["kind"].(string)
		}
	}
	if names["plan: pro"] != "positive:property" || names["plan: free"] != "negative:property" {
		t.Fatalf("properties: %v", names)
	}
	for n := range names {
		if len(n) > 12 || n == "seats: 3" {
			t.Fatalf("unexpected item %q", n)
		}
	}
	for _, n := range []string{"email: ana@acme.example", "email: bo@acme.example"} {
		if _, ok := names[n]; ok {
			t.Fatalf("email correlated: %v", names)
		}
	}
}
