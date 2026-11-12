package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Site analytics: a brand's traffic as Outflare's own tag counted it
// (site_sessions and the page.viewed events behind them), in the same
// shape as a connected tool's reports, so Website analytics has numbers
// for a brand that connected no analytics tool. Computed live; nothing is
// stored beyond the visits themselves.

func (s *Server) routesSiteAnalytics(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/analytics/tracking", s.onBrand(auth.ScopeAnalyticsRead, s.siteAnalytics))
}

// sitePageRow is a page with the website (host) it is on, so pages of
// example.com and app.example.com with the same path stay apart.
type sitePageRow struct {
	connectors.AnalyticsRow
	Website string `json:"website"`
}

// Hosts, lowercased: a visit's landing page and a page view's page.
const (
	siteSessionHost = `COALESCE(NULLIF(lower(substring(landing_url FROM '^https?://([^/:?#]+)')), ''), '(none)')`
	sitePageHost    = `COALESCE(NULLIF(lower(context->'page'->>'host'), ''), NULLIF(lower(substring(context->'page'->>'url' FROM '^https?://([^/:?#]+)')), ''), '(none)')`
)

// siteAnalyticsReport is the brand's traffic from its own tag over a range.
type siteAnalyticsReport struct {
	From   string                      `json:"from"`
	To     string                      `json:"to"`
	Totals connectors.Metrics          `json:"totals"`
	Series []connectors.AnalyticsPoint `json:"series"`
	// Breakdowns, biggest first, at most 20 rows each.
	Pages     []sitePageRow             `json:"pages"`
	Sources   []connectors.AnalyticsRow `json:"sources"`
	Websites  []connectors.AnalyticsRow `json:"websites"`
	Countries []connectors.AnalyticsRow `json:"countries"`
	Devices   []connectors.AnalyticsRow `json:"devices"`
	Notes     []string                  `json:"notes"`
}

func (s *Server) siteAnalytics(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	// Today counts too: these are the tag's own visits, not a sync.
	q := r.URL.Query()
	// Days are the brand's (B4a), or ?timezone='s.
	loc, ok := s.reportZone(w, r)
	if !ok {
		return
	}
	to := civilDay(time.Now(), loc)
	out := siteAnalyticsReport{To: to.Format(time.DateOnly), From: to.AddDate(0, 0, -27).Format(time.DateOnly), Notes: []string{}}
	for name, dst := range map[string]*string{"from": &out.From, "to": &out.To} {
		if v := q.Get(name); v != "" {
			if dateOnly(v) == "" {
				writeError(w, r, http.StatusBadRequest, "invalid_request", name+" is a date, YYYY-MM-DD")
				return
			}
			*dst = dateOnly(v)
		}
	}
	from, _ := time.Parse(time.DateOnly, out.From)
	end, _ := time.Parse(time.DateOnly, out.To)
	if from.After(end) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "from is after to")
		return
	}
	if end.Sub(from) > 366*24*time.Hour {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "the range is at most a year")
		return
	}
	from, end = dayStart(from, loc), dayStart(end.AddDate(0, 0, 1), loc)
	ctx := r.Context()
	// website narrows every number to one host, as the websites list names it.
	website := strings.ToLower(strings.TrimSpace(q.Get("website")))
	args := []any{a.WorkspaceID, a.Brand, from, end, website}
	var err error
	if out.Totals, err = s.siteTotals(ctx, args); err != nil {
		s.internal(w, r, err)
		return
	}
	if out.Series, err = s.siteSeries(ctx, args, loc); err != nil {
		s.internal(w, r, err)
		return
	}
	if out.Pages, err = s.sitePages(ctx, args); err != nil {
		s.internal(w, r, err)
		return
	}
	// A visit's source: the campaign's or the site that sent it, else its
	// channel (direct has neither).
	sources := siteGroup(`COALESCE(NULLIF(source, ''), NULLIF(referrer_host, ''), CASE WHEN channel IN ('', 'direct') THEN '(direct)' ELSE channel END)`)
	websites := siteGroup(siteSessionHost)
	countries := siteGroup(`COALESCE(NULLIF(country, ''), '(none)')`)
	devices := siteGroup(`COALESCE(NULLIF(device, ''), '(none)')`)
	for _, b := range []struct {
		sql string
		dst *[]connectors.AnalyticsRow
	}{{sources, &out.Sources}, {websites, &out.Websites}, {countries, &out.Countries}, {devices, &out.Devices}} {
		if *b.dst, err = s.siteRows(ctx, b.sql, args); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	out.Notes = append(out.Notes, "Counted by Outflare's own tag on your site. Bots and crawlers are left out. A visitor who doesn't allow cookies counts once per visit.")
	writeJSON(w, http.StatusOK, out)
}

