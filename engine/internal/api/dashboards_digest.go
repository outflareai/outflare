package api

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/digests"
	"github.com/outflareai/outflare/engine/internal/insights"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// Dashboards in the digest (docs/insights.md): a dashboard whose settings
// say so puts its trends' numbers in the workspace's digest, each over the
// digest's period against the one before, under the dashboard's name.

func init() {
	digests.Register(digests.Builder{ID: "dashboards", Title: "Dashboards", Build: dashboardsDigest})
}

// digestDashboards is the most dashboards one digest carries, and
// digestTiles the most tiles of each: a digest is a page, not a report.
const (
	digestDashboards = 5
	digestTiles      = 12
)

func dashboardsDigest(ctx context.Context, q digests.Querier, ws, brand string, p digests.Period) (digests.Section, error) {
	var out digests.Section
	rows, err := q.Query(ctx, `SELECT d.id::text, d.brand_id::text, d.name, (SELECT count(*) FROM brands b WHERE b.workspace_id = d.workspace_id) > 1
		FROM dashboards d WHERE d.workspace_id = $1 AND COALESCE(d.brand_id, outflare_first_brand(d.workspace_id)) = $3 AND d.settings->>'digest' = 'true' ORDER BY d.name LIMIT $2`, ws, digestDashboards, brand)
	if err != nil {
		return out, err
	}
	type dash struct {
		id, brand, name string
		manyBrands      bool
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (dash, error) {
		var d dash
		return d, r.Scan(&d.id, &d.brand, &d.name, &d.manyBrands)
	})
	if err != nil {
		return out, err
	}
	for _, d := range list {
		// The digest's period, as fixed dates in the brand's reporting time
		// zone (to is inclusive).
		var tz string
		if err := q.QueryRow(ctx, `SELECT timezone FROM brands WHERE id = $1`, d.brand).Scan(&tz); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return out, err
		}
		loc := zone(tz)
		rng := &dateRange{From: p.Start.In(loc).Format(time.DateOnly), To: p.End.In(loc).Add(-time.Second).Format(time.DateOnly)}
		tiles, err := q.Query(ctx, `SELECT i.name, i.spec FROM dashboard_tiles t JOIN insights i ON i.id = t.insight_id
			WHERE t.dashboard_id = $1 AND i.kind = 'trend' ORDER BY t.y, t.x, t.created_at LIMIT $2`, d.id, digestTiles)
		if err != nil {
			return out, err
		}
		type tile struct {
			name string
			spec []byte
		}
		specs, err := pgx.CollectRows(tiles, func(r pgx.CollectableRow) (tile, error) {
			var t tile
			return t, r.Scan(&t.name, &t.spec)
		})
		if err != nil {
			return out, err
		}
		prefix := d.name
		if d.manyBrands {
			var brand string
			if err := q.QueryRow(ctx, `SELECT name FROM brands WHERE id = $1`, d.brand).Scan(&brand); err != nil {
				return out, err
			}
			prefix = brand + " · " + d.name
		}
		for i, t := range specs {
			start := time.Now()
			tctx, cancel := context.WithTimeout(ctx, insightTimeout)
			res, err := runSavedInsight(tctx, q, ws, d.brand, "trend", t.spec, rng, true, start)
			cancel()
			if err != nil {
				// One tile that can't run (a spec that lost its event, a
				// slow query) leaves the digest's other numbers alone.
				outcome := "error"
				if _, inv := err.(insights.Invalid); inv {
					outcome = "invalid"
				} else if tctx.Err() != nil {
					outcome = "timeout"
				}
				metrics.InsightRun("digest", outcome, time.Since(start))
				continue
			}
			metrics.InsightRun("digest", "success", time.Since(start))
			tr := res.(insights.TrendResult)
			series := tr.Series
			if tr.Formula != nil {
				series = []insights.SeriesResult{*tr.Formula}
			}
			for j, sr := range series {
				if sr.Total == nil {
					continue
				}
				// One number (a formula, or a lone series) goes by the
				// trend's name; several by each series' label.
				label := sr.Label
				if len(series) == 1 {
					label = t.name
				}
				m := digests.Metric{Key: d.id + ":" + itoa(i) + ":" + itoa(j), Label: prefix + " · " + label, Value: *sr.Total}
				if tr.Previous != nil {
					prev := tr.Previous.Series
					if tr.Previous.Formula != nil {
						prev = []insights.SeriesResult{*tr.Previous.Formula}
					}
					if j < len(prev) && prev[j].Total != nil {
						v := *prev[j].Total
						m.Previous = &v
					}
				}
				out.Metrics = append(out.Metrics, m)
			}
		}
	}
	return out, nil
}
