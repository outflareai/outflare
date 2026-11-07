package api

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// Right now: who is on the brand's website this minute, as its own tag
// sees it. Read live from the visits (site_sessions) and the events behind
// them; nothing is stored for it. The web app asks again every few
// seconds. It names no one: visitors are counts, and the feed of what is
// happening says the page, the event, the country and where the visit came
// from, never who.

func (s *Server) routesSiteLive(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/analytics/live", s.onBrand(auth.ScopeAnalyticsRead, s.siteLive))
}

const (
	// liveActive is how recently a visitor did something to count as on
	// the site now.
	liveActive = 5 * time.Minute
	// liveWindow is how far back the chart and the feed go.
	liveWindow = 30 * time.Minute
)

type liveCount struct {
	Key      string `json:"key"`
	Visitors int    `json:"visitors"`
}

type livePage struct {
	Website  string `json:"website"`
	Path     string `json:"path"`
	Visitors int    `json:"visitors"`
}

type liveMinute struct {
	At       time.Time `json:"at"`
	Visitors int       `json:"visitors"`
}

type liveEvent struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

// liveItem is one thing that just happened: a page view or an event.
type liveItem struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Name    string    `json:"name"`
	Label   string    `json:"label"`
	Website string    `json:"website"`
	Path    string    `json:"path"`
	Country string    `json:"country"`
	Source  string    `json:"source"`
	// Whether the visitor is a known person or company in the brand's
	// records; never who.
	Known bool `json:"known"`
}

type siteLiveReport struct {
	At time.Time `json:"at"`
	// Visitors active in the last five minutes, and in the last 30.
	Now       int          `json:"now"`
	Last30    int          `json:"last_30_minutes"`
	PerMinute []liveMinute `json:"per_minute"`
	Pages     []livePage   `json:"pages"`
	Sources   []liveCount  `json:"sources"`
	Countries []liveCount  `json:"countries"`
	Devices   []liveCount  `json:"devices"`
	// Events (not page views) in the last hour, most first.
	Events []liveEvent `json:"events"`
	Feed   []liveItem  `json:"feed"`
}

// liveSource is a visit's source, as the analytics report names it.
const liveSource = `COALESCE(NULLIF(source, ''), NULLIF(referrer_host, ''), CASE WHEN channel IN ('', 'direct') THEN '(direct)' ELSE channel END)`

func (s *Server) siteLive(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	now := time.Now().UTC()
	out := siteLiveReport{At: now, PerMinute: []liveMinute{}, Pages: []livePage{}, Sources: []liveCount{}, Countries: []liveCount{}, Devices: []liveCount{}, Events: []liveEvent{}, Feed: []liveItem{}}
	ws, brand := a.WorkspaceID, a.Brand
	active, window := now.Add(-liveActive), now.Add(-liveWindow)
	// A visit still going started within the day; the bound keeps the
	// read on the workspace's recent visits.
	day := now.Add(-24 * time.Hour)
	if err := s.pool.QueryRow(ctx, `SELECT count(DISTINCT anonymous_id) FILTER (WHERE last_seen_at >= $4), count(DISTINCT anonymous_id)
		FROM site_sessions WHERE workspace_id = $1 AND brand_id = $2 AND started_at >= $3 AND last_seen_at >= $5`,
		ws, brand, day, active, window).Scan(&out.Now, &out.Last30); err != nil {
		s.internal(w, r, err)
		return
	}
	for _, b := range []struct {
		expr string
		dst  *[]liveCount
	}{{liveSource, &out.Sources}, {`COALESCE(NULLIF(country, ''), '(none)')`, &out.Countries}, {`COALESCE(NULLIF(device, ''), '(none)')`, &out.Devices}} {
		rows, err := s.pool.Query(ctx, `SELECT `+b.expr+` AS k, count(DISTINCT anonymous_id) FROM site_sessions
			WHERE workspace_id = $1 AND brand_id = $2 AND started_at >= $3 AND last_seen_at >= $4
			GROUP BY k ORDER BY 2 DESC, k LIMIT 10`, ws, brand, day, active)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		got, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (liveCount, error) {
			var c liveCount
			return c, row.Scan(&c.Key, &c.Visitors)
		})
		if err != nil {
			s.internal(w, r, err)
			return
		}
		*b.dst = got
	}
	var err error
	if out.PerMinute, err = s.livePerMinute(ctx, ws, brand, window, now); err != nil {
		s.internal(w, r, err)
		return
	}
	// The page each active visitor is on: their latest page view.
	rows, err := s.pool.Query(ctx, `SELECT host, path, count(*) FROM (
			SELECT DISTINCT ON (anonymous_id) `+sitePageHost+` AS host, COALESCE(context->'page'->>'path', '') AS path
			FROM customer_events WHERE brand_id = $2 AND workspace_id = $1 AND name = 'page.viewed' AND occurred_at >= $3 AND anonymous_id IS NOT NULL
			ORDER BY anonymous_id, occurred_at DESC) p
		GROUP BY host, path ORDER BY 3 DESC, path LIMIT 10`, ws, brand, active)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out.Pages, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (livePage, error) {
		var p livePage
		return p, row.Scan(&p.Website, &p.Path, &p.Visitors)
	}); err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err = s.pool.Query(ctx, `SELECT e.name, COALESCE(d.label, ''), count(*) FROM customer_events e
		LEFT JOIN LATERAL (SELECT label FROM event_definitions d WHERE d.workspace_id = e.workspace_id AND d.name = e.name
			AND (d.brand_id = e.brand_id OR d.brand_id IS NULL) ORDER BY d.brand_id NULLS LAST LIMIT 1) d ON true
		WHERE e.brand_id = $2 AND e.workspace_id = $1 AND e.kind = 'track' AND e.occurred_at >= $3
		GROUP BY 1, 2 ORDER BY 3 DESC, 1 LIMIT 8`, ws, brand, now.Add(-time.Hour))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out.Events, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (liveEvent, error) {
		var e liveEvent
		return e, row.Scan(&e.Name, &e.Label, &e.Count)
	}); err != nil {
		s.internal(w, r, err)
		return
	}
	if out.Feed, err = s.liveFeed(ctx, ws, brand, window); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// livePerMinute is the visitors who viewed a page in each of the last 30
