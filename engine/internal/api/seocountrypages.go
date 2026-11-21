package api

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// Country pages: how pages made for a country (or a city, or a network)
// rank in that country. For each page under a path (such as /proxies/),
// its target country is the one its target keyword is tracked in, else
// the one Search Console shows it in most; its numbers are Search
// Console's for that country only, over the 28 days to the newest stored
// day against the 28 before, beside the tracked rank there. Pages group by
// template, the path without its last part, so a template change that
// lifts or sinks a whole group shows as one move. Reads only what is
// stored.

const (
	countryPageRows  = 500
	countryTemplates = 30
)

type countryPageMetrics struct {
	Clicks      float64  `json:"clicks"`
	Impressions float64  `json:"impressions"`
	CTR         *float64 `json:"ctr"`
	Position    *float64 `json:"position"`
}

type countryPageRow struct {
	URL      string `json:"url"`
	Path     string `json:"path"`
	Template string `json:"template"`
	// The language part of the path, such as de, or "".
	Locale string `json:"locale"`
	// Lowercase alpha-3, as Search Console names countries.
	TargetCountry string `json:"target_country"`
	// tracked (the target keyword is tracked there) or search (the
	// country Search Console shows the page in most).
	CountryFrom string             `json:"country_from"`
	Keyword     string             `json:"keyword"`
	Current     countryPageMetrics `json:"current"`
	Previous    countryPageMetrics `json:"previous"`
	// Every country together, the 28 days to the newest day.
	AllCountries    countryPageMetrics `json:"all_countries"`
	TrackedPosition *int               `json:"tracked_position"`
	TrackedPrevious *int               `json:"tracked_previous"`
	TrackedURL      string             `json:"tracked_url"`
	TrackedDay      *string            `json:"tracked_day"`
	// The position the band uses: tracked, else Search Console's in the
	// target country.
	Position  *float64 `json:"position"`
	Band      string   `json:"band"`
	Untracked bool     `json:"untracked"`
	acc       map[string]*countryAcc
}

type countryAcc struct{ c, i, pw, pc, pi, ppw float64 }

type countryTemplate struct {
	Template       string   `json:"template"`
	Pages          int      `json:"pages"`
	Clicks         float64  `json:"clicks"`
	PreviousClicks float64  `json:"previous_clicks"`
	MedianPosition *float64 `json:"median_position"`
	MedianPrevious *float64 `json:"median_previous"`
	Top10          int      `json:"top10"`
	Striking       int      `json:"striking"`
	Untracked      int      `json:"untracked"`
}

