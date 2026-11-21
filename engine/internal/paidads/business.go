package paidads

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/fxrates"
)

// The whole business: every payment the brand took against everything it
// spent on ads, whichever ad or channel got the credit. No attribution is
// involved in the headline number, so no network's own counting can move
// it. Beside it: what each network says it won against what Outflare
// counted, and whether the customers of each month earned back what the
// month's ads cost.

// BusinessQuery is one read of the whole business.
type BusinessQuery struct {
	WorkspaceID, BrandID string
	// From and To are dates, inclusive; payments are bucketed in Location
	// (UTC when nil), the networks' spend by their accounts' days.
	From, To time.Time
	Location *time.Location
	// Econ gives the currency everything is turned into, with Rates.
	Econ  Economics
	Rates Rates
	// Now is the present, for which cohorts have had their 30, 60, 90 and
	// 120 days; zero is time.Now.
	Now time.Time
}

// BusinessTotals are one period's numbers, money in micros of the brand's
// currency.
type BusinessTotals struct {
	// Every payment less every refund, from ads or not.
	RevenueMicros int64 `json:"revenue_micros"`
	// What every network charged.
	SpendMicros int64 `json:"spend_micros"`
	// Revenue over spend; nil without spend.
	RevenuePerSpend *float64 `json:"revenue_per_spend"`
	// People whose first ever payment fell in the period, and how many of
	// them an ad click brought.
	NewCustomers        int `json:"new_customers"`
	NewCustomersFromAds int `json:"new_customers_from_ads"`
	// Spend over every new customer; nil without one.
	CostPerNewCustomerMicros *int64 `json:"cost_per_new_customer_micros"`
}

// BusinessSpend is what one network charged.
type BusinessSpend struct {
	Connector   string `json:"connector"`
	SpendMicros int64  `json:"spend_micros"`
}

// BusinessWeek is one week (named by its Monday) of the range.
type BusinessWeek struct {
	Week          string          `json:"week"`
	RevenueMicros int64           `json:"revenue_micros"`
	SpendMicros   int64           `json:"spend_micros"`
	Spend         []BusinessSpend `json:"spend"`
}

// BusinessNetwork is one network over the range: what it charged, the
// conversions it says it won, and the new paying customers Outflare traced
// to its clicks.
type BusinessNetwork struct {
	Connector             string  `json:"connector"`
	SpendMicros           int64   `json:"spend_micros"`
	Share                 float64 `json:"share"`
	PlatformConversions   float64 `json:"platform_conversions"`
	NewPayers             int     `json:"new_payers"`
	CostPerNewPayerMicros *int64  `json:"cost_per_new_payer_micros"`
}

// CohortDays are the ages a cohort's revenue is read at.
var CohortDays = []int{30, 60, 90, 120}

// BusinessCohort is the people whose first payment fell in one month.
type BusinessCohort struct {
	Month        string `json:"month"`
	NewCustomers int    `json:"new_customers"`
	SpendMicros  int64  `json:"spend_micros"`
	// The month's ad spend over its new customers; nil without either.
	CostEachMicros *int64 `json:"cost_each_micros"`
	// What a customer had paid 30, 60, 90 and 120 days after their first
	// payment (CohortDays), averaged over the customers who have had that
	// long. Nil until at least half the cohort has.
	RevenueEachMicros []*int64 `json:"revenue_each_micros"`
	// Days until a customer has paid what they cost. PaybackEstimated says
	// the cohort isn't there yet and the number carries its pace forward.
	// Nil while there is nothing to go on, or at over a year.
	PaybackDays      *int `json:"payback_days"`
	PaybackEstimated bool `json:"payback_estimated"`
}

// Business is the whole-business report.
type Business struct {
	Currency string         `json:"currency"`
	Totals   BusinessTotals `json:"totals"`
	// Previous is the same number of days just before the range.
	Previous BusinessTotals    `json:"previous"`
	Weeks    []BusinessWeek    `json:"weeks"`
	Networks []BusinessNetwork `json:"networks"`
	Cohorts  []BusinessCohort  `json:"cohorts"`
	// LeftOut are currencies money was in that have no exchange rate, so
	// it is in none of the numbers.
	LeftOut []string `json:"left_out_currencies"`
}