// siteGroup breaks the brand's visits down by one expression.
func siteGroup(expr string) string {
	return `SELECT ` + expr + ` AS k, count(DISTINCT anonymous_id)::float8, sum(pageviews)::float8, count(*)::float8
		FROM site_sessions
		WHERE workspace_id = $1 AND brand_id = $2 AND started_at >= $3 AND started_at < $4 AND ($5 = '' OR ` + siteSessionHost + ` = $5)
		GROUP BY k ORDER BY 4 DESC NULLS LAST, 3 DESC, k LIMIT 20`
}

// siteRows reads key, visitors, page views and visits.
func (s *Server) siteRows(ctx context.Context, sql string, args []any) ([]connectors.AnalyticsRow, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []connectors.AnalyticsRow{}
	for rows.Next() {
		var row connectors.AnalyticsRow
		if err := rows.Scan(&row.Key, &row.Visitors, &row.Pageviews, &row.Sessions); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// siteTotals: visitors, visits, page views, the share of visits that saw
// one page, and the average visit length.
func (s *Server) siteTotals(ctx context.Context, args []any) (connectors.Metrics, error) {
	var m connectors.Metrics
	err := s.pool.QueryRow(ctx, `SELECT count(DISTINCT anonymous_id)::float8, count(*)::float8, COALESCE(sum(pageviews), 0)::float8,
			CASE WHEN count(*) > 0 THEN (count(*) FILTER (WHERE pageviews <= 1))::float8 / count(*) END,
			avg(extract(epoch FROM last_seen_at - started_at))::float8
		FROM site_sessions
		WHERE workspace_id = $1 AND brand_id = $2 AND started_at >= $3 AND started_at < $4 AND ($5 = '' OR `+siteSessionHost+` = $5)`, args...).
		Scan(&m.Visitors, &m.Sessions, &m.Pageviews, &m.BounceRate, &m.AvgDuration)
	return m, err
}

// siteSeries is one point per day of the range, zero on days with none.
// Its days are in loc.
func (s *Server) siteSeries(ctx context.Context, args []any, loc *time.Location) ([]connectors.AnalyticsPoint, error) {
	rows, err := s.pool.Query(ctx, `SELECT to_char(d, 'YYYY-MM-DD'), COALESCE(v.visitors, 0)::float8, COALESCE(v.visits, 0)::float8, COALESCE(v.pageviews, 0)::float8
		FROM generate_series($3::timestamptz AT TIME ZONE $6, $4::timestamptz AT TIME ZONE $6 - interval '1 day', interval '1 day') d
		LEFT JOIN (
			SELECT (started_at AT TIME ZONE $6)::date AS day, count(DISTINCT anonymous_id) AS visitors, count(*) AS visits, sum(pageviews) AS pageviews
			FROM site_sessions
			WHERE workspace_id = $1 AND brand_id = $2 AND started_at >= $3 AND started_at < $4 AND ($5 = '' OR `+siteSessionHost+` = $5)
			GROUP BY 1
		) v ON v.day = d::date
		ORDER BY d`, append(slices.Clone(args), loc.String())...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []connectors.AnalyticsPoint{}
	for rows.Next() {
		var p connectors.AnalyticsPoint
		if err := rows.Scan(&p.Date, &p.Visitors, &p.Sessions, &p.Pageviews); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// sitePages is the top pages by page views, each with its website.
func (s *Server) sitePages(ctx context.Context, args []any) ([]sitePageRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+sitePageHost+` AS host, COALESCE(NULLIF(context->'page'->>'path', ''), '(none)') AS k,
			count(DISTINCT anonymous_id)::float8, count(*)::float8
		FROM customer_events
		WHERE workspace_id = $1 AND brand_id = $2 AND name = 'page.viewed' AND occurred_at >= $3 AND occurred_at < $4
			AND ($5 = '' OR `+sitePageHost+` = $5)
		GROUP BY host, k ORDER BY 4 DESC, host, k LIMIT 50`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []sitePageRow{}
	for rows.Next() {
		var row sitePageRow
		if err := rows.Scan(&row.Website, &row.Key, &row.Visitors, &row.Pageviews); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
