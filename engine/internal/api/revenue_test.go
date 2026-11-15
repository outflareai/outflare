package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/revenue"
)

// Revenue: subscriptions are counted into days, with what was new, grew,
// shrank and left; the report adds what was collected, renewals coming
// up and the paying customers whose health score is low.
func TestRevenue(t *testing.T) {
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
	count := func() {
		t.Helper()
		if err := revenue.Count(ctx, e.pool, ws, brand); err != nil {
			t.Fatal(err)
		}
	}
	report := func() map[string]any {
		t.Helper()
		return owner.must(200, "GET", base+"/revenue?days=30", nil)
	}

	// Nothing to report on: every part is empty, and counting adds no day.
	count()
	got := report()
	if got["recurring"] != nil || got["collected"] != nil || len(got["periods"].([]any)) != 0 || len(got["renewals"].([]any)) != 0 || got["group"] != "day" {
		t.Fatalf("an empty report: %v", got)
	}
	owner.must(400, "GET", base+"/revenue?days=3", nil)

	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme"})["id"].(string)
	north := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Northpeak"})["id"].(string)
	var integ string
	if err := e.pool.QueryRow(ctx, `INSERT INTO integrations (workspace_id, connector, name, credentials) VALUES ($1, 'stripe', 'Stripe', '\x00') RETURNING id::text`, ws).Scan(&integ); err != nil {
		t.Fatal(err)
	}
	sub := func(id, rec string, monthly int, status string) {
		exec(`INSERT INTO payment_subscriptions (workspace_id, integration_id, provider, provider_id, record_id, customer_id, status, monthly_minor, currency, started_at)
			VALUES ($1, $2, 'stripe', $3, $4, $3, $5, $6, 'USD', now())
			ON CONFLICT (integration_id, provider_id) DO UPDATE SET status = EXCLUDED.status, monthly_minor = EXCLUDED.monthly_minor`, ws, integ, id, rec, status, monthly)
	}
	recurring := func() map[string]any {
		t.Helper()
		r, _ := report()["recurring"].(map[string]any)
		if r == nil {
			t.Fatal("no recurring revenue in the report")
		}
		return r
	}
	want := func(r map[string]any, pairs ...any) {
		t.Helper()
		for i := 0; i < len(pairs); i += 2 {
			if r[pairs[i].(string)] != float64(pairs[i+1].(int)) {
				t.Fatalf("%s = %v, want %v (%v)", pairs[i], r[pairs[i].(string)], pairs[i+1], r)
			}
		}
	}

	// The first count is where everyone already stood: nothing is new.
	sub("sub_a", acme, 10000, "active")
	count()
	want(recurring(), "mrr_minor", 10000, "arr_minor", 120000, "customers", 1, "new_minor", 0, "start_mrr_minor", 10000)

	// A new customer, and Acme grows. Counting twice books it once.
	sub("sub_n", north, 5000, "active")
	sub("sub_a", acme, 15000, "active")
	count()
	count()
	want(recurring(), "mrr_minor", 20000, "customers", 2, "new_minor", 5000, "new_customers", 1, "expansion_minor", 5000)

	// Acme shrinks and Northpeak leaves, then comes back.
	sub("sub_a", acme, 12000, "past_due")
	sub("sub_n", north, 5000, "canceled")
	count()
	r := recurring()
	want(r, "mrr_minor", 12000, "customers", 1, "contraction_minor", 3000, "churned_minor", 5000, "churned_customers", 1)
	if r["net_retention"].(float64) != 0.7 || r["gross_retention"].(float64) != 0.2 {
		// Started at 100: +50 expansion, -30 contraction, -50 churned; the
		// new 50 is not retention.
		t.Fatalf("retention: %v", r)
	}
	sub("sub_n", north, 4000, "active")
	count()
	want(recurring(), "mrr_minor", 16000, "reactivation_minor", 4000, "new_minor", 5000)
	periods := report()["periods"].([]any)
	if last := periods[len(periods)-1].(map[string]any); len(periods) != 30 || last["mrr_minor"] != float64(16000) || last["churned_minor"] != float64(5000) ||
		last["period"] != time.Now().UTC().Format(time.DateOnly) {
		t.Fatalf("the periods: %d, last %v", len(periods), last)
	}

	// A low health score puts Acme's revenue at risk.
	exec(`UPDATE records SET attributes = attributes || '{"scores": {"health": 20, "health_band": "low"}}' WHERE id = $1`, acme)
	count()
	want(recurring(), "at_risk_minor", 12000, "at_risk_customers", 1)
	got = report()
	risk := got["at_risk"].([]any)
	if len(risk) != 1 || risk[0].(map[string]any)["record_id"] != acme || risk[0].(map[string]any)["health"] != float64(20) {
		t.Fatalf("at risk: %v", risk)
	}

	// Renewals: Acme's own date, late ones too, but not one far out.
	soon := time.Now().UTC().AddDate(0, 0, 20).Format(time.DateOnly)
	owner.must(201, "POST", base+"/catalog/types/account/fields", map[string]any{"label": "Renews on", "value_type": "date"})
	owner.must(200, "PATCH", base+"/records/"+acme, map[string]any{"attributes": map[string]any{"renews_on": soon}})
	owner.must(200, "PATCH", base+"/records/"+north, map[string]any{"attributes": map[string]any{"renews_on": time.Now().UTC().AddDate(0, 0, 200).Format(time.DateOnly)}})
	renewals := report()["renewals"].([]any)
	if len(renewals) != 1 {
		t.Fatalf("renewals: %v", renewals)
	}
	if rn := renewals[0].(map[string]any); rn["record_id"] != acme || rn["renews_on"] != soon || rn["health_band"] != "low" || rn["mrr_minor"] != float64(12000) {
		t.Fatalf("the renewal: %v", rn)
	}

	// Collected: payments less refunds, against the days before.
	exec(`INSERT INTO transactions (workspace_id, brand_id, record_id, kind, amount_minor, brand_amount_minor, source, source_id, occurred_at) VALUES
		($1, $2, $3, 'payment', 9000, 9000, 'test', 'p1', now() - interval '2 days'),
		($1, $2, $3, 'refund', 1000, 1000, 'test', 'r1', now() - interval '1 day'),
		($1, $2, $3, 'payment', 7000, 7000, 'test', 'p0', now() - interval '40 days')`, ws, brand, acme)
	coll := report()["collected"].(map[string]any)
	want(coll, "paid_minor", 9000, "refunded_minor", 1000, "previous_paid_minor", 7000)
	// Acme paid more than in the days before.
	sp := report()["spending"].(map[string]any)
	if up := sp["up"].([]any); len(up) != 1 || len(sp["down"].([]any)) != 0 || up[0].(map[string]any)["record_id"] != acme || up[0].(map[string]any)["previous_paid_minor"] != float64(7000) {
		t.Fatalf("spending: %v", sp)
	}

	// By product: nothing until a payment names one, then each product
	// and what named none, the biggest first.
	if list := report()["products"].([]any); len(list) != 0 {
		t.Fatalf("products with none named: %v", list)
	}
	var product string
	if err := e.pool.QueryRow(ctx, `INSERT INTO products (workspace_id, brand_id, key, name) VALUES ($1, $2, 'residential', 'Residential') RETURNING id::text`, ws, brand).Scan(&product); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO transactions (workspace_id, brand_id, record_id, product_id, kind, amount_minor, brand_amount_minor, source, source_id, occurred_at) VALUES
		($1, $2, $3, $5, 'payment', 20000, 20000, 'test', 'pp1', now() - interval '3 days'),
		($1, $2, $4, $5, 'payment', 5000, 5000, 'test', 'pp2', now() - interval '3 days'),
		($1, $2, $3, $5, 'payment', 4000, 4000, 'test', 'pp0', now() - interval '45 days')`, ws, brand, acme, north, product)
	list := report()["products"].([]any)
	if len(list) != 2 {
		t.Fatalf("products: %v", list)
	}
	if top := list[0].(map[string]any); top["product_id"] != product || top["name"] != "Residential" {
		t.Fatalf("the top product: %v", top)
	} else {
		want(top, "paid_minor", 25000, "previous_paid_minor", 4000, "customers", 2, "refunded_minor", 0)
	}
	if rest := list[1].(map[string]any); rest["product_id"] != nil {
		t.Fatalf("what named no product: %v", rest)
	} else {
		want(rest, "paid_minor", 9000, "refunded_minor", 1000, "previous_paid_minor", 7000, "customers", 1)
	}

	// Another brand sees none of it.
	other := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Repocket", "handle": "repocket"})["id"].(string)
	if got := owner.must(200, "GET", "/v1/brands/"+other+"/revenue", nil); got["recurring"] != nil || got["collected"] != nil || len(got["at_risk"].([]any)) != 0 {
		t.Fatalf("another brand's report: %v", got)
	}

	// The background round counts every brand with subscriptions.
	if n, err := (&revenue.Service{Pool: e.pool}).Round(ctx); err != nil || n < 1 {
		t.Fatalf("a round: %d %v", n, err)
	}
}
