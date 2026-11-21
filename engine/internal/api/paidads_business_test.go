package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// The whole business: every payment against every network's spend, what a
// network claims beside what Outflare counted, and payback by month.
func TestPaidAdsBusiness(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	exec := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	brand := exec(`SELECT outflare_first_brand($1)::text`, ws)
	base := "/v1/brands/" + brand + "/ads"
	owner.must(200, "PUT", base+"/economics", map[string]any{"currency": "USD", "target_roas": 3.0})

	integ := exec(`INSERT INTO integrations (brand_id, workspace_id, connector, name, credentials, status) VALUES ($1, $2, 'meta_ads', 'Meta Ads', '\x00', 'connected') RETURNING id::text`, brand, ws)
	acct := exec(`INSERT INTO ad_accounts (brand_id, workspace_id, integration_id, connector, external_id, name, currency) VALUES ($1, $2, $3, 'meta_ads', 'act_1', 'Northpeak', 'USD') RETURNING id::text`, brand, ws, integ)
	camp := exec(`INSERT INTO ad_entities (brand_id, workspace_id, ad_account_id, external_id, name, status) VALUES ($1, $2, $3, 'c1', 'Prospecting', 'active') RETURNING id::text`, brand, ws, acct)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	spend := func(daysAgo int, micros int64, conversions float64) {
		t.Helper()
		exec(`INSERT INTO ad_metrics_daily (ad_entity_id, day, workspace_id, currency, spend_micros, impressions, clicks, conversions, brand_id) VALUES ($1, $2::date, $3, 'USD', $4, 1000, 10, $5, $6) RETURNING 'x'`,
			camp, today.AddDate(0, 0, -daysAgo).Format(time.DateOnly), ws, micros, conversions, brand)
	}
	n := 0
	person := func() string {
		t.Helper()
		n++
		return exec(`INSERT INTO records (brand_id, workspace_id, record_type, name) VALUES ($1, $2, 'person', $3) RETURNING id::text`, brand, ws, fmt.Sprintf("Person %d", n))
	}
	event := func(rec, name, props string, at time.Time) {
		t.Helper()
		exec(`INSERT INTO customer_events (brand_id, workspace_id, record_id, kind, name, properties, occurred_at, message_id) VALUES ($1, $2, $3, 'track', $4, $5::jsonb, $6, gen_random_uuid()::text) RETURNING id::text`, brand, ws, rec, name, props, at)
	}
	at := func(daysAgo int) time.Time { return today.AddDate(0, 0, -daysAgo).Add(12 * time.Hour) }

	// This period: $200 of spend, the network claiming 5.
	spend(2, 120_000_000, 3)
	spend(5, 80_000_000, 2)
	// The period before: $100.
	spend(30, 100_000_000, 1)

	// Three new customers this period: one from an ad, two from elsewhere.
	fromAd := person()
	event(fromAd, "payment.succeeded", `{"amount_minor": 10000, "currency": "USD", "payment_id": "p1"}`, at(3))
	exec(`INSERT INTO ad_acquisitions (workspace_id, brand_id, record_id, connector, ad_account_id, campaign_entity_id, clicked_at, window_days, first_paid_at) VALUES ($1, $2, $3, 'meta_ads', $4, $5, $6, 30, $7) RETURNING id::text`,
		ws, brand, fromAd, acct, camp, at(4), at(3))
	a, b := person(), person()
	event(a, "payment.succeeded", `{"revenue": 250.5, "currency": "usd", "payment_id": "p2"}`, at(3))
	event(b, "payment.succeeded", `{"amount_minor": "5000", "payment_id": "p3"}`, at(6))
	// A refund with no amount takes its payment back.
	event(b, "payment.refunded", `{"payment_id": "p3"}`, at(1))
	// An old customer pays again: revenue, but not a new customer.
	old := person()
	event(old, "payment.succeeded", `{"amount_minor": 20000, "currency": "USD", "payment_id": "p0"}`, at(100))
	event(old, "payment.succeeded", `{"amount_minor": 20000, "currency": "USD", "payment_id": "p4"}`, at(2))
	// The period before: one new customer, $50.
	before := person()
	event(before, "payment.succeeded", `{"amount_minor": 5000, "currency": "USD", "payment_id": "p5"}`, at(35))

	var got struct {
		Currency   string   `json:"currency"`
		TargetROAS *float64 `json:"target_roas"`
		CohortDays []int    `json:"cohort_days"`
		Totals     struct {
			Revenue      int64    `json:"revenue_micros"`
			Spend        int64    `json:"spend_micros"`
			PerSpend     *float64 `json:"revenue_per_spend"`
			New          int      `json:"new_customers"`
			FromAds      int      `json:"new_customers_from_ads"`
			CostPerNewMu *int64   `json:"cost_per_new_customer_micros"`
		} `json:"totals"`
		Previous struct {
			Revenue int64 `json:"revenue_micros"`
			Spend   int64 `json:"spend_micros"`
			New     int   `json:"new_customers"`
		} `json:"previous"`
		Weeks []struct {
			Week    string `json:"week"`
			Revenue int64  `json:"revenue_micros"`
			Spend   int64  `json:"spend_micros"`
		} `json:"weeks"`
		Networks []struct {
			Connector string  `json:"connector"`
			Spend     int64   `json:"spend_micros"`
			Share     float64 `json:"share"`
			Says      float64 `json:"platform_conversions"`
			Counted   int     `json:"new_payers"`
			CostEach  *int64  `json:"cost_per_new_payer_micros"`
		} `json:"networks"`
		Cohorts []struct {
			Month       string   `json:"month"`
			New         int      `json:"new_customers"`
			RevenueEach []*int64 `json:"revenue_each_micros"`
		} `json:"cohorts"`
	}
	raw, _ := json.Marshal(owner.must(200, "GET", base+"/business", nil))
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	// 100 + 250.50 + 50 - 50 + 200.
	if got.Totals.Revenue != 550_500_000 || got.Totals.Spend != 200_000_000 {
		t.Fatalf("totals: revenue %d spend %d", got.Totals.Revenue, got.Totals.Spend)
	}
	if got.Totals.PerSpend == nil || *got.Totals.PerSpend < 2.75 || *got.Totals.PerSpend > 2.76 {
		t.Fatalf("revenue per spend: %v", got.Totals.PerSpend)
	}
	if got.Totals.New != 3 || got.Totals.FromAds != 1 || got.Totals.CostPerNewMu == nil || *got.Totals.CostPerNewMu != 66_666_666 {
		t.Fatalf("new customers: %+v", got.Totals)
	}
	if got.Previous.Revenue != 50_000_000 || got.Previous.Spend != 100_000_000 || got.Previous.New != 1 {
		t.Fatalf("previous: %+v", got.Previous)
	}
	if got.Currency != "USD" || got.TargetROAS == nil || *got.TargetROAS != 3 || len(got.CohortDays) != 4 {
		t.Fatalf("head: %s %v %v", got.Currency, got.TargetROAS, got.CohortDays)
	}
	if len(got.Networks) != 1 || got.Networks[0].Connector != "meta_ads" || got.Networks[0].Says != 5 || got.Networks[0].Counted != 1 ||
		got.Networks[0].Share != 1 || got.Networks[0].CostEach == nil || *got.Networks[0].CostEach != 200_000_000 {
		t.Fatalf("networks: %+v", got.Networks)
	}
	var weekRevenue, weekSpend int64
	for _, w := range got.Weeks {
		weekRevenue += w.Revenue
		weekSpend += w.Spend
	}
	if weekRevenue != got.Totals.Revenue || weekSpend != got.Totals.Spend {
		t.Fatalf("weeks don't add up: %+v", got.Weeks)
	}
	if len(got.Cohorts) != 5 {
		t.Fatalf("cohorts: %+v", got.Cohorts)
	}
	people := 0
	for _, c := range got.Cohorts {
		people += c.New
		if len(c.RevenueEach) != 4 {
			t.Fatalf("cohort %s: %v", c.Month, c.RevenueEach)
		}
	}
	// The customer of 100 days ago is in a cohort when their month is one
	// of the five; the other four always are.
	if people != 4 && people != 5 {
		t.Fatalf("cohort people: %d in %+v", people, got.Cohorts)
	}
	owner.must(400, "GET", base+"/business?from=nope", nil)
}
