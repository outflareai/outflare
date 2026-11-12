package linkorders

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/coverage"
)

// ImpactWindowDays is how many days before and after publication are
// included in a stored Search Console / rankings look.
const ImpactWindowDays = 28

// ImpactNote is returned on every impact payload. The figures do not
// claim that the link caused a change.
const ImpactNote = "These figures are stored Search Console and ranking snapshots around publication. They do not show that the link caused a change."

// Impact is stored Search Console and ranking points around an order's
// published_at. Causality is always false. No connector is called.
type Impact struct {
	PublishedAt   *time.Time     `json:"published_at"`
	TargetURL     string         `json:"target_url"`
	Causality     bool           `json:"causality"`
	Note          string         `json:"note"`
	SearchConsole ImpactConsole  `json:"search_console"`
	Rankings      ImpactRankings `json:"rankings"`
}

// ImpactConsole is daily Search Console totals for the target URL.
type ImpactConsole struct {
	Available bool             `json:"available"`
	Baseline  *ImpactTotals    `json:"baseline"`
	After     *ImpactTotals    `json:"after"`
	Series    []ImpactDayPoint `json:"series"`
}

// ImpactRankings is stored keyword-position snapshots for the target URL.
type ImpactRankings struct {
	Available bool              `json:"available"`
	Series    []ImpactRankPoint `json:"series"`
}

// ImpactTotals sums one side of the publication window.
type ImpactTotals struct {
	From        string   `json:"from"`
	To          string   `json:"to"`
	Clicks      int64    `json:"clicks"`
	Impressions int64    `json:"impressions"`
	Position    *float64 `json:"position"`
}

// ImpactDayPoint is one day of stored Search Console totals for the page.
type ImpactDayPoint struct {
	Day         string   `json:"day"`
	Clicks      int64    `json:"clicks"`
	Impressions int64    `json:"impressions"`
	Position    *float64 `json:"position"`
}

// ImpactRankPoint is one stored keyword position for the target page.
type ImpactRankPoint struct {
	Day      string   `json:"day"`
	Keyword  string   `json:"keyword"`
	Position *float64 `json:"position"`
	URL      string   `json:"url"`
}

// LoadImpact reads already-saved search_daily and seo_snapshots around
// the order's published_at. It does not call a provider. The order must
// already be published.
func LoadImpact(ctx context.Context, q Querier, workspaceID, brandID, orderID uuid.UUID) (*Impact, error) {
	o, err := GetOrder(ctx, q, workspaceID, brandID, orderID)
	if err != nil {
		return nil, err
	}
	if o.PublishedAt == nil {
		return nil, fmt.Errorf("%w: impact is available after the order is published", ErrConflict)
	}
	pub := o.PublishedAt.UTC()
	from := pub.AddDate(0, 0, -ImpactWindowDays)
	to := pub.AddDate(0, 0, ImpactWindowDays)
	pages := pageVariants(o.TargetURL)
	out := &Impact{
		PublishedAt:   o.PublishedAt,
		TargetURL:     o.TargetURL,
		Causality:     false,
		Note:          ImpactNote,
		SearchConsole: ImpactConsole{Series: []ImpactDayPoint{}},
		Rankings:      ImpactRankings{Series: []ImpactRankPoint{}},
	}

	rows, err := q.Query(ctx, `
		SELECT day::text,
		       coalesce(sum(clicks), 0),
		       coalesce(sum(impressions), 0),
		       CASE WHEN coalesce(sum(impressions), 0) > 0
		            THEN sum(position * impressions) / sum(impressions)
		            ELSE avg(position) END
		FROM search_daily
		WHERE workspace_id = $1
		  AND day BETWEEN $2::date AND $3::date
		  AND page = ANY($4)
		GROUP BY day
		ORDER BY day`,
		workspaceID, from, to, pages)
	if err != nil {
		return nil, err
	}
	series, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ImpactDayPoint, error) {
		var v ImpactDayPoint
		err := row.Scan(&v.Day, &v.Clicks, &v.Impressions, &v.Position)
		return v, err
	})
	if err != nil {
		return nil, err
	}
	if series == nil {
		series = []ImpactDayPoint{}
	}
	out.SearchConsole.Series = series
	out.SearchConsole.Available = len(series) > 0
	pubDay := pub.Format("2006-01-02")
	var before, after []ImpactDayPoint
	for _, p := range series {
		if p.Day < pubDay {
			before = append(before, p)
		} else {
			after = append(after, p)
		}
	}
	out.SearchConsole.Baseline = sumPoints(before)
	out.SearchConsole.After = sumPoints(after)

	rankRows, err := q.Query(ctx, `
		SELECT day::text, key, url, metrics
		FROM seo_snapshots
		WHERE workspace_id = $1 AND report = $2
		  AND day BETWEEN $3::date AND $4::date
		  AND key <> '' AND url = ANY($5)
		ORDER BY day, key, connector`,
		workspaceID, connectors.SEOKeywords, from, to, pages)
	if err != nil {
		return nil, err
	}
	ranks, err := pgx.CollectRows(rankRows, func(row pgx.CollectableRow) (ImpactRankPoint, error) {
		var v ImpactRankPoint
		var raw []byte
		err := row.Scan(&v.Day, &v.Keyword, &v.URL, &raw)
		var m connectors.SEOMetrics
		_ = json.Unmarshal(raw, &m)
		v.Position = m.Position
		return v, err
	})
	if err != nil {
		return nil, err
	}
	if ranks == nil {
		ranks = []ImpactRankPoint{}
	}
	out.Rankings.Series = ranks
	out.Rankings.Available = len(ranks) > 0
	return out, nil
}

func sumPoints(points []ImpactDayPoint) *ImpactTotals {
	if len(points) == 0 {
		return nil
	}
	v := &ImpactTotals{From: points[0].Day, To: points[len(points)-1].Day}
	var weighted, impressions float64
	for _, p := range points {
		v.Clicks += p.Clicks
		v.Impressions += p.Impressions
		if p.Position != nil && p.Impressions > 0 {
			weighted += *p.Position * float64(p.Impressions)
			impressions += float64(p.Impressions)
		}
	}
	if impressions > 0 {
		pos := weighted / impressions
		v.Position = &pos
	}
	return v
}

func pageVariants(raw string) []string {
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
		}
	}
	add(raw)
	if c, err := coverage.Canonical(raw); err == nil {
		add(c)
	}
	if u, err := url.Parse(strings.TrimSpace(raw)); err == nil && u.Host != "" {
		u.Fragment = ""
		u.Host = strings.ToLower(u.Host)
		if u.Path == "" {
			u.Path = "/"
		}
		add(u.String())
		noSlash := *u
		noSlash.Path = strings.TrimSuffix(u.Path, "/")
		if noSlash.Path == "" {
			noSlash.Path = "/"
		}
		add(noSlash.String())
		if strings.HasPrefix(strings.ToLower(u.Host), "www.") {
			u.Host = u.Host[4:]
			add(u.String())
		} else {
			u.Host = "www." + u.Host
			add(u.String())
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	return out
}