func (s *Server) seoCountryPages(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a := accessFrom(ctx)
	ws, brand := a.WorkspaceID, a.Brand
	prefix := strings.TrimSpace(r.URL.Query().Get("prefix"))
	if prefix != "" && !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	if len(prefix) > 500 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "prefix is at most 500 characters")
		return
	}
	var newest *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT max(day) FROM search_segments WHERE workspace_id = $1 AND brand_id = $2`, ws, brand).Scan(&newest); err != nil {
		s.internal(w, r, err)
		return
	}
	notes := []string{
		"A page's target country is the country its target keyword is tracked in (the one where the page shows most, when it is tracked in several), else the country Search Console shows it in most.",
		"Clicks, impressions, CTR and position are Search Console's for the target country only, over the 28 days to the newest stored day against the 28 before; All countries is every country together.",
		"The band uses the tracked rank in the target country when there is one, else Search Console's position there. Untracked: no target keyword, or it is not tracked in the target country.",
		"A template is the path without its last part, such as /proxies/ for /proxies/germany. Median positions move together when a template change lifts or sinks the group.",
	}
	res := map[string]any{"prefix": prefix, "from": nil, "to": nil, "previous_from": nil, "previous_to": nil,
		"templates": []countryTemplate{}, "pages": []countryPageRow{}, "summary": map[string]int{"pages": 0, "top3": 0, "top10": 0, "striking": 0, "beyond": 0, "none": 0, "untracked": 0}, "notes": notes}
	if newest == nil {
		writeJSON(w, http.StatusOK, res)
		return
	}
	to := *newest
	from := to.AddDate(0, 0, -27)
	prevTo := from.AddDate(0, 0, -1)
	prevFrom := prevTo.AddDate(0, 0, -27)
	res["from"], res["to"] = from.Format(time.DateOnly), to.Format(time.DateOnly)
	res["previous_from"], res["previous_to"] = prevFrom.Format(time.DateOnly), prevTo.Format(time.DateOnly)

	pages, err := s.countryPageSearch(ctx, ws, brand, prevFrom, from, to)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// The templates to choose from, before the prefix narrows the pages.
	templates := map[string]*countryTemplate{}
	var rows []*countryPageRow
	for _, p := range pages {
		if prefix == "" || strings.HasPrefix(p.Path, prefix) {
			rows = append(rows, p)
		}
	}
	slices.SortFunc(rows, func(x, y *countryPageRow) int {
		switch {
		case x.AllCountries.Impressions > y.AllCountries.Impressions:
			return -1
		case x.AllCountries.Impressions < y.AllCountries.Impressions:
			return 1
		}
		return strings.Compare(x.URL, y.URL)
	})
	if len(rows) > countryPageRows {
		rows = rows[:countryPageRows]
	}
	if err := s.countryPageTargets(ctx, ws, brand, rows); err != nil {
		s.internal(w, r, err)
		return
	}

	sum := map[string]int{"pages": len(rows), "top3": 0, "top10": 0, "striking": 0, "beyond": 0, "none": 0, "untracked": 0}
	positions := map[string][2][]float64{}
	out := make([]countryPageRow, 0, len(rows))
	for _, p := range rows {
		m := p.acc[p.TargetCountry]
		if m != nil {
			p.Current = countryMetrics(m.c, m.i, m.pw)
			p.Previous = countryMetrics(m.pc, m.pi, m.ppw)
		}
		p.Position = p.Current.Position
		if p.TrackedPosition != nil {
			v := float64(*p.TrackedPosition)
			p.Position = &v
		}
		switch {
		case p.Position == nil:
			p.Band = "none"
		case *p.Position <= 3.5:
			p.Band = "top3"
		case *p.Position <= 10.5:
			p.Band = "top10"
		case *p.Position <= 20.5:
			p.Band = "striking"
		default:
			p.Band = "beyond"
		}
		sum[p.Band]++
		if p.Untracked {
			sum["untracked"]++
		}
		t := templates[p.Template]
		if t == nil {
			t = &countryTemplate{Template: p.Template}
			templates[p.Template] = t
		}
		t.Pages++
		t.Clicks += p.Current.Clicks
		t.PreviousClicks += p.Previous.Clicks
		if p.Band == "top3" || p.Band == "top10" {
			t.Top10++
		}
		if p.Band == "striking" {
			t.Striking++
		}
		if p.Untracked {
			t.Untracked++
		}
		ps := positions[p.Template]
		if p.Current.Position != nil {
			ps[0] = append(ps[0], *p.Current.Position)
		}
		if p.Previous.Position != nil {
			ps[1] = append(ps[1], *p.Previous.Position)
		}
		positions[p.Template] = ps
		p.acc = nil
		out = append(out, *p)
	}
	list := make([]countryTemplate, 0, len(templates))
	for k, t := range templates {
		t.MedianPosition, t.MedianPrevious = medianOf(positions[k][0]), medianOf(positions[k][1])
		list = append(list, *t)
	}
	slices.SortFunc(list, func(x, y countryTemplate) int {
		if x.Pages != y.Pages {
			return y.Pages - x.Pages
		}
		return strings.Compare(x.Template, y.Template)
	})
	if len(list) > countryTemplates {
		list = list[:countryTemplates]
	}

	// The templates of every page, to pick a prefix from.
	choices := map[string]int{}
	for _, p := range pages {
		choices[p.Template]++
	}
	type choice struct {
		Template string `json:"template"`
		Pages    int    `json:"pages"`
	}
	picks := []choice{}
	for k, n := range choices {
		if n >= 3 {
			picks = append(picks, choice{k, n})
		}
	}
	slices.SortFunc(picks, func(x, y choice) int {
		if x.Pages != y.Pages {
			return y.Pages - x.Pages
		}
		return strings.Compare(x.Template, y.Template)
	})
	if len(picks) > countryTemplates {
		picks = picks[:countryTemplates]
	}

	res["templates"], res["pages"], res["summary"], res["template_choices"] = list, out, sum, picks
	writeJSON(w, http.StatusOK, res)
}

func countryMetrics(c, i, pw float64) countryPageMetrics {
	m := countryPageMetrics{Clicks: c, Impressions: i}
	if i > 0 {
		ctr, pos := c/i, pw/i
		m.CTR, m.Position = &ctr, &pos
	}
	return m
}

func medianOf(v []float64) *float64 {
	if len(v) == 0 {
		return nil
	}
	sort.Float64s(v)
	m := v[len(v)/2]
	if len(v)%2 == 0 {
		m = (v[len(v)/2-1] + v[len(v)/2]) / 2
	}
	return &m
}

// countryPageSearch reads every page's Search Console numbers by country
// for the two periods.
func (s *Server) countryPageSearch(ctx context.Context, ws, brand string, prevFrom, from, to time.Time) ([]*countryPageRow, error) {
	rows, err := s.pool.Query(ctx, `
		WITH x AS (
			SELECT integration_id, page, lower(country) AS country, day, sum(clicks)::float8 c, sum(impressions)::float8 i, sum(position * impressions)::float8 pw
			FROM search_segments
			WHERE workspace_id = $1 AND brand_id = $2 AND day BETWEEN $3 AND $5
			GROUP BY integration_id, page, country, day
		),
		d AS (SELECT page, country, day, max(c) c, max(i) i, max(pw) pw FROM x GROUP BY page, country, day)
		SELECT page, country,
			COALESCE(sum(c) FILTER (WHERE day >= $4), 0), COALESCE(sum(i) FILTER (WHERE day >= $4), 0), COALESCE(sum(pw) FILTER (WHERE day >= $4), 0),
			COALESCE(sum(c) FILTER (WHERE day < $4), 0), COALESCE(sum(i) FILTER (WHERE day < $4), 0), COALESCE(sum(pw) FILTER (WHERE day < $4), 0)
		FROM d GROUP BY page, country`, ws, brand, prevFrom, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	by := map[string]*countryPageRow{}
	for rows.Next() {
		var page, country string
		var a countryAcc
		if err := rows.Scan(&page, &country, &a.c, &a.i, &a.pw, &a.pc, &a.pi, &a.ppw); err != nil {
			return nil, err
		}
		p := by[page]
		if p == nil {
			_, path := boardURLKey(page)
			tpl := path
			if i := strings.LastIndex(strings.TrimSuffix(path, "/"), "/"); i >= 0 {
				tpl = path[:i+1]
			}
			p = &countryPageRow{URL: page, Path: path, Template: tpl, Locale: urlLocale(page), acc: map[string]*countryAcc{}}
			by[page] = p
		}
		v := a
		p.acc[country] = &v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*countryPageRow, 0, len(by))
	for _, p := range by {
		var c, i, pw float64
		best, bestImpr := "", -1.0
		for k, a := range p.acc {
			c, i, pw = c+a.c, i+a.i, pw+a.pw
			if a.i > bestImpr || (a.i == bestImpr && k < best) {
				best, bestImpr = k, a.i
			}
		}
		p.AllCountries = countryMetrics(c, i, pw)
		p.TargetCountry, p.CountryFrom = best, "search"
		if i > 0 {
			out = append(out, p)
		}
	}
	return out, nil
}

// countryPageTargets adds each page's target keyword and its tracked rank
// in the target country, which then becomes the target country.
func (s *Server) countryPageTargets(ctx context.Context, ws, brand string, pages []*countryPageRow) error {
	if len(pages) == 0 {
		return nil
	}
	urls := make([]string, len(pages))
	by := map[string]*countryPageRow{}
	for i, p := range pages {
		urls[i] = p.URL
		by[p.URL] = p
	}
	rows, err := s.pool.Query(ctx, `
		SELECT t.url, t.keyword FROM page_targets t JOIN seo_sites s ON s.id = t.site_id AND s.workspace_id = t.workspace_id
		WHERE t.workspace_id = $1 AND s.brand_id = $2 AND t.status = 'set' AND t.url = ANY($3)`, ws, brand, urls)
	if err != nil {
		return err
	}
	kws := map[string][]*countryPageRow{}
	for rows.Next() {
		var u, k string
		if err := rows.Scan(&u, &k); err != nil {
			rows.Close()
			return err
		}
		if p := by[u]; p != nil {
			p.Keyword = k
			kws[k] = append(kws[k], p)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range pages {
		p.Untracked = true
	}
	if len(kws) == 0 {
		return nil
	}
	list := make([]string, 0, len(kws))
	for k := range kws {
		list = append(list, k)
	}
	rows, err = s.pool.Query(ctx, `
		SELECT k.keyword, k.country, o.position, o.url, to_char(o.day, 'YYYY-MM-DD'), p.position
		FROM tracked_keywords k
		JOIN seo_sites s ON s.id = k.site_id AND s.workspace_id = k.workspace_id
		LEFT JOIN LATERAL (SELECT position, url, day FROM rank_observations WHERE workspace_id = $1 AND tracked_id = k.id ORDER BY day DESC LIMIT 1) o ON true
		LEFT JOIN LATERAL (SELECT position FROM rank_observations WHERE workspace_id = $1 AND tracked_id = k.id AND day < o.day ORDER BY day DESC LIMIT 1) p ON true
		WHERE k.workspace_id = $1 AND s.brand_id = $2 AND k.status = 'active' AND k.keyword = ANY($3)`, ws, brand, list)
	if err != nil {
		return err
	}
	defer rows.Close()
	type rank struct {
		country   string
		pos, prev *int
		url       string
		day       *string
	}
	ranks := map[string][]rank{}
	for rows.Next() {
		var k, c string
		var x rank
		var u *string
		if err := rows.Scan(&k, &c, &x.pos, &u, &x.day, &x.prev); err != nil {
			return err
		}
		x.country = countryAlpha3(c)
		if u != nil {
			x.url = *u
		}
		if x.country != "" {
			ranks[k] = append(ranks[k], x)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for k, ps := range kws {
		for _, p := range ps {
			var pick *rank
			bestImpr := -1.0
			for i := range ranks[k] {
				x := &ranks[k][i]
				impr := 0.0
				if a := p.acc[x.country]; a != nil {
					impr = a.i
				}
				if impr > bestImpr {
					pick, bestImpr = x, impr
				}
			}
			if pick == nil {
				continue
			}
			p.TargetCountry, p.CountryFrom, p.Untracked = pick.country, "tracked", false
			p.TrackedPosition, p.TrackedPrevious, p.TrackedURL, p.TrackedDay = pick.pos, pick.prev, pick.url, pick.day
		}
	}
	return nil
}

// languageSegment is a path's first part when it names a language, such
// as de or pt-br.
var languageSegment = regexp.MustCompile(`^/([a-z]{2}(?:-[a-z]{2})?)(?:/|$)`)

func urlLocale(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if m := languageSegment.FindStringSubmatch(strings.ToLower(u.Path)); m != nil {
		return m[1]
	}
	return ""
}
