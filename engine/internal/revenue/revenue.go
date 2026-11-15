// Package revenue counts each brand's recurring revenue and how it moves.
//
// Billing connectors keep every customer's subscriptions
// (payment_subscriptions). A few times an hour this package adds them up
// per paying person or company in the brand's currency, compares each
// with what it brought in at the last count, and books the difference to
// the day (UTC) as new, expansion, reactivation, contraction or churned
// revenue. The day's row also keeps where the day ended: recurring
// revenue, paying customers, how much of it sits with customers whose
// health score is low, and how many records are in each band of each
// score. The Revenue page and API read those rows.
//
// The first count for a brand only notes where everyone stands: revenue
// that was already there is not booked as new.
package revenue

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/fxrates"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// HealthKey is the score whose low band counts as revenue at risk.
const HealthKey = "health"

// Service counts revenue in the background.
type Service struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
}

// Run counts every brand's revenue now and then every tick, until ctx
// ends.
func (s *Service) Run(ctx context.Context, tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		start := time.Now()
		if _, err := s.Round(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.Log, track.Event{Name: "revenue.count_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		metrics.SetRevenueRound(time.Since(start).Seconds())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Round counts the revenue of every brand that has subscriptions or
// scores, and reports how many brands it counted.
func (s *Service) Round(ctx context.Context) (int, error) {
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT workspace_id::text, brand_id::text FROM payment_subscriptions
			WHERE brand_id IS NOT NULL AND record_id IS NOT NULL
		UNION SELECT workspace_id::text, brand_id::text FROM score_definitions WHERE status = 'active'
		UNION SELECT workspace_id::text, brand_id::text FROM revenue_customers WHERE mrr_minor > 0`)
	if err != nil {
		return 0, err
	}
	type ref struct{ ws, brand string }
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ref, error) {
		var r ref
		return r, row.Scan(&r.ws, &r.brand)
	})
	if err != nil {
		return 0, err
	}
	n := 0
	var first error
	for _, r := range list {
		if err := Count(ctx, s.Pool, r.ws, r.brand); err != nil {
			metrics.RevenueCounted("error")
			if first == nil {
				first = err
			}
			continue
		}
		metrics.RevenueCounted("counted")
		n++
	}
	return n, first
}

// Count books a brand's recurring revenue as it stands now to today's
// row.
func Count(ctx context.Context, pool *pgxpool.Pool, ws, brand string) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var currency string
		// The brand's row is the lock: two counts never book the same move.
		err := tx.QueryRow(ctx, `SELECT currency FROM brands WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, brand, ws).Scan(&currency)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		rates, err := usdRates(ctx, tx)
		if err != nil {
			return err
		}
		// A merged record's last count belongs to the record kept.
		if _, err := tx.Exec(ctx, `WITH moved AS (
				DELETE FROM revenue_customers c USING records r WHERE c.brand_id = $1 AND r.id = c.record_id AND r.merged_into IS NOT NULL
				RETURNING r.merged_into AS record_id, c.workspace_id, c.brand_id, c.mrr_minor)
			INSERT INTO revenue_customers (record_id, workspace_id, brand_id, mrr_minor)
			SELECT record_id, workspace_id, brand_id, sum(mrr_minor) FROM moved GROUP BY 1, 2, 3
			ON CONFLICT (record_id) DO UPDATE SET mrr_minor = revenue_customers.mrr_minor + EXCLUDED.mrr_minor, updated_at = now()`, brand); err != nil {
			return err
		}

		// What each paying record brings in a month now.
		rows, err := tx.Query(ctx, `SELECT p.record_id::text, p.currency, sum(p.monthly_minor)
			FROM payment_subscriptions p JOIN records r ON r.id = p.record_id AND r.merged_into IS NULL AND r.deleted_at IS NULL
			WHERE p.workspace_id = $1 AND p.brand_id = $2 AND p.status IN ('active', 'past_due') AND p.monthly_minor > 0
			GROUP BY 1, 2`, ws, brand)
		if err != nil {
			return err
		}
		now := map[string]int64{}
		var rec, cur string
		var minor int64
		_, err = pgx.ForEachRow(rows, []any{&rec, &cur, &minor}, func() error {
			if v, ok := convert(rates, minor, cur, currency); ok {
				now[rec] += v
			}
			return nil
		})
		if err != nil {
			return err
		}

		// What it brought in at the last count.
		rows, err = tx.Query(ctx, `SELECT record_id::text, mrr_minor FROM revenue_customers WHERE brand_id = $1`, brand)
		if err != nil {
			return err
		}
		before := map[string]int64{}
		_, err = pgx.ForEachRow(rows, []any{&rec, &minor}, func() error {
			before[rec] = minor
			return nil
		})
		if err != nil {
			return err
		}
		var counted bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM revenue_days WHERE brand_id = $1)`, brand).Scan(&counted); err != nil {
			return err
		}

		var d day
		for rec, v := range now {
			d.MRR += v
			d.Customers++
			was, known := before[rec]
			if v == was && known {
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO revenue_customers (record_id, workspace_id, brand_id, mrr_minor) VALUES ($1, $2, $3, $4)
				ON CONFLICT (record_id) DO UPDATE SET mrr_minor = EXCLUDED.mrr_minor, updated_at = now()`, rec, ws, brand, v); err != nil {
				return err
			}
			switch {
			case !counted:
				// The first count: where everyone already stood.
			case !known:
				d.New += v
				d.NewCustomers++
			case was == 0:
				d.Reactivation += v
			case v > was:
				d.Expansion += v - was
			default:
				d.Contraction += was - v
			}
		}
		for rec, was := range before {
			if _, still := now[rec]; still || was == 0 {
				continue
			}
			// Kept at zero, so coming back later is a reactivation.
			if _, err := tx.Exec(ctx, `UPDATE revenue_customers SET mrr_minor = 0, updated_at = now() WHERE record_id = $1`, rec); err != nil {
				return err
			}
			if counted {
				d.Churned += was
				d.ChurnedCustomers++
			}
		}

		// Recurring revenue with customers whose health score is low.
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(c.mrr_minor), 0), count(*) FROM revenue_customers c JOIN records r ON r.id = c.record_id
			WHERE c.brand_id = $1 AND c.mrr_minor > 0 AND r.attributes->'scores'->>$2 = 'low'`, brand, HealthKey+"_band").Scan(&d.AtRisk, &d.AtRiskCustomers); err != nil {
			return err
		}
		bands, err := scoreBands(ctx, tx, ws, brand)
		if err != nil {
			return err
		}
		if d.Customers == 0 && len(bands) == 0 && !counted {
			// Nothing to report on yet.
			return nil
		}
		raw, err := json.Marshal(bands)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO revenue_days (workspace_id, brand_id, day, currency, mrr_minor, customers, at_risk_minor, at_risk_customers,
				new_minor, expansion_minor, reactivation_minor, contraction_minor, churned_minor, new_customers, churned_customers, bands)
			VALUES ($1, $2, (now() AT TIME ZONE 'UTC')::date, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
			ON CONFLICT (brand_id, day) DO UPDATE SET currency = EXCLUDED.currency, mrr_minor = EXCLUDED.mrr_minor, customers = EXCLUDED.customers,
				at_risk_minor = EXCLUDED.at_risk_minor, at_risk_customers = EXCLUDED.at_risk_customers,
				new_minor = revenue_days.new_minor + EXCLUDED.new_minor, expansion_minor = revenue_days.expansion_minor + EXCLUDED.expansion_minor,
				reactivation_minor = revenue_days.reactivation_minor + EXCLUDED.reactivation_minor,
				contraction_minor = revenue_days.contraction_minor + EXCLUDED.contraction_minor, churned_minor = revenue_days.churned_minor + EXCLUDED.churned_minor,
				new_customers = revenue_days.new_customers + EXCLUDED.new_customers, churned_customers = revenue_days.churned_customers + EXCLUDED.churned_customers,
				bands = EXCLUDED.bands, updated_at = now()`,
			ws, brand, currency, d.MRR, d.Customers, d.AtRisk, d.AtRiskCustomers, d.New, d.Expansion, d.Reactivation, d.Contraction, d.Churned,
			d.NewCustomers, d.ChurnedCustomers, raw)
		return err
	})
}

