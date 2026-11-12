package api

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/insights"
)

// runSavedInsight answers a saved insight's spec as the dashboard, the
// share link, the digest and the alerts on a number do: with the saved
// dates unless rng replaces them, and, for a trend, against the period
// before when compare is set. The result is the same the run endpoints
// give (funnelOut, TrendResult or RetentionResult).
//
// Its days are the brand's, in its reporting time zone (B4a).
func runSavedInsight(ctx context.Context, q insights.Querier, ws, brand, kind string, spec []byte, rng *dateRange, compare bool, now time.Time) (any, error) {
	var tz string
	if err := q.QueryRow(ctx, `SELECT timezone FROM brands WHERE workspace_id = $1 AND id = $2`, ws, brand).Scan(&tz); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	now = now.In(zone(tz))
	switch kind {
	case "funnel":
		var f funnelSpec
		if err := json.Unmarshal(spec, &f); err != nil {
			return nil, err
		}
		if rng != nil {
			f.DateRange = *rng
		}
		fn, err := f.funnel(now)
		if err != nil {
			return nil, err
		}
		c, err := insights.Compile(ctx, q, ws, brand, fn)
		if err != nil {
			return nil, err
		}
		res, err := c.Run(ctx, q)
		if err != nil {
			return nil, err
		}
		return funnelOut{Result: res, From: fn.From, To: fn.To}, nil
	case "retention":
		var r retentionSpec
		if err := json.Unmarshal(spec, &r); err != nil {
			return nil, err
		}
		if rng != nil {
			r.DateRange = *rng
		}
		rt, err := r.retention(now)
		if err != nil {
			return nil, err
		}
		c, err := insights.CompileRetention(ctx, q, ws, brand, rt)
		if err != nil {
			return nil, err
		}
		return c.Run(ctx, q)
	case "lifecycle":
		var l lifecycleSpec
		if err := json.Unmarshal(spec, &l); err != nil {
			return nil, err
		}
		if rng != nil {
			l.DateRange = *rng
		}
		lt, err := l.lifecycle(now)
		if err != nil {
			return nil, err
		}
		c, err := insights.CompileLifecycle(ctx, q, ws, brand, lt)
		if err != nil {
			return nil, err
		}
		return c.Run(ctx, q)
	case "paths":
		var p pathsSpec
		if err := json.Unmarshal(spec, &p); err != nil {
			return nil, err
		}
		if rng != nil {
			p.DateRange = *rng
		}
		pt, err := p.paths(now)
		if err != nil {
			return nil, err
		}
		res, err := insights.RunPaths(ctx, q, ws, brand, pt)
		if err != nil {
			return nil, err
		}
		return pathsOut{PathsResult: res, From: pt.From, To: pt.To}, nil
	default:
		var t trendSpec
		if err := json.Unmarshal(spec, &t); err != nil {
			return nil, err
		}
		if rng != nil {
			t.DateRange = *rng
		}
		t.Compare = t.Compare || compare
		tr, err := t.trend(now)
		if err != nil {
			return nil, err
		}
		c, err := insights.CompileTrend(ctx, q, ws, brand, tr)
		if err != nil {
			return nil, err
		}
		return c.Run(ctx, q)
	}
}

// trendNumber is the one number a trend stands for: its formula's total,
// else its first series' total, with the same for the period before when
// the trend compared. nil when there is none.
func trendNumber(res insights.TrendResult) (value, previous *float64) {
	pick := func(r *insights.TrendResult) *float64 {
		if r == nil {
			return nil
		}
		if r.Formula != nil {
			return r.Formula.Total
		}
		if len(r.Series) > 0 {
			return r.Series[0].Total
		}
		return nil
	}
	return pick(&res), pick(res.Previous)
}
