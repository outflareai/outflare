package api

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/coverage"
)

// valueQuerier is a pool or a transaction.
type valueQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// maxValued is how many placements one call values, so a brand with tens
// of thousands catches up over a few reads instead of one slow one.
const maxValued = 5000

// brandTopic is what the brand is about: the searches its own sites rank
// for (the most searched first), or, before its SEO connection read any,
// its competitors'. Brand and competitor names are left out.
func brandTopic(ctx context.Context, q valueQuerier, ws, brand string) (*coverage.Topic, error) {
	rows, err := q.Query(ctx, `WITH own AS (
  SELECT h FROM seo_sites s, LATERAL (VALUES (s.host), ('www.'||s.host)) v(h) WHERE s.workspace_id=$1 AND s.brand_id=$2),
 theirs AS (
  SELECT h FROM seo_competitors c, LATERAL (VALUES (c.host), ('www.'||c.host)) v(h) WHERE c.workspace_id=$1 AND c.brand_id=$2 AND c.status='tracked'),
 pick AS (
  SELECT k.key, max(coalesce((k.metrics->>'volume')::numeric, 0)) AS volume, bool_or(k.host IN (SELECT h FROM own)) AS ours
  FROM seo_snapshots k WHERE k.workspace_id=$1 AND k.report='keywords' AND k.day > current_date - 120
   AND (k.host IN (SELECT h FROM own) OR k.host IN (SELECT h FROM theirs))
  GROUP BY k.key)
 SELECT key FROM pick ORDER BY ours DESC, volume DESC, key LIMIT 300`, ws, brand)
	if err != nil {
		return nil, err
	}
	phrases, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `SELECT name FROM brands WHERE workspace_id=$1 AND id=$2
 UNION ALL SELECT handle FROM brands WHERE workspace_id=$1 AND id=$2
 UNION ALL SELECT coalesce(nullif(name,''), host) FROM seo_competitors WHERE workspace_id=$1 AND brand_id=$2`, ws, brand)
	if err != nil {
		return nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	// A competitor's host ("rival.example") is a name too.
	for i, n := range names {
		names[i] = strings.Split(strings.TrimPrefix(n, "www."), ".")[0] + " " + n
	}
	return coverage.NewTopic(phrases, names), nil
}

// valueCompetitorPlacements values the brand's competitor placements:
// those on the pages given, or, with no pages, the ones not valued yet.
// A page's value depends on every competitor on it, so all of a page's
// rows are valued together.
func valueCompetitorPlacements(ctx context.Context, q valueQuerier, ws, brand string, urls []string) (int, error) {
	filter := "p.value IS NULL"
	args := []any{ws, brand, maxValued}
	if urls != nil {
		if len(urls) == 0 {
			return 0, nil
		}
		filter = "p.url = ANY($4)"
		args = append(args, urls)
	}
	rows, err := q.Query(ctx, `SELECT p.id::text, p.url, p.title, p.anchor, p.kind, p.arrangement='likely_paid', p.nofollow,
  p.domain_rating, p.url_rating, p.domain_traffic,
  (SELECT count(DISTINCT o.competitor_id) FROM coverage_competitor_placements o
    WHERE o.workspace_id=p.workspace_id AND o.brand_id=p.brand_id AND o.url=p.url AND o.gone_at IS NULL)::int
 FROM coverage_competitor_placements p WHERE p.workspace_id=$1 AND p.brand_id=$2 AND `+filter+` LIMIT $3`, args...)
	if err != nil {
		return 0, err
	}
	var ids, access []string
	var links []coverage.Link
	var values []float64
	var reasons []string // each joined by "\n", split again in SQL
	for rows.Next() {
		var id string
		var l coverage.Link
		if err := rows.Scan(&id, &l.URL, &l.Title, &l.Anchor, &l.Kind, &l.Paid, &l.Nofollow, &l.DomainRating, &l.URLRating, &l.DomainTraffic, &l.Competitors); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
		values = append(values, 0)
		access = append(access, "")
		reasons = append(reasons, "")
		links = append(links, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	topic, err := brandTopic(ctx, q, ws, brand)
	if err != nil {
		return 0, err
	}
	for i := range links {
		links[i].Topic = topic
		v := coverage.Value(links[i])
		values[i], access[i], reasons[i] = v.Value, v.Access, strings.Join(v.Reasons, "\n")
	}
	_, err = q.Exec(ctx, `UPDATE coverage_competitor_placements p SET value=v.value, access=v.access,
  value_reasons=CASE WHEN v.reasons='' THEN '{}'::text[] ELSE string_to_array(v.reasons, E'\n') END
 FROM unnest($3::uuid[], $4::float8[], $5::text[], $6::text[]) AS v(id, value, access, reasons)
 WHERE p.workspace_id=$1 AND p.brand_id=$2 AND p.id=v.id`, ws, brand, ids, values, access, reasons)
	return len(ids), err
}