// paySQL is every payment and refund of a brand as (record_id, occurred_at,
// name, currency, minor), a refund negative: the amounts as the ledger
// reads them (value in acquire.go), a refund with no amount taking its
// payment's back. $1 workspace, $2 brand, $3 the currency to assume.
var paySQL = `pay AS (
	SELECT x.record_id, x.occurred_at, x.name, x.currency,
		CASE WHEN x.name = 'payment.refunded' THEN -abs(CASE WHEN x.minor <> 0 THEN x.minor ELSE COALESCE((
			SELECT ` + minorSQL("o") + ` FROM customer_events o
			WHERE o.workspace_id = $1 AND o.record_id = x.record_id AND o.kind = 'track' AND o.name = 'payment.succeeded'
			  AND x.pid <> '' AND o.properties->>'payment_id' = x.pid ORDER BY o.occurred_at LIMIT 1), 0) END)
		ELSE x.minor END AS minor
	FROM (
		SELECT e.record_id, e.occurred_at, e.name, COALESCE(e.properties->>'payment_id', '') AS pid,
			CASE WHEN upper(btrim(COALESCE(e.properties->>'currency', ''))) ~ '^[A-Z]{3}$' THEN upper(btrim(e.properties->>'currency')) ELSE $3 END AS currency,
			` + minorSQL("e") + ` AS minor
		FROM customer_events e
		WHERE e.workspace_id = $1 AND e.kind = 'track' AND e.record_id IS NOT NULL AND e.name IN ('payment.succeeded', 'payment.refunded')
		  AND COALESCE(e.brand_id, outflare_first_brand(e.workspace_id)) = $2::uuid
	) x
), firsts AS (
	SELECT record_id, min(occurred_at) AS first_at FROM pay WHERE name = 'payment.succeeded' GROUP BY record_id
)`

// minorSQL reads an event's money in minor units: amount_minor, or revenue
// or value in major units.
func minorSQL(alias string) string {
	num := func(k string) string {
		f := alias + `.properties->>'` + k + `'`
		return `CASE WHEN ` + f + ` ~ '^-?[0-9]+(\.[0-9]+)?$' THEN (` + f + `)::numeric END`
	}
	return `COALESCE(round(` + num("amount_minor") + `), round(` + num("revenue") + ` * 100), round(` + num("value") + ` * 100), 0)::bigint`
}

