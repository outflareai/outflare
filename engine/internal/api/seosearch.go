package api

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/seointel"
)

// Search Console page by page, for one SEO site: clicks, impressions and
// positions by page and query, each page's history, how Google indexed the
// pages, and the sitemaps it read. The analytics sync stores them
// (analyticsync, search.go) from the workspace's own Search Console
// sign-in; these routes only read.

func (s *Server) routesSEOSearch(mux *http.ServeMux) {
	read := auth.ScopeSEORead
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo-sites/{site}/search", s.inBrand(read, s.siteSearch))
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo-sites/{site}/search/history", s.inBrand(read, s.siteSearchHistory))
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo-sites/{site}/indexing", s.inBrand(read, s.siteIndexing))
	mux.HandleFunc("GET /v1/workspaces/{ws}/seo-sites/{site}/sitemaps", s.inBrand(read, s.siteSitemaps))
}

type searchRowView struct {
	Page        *string `json:"page,omitempty"`
	Query       *string `json:"query,omitempty"`
	Clicks      int     `json:"clicks"`
	Impressions int     `json:"impressions"`
	CTR         float64 `json:"ctr"`
	Position    float64 `json:"position"`
}

type searchWindow struct {
	from, to string
}

// searchSource loads the site and the property its search data comes from.
func (s *Server) searchSource(w http.ResponseWriter, r *http.Request) (*seoSite, *seointel.Source, bool) {
	a := accessFrom(r.Context())
	site, ok := s.seoSiteFor(w, r, a)
	if !ok {
		return nil, nil, false
	}
	src, err := seointel.SourceFor(r.Context(), s.pool, a.WorkspaceID, site.Host)
	if err != nil {
		s.internal(w, r, err)
		return nil, nil, false
	}
	return site, src, true
}

// searchDates reads from and to: by default the 28 days that end on the
// newest day Search Console has sent.
func searchDates(w http.ResponseWriter, r *http.Request, src *seointel.Source) (searchWindow, bool) {
	to := time.Now().UTC().AddDate(0, 0, -1)
	if src != nil && src.NewestDay != nil {
		if d, err := time.Parse(time.DateOnly, *src.NewestDay); err == nil {
			to = d
		}
	}
	win := searchWindow{to: to.Format(time.DateOnly), from: to.AddDate(0, 0, -27).Format(time.DateOnly)}
	q := r.URL.Query()
	for name, dst := range map[string]*string{"from": &win.from, "to": &win.to} {
		if v := q.Get(name); v != "" {
			if dateOnly(v) == "" {
				writeError(w, r, http.StatusBadRequest, "invalid_request", name+" is a date, YYYY-MM-DD")
				return win, false
			}
			*dst = dateOnly(v)
		}
	}
	if win.from > win.to {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "from is after to")
		return win, false
	}
	return win, true
}

var (
	searchGroups = []string{"page", "query", "page_query"}
	searchSorts  = []string{"clicks", "impressions", "ctr", "position"}
)

