package api_test

import (
	"context"
	"testing"
	"time"
)

// An optional step shows who did it without stopping those who didn't:
// they go on to the next step, and the step says how many skipped it.
func TestFunnelOptionalStep(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	person := func(name string) string {
		return owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": name})["id"].(string)
	}
	ana, bo, cy, di, ed := person("Ana"), person("Bo"), person("Cy"), person("Di"), person("Ed")

	day := 24 * time.Hour
	t0 := time.Now().UTC().Truncate(day).Add(-5*day + time.Hour)
	event := func(name, record string, at time.Time) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, brand_id, record_id, kind, name, occurred_at, properties)
			VALUES ($1, $2, $3, 'track', $4, $5, '{}')`, ws, brand, record, name, at); err != nil {
			t.Fatal(err)
		}
	}
	// Everyone lands. Ana signs up and pays; Bo pays without signing up;
	// Cy signs up and stops; Di does nothing more; Ed signed up before the
	// funnel's dates, so his payment has no sign-up after the landing.
	for _, p := range []string{ana, bo, cy, di, ed} {
		event("landed", p, t0)
	}
	event("signed_up", ed, t0.Add(-40*day))
	event("signed_up", ana, t0.Add(time.Hour))
	event("signed_up", cy, t0.Add(time.Hour))
	event("paid", ana, t0.Add(3*time.Hour))
	event("paid", bo, t0.Add(2*time.Hour))
	event("paid", ed, t0.Add(4*time.Hour))

	dates := map[string]any{"from": t0.Add(-day).Format(time.DateOnly), "to": time.Now().UTC().Format(time.DateOnly)}
	funnel := func(optional bool) map[string]any {
		signUp := map[string]any{"kind": "event", "event": "signed_up"}
		if optional {
			signUp["optional"] = true
		}
		return map[string]any{"date_range": dates, "steps": []map[string]any{{"kind": "event", "event": "landed"}, signUp, {"kind": "event", "event": "paid"}}}
	}
	step := func(res map[string]any, n int) map[string]any {
		return res["steps"].([]any)[n-1].(map[string]any)
	}

	// Required, as before: only Ana signs up and then pays.
	res := owner.must(200, "POST", base+"/insights/funnel", funnel(false))
	if step(res, 1)["count"] != 5.0 || step(res, 2)["count"] != 2.0 || step(res, 3)["count"] != 1.0 || step(res, 2)["dropped"] != 3.0 {
		t.Fatalf("required: %v", res["steps"])
	}
	if _, ok := step(res, 2)["optional"]; ok {
		t.Fatalf("a required step is not marked optional: %v", step(res, 2))
	}

	// Optional: the sign-up still shows its two, and all three payers count.
	res = owner.must(200, "POST", base+"/insights/funnel", funnel(true))
	s2, s3 := step(res, 2), step(res, 3)
	if step(res, 1)["count"] != 5.0 || s2["count"] != 2.0 || s3["count"] != 3.0 {
		t.Fatalf("optional counts: %v", res["steps"])
	}
	if s2["optional"] != true || s2["skipped"] != 2.0 || s2["dropped"] != 0.0 {
		t.Fatalf("the optional step: %v", s2)
	}
	// The last step is measured against step 1, the last required one.
	if s3["dropped"] != 2.0 || s3["conversion_from_previous"] != 0.6 || s3["conversion_from_start"] != 0.6 {
		t.Fatalf("the step after it: %v", s3)
	}
	// Bo took 2 hours from landing, Ana 2 from signing up, Ed 4 from landing.
	if s3["median_seconds"] != 7200.0 {
		t.Fatalf("time to the step after it: %v", s3["median_seconds"])
	}

	// The people behind the last step are all three payers, with their
	// times; those who dropped are the two who never paid.
	people := owner.must(200, "POST", base+"/insights/funnel/people", map[string]any{"funnel": funnel(true), "step": 3})["data"].([]any)
	if len(people) != 3 {
		t.Fatalf("people at step 3: %v", people)
	}
	for _, p := range people {
		m := p.(map[string]any)
		want := 2
		if m["name"] == "Ana" {
			want = 3
		}
		if len(m["times"].([]any)) != want {
			t.Fatalf("%s has %v times, want %d", m["name"], m["times"], want)
		}
	}
	dropped := owner.must(200, "POST", base+"/insights/funnel/people", map[string]any{"funnel": funnel(true), "step": 3, "dropped": true})["data"].([]any)
	names := map[string]bool{}
	for _, p := range dropped {
		names[p.(map[string]any)["name"].(string)] = true
	}
	if len(dropped) != 2 || !names["Cy"] || !names["Di"] {
		t.Fatalf("dropped before step 3: %v", dropped)
	}

	// Optional is for a step in the middle of an in-order funnel, and
	// nothing may count from one.
	first := funnel(false)
	first["steps"].([]map[string]any)[0]["optional"] = true
	last := funnel(false)
	last["steps"].([]map[string]any)[2]["optional"] = true
	anyOrder := funnel(true)
	anyOrder["order"] = "any"
	from := funnel(true)
	from["steps"].([]map[string]any)[2]["within"] = map[string]any{"value": 1, "unit": "day", "from": 2}
	between := funnel(true)
	between["exclusions"] = []map[string]any{{"kind": "event", "event": "refunded", "from": 2, "to": 3}}
	for name, bad := range map[string]map[string]any{"first": first, "last": last, "any order": anyOrder, "within from": from, "exclusion": between} {
		if got, _ := owner.do("POST", base+"/insights/funnel", bad); got != 400 {
			t.Fatalf("%s: got %d, want 400", name, got)
		}
	}
}