// WholeBusiness reads the report.
func WholeBusiness(ctx context.Context, q Querier, p BusinessQuery) (Business, error) {
	loc := p.Location
	if loc == nil {
		loc = time.UTC
	}
	now := p.Now
	if now.IsZero() {
		now = time.Now()
	}
	cur := p.Econ.Currency
	if len(cur) != 3 {
		cur = "USD"
	}
	out := Business{Currency: cur, Weeks: []BusinessWeek{}, Networks: []BusinessNetwork{}, Cohorts: []BusinessCohort{}, LeftOut: []string{}}
	days := int(p.To.Sub(p.From).Hours()/24) + 1
	prevFrom, prevTo := p.From.AddDate(0, 0, -days), p.From.AddDate(0, 0, -1)
	// Cohorts: the month of To and the four before it.
	cohortFrom := time.Date(p.To.Year(), p.To.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -4, 0)
	start := prevFrom
	if cohortFrom.Before(start) {
		start = cohortFrom
	}
	date := func(t time.Time) string { return t.UTC().Format(time.DateOnly) }
	in := func(day string, from, to time.Time) bool { return day >= date(from) && day <= date(to) }
	leftOut := map[string]bool{}
	conv := func(micros int64, from string) (int64, bool) {
		if from == cur {
			return micros, true
		}
		v, ok := p.Rates.Micros(micros, from, cur)
		if !ok {
			leftOut[from] = true
		}
		return v, ok
	}
	minor := func(v int64, from string) (int64, bool) {
		return conv(int64(math.Round(float64(v)/math.Pow10(fxrates.MinorDigits(from))*1e6)), from)
	}
	weekOf := func(day string) string {
		t, _ := time.Parse(time.DateOnly, day)
		return date(t.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7)))
	}
	weeks := map[string]*BusinessWeek{}
	week := func(day string) *BusinessWeek {
		k := weekOf(day)
		if weeks[k] == nil {
			weeks[k] = &BusinessWeek{Week: k, Spend: []BusinessSpend{}}
		}
		return weeks[k]
	}
	nets := map[string]*BusinessNetwork{}
	net := func(c string) *BusinessNetwork {
		if nets[c] == nil {
			nets[c] = &BusinessNetwork{Connector: c}
		}
		return nets[c]
	}
	cohorts := map[string]*BusinessCohort{}
	for m := cohortFrom; !m.After(p.To); m = m.AddDate(0, 1, 0) {
		k := m.Format("2006-01")
		cohorts[k] = &BusinessCohort{Month: k, RevenueEachMicros: make([]*int64, len(CohortDays))}
	}

	// What the networks charged and claim, per day.
	rows, err := q.Query(ctx, `SELECT to_char(m.day, 'YYYY-MM-DD'), a.connector, a.currency, sum(m.spend_micros)::bigint, sum(COALESCE(m.conversions, 0))::float8
		FROM ad_metrics_daily m JOIN ad_entities e ON e.id = m.ad_entity_id JOIN ad_accounts a ON a.id = e.ad_account_id
		WHERE m.workspace_id = $1 AND e.workspace_id = $1 AND COALESCE(e.brand_id, a.brand_id) = $2::uuid AND e.level = 'campaign'
		  AND m.day BETWEEN $3::date AND $4::date GROUP BY 1, 2, 3`, p.WorkspaceID, p.BrandID, date(start), date(p.To))
	if err != nil {
		return out, err
	}
	var day, connector, currency string
	var amount int64
	var claimed float64
	if _, err := pgx.ForEachRow(rows, []any{&day, &connector, &currency, &amount, &claimed}, func() error {
		spend, ok := conv(amount, currency)
		if !ok {
			spend = 0
		}
		if c := cohorts[day[:7]]; c != nil {
			c.SpendMicros += spend
		}
		switch {
		case in(day, p.From, p.To):
			out.Totals.SpendMicros += spend
			n := net(connector)
			n.SpendMicros += spend
			n.PlatformConversions += claimed
			w := week(day)
			w.SpendMicros += spend
			at := -1
			for i := range w.Spend {
				if w.Spend[i].Connector == connector {
					at = i
				}
			}
			if at < 0 {
				w.Spend = append(w.Spend, BusinessSpend{Connector: connector})
				at = len(w.Spend) - 1
			}
			w.Spend[at].SpendMicros += spend
		case in(day, prevFrom, prevTo):
			out.Previous.SpendMicros += spend
		}
		return nil
	}); err != nil {
		return out, err
	}

	// Every payment and refund, per day in the brand's time zone.
	zone := loc.String()
	base := []any{p.WorkspaceID, p.BrandID, cur}
	rows, err = q.Query(ctx, `WITH `+paySQL+`
		SELECT to_char((occurred_at AT TIME ZONE $4)::date, 'YYYY-MM-DD'), currency, sum(minor)::bigint FROM pay
		WHERE occurred_at >= $5 AND occurred_at < $6 GROUP BY 1, 2`,
		append(base, zone, DayStart(prevFrom, loc), DayStart(p.To.AddDate(0, 0, 1), loc))...)
	if err != nil {
		return out, err
	}
	if _, err := pgx.ForEachRow(rows, []any{&day, &currency, &amount}, func() error {
		v, ok := minor(amount, currency)
		if !ok {
			return nil
		}
		if in(day, p.From, p.To) {
			out.Totals.RevenueMicros += v
			week(day).RevenueMicros += v
		} else {
			out.Previous.RevenueMicros += v
		}
		return nil
	}); err != nil {
		return out, err
	}

	// New customers by the day of their first payment, and the network
	// whose click brought them, if one did.
	rows, err = q.Query(ctx, `WITH `+paySQL+`
		SELECT to_char((f.first_at AT TIME ZONE $4)::date, 'YYYY-MM-DD'), COALESCE(q.connector, ''), count(*)::bigint
		FROM firsts f LEFT JOIN ad_acquisitions q ON q.brand_id = $2::uuid AND q.record_id = f.record_id AND q.first_paid_at IS NOT NULL
		WHERE f.first_at >= $5 AND f.first_at < $6 GROUP BY 1, 2`,
		append(base, zone, DayStart(start, loc), DayStart(p.To.AddDate(0, 0, 1), loc))...)
	if err != nil {
		return out, err
	}
	if _, err := pgx.ForEachRow(rows, []any{&day, &connector, &amount}, func() error {
		n := int(amount)
		if c := cohorts[day[:7]]; c != nil {
			c.NewCustomers += n
		}
		t := &out.Previous
		switch {
		case in(day, p.From, p.To):
			t = &out.Totals
			if connector != "" {
				net(connector).NewPayers += n
			}
		case !in(day, prevFrom, prevTo):
			return nil
		}
		t.NewCustomers += n
		if connector != "" {
			t.NewCustomersFromAds += n
		}
		return nil
	}); err != nil {
		return out, err
	}

	// What each month's customers had paid by 30, 60, 90 and 120 days, over
	// those who have had that long.
	type aged struct {
		people int
		micros int64
	}
	age := map[string][]aged{}
	for k := range cohorts {
		age[k] = make([]aged, len(CohortDays))
	}
	slot := map[int]int{}
	for i, d := range CohortDays {
		slot[d] = i
	}
	cohortArgs := append(base, zone, DayStart(cohortFrom, loc), DayStart(p.To.AddDate(0, 0, 1), loc), now, CohortDays)
	const matured = `FROM firsts f CROSS JOIN unnest($8::int[]) AS d(n)
		WHERE f.first_at >= $5 AND f.first_at < $6 AND f.first_at + d.n * interval '1 day' <= $7`
	var month string
	var n int
	rows, err = q.Query(ctx, `WITH `+paySQL+`
		SELECT to_char(f.first_at AT TIME ZONE $4, 'YYYY-MM'), d.n, count(*)::bigint `+matured+` GROUP BY 1, 2`, cohortArgs...)
	if err != nil {
		return out, err
	}
	if _, err := pgx.ForEachRow(rows, []any{&month, &n, &amount}, func() error {
		if a := age[month]; a != nil {
			a[slot[n]].people = int(amount)
		}
		return nil
	}); err != nil {
		return out, err
	}
	rows, err = q.Query(ctx, `WITH `+paySQL+`, m AS (
			SELECT f.record_id, f.first_at, d.n `+matured+`)
		SELECT to_char(m.first_at AT TIME ZONE $4, 'YYYY-MM'), m.n, pay.currency, sum(pay.minor)::bigint
		FROM m JOIN pay ON pay.record_id = m.record_id AND pay.occurred_at >= m.first_at AND pay.occurred_at < m.first_at + m.n * interval '1 day'
		GROUP BY 1, 2, 3`, cohortArgs...)
	if err != nil {
		return out, err
	}
	if _, err := pgx.ForEachRow(rows, []any{&month, &n, &currency, &amount}, func() error {
		if v, ok := minor(amount, currency); ok && age[month] != nil {
			age[month][slot[n]].micros += v
		}
		return nil
	}); err != nil {
		return out, err
	}

	finish := func(t *BusinessTotals) {
		if t.SpendMicros > 0 {
			v := float64(t.RevenueMicros) / float64(t.SpendMicros)
			t.RevenuePerSpend = &v
		}
		if t.NewCustomers > 0 && t.SpendMicros > 0 {
			v := t.SpendMicros / int64(t.NewCustomers)
			t.CostPerNewCustomerMicros = &v
		}
	}
	finish(&out.Totals)
	finish(&out.Previous)
	for _, w := range weeks {
		sort.Slice(w.Spend, func(a, b int) bool { return w.Spend[a].SpendMicros > w.Spend[b].SpendMicros })
		out.Weeks = append(out.Weeks, *w)
	}
	sort.Slice(out.Weeks, func(a, b int) bool { return out.Weeks[a].Week < out.Weeks[b].Week })
	for _, n := range nets {
		if out.Totals.SpendMicros > 0 {
			n.Share = float64(n.SpendMicros) / float64(out.Totals.SpendMicros)
		}
		if n.NewPayers > 0 && n.SpendMicros > 0 {
			v := n.SpendMicros / int64(n.NewPayers)
			n.CostPerNewPayerMicros = &v
		}
		out.Networks = append(out.Networks, *n)
	}
	sort.Slice(out.Networks, func(a, b int) bool {
		if out.Networks[a].SpendMicros != out.Networks[b].SpendMicros {
			return out.Networks[a].SpendMicros > out.Networks[b].SpendMicros
		}
		return out.Networks[a].Connector < out.Networks[b].Connector
	})
	for k, c := range cohorts {
		if c.NewCustomers > 0 && c.SpendMicros > 0 {
			v := c.SpendMicros / int64(c.NewCustomers)
			c.CostEachMicros = &v
		}
		for i, a := range age[k] {
			if a.people > 0 && a.people*2 >= c.NewCustomers {
				v := a.micros / int64(a.people)
				c.RevenueEachMicros[i] = &v
			}
		}
		c.PaybackDays, c.PaybackEstimated = payback(c.CostEachMicros, c.RevenueEachMicros)
		out.Cohorts = append(out.Cohorts, *c)
	}
	sort.Slice(out.Cohorts, func(a, b int) bool { return out.Cohorts[a].Month < out.Cohorts[b].Month })
	for c := range leftOut {
		out.LeftOut = append(out.LeftOut, c)
	}
	sort.Strings(out.LeftOut)
	return out, nil
}

// payback is the day a customer has paid what they cost: on the straight
// line between the ages that were read, or, when the last one read is
// still short, at the pace so far (estimated). Nil without a cost or an
// age to read, and when the pace takes over a year.
func payback(cost *int64, each []*int64) (*int, bool) {
	if cost == nil {
		return nil, false
	}
	prevDay, prev := 0, int64(0)
	read := false
	for i, v := range each {
		if v == nil {
			break
		}
		read = true
		if *v >= *cost {
			d := CohortDays[i]
			if *v > prev {
				d = prevDay + int(math.Ceil(float64(*cost-prev)/float64(*v-prev)*float64(CohortDays[i]-prevDay)))
			}
			return &d, false
		}
		prevDay, prev = CohortDays[i], *v
	}
	if !read || prev <= 0 {
		return nil, false
	}
	d := int(math.Ceil(float64(*cost) / float64(prev) * float64(prevDay)))
	if d > 365 {
		return nil, true
	}
	return &d, true
}
