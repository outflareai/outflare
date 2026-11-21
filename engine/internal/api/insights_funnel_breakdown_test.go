package api_test

import (
	"context"
	"testing"
	"time"
)

// A funnel can be split by a field of a later step, or of the person.
func TestFunnelBreakdownByStepAndPerson(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	person := func(name, attrs string) string {
		id := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": name})["id"].(string)
		if _, err := e.pool.Exec(ctx, `UPDATE records SET attributes = attributes || $2::jsonb WHERE id = $1`, id, attrs); err != nil {
			t.Fatal(err)
		}
		return id
	}
	for _, f := range []map[string]any{
		{"label": "Segment", "value_type": "text"},
		{"label": "Seats", "value_type": "number"},
		{"label": "Perks", "value_type": "text", "multiple": true},
		{"label": "Salary band", "value_type": "text", "sensitivity": "confidential"},
		{"label": "Work email", "value_type": "email"},
	} {
		owner.must(201, "POST", base+"/catalog/types/person/fields", f)
	}
	ana := person("Ana", `{"segment": "agency", "seats": 12, "perks": ["beta", "vip"]}`)
	bo := person("Bo", `{"segment": "agency"}`)
	cy := person("Cy", `{"segment": "saas"}`)

	day := 24 * time.Hour
	t0 := time.Now().UTC().Truncate(day).Add(-5*day + time.Hour)
	event := func(name string, record, anon any, at time.Time, props string) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, brand_id, record_id, anonymous_id, kind, name, occurred_at, properties)
			VALUES ($1, $2, $3, $4, 'track', $5, $6, $7::jsonb)`, ws, brand, record, anon, name, at, props); err != nil {
			t.Fatal(err)
		}
	}
	// Four people sign up. Ana picks pro, then team later (the first one
	// counts); Bo picks free; Cy and a stranger never pick a plan.
	event("signed_up", ana, nil, t0, `{}`)
	event("signed_up", bo, nil, t0, `{}`)
	event("signed_up", cy, nil, t0, `{}`)
	event("signed_up", nil, "b-stranger", t0, `{}`)
	event("plan_picked", ana, nil, t0.Add(time.Hour), `{"plan": "pro"}`)
	event("plan_picked", ana, nil, t0.Add(2*time.Hour), `{"plan": "team"}`)
	event("plan_picked", bo, nil, t0.Add(time.Hour), `{"plan": "free"}`)
	event("paid", ana, nil, t0.Add(3*time.Hour), `{}`)

	steps := []map[string]any{{"kind": "event", "event": "signed_up"}, {"kind": "event", "event": "plan_picked"}, {"kind": "event", "event": "paid"}}
	dates := map[string]any{"from": t0.Add(-day).Format(time.DateOnly), "to": time.Now().UTC().Format(time.DateOnly)}
	run := func(breakdown string) map[string][]float64 {
		t.Helper()
		res := owner.must(200, "POST", base+"/insights/funnel", map[string]any{"date_range": dates, "steps": steps, "breakdown": breakdown})
		if total := res["steps"].([]any); total[0].(map[string]any)["count"] != 4.0 || total[1].(map[string]any)["count"] != 2.0 || total[2].(map[string]any)["count"] != 1.0 {
			t.Fatalf("%s: the totals changed: %v", breakdown, total)
		}
		out := map[string][]float64{}
		for _, g := range res["breakdown"].([]any) {
			var counts []float64
			for _, s := range g.(map[string]any)["steps"].([]any) {
				counts = append(counts, s.(map[string]any)["count"].(float64))
			}
			out[g.(map[string]any)["value"].(string)] = counts
		}
		return out
	}
	same := func(what string, got map[string][]float64, want map[string][]float64) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: %v, want %v", what, got, want)
		}
		for k, w := range want {
			g := got[k]
			if len(g) != len(w) || g[0] != w[0] || g[1] != w[1] || g[2] != w[2] {
				t.Fatalf("%s: %v, want %v", what, got, want)
			}
		}
	}

	// By the plan of step 2: the row that took each person there, and
	// none for the two who never got there.
	same("step 2", run("step:2:property:plan"), map[string][]float64{"pro": {1, 1, 1}, "free": {1, 1, 0}, "": {2, 0, 0}})
	// Step 1 by number is the breakdown there has always been.
	same("step 1", run("step:1:property:plan"), map[string][]float64{"": {4, 2, 1}})
	// By a field of the person: text, a number, a list; a stranger has none.
	same("segment", run("person:segment"), map[string][]float64{"agency": {2, 2, 1}, "saas": {1, 0, 0}, "": {1, 0, 0}})
	same("seats", run("person:seats"), map[string][]float64{"12": {1, 1, 1}, "": {3, 1, 0}})
	same("perks", run("person:perks"), map[string][]float64{"beta,vip": {1, 1, 1}, "": {3, 1, 0}})

	// The catalog lists what people can be split by, and nothing else.
	cat := owner.must(200, "GET", base+"/insights/catalog", nil)
	keys := map[string]bool{}
	for _, f := range cat["person_fields"].([]any) {
		keys[f.(map[string]any)["key"].(string)] = true
	}
	if !keys["segment"] || !keys["seats"] || !keys["perks"] || !keys["country"] || keys["first_name"] || keys["salary_band"] || keys["work_email"] || keys["companies"] {
		t.Fatalf("person fields: %v", keys)
	}
	if len(cat["company_fields"].([]any)) == 0 {
		t.Fatalf("company fields: %v", cat["company_fields"])
	}

	// The people behind one value.
	people := owner.must(200, "POST", base+"/insights/funnel/people", map[string]any{
		"funnel": map[string]any{"date_range": dates, "steps": steps, "breakdown": "step:2:property:plan"}, "step": 2, "breakdown": "free"})
	if list := people["data"].([]any); len(list) != 1 || list[0].(map[string]any)["name"] != "Bo" {
		t.Fatalf("people who picked free: %v", people)
	}
	people = owner.must(200, "POST", base+"/insights/funnel/people", map[string]any{
		"funnel": map[string]any{"date_range": dates, "steps": steps, "breakdown": "person:segment"}, "step": 1, "breakdown": "agency"})
	if list := people["data"].([]any); len(list) != 2 {
		t.Fatalf("agency people: %v", people)
	}

	// In any order there is no step 2, but there is still a person.
	loose := map[string]any{"date_range": dates, "steps": steps, "order": "any", "breakdown": "person:segment"}
	res := owner.must(200, "POST", base+"/insights/funnel", loose)
	if n := len(res["breakdown"].([]any)); n != 3 {
		t.Fatalf("any order by person: %v", res["breakdown"])
	}
	for _, bad := range []string{"step:2:property:plan"} {
		loose["breakdown"] = bad
		owner.must(400, "POST", base+"/insights/funnel", loose)
	}
	for _, bad := range []string{"step:4:property:plan", "step:0:property:plan", "step:2:", "step:two:name", "step:2:colour", "person:", "person:has space",
		// Not a field, a name, a confidential field, an address.
		"person:nope", "person:first_name", "person:salary_band", "person:work_email"} {
		owner.must(400, "POST", base+"/insights/funnel", map[string]any{"date_range": dates, "steps": steps, "breakdown": bad})
	}
}
