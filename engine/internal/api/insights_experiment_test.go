package api_test

import (
	"context"
	"testing"
	"time"
)

// Funnels and trends split by an A/B test's version: experiment:<flag> is
// the version each visitor was first shown, and filters can keep one.
func TestInsightsByTestVersion(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	at := time.Now().UTC().Add(-48 * time.Hour)
	event := func(anon, name, props string, when time.Time) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, brand_id, anonymous_id, kind, name, properties, occurred_at)
			VALUES ($1, $2, $3, 'track', $4, $5::jsonb, $6)`, ws, brand, anon, name, props, when); err != nil {
			t.Fatal(err)
		}
	}
	// Four visitors see the pricing page; a and b were shown version b
	// (a also saw control later, which doesn't count), c the control, d no
	// version. a and c sign up.
	for i, anon := range []string{"v-a", "v-b", "v-c", "v-d"} {
		when := at.Add(time.Duration(i) * time.Minute)
		event(anon, "pricing_viewed", `{}`, when)
	}
	event("v-a", "experiment_viewed", `{"flag": "pricing-test", "variant": "b"}`, at.Add(-time.Minute))
	event("v-a", "experiment_viewed", `{"flag": "pricing-test", "variant": "control"}`, at.Add(time.Hour))
	event("v-b", "experiment_viewed", `{"flag": "pricing-test", "variant": "b"}`, at)
	event("v-c", "experiment_viewed", `{"flag": "pricing-test", "variant": "control"}`, at)
	event("v-c", "experiment_viewed", `{"flag": "other-test", "variant": "b"}`, at)
	event("v-a", "signed_up", `{}`, at.Add(10*time.Minute))
	event("v-c", "signed_up", `{}`, at.Add(10*time.Minute))

	dates := map[string]any{"from": at.Add(-24 * time.Hour).Format(time.DateOnly), "to": time.Now().UTC().Format(time.DateOnly)}
	res := owner.must(200, "POST", base+"/insights/funnel", map[string]any{
		"date_range": dates, "breakdown": "experiment:pricing-test",
		"steps": []map[string]any{{"kind": "event", "event": "pricing_viewed"}, {"kind": "event", "event": "signed_up"}},
	})
	got := map[string][2]float64{}
	for _, g := range res["breakdown"].([]any) {
		g := g.(map[string]any)
		st := g["steps"].([]any)
		got[g["value"].(string)] = [2]float64{st[0].(map[string]any)["count"].(float64), st[1].(map[string]any)["count"].(float64)}
	}
	if got["b"] != [2]float64{2, 1} || got["control"] != [2]float64{1, 1} || got[""] != [2]float64{1, 0} {
		t.Fatalf("funnel by version: %v", got)
	}

	// A trend kept to version b.
	tr := owner.must(200, "POST", base+"/insights/trend", map[string]any{"date_range": dates, "series": []map[string]any{{
		"kind": "event", "event": "pricing_viewed", "filters": []map[string]any{{"property": "experiment:pricing-test", "op": "eq", "value": "b"}},
	}}})
	if total := tr["series"].([]any)[0].(map[string]any)["total"]; total != 2.0 {
		t.Fatalf("trend kept to b: %v", total)
	}
	owner.must(400, "POST", base+"/insights/funnel", map[string]any{"date_range": dates, "breakdown": "experiment:Not A Key",
		"steps": []map[string]any{{"kind": "event", "event": "pricing_viewed"}, {"kind": "event", "event": "signed_up"}}})
}
