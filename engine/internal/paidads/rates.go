package paidads

import (
	"context"
	"math"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/fxrates"
)

// Rates are US dollars per unit of each currency (US dollars are 1): the
// latest daily reference rates Outflare keeps (fxrates). They turn every
// ad account's money into the brand's one currency, so an account billed
// in another currency adds up with the rest.
type Rates map[string]float64

// LoadRates reads the latest rate of each currency, no older than
// fxrates.MaxAge.
func LoadRates(ctx context.Context, q Querier) (Rates, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT ON (currency) currency, usd_per_unit::float8 FROM exchange_rates
		WHERE day > current_date - $1::int ORDER BY currency, day DESC`, int(fxrates.MaxAge.Hours()/24))
	if err != nil {
		return nil, err
	}
	out := Rates{"USD": 1}
	var cur string
	var rate float64
	_, err = pgx.ForEachRow(rows, []any{&cur, &rate}, func() error {
		out[cur] = rate
		return nil
	})
	return out, err
}

// Rate is how many units of to one unit of from buys; false when either
// has no rate. The same currency is always 1.
func (r Rates) Rate(from, to string) (float64, bool) {
	if from == to {
		return 1, true
	}
	a, okA := r[from]
	b, okB := r[to]
	if !okA || !okB || b == 0 {
		return 0, false
	}
	return a / b, true
}

// Micros converts micros of from into micros of to; false without a rate.
func (r Rates) Micros(v int64, from, to string) (int64, bool) {
	k, ok := r.Rate(from, to)
	if !ok {
		return v, false
	}
	return int64(math.Round(float64(v) * k)), true
}

// money adds a list of amounts up into to, in its minor unit. Amounts in a
// currency with no rate stay as they are, after it.
func (r Rates) money(list []Money, to string) []Money {
	if len(list) == 0 {
		return list
	}
	var sum float64
	found := false
	out := []Money{}
	for _, m := range list {
		k, ok := r.Rate(m.Currency, to)
		if !ok {
			out = append(out, m)
			continue
		}
		found = true
		sum += float64(m.AmountMinor) / math.Pow10(fxrates.MinorDigits(m.Currency)) * k * math.Pow10(fxrates.MinorDigits(to))
	}
	if found {
		out = append([]Money{{Currency: to, AmountMinor: int64(math.Round(sum))}}, out...)
	}
	return out
}

// convert turns the row's money into to, before judge works the costs
// out. A row whose account currency has no rate stays as it is.
func (row *PerformanceRow) convert(r Rates, to string) {
	row.AccountCurrency = row.Currency
	if r == nil || to == "" {
		return
	}
	k, ok := r.Rate(row.Currency, to)
	if !ok {
		return
	}
	row.SpendMicros = int64(math.Round(float64(row.SpendMicros) * k))
	row.PreviousSpendMicros = int64(math.Round(float64(row.PreviousSpendMicros) * k))
	row.Currency = to
	row.NewRevenue, row.ReturningRevenue, row.LifetimeRevenue = r.money(row.NewRevenue, to), r.money(row.ReturningRevenue, to), r.money(row.LifetimeRevenue, to)
	row.ModelRevenue = r.money(row.ModelRevenue, to)
	life := map[string]int64{}
	for cur, minor := range row.lifetime {
		for _, m := range r.money([]Money{{Currency: cur, AmountMinor: minor}}, to) {
			life[m.Currency] += m.AmountMinor
		}
	}
	row.lifetime = life
}
