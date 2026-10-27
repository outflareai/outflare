package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/seointel"
)

// Conversions by landing page for an SEO site, from the product analytics
// that counted them (PostHog), side by side with Search Console's clicks
// for the same pages. Reads only what the analytics sync stored.

func (s *Server) routesSEOConversions(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo-sites/{site}/conversions", s.inBrand(auth.ScopeSEORead, s.siteConversions))
}

type conversionTotals struct {
	Sessions             int     `json:"sessions"`
	Visitors             int     `json:"visitors"`
	Conversions          int     `json:"conversions"`
	ConversionRate       float64 `json:"conversion_rate"`
	SearchSessions       int     `json:"search_sessions"`
	SearchConversions    int     `json:"search_conversions"`
	SearchConversionRate float64 `json:"search_conversion_rate"`
}

type pageConversionView struct {
	URL                  string   `json:"url"`
	Keyword              *string  `json:"keyword"`
	Sessions             int      `json:"sessions"`
	Visitors             int      `json:"visitors"`
	Conversions          int      `json:"conversions"`
	ConversionRate       float64  `json:"conversion_rate"`
	SearchSessions       int      `json:"search_sessions"`
	SearchConversions    int      `json:"search_conversions"`
	SearchConversionRate float64  `json:"search_conversion_rate"`
	Clicks               *int     `json:"clicks"`
	Impressions          *int     `json:"impressions"`
	Position             *float64 `json:"position"`
}

var conversionSorts = map[string]string{
	"conversions":        "conversions DESC, sessions DESC",
	"search_conversions": "search_conversions DESC, search_sessions DESC",
	"sessions":           "sessions DESC, conversions DESC",
	"search_sessions":    "search_sessions DESC, search_conversions DESC",
	"conversion_rate":    "(conversions::float8 / NULLIF(sessions, 0)) DESC NULLS LAST, sessions DESC",
	"clicks":             "clicks DESC NULLS LAST, sessions DESC",
}

func conversionRate(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

func (s *Server) siteConversions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	site, ok := s.seoSiteFor(w, r, a)
	if !ok {
		return
	}
	ctx, ws, hosts := r.Context(), a.WorkspaceID, seointel.PageHosts(site.Host)
	q := r.URL.Query()
	sort := q.Get("sort")
	if sort == "" {
		sort = "conversions"
	}
	order, ok := conversionSorts[sort]
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "sort is conversions, search_conversions, sessions, search_sessions, conversion_rate or clicks")
		return
	}
	limit, err := limitParam(r, 100, 1000)
	if s.allocError(w, r, err) {
		return
	}
	// The source is the connection and site with the most visits to the
	// site's hosts lately, so a test project never adds to production's.
	src, err := seointel.ConversionSourceFor(ctx, s.pool, ws, site.Host)
	out := map[string]any{"source": nil, "data": []pageConversionView{}, "totals": conversionTotals{}}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if src == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	out["source"] = src
	to := time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	if src.NewestDay != nil {
		to = *src.NewestDay
	}
	end, _ := time.Parse(time.DateOnly, to)
	win := searchWindow{to: to, from: end.AddDate(0, 0, -27).Format(time.DateOnly)}
	for name, dst := range map[string]*string{"from": &win.from, "to": &win.to} {
		if v := q.Get(name); v != "" {
			if dateOnly(v) == "" {
				writeError(w, r, http.StatusBadRequest, "invalid_request", name+" is a date, YYYY-MM-DD")
				return
			}
			*dst = dateOnly(v)
		}
	}
	if win.from > win.to {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "from is after to")
		return
	}
	out["from"], out["to"] = win.from, win.to
	// Search Console's numbers for the same pages and days, when a
	// property covers the site.
	sc, err := seointel.SourceFor(ctx, s.pool, ws, site.Host)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	scID, scSite := "", ""
	if sc != nil {
		scID, scSite = sc.IntegrationID, sc.Property
	}
	rows, err := s.pool.Query(ctx, `
		WITH pages AS (
			SELECT 'https://' || lower(host) || path AS url, sum(sessions)::int AS sessions, sum(visitors)::int AS visitors,
			       sum(conversions)::int AS conversions, sum(search_sessions)::int AS search_sessions, sum(search_conversions)::int AS search_conversions
			FROM page_conversions
			WHERE workspace_id = $1 AND integration_id = $2 AND site = $3 AND lower(host) = ANY($4) AND day BETWEEN $5 AND $6
			GROUP BY 1
		), search AS (
			SELECT u.url, sum(sd.clicks)::int AS clicks, sum(sd.impressions)::int AS impressions,
			       sum(sd.position * sd.impressions) / NULLIF(sum(sd.impressions), 0) AS position
			FROM search_daily sd
			CROSS JOIN LATERAL (SELECT regexp_replace(regexp_replace(sd.page, '[?#].*$', ''), '^http://', 'https://') AS url) u
			WHERE sd.workspace_id = $1 AND sd.integration_id::text = $7 AND sd.site = $8 AND sd.day BETWEEN $5 AND $6
			  AND u.url IN (SELECT url FROM pages)
			GROUP BY 1
		)
		SELECT p.url, t.keyword, p.sessions, p.visitors, p.conversions, p.search_sessions, p.search_conversions, x.clicks, x.impressions, x.position
		FROM pages p
		LEFT JOIN search x ON x.url = p.url
		LEFT JOIN page_targets t ON t.workspace_id = $1 AND t.site_id = $9 AND t.url = p.url AND t.status = 'set'
		ORDER BY `+order+`, p.url LIMIT `+strconv.Itoa(limit),
		ws, src.IntegrationID, src.Site, hosts, win.from, win.to, scID, scSite, site.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (pageConversionView, error) {
		var v pageConversionView
		err := row.Scan(&v.URL, &v.Keyword, &v.Sessions, &v.Visitors, &v.Conversions, &v.SearchSessions, &v.SearchConversions, &v.Clicks, &v.Impressions, &v.Position)
		v.ConversionRate, v.SearchConversionRate = conversionRate(v.Conversions, v.Sessions), conversionRate(v.SearchConversions, v.SearchSessions)
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var t conversionTotals
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(sum(sessions), 0)::int, COALESCE(sum(visitors), 0)::int, COALESCE(sum(conversions), 0)::int,
		       COALESCE(sum(search_sessions), 0)::int, COALESCE(sum(search_conversions), 0)::int
		FROM page_conversions
		WHERE workspace_id = $1 AND integration_id = $2 AND site = $3 AND lower(host) = ANY($4) AND day BETWEEN $5 AND $6`,
		ws, src.IntegrationID, src.Site, hosts, win.from, win.to).Scan(&t.Sessions, &t.Visitors, &t.Conversions, &t.SearchSessions, &t.SearchConversions); err != nil {
		s.internal(w, r, err)
		return
	}
	t.ConversionRate, t.SearchConversionRate = conversionRate(t.Conversions, t.Sessions), conversionRate(t.SearchConversions, t.SearchSessions)
	if list != nil {
		out["data"] = list
	}
	out["totals"] = t
	writeJSON(w, http.StatusOK, out)
}
