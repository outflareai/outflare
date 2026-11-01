package searchinsights

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/digests"
)

func init() {
	digests.Register(digests.Builder{ID: "search_insights", Title: "Search work and collection", Build: buildDigest})
}
func buildDigest(ctx context.Context, q digests.Querier, ws string, p digests.Period) (digests.Section, error) {
	var s digests.Section
	var open, done, failed, reports float64
	err := q.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM search_insight_items WHERE workspace_id=$1 AND status IN ('open','in_progress'))::float8,
 (SELECT count(*) FROM search_insight_items WHERE workspace_id=$1 AND status='done' AND updated_at>=$2 AND updated_at<$3)::float8,
 (SELECT count(*) FROM jobs WHERE workspace_id=$1 AND kind='search_insights.refresh' AND status='dead' AND updated_at>=$2 AND updated_at<$3)::float8,
 (SELECT count(*) FROM search_report_runs WHERE workspace_id=$1 AND status='succeeded' AND created_at>=$2 AND created_at<$3)::float8`, ws, p.Start, p.End).Scan(&open, &done, &failed, &reports)
	if err != nil {
		return s, err
	}
	s.Metrics = []digests.Metric{{Key: "open_work", Label: "Open search actions", Value: open}, {Key: "completed_work", Label: "Search actions completed", Value: done}, {Key: "failed_collections", Label: "Failed search collections", Value: failed}, {Key: "report_snapshots", Label: "Report snapshots generated", Value: reports}}
	rows, err := q.Query(ctx, `SELECT x.title,s.host FROM search_insight_items x JOIN seo_sites s ON s.id=x.site_id AND s.workspace_id=x.workspace_id WHERE x.workspace_id=$1 AND x.status IN ('open','in_progress') ORDER BY x.updated_at DESC LIMIT 3`, ws)
	if err != nil {
		return s, err
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (digests.Item, error) {
		var title, host string
		err := r.Scan(&title, &host)
		return digests.Item{Text: host + ": " + title, Tab: "search-insights", Tone: "neutral"}, err
	})
	if err != nil {
		return s, err
	}
	s.Items = items
	if failed > 0 {
		s.Items = append(s.Items, digests.Item{Text: fmt.Sprintf("%.0f search collections failed. Review Activity and retry after resolving the connection or provider error.", failed), Tab: "search-insights", Tone: "bad"})
	}
	return s, nil
}