func (s *Server) siteSearch(w http.ResponseWriter, r *http.Request) {
	site, src, ok := s.searchSource(w, r)
	if !ok {
		return
	}
	win, ok := searchDates(w, r, src)
	if !ok {
		return
	}
	q := r.URL.Query()
	group, sort := q.Get("group"), q.Get("sort")
	if group == "" {
		group = "page"
	}
	if sort == "" {
		sort = "clicks"
	}
	if !slices.Contains(searchGroups, group) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "group is page, query or page_query")
		return
	}
	if !slices.Contains(searchSorts, sort) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "sort is clicks, impressions, ctr or position")
		return
	}
	limit, err := limitParam(r, 100, 1000)
	if s.allocError(w, r, err) {
		return
	}
	out := map[string]any{"source": src, "from": win.from, "to": win.to, "group": group, "data": []searchRowView{},
		"totals": searchRowView{}}
	if src == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	args := []any{accessFrom(r.Context()).WorkspaceID, src.IntegrationID, src.Property, win.from, win.to, seointel.PageHosts(site.Host),
		q.Get("page"), q.Get("keyword"), strings.ToLower(strings.TrimSpace(q.Get("contains")))}
	where := `sd.workspace_id = $1 AND sd.integration_id = $2 AND sd.site = $3 AND sd.day BETWEEN $4 AND $5
		AND ` + seointel.PageHostSQL("sd.page") + ` = ANY($6)
		AND ($7 = '' OR sd.page = $7) AND ($8 = '' OR sd.query = $8)
		AND ($9 = '' OR strpos(lower(sd.query), $9) > 0 OR strpos(lower(sd.page), $9) > 0)`
	var cols string
	switch group {
	case "page":
		cols = "sd.page, NULL::text"
	case "query":
		cols = "NULL::text, sd.query"
	default:
		cols = "sd.page, sd.query"
	}
	order := map[string]string{"clicks": "clicks DESC, impressions DESC", "impressions": "impressions DESC, clicks DESC",
		"ctr": "ctr DESC, impressions DESC", "position": "position ASC, impressions DESC"}[sort]
	ctx := r.Context()
	rows, err := s.pool.Query(ctx, `
		SELECT p, qu, clicks, impressions, CASE WHEN impressions > 0 THEN clicks::float8 / impressions ELSE 0 END AS ctr, position
		FROM (SELECT `+cols+` AS x, sum(sd.clicks)::int AS clicks, sum(sd.impressions)::int AS impressions,
		             COALESCE(sum(sd.position * sd.impressions) / NULLIF(sum(sd.impressions), 0), 0) AS position
		      FROM search_daily sd WHERE `+where+` GROUP BY 1, 2) g(p, qu, clicks, impressions, position)
		ORDER BY `+order+`, p NULLS FIRST, qu NULLS FIRST LIMIT `+strconv.Itoa(limit), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (searchRowView, error) {
		var v searchRowView
		return v, row.Scan(&v.Page, &v.Query, &v.Clicks, &v.Impressions, &v.CTR, &v.Position)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var t searchRowView
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(sum(sd.clicks), 0)::int, COALESCE(sum(sd.impressions), 0)::int,
		       COALESCE(sum(sd.position * sd.impressions) / NULLIF(sum(sd.impressions), 0), 0)
		FROM search_daily sd WHERE `+where, args...).Scan(&t.Clicks, &t.Impressions, &t.Position); err != nil {
		s.internal(w, r, err)
		return
	}
	if t.Impressions > 0 {
		t.CTR = float64(t.Clicks) / float64(t.Impressions)
	}
	if list != nil {
		out["data"] = list
	}
	out["totals"] = t
	writeJSON(w, http.StatusOK, out)
}

type searchDayView struct {
	Day         string  `json:"day"`
	Clicks      int     `json:"clicks"`
	Impressions int     `json:"impressions"`
	CTR         float64 `json:"ctr"`
	Position    float64 `json:"position"`
}

// siteSearchHistory is one day per point for the site, a page or a query.
func (s *Server) siteSearchHistory(w http.ResponseWriter, r *http.Request) {
	site, src, ok := s.searchSource(w, r)
	if !ok {
		return
	}
	win, ok := searchDates(w, r, src)
	if !ok {
		return
	}
	q := r.URL.Query()
	out := map[string]any{"source": src, "from": win.from, "to": win.to, "data": []searchDayView{}}
	if src == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT d::date::text, COALESCE(sum(sd.clicks), 0)::int, COALESCE(sum(sd.impressions), 0)::int,
		       COALESCE(sum(sd.position * sd.impressions) / NULLIF(sum(sd.impressions), 0), 0)
		FROM generate_series($4::date, $5::date, interval '1 day') d
		LEFT JOIN search_daily sd ON sd.day = d::date AND sd.workspace_id = $1 AND sd.integration_id = $2 AND sd.site = $3
			AND `+seointel.PageHostSQL("sd.page")+` = ANY($6) AND ($7 = '' OR sd.page = $7) AND ($8 = '' OR sd.query = $8)
		GROUP BY d ORDER BY d`,
		accessFrom(r.Context()).WorkspaceID, src.IntegrationID, src.Property, win.from, win.to, seointel.PageHosts(site.Host), q.Get("page"), q.Get("keyword"))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (searchDayView, error) {
		var v searchDayView
		err := row.Scan(&v.Day, &v.Clicks, &v.Impressions, &v.Position)
		if v.Impressions > 0 {
			v.CTR = float64(v.Clicks) / float64(v.Impressions)
		}
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list != nil {
		out["data"] = list
	}
	writeJSON(w, http.StatusOK, out)
}

type inspectionView struct {
	URL              string     `json:"url"`
	Indexed          bool       `json:"indexed"`
	Verdict          string     `json:"verdict"`
	Coverage         string     `json:"coverage"`
	Indexing         string     `json:"indexing"`
	RobotsTxt        string     `json:"robots_txt"`
	PageFetch        string     `json:"page_fetch"`
	GoogleCanonical  string     `json:"google_canonical"`
	UserCanonical    string     `json:"user_canonical"`
	CanonicalDiffers bool       `json:"canonical_differs"`
	LastCrawl        *time.Time `json:"last_crawl"`
	CrawledAs        string     `json:"crawled_as"`
	Sitemaps         []string   `json:"sitemaps"`
	Mobile           string     `json:"mobile"`
	RichResults      string     `json:"rich_results"`
	Link             string     `json:"link"`
	PreviousVerdict  string     `json:"previous_verdict"`
	VerdictChangedAt *time.Time `json:"verdict_changed_at"`
	InspectedAt      time.Time  `json:"inspected_at"`
}

var indexingStates = []string{"indexed", "not_indexed", "canonical_differs", "changed"}

func (s *Server) siteIndexing(w http.ResponseWriter, r *http.Request) {
	site, src, ok := s.searchSource(w, r)
	if !ok {
		return
	}
	state := r.URL.Query().Get("state")
	if state != "" && !slices.Contains(indexingStates, state) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "state is indexed, not_indexed, canonical_differs or changed")
		return
	}
	limit, err := limitParam(r, 200, 1000)
	if s.allocError(w, r, err) {
		return
	}
	summary := map[string]int{"inspected": 0, "indexed": 0, "not_indexed": 0, "canonical_differs": 0, "changed": 0}
	out := map[string]any{"source": src, "summary": summary, "data": []inspectionView{}}
	if src == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	ctx := r.Context()
	args := []any{accessFrom(ctx).WorkspaceID, src.IntegrationID, src.Property, seointel.PageHosts(site.Host)}
	where := `i.workspace_id = $1 AND i.integration_id = $2 AND i.site = $3 AND ` + seointel.PageHostSQL("i.url") + ` = ANY($4)`
	var inspected, indexed, notIndexed, differs, changed int
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*)::int, count(*) FILTER (WHERE i.verdict = 'PASS')::int, count(*) FILTER (WHERE i.verdict <> 'PASS')::int,
		       count(*) FILTER (WHERE `+canonicalDiffers+`)::int,
		       count(*) FILTER (WHERE i.verdict_changed_at > now() - interval '7 days')::int
		FROM url_inspections i WHERE `+where, args...).Scan(&inspected, &indexed, &notIndexed, &differs, &changed); err != nil {
		s.internal(w, r, err)
		return
	}
	summary["inspected"], summary["indexed"], summary["not_indexed"], summary["canonical_differs"], summary["changed"] =
		inspected, indexed, notIndexed, differs, changed
	switch state {
	case "indexed":
		where += ` AND i.verdict = 'PASS'`
	case "not_indexed":
		where += ` AND i.verdict <> 'PASS'`
	case "canonical_differs":
		where += ` AND ` + canonicalDiffers
	case "changed":
		where += ` AND i.verdict_changed_at > now() - interval '7 days'`
	}
	rows, err := s.pool.Query(ctx, `
		SELECT i.url, i.verdict, i.coverage, i.indexing, i.robots_txt, i.page_fetch, i.google_canonical, i.user_canonical, `+canonicalDiffers+`,
		       i.last_crawl, i.crawled_as, i.sitemaps, i.mobile, i.rich_results, i.link, i.previous_verdict, i.verdict_changed_at, i.inspected_at
		FROM url_inspections i WHERE `+where+`
		ORDER BY (i.verdict = 'PASS'), i.verdict_changed_at DESC NULLS LAST, i.url LIMIT `+strconv.Itoa(limit), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (inspectionView, error) {
		var v inspectionView
		err := row.Scan(&v.URL, &v.Verdict, &v.Coverage, &v.Indexing, &v.RobotsTxt, &v.PageFetch, &v.GoogleCanonical, &v.UserCanonical,
			&v.CanonicalDiffers, &v.LastCrawl, &v.CrawledAs, &v.Sitemaps, &v.Mobile, &v.RichResults, &v.Link, &v.PreviousVerdict,
			&v.VerdictChangedAt, &v.InspectedAt)
		v.Indexed = v.Verdict == "PASS"
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list != nil {
		out["data"] = list
	}
	writeJSON(w, http.StatusOK, out)
}

// canonicalDiffers: Google chose another canonical than the page asks for.
const canonicalDiffers = `(i.google_canonical <> '' AND i.user_canonical <> '' AND rtrim(i.google_canonical, '/') <> rtrim(i.user_canonical, '/'))`

type sitemapView struct {
	Path          string     `json:"path"`
	LastSubmitted *time.Time `json:"last_submitted"`
	LastRead      *time.Time `json:"last_read"`
	Pending       bool       `json:"pending"`
	Index         bool       `json:"index"`
	Errors        int        `json:"errors"`
	Warnings      int        `json:"warnings"`
	Submitted     int        `json:"submitted"`
	Indexed       int        `json:"indexed"`
	CheckedAt     time.Time  `json:"checked_at"`
}

func (s *Server) siteSitemaps(w http.ResponseWriter, r *http.Request) {
	site, src, ok := s.searchSource(w, r)
	if !ok {
		return
	}
	out := map[string]any{"source": src, "data": []sitemapView{}}
	if src == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT path, last_submitted, last_read, pending, is_index, errors, warnings, submitted, indexed, checked_at
		FROM search_sitemaps m WHERE m.workspace_id = $1 AND m.integration_id = $2 AND m.site = $3
		  AND (`+seointel.PageHostSQL("m.path")+` = ANY($4) OR m.path NOT LIKE '%://%')
		ORDER BY m.errors DESC, m.path`, accessFrom(r.Context()).WorkspaceID, src.IntegrationID, src.Property, seointel.PageHosts(site.Host))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (sitemapView, error) {
		var v sitemapView
		return v, row.Scan(&v.Path, &v.LastSubmitted, &v.LastRead, &v.Pending, &v.Index, &v.Errors, &v.Warnings, &v.Submitted, &v.Indexed, &v.CheckedAt)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list != nil {
		out["data"] = list
	}
	writeJSON(w, http.StatusOK, out)
}
