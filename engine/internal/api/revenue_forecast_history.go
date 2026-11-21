package api

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The forecast week by week. The forecast is worked out when read, so on
// its own it can't say where it stood last week. A reading of each
// brand's current quarter is kept once a day, one row per week (the last
// reading of the week wins), and the forecast shows the weeks beside it.

// forecastWeek is what a quarter's forecast said in one week.
type forecastWeek struct {
	// Week is the Monday the week starts on.
	Week     string `json:"week"`
	Target   *int64 `json:"target_minor"`
	Won      int64  `json:"won_minor"`
	Renewals int64  `json:"renewals_expected_minor"`
	Deals    int64  `json:"deals_expected_minor"`
	Forecast int64  `json:"forecast_minor"`
	BestCase int64  `json:"best_case_minor"`
	Open     int    `json:"open"`
	// Change is the forecast less the week before's; null for the first
	// week kept.
	Change *int64 `json:"change_minor"`
}

// weekOf is the Monday of the week a day falls in.
func weekOf(d time.Time) time.Time {
	d = d.UTC().Truncate(24 * time.Hour)
	return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7))
}

// forecastHistory lists a quarter's weeks, the oldest first.
func forecastHistory(ctx context.Context, tx pgx.Tx, a *access, quarter string) ([]forecastWeek, error) {
	rows, err := tx.Query(ctx, `SELECT week::text, target_minor, won_minor, renewals_expected_minor, deals_expected_minor,
			forecast_minor, best_case_minor, open_count
		FROM revenue_forecast_history WHERE brand_id = $1 AND workspace_id = $2 AND quarter = $3 ORDER BY week`,
		a.Brand, a.WorkspaceID, quarter)
	if err != nil {
		return nil, err
	}
	out := []forecastWeek{}
	var w forecastWeek
	_, err = pgx.ForEachRow(rows, []any{&w.Week, &w.Target, &w.Won, &w.Renewals, &w.Deals, &w.Forecast, &w.BestCase, &w.Open}, func() error {
		x := w
		if n := len(out); n > 0 {
			d := x.Forecast - out[n-1].Forecast
			x.Change = &d
		}
		out = append(out, x)
		return nil
	})
	return out, err
}

// keepForecast saves what the forecast says now as this week's reading.
func keepForecast(ctx context.Context, tx pgx.Tx, a *access, f revenueForecast, now time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO revenue_forecast_history (workspace_id, brand_id, quarter, week, currency, target_minor,
			won_minor, renewals_expected_minor, deals_expected_minor, forecast_minor, best_case_minor, open_count, taken_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (brand_id, quarter, week) DO UPDATE SET currency = EXCLUDED.currency, target_minor = EXCLUDED.target_minor,
			won_minor = EXCLUDED.won_minor, renewals_expected_minor = EXCLUDED.renewals_expected_minor,
			deals_expected_minor = EXCLUDED.deals_expected_minor, forecast_minor = EXCLUDED.forecast_minor,
			best_case_minor = EXCLUDED.best_case_minor, open_count = EXCLUDED.open_count, taken_at = EXCLUDED.taken_at`,
		a.WorkspaceID, a.Brand, f.Quarter, weekOf(now), f.Currency, f.Target,
		f.Won.Total, f.Renewals.Expected, f.NewDeals.Expected+f.Expansion.Expected, f.Forecast, f.BestCase,
		f.Renewals.Count+f.NewDeals.Count+f.Expansion.Count, now)
	return err
}

// RunForecastHistory keeps each brand's forecast reading every interval,
// until ctx ends.
func (s *Server) RunForecastHistory(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := s.KeepForecasts(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.log, track.Event{Name: "forecast_history.sweep_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// KeepForecasts saves a reading for every brand that has a deal or a
// forecast target and no reading in the last 20 hours, and reports how
// many it saved. Each brand goes in its own transaction, so one that
// fails doesn't hold up the rest.
func (s *Server) KeepForecasts(ctx context.Context, now time.Time) (int, error) {
	rows, err := s.pool.Query(ctx, `SELECT b.workspace_id::text, b.id::text FROM brands b
		WHERE (EXISTS (SELECT 1 FROM revenue_forecast_settings f WHERE f.brand_id = b.id)
			OR EXISTS (SELECT 1 FROM deals d WHERE d.workspace_id = b.workspace_id AND d.brand_id = b.id))
		AND NOT EXISTS (SELECT 1 FROM revenue_forecast_history h WHERE h.brand_id = b.id AND h.taken_at > $1::timestamptz - interval '20 hours')
		ORDER BY b.id LIMIT 500`, now)
	if err != nil {
		return 0, err
	}
	type brand struct{ ws, id string }
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (brand, error) {
		var b brand
		return b, r.Scan(&b.ws, &b.id)
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, b := range all {
		a := &access{WorkspaceID: b.ws, Brand: b.id}
		err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			f, err := buildForecast(ctx, tx, a, quarterOf(now))
			if err != nil {
				return err
			}
			return keepForecast(ctx, tx, a, f, now)
		})
		if err != nil {
			if ctx.Err() != nil {
				return n, ctx.Err()
			}
			metrics.ForecastKept("error")
			code := "internal"
			if errors.Is(err, errNotFound) {
				code = "not_found"
			}
			track.Fail(ctx, s.log, track.Event{Name: "forecast_history.keep_failed", WorkspaceID: b.ws, ActorType: "system", Surface: "job"}, code, err)
			continue
		}
		metrics.ForecastKept("kept")
		n++
	}
	return n, nil
}