// minutes, oldest first, zero for a quiet minute.
func (s *Server) livePerMinute(ctx context.Context, ws, brand string, window, now time.Time) ([]liveMinute, error) {
	rows, err := s.pool.Query(ctx, `SELECT m, COALESCE(v, 0) FROM generate_series(date_trunc('minute', $3::timestamptz), date_trunc('minute', $4::timestamptz), interval '1 minute') m
		LEFT JOIN (SELECT date_trunc('minute', occurred_at) AS at, count(DISTINCT anonymous_id) AS v FROM customer_events
			WHERE brand_id = $2 AND workspace_id = $1 AND name = 'page.viewed' AND occurred_at >= $3
			GROUP BY 1) e ON e.at = m
		ORDER BY m`, ws, brand, window.Add(time.Minute), now)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (liveMinute, error) {
		var m liveMinute
		err := row.Scan(&m.At, &m.Visitors)
		m.At = m.At.UTC()
		return m, err
	})
}

// liveFeed is the last 30 page views and events, newest first, each with
// its visit's country and source.
func (s *Server) liveFeed(ctx context.Context, ws, brand string, window time.Time) ([]liveItem, error) {
	rows, err := s.pool.Query(ctx, `SELECT e.occurred_at, e.kind, e.name, COALESCE(d.label, ''),
			CASE WHEN e.kind = 'page' THEN `+sitePageHost+` ELSE '' END,
			COALESCE(e.context->'page'->>'path', ''), COALESCE(v.country, ''), COALESCE(v.src, ''), e.record_id IS NOT NULL
		FROM customer_events e
		LEFT JOIN LATERAL (SELECT label FROM event_definitions d WHERE d.workspace_id = e.workspace_id AND d.name = e.name
			AND (d.brand_id = e.brand_id OR d.brand_id IS NULL) ORDER BY d.brand_id NULLS LAST LIMIT 1) d ON e.kind = 'track'
		LEFT JOIN LATERAL (SELECT country, `+liveSource+` AS src FROM site_sessions
			WHERE workspace_id = e.workspace_id AND anonymous_id = e.anonymous_id AND last_seen_at >= $3
			ORDER BY last_seen_at DESC LIMIT 1) v ON e.anonymous_id IS NOT NULL
		WHERE e.brand_id = $2 AND e.workspace_id = $1 AND e.kind IN ('page', 'track') AND e.occurred_at >= $3
		ORDER BY e.occurred_at DESC, e.id DESC LIMIT 30`, ws, brand, window)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (liveItem, error) {
		var it liveItem
		err := row.Scan(&it.At, &it.Kind, &it.Name, &it.Label, &it.Website, &it.Path, &it.Country, &it.Source, &it.Known)
		it.At = it.At.UTC()
		if it.Website == "(none)" {
			it.Website = ""
		}
		return it, err
	})
}