type day struct {
	MRR, AtRisk, New, Expansion, Reactivation, Contraction, Churned int64
	Customers, AtRiskCustomers, NewCustomers, ChurnedCustomers      int
}

// scoreBands counts the brand's records in each band of each active score.
func scoreBands(ctx context.Context, tx pgx.Tx, ws, brand string) (map[string]map[string]int, error) {
	rows, err := tx.Query(ctx, `SELECT d.key, r.attributes->'scores'->>(d.key || '_band'), count(*)
		FROM score_definitions d JOIN records r ON r.workspace_id = d.workspace_id AND r.brand_id = d.brand_id AND r.record_type = d.record_type
			AND r.attributes ? 'scores' AND r.merged_into IS NULL AND r.archived_at IS NULL AND r.deleted_at IS NULL
		WHERE d.workspace_id = $1 AND d.brand_id = $2 AND d.status = 'active' GROUP BY 1, 2`, ws, brand)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]int{}
	var key string
	var band *string
	var n int
	_, err = pgx.ForEachRow(rows, []any{&key, &band, &n}, func() error {
		if band == nil {
			return nil
		}
		if out[key] == nil {
			out[key] = map[string]int{}
		}
		out[key][*band] = n
		return nil
	})
	return out, err
}

// usdRates reads the latest US dollars per unit of each currency.
func usdRates(ctx context.Context, tx pgx.Tx) (map[string]float64, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (currency) currency, usd_per_unit::float8 FROM exchange_rates
		WHERE day > current_date - $1::int ORDER BY currency, day DESC`, int(fxrates.MaxAge.Hours()/24))
	if err != nil {
		return nil, err
	}
	out := map[string]float64{"USD": 1}
	var cur string
	var rate float64
	_, err = pgx.ForEachRow(rows, []any{&cur, &rate}, func() error {
		out[cur] = rate
		return nil
	})
	return out, err
}

// convert turns an amount in from's minor unit into to's, or reports
// false when a rate is missing.
func convert(rates map[string]float64, minor int64, from, to string) (int64, bool) {
	if from == to {
		return minor, true
	}
	a, okA := rates[from]
	b, okB := rates[to]
	if !okA || !okB {
		return 0, false
	}
	units := float64(minor) / math.Pow10(fxrates.MinorDigits(from))
	return int64(math.Round(units * a / b * math.Pow10(fxrates.MinorDigits(to)))), true
}

// Rates reads the latest US dollars per unit of each currency, for
// Convert.
func Rates(ctx context.Context, tx pgx.Tx) (map[string]float64, error) { return usdRates(ctx, tx) }

// Convert turns an amount in from's minor unit into to's, or reports
// false when a rate is missing.
func Convert(rates map[string]float64, minor int64, from, to string) (int64, bool) {
	return convert(rates, minor, from, to)
}
