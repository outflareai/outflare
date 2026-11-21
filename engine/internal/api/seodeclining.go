package api

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Declining pages: a fast list of the brand's pages whose Search Console
// clicks fell against the period before, sorted into the ones that just
// started falling, the ones still falling and the ones recovering, with
// where the loss is (country, device, query), what it costs, and flags
// that point at a cause (a broken or redirected page, another of the
// site's pages taking the query). Periods are whole: the last 7 or 28
// days to the newest stored Search Console day against the same length
// before, so a partial week is never set against a full one. Reads only
// what is stored.

const (
	declineShare     = 0.3 // at least 30% fewer clicks
	declineFloor     = 50  // clicks in the period before, for a share drop
	declineAbsolute  = 100 // clicks lost that always count
	declineRows      = 200
	competeFloorImpr = 10
)

type declineSplit struct {
	Key            string  `json:"key"`
	Clicks         float64 `json:"clicks"`
	PreviousClicks float64 `json:"previous_clicks"`
}

type declinePage struct {
	Host           string   `json:"host"`
	Path           string   `json:"path"`
	URL            string   `json:"url"`
	Cohort         string   `json:"cohort"`
	Clicks         float64  `json:"clicks"`
	PreviousClicks float64  `json:"previous_clicks"`
	EarlierClicks  float64  `json:"earlier_clicks"`
	Change         float64  `json:"change"`
	Impressions    float64  `json:"impressions"`
	PreviousImpr   float64  `json:"previous_impressions"`
	Position       *float64 `json:"position"`
	PreviousPos    *float64 `json:"previous_position"`
	CTR            *float64 `json:"ctr"`
	PreviousCTR    *float64 `json:"previous_ctr"`
	// What changed most: clicks, impressions, position or ctr.
	Driver           string         `json:"driver"`
	Signups          *float64       `json:"signups"`
	PreviousSignups  *float64       `json:"previous_signups"`
	Revenue          *float64       `json:"revenue"`
	PreviousRevenue  *float64       `json:"previous_revenue"`
	ModeledLoss      float64        `json:"modeled_loss"`
	TopCountry       *declineSplit  `json:"top_country"`
	TopDevice        *declineSplit  `json:"top_device"`
	TopQuery         *declineSplit  `json:"top_query"`
	Countries        []declineSplit `json:"countries"`
	Technical        string         `json:"technical"`
	CompetingPages   int            `json:"competing_pages"`
	pw, pi, ppw, ppi float64
}

func (s *Server) seoDeclining(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a := accessFrom(ctx)
	ws, brand := a.WorkspaceID, a.Brand
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || (n != 7 && n != 28) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "days is 7 or 28")
			return
		}
		days = n
	}
	var newest *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT max(day) FROM search_daily WHERE workspace_id = $1 AND brand_id = $2`, ws, brand).Scan(&newest); err != nil {
		s.internal(w, r, err)
		return
	}
	notes := []string{
		"Periods are whole: the last " + strconv.Itoa(days) + " days to the newest stored Search Console day against the " + strconv.Itoa(days) + " before, and those against the " + strconv.Itoa(days) + " before them for the cohorts.",
		"A page is declining when it lost at least 30% of its clicks with at least 50 in the period before, or 100 clicks or more whatever the share. New: it was not declining the period before. Still declining: it was. Recovering: it was declining and now gained clicks.",
		"The modeled loss is clicks lost times revenue per organic click (first-touch organic revenue over Search Console clicks in the last 28 days). The page's own sign-ups and revenue are shown beside it.",
		"Technical flags come from the latest finished crawl of the site: an error status, a redirect or noindex. Competing pages are other pages with at least 10 impressions for the page's biggest losing query in the latest period.",
	}
	empty := map[string]any{"days": days, "from": nil, "to": nil, "previous_from": nil, "previous_to": nil, "rpc": nil,
		"summary": map[string]float64{"new": 0, "still": 0, "recovering": 0, "clicks_lost": 0, "modeled_loss": 0},
		"pages":   []declinePage{}, "notes": notes}
	if newest == nil {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	to := *newest
	from := to.AddDate(0, 0, -(days - 1))
	prevTo := from.AddDate(0, 0, -1)
	prevFrom := prevTo.AddDate(0, 0, -(days - 1))
	earlyTo := prevFrom.AddDate(0, 0, -1)
	earlyFrom := earlyTo.AddDate(0, 0, -(days - 1))

	pages, err := s.declinePages(ctx, ws, brand, earlyFrom, prevFrom, from, to)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	declined := func(cur, prev float64) bool {
		lost := prev - cur
		return lost >= declineAbsolute || (prev >= declineFloor && lost >= declineShare*prev)
	}
	var out []*declinePage
	for _, p := range pages {
		now, before := declined(p.Clicks, p.PreviousClicks), declined(p.PreviousClicks, p.EarlierClicks)
		switch {
		case now && before:
			p.Cohort = "still"
		case now:
			p.Cohort = "new"
		case before && p.Clicks > p.PreviousClicks:
			p.Cohort = "recovering"
		default:
			continue
		}
		out = append(out, p)
	}
	slices.SortFunc(out, func(x, y *declinePage) int {
		dx, dy := x.PreviousClicks-x.Clicks, y.PreviousClicks-y.Clicks
		switch {
		case dx > dy:
			return -1
		case dx < dy:
			return 1
		}
		return strings.Compare(x.URL, y.URL)
	})
	if len(out) > declineRows {
		out = out[:declineRows]
	}

	rpc, err := s.organicRPC(ctx, ws, brand, to.AddDate(0, 0, -27), to)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if err := s.declineDetails(ctx, ws, brand, out, prevFrom, prevTo, from, to); err != nil {
		s.internal(w, r, err)
		return
	}
	sum := map[string]float64{"new": 0, "still": 0, "recovering": 0, "clicks_lost": 0, "modeled_loss": 0}
	list := make([]declinePage, 0, len(out))
	for _, p := range out {
		if p.Cohort != "recovering" {
			lost := p.PreviousClicks - p.Clicks
			p.ModeledLoss = lost * rpc
			sum["clicks_lost"] += lost
			sum["modeled_loss"] += p.ModeledLoss
		}
		sum[p.Cohort]++
		if p.Countries == nil {
			p.Countries = []declineSplit{}
		}
		list = append(list, *p)
	}
	res := empty
	res["from"], res["to"] = from.Format(time.DateOnly), to.Format(time.DateOnly)
	res["previous_from"], res["previous_to"] = prevFrom.Format(time.DateOnly), prevTo.Format(time.DateOnly)
	res["rpc"], res["summary"], res["pages"] = rpc, sum, list
	writeJSON(w, http.StatusOK, res)
}

// organicRPC is first-touch organic revenue over Search Console clicks
// between two days; 0 without either.
func (s *Server) organicRPC(ctx context.Context, ws, brand string, from, to time.Time) (float64, error) {
	var revenue, clicks *float64
	if err := s.pool.QueryRow(ctx, `
		WITH d AS (SELECT day, host, path, max(revenue_usd)::float8 r FROM organic_conversions
			WHERE workspace_id = $1 AND brand_id = $2 AND day BETWEEN $3 AND $4 GROUP BY day, host, path)
		SELECT sum(r) FROM d`, ws, brand, from, to).Scan(&revenue); err != nil {
		return 0, err
	}
	if err := s.pool.QueryRow(ctx, `
		WITH d AS (SELECT host, day, max(clicks) c FROM analytics_daily
			WHERE workspace_id = $1 AND brand_id = $2 AND report = 'traffic' AND key = '' AND host <> '' AND day BETWEEN $3 AND $4 AND clicks IS NOT NULL
			GROUP BY host, day)
		SELECT sum(c)::float8 FROM d`, ws, brand, from, to).Scan(&clicks); err != nil {
		return 0, err
	}
	if revenue == nil || clicks == nil || *clicks <= 0 {
		return 0, nil
	}
	return *revenue / *clicks, nil
}

// segmentsTable is where page totals come from: the page, country and
// device segments, which keep the queries Google hides, or the query rows
// when a brand has no segments.
func (s *Server) segmentsTable(ctx context.Context, ws, brand string, from, to time.Time) (string, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM search_segments WHERE workspace_id = $1 AND brand_id = $2 AND day BETWEEN $3 AND $4)`,
		ws, brand, from, to).Scan(&ok)
	if ok {
		return "search_segments", err
	}
	return "search_daily", err
}

// declinePages reads each page's clicks, impressions and position over the
// three periods: earlier (from earlyFrom), previous (from prevFrom) and
// current (from curFrom to to).
func (s *Server) declinePages(ctx context.Context, ws, brand string, earlyFrom, prevFrom, curFrom, to time.Time) ([]*declinePage, error) {
	table, err := s.segmentsTable(ctx, ws, brand, earlyFrom, to)
	if err != nil {
		return nil, err
	}
	// The table name is one of two constants.
	rows, err := s.pool.Query(ctx, `
		WITH x AS (
			SELECT integration_id, page, day, sum(clicks)::float8 c, sum(impressions)::float8 i, sum(position * impressions)::float8 pw
			FROM `+table+`
			WHERE workspace_id = $1 AND brand_id = $2 AND day BETWEEN $3 AND $4
			GROUP BY integration_id, page, day
		),
		d AS (SELECT page, day, max(c) c, max(i) i, max(pw) pw FROM x GROUP BY page, day)
		SELECT page, CASE WHEN day >= $6 THEN 0 WHEN day >= $5 THEN 1 ELSE 2 END AS w, sum(c), sum(i), sum(pw)
		FROM d GROUP BY 1, 2`, ws, brand, earlyFrom, to, prevFrom, curFrom)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	by := map[string]*declinePage{}
	for rows.Next() {
		var page string
		var win int
		var c, i, pw float64
		if err := rows.Scan(&page, &win, &c, &i, &pw); err != nil {
			return nil, err
		}
		p := by[page]
		if p == nil {
			h, path := boardURLKey(page)
			p = &declinePage{URL: page, Host: h, Path: path}
			by[page] = p
		}
		switch win {
		case 0:
			p.Clicks, p.Impressions, p.pw, p.pi = p.Clicks+c, p.Impressions+i, p.pw+pw, p.pi+i
		case 1:
			p.PreviousClicks, p.PreviousImpr, p.ppw, p.ppi = p.PreviousClicks+c, p.PreviousImpr+i, p.ppw+pw, p.ppi+i
		default:
			p.EarlierClicks += c
		}
	}
	out := make([]*declinePage, 0, len(by))
	for _, p := range by {
		if p.pi > 0 {
			v := p.pw / p.pi
			p.Position = &v
		}
		if p.ppi > 0 {
			v := p.ppw / p.ppi
			p.PreviousPos = &v
		}
		if p.Impressions > 0 {
			v := p.Clicks / p.Impressions
			p.CTR = &v
		}
		if p.PreviousImpr > 0 {
			v := p.PreviousClicks / p.PreviousImpr
			p.PreviousCTR = &v
		}
		if p.PreviousClicks > 0 {
			p.Change = (p.Clicks - p.PreviousClicks) / p.PreviousClicks
		}
		p.Driver = declineDriver(p)
		out = append(out, p)
	}
	return out, rows.Err()
}

// declineDriver names what moved most: impressions (less demand or lost
// visibility), position, or CTR (the snippet draws fewer clicks).
func declineDriver(p *declinePage) string {
	if p.PreviousImpr <= 0 {
		return "clicks"
	}
	impr := (p.Impressions - p.PreviousImpr) / p.PreviousImpr
	var ctr float64
	if p.CTR != nil && p.PreviousCTR != nil && *p.PreviousCTR > 0 {
		ctr = (*p.CTR - *p.PreviousCTR) / *p.PreviousCTR
	}
	if p.Position != nil && p.PreviousPos != nil && *p.Position-*p.PreviousPos >= 2 {
		return "position"
	}
	if impr <= ctr {
		return "impressions"
	}
	return "ctr"
}

// declineDetails adds the split by country, device and query, the
// conversions, the technical flag and the competing pages.
func (s *Server) declineDetails(ctx context.Context, ws, brand string, pages []*declinePage, prevFrom, prevTo, from, to time.Time) error {
	if len(pages) == 0 {
		return nil
	}
	urls := make([]string, len(pages))
	by := map[string]*declinePage{}
	for i, p := range pages {
		urls[i] = p.URL
		by[p.URL] = p
	}

	// Country and device splits, from the segments only.
	for _, dim := range []string{"country", "device"} {
		rows, err := s.pool.Query(ctx, `
			WITH x AS (
				SELECT integration_id, page, `+dim+` AS k, day, sum(clicks)::float8 c FROM search_segments
				WHERE workspace_id = $1 AND brand_id = $2 AND day BETWEEN $3 AND $4 AND page = ANY($6)
				GROUP BY integration_id, page, k, day
			),
			d AS (SELECT page, k, day, max(c) c FROM x GROUP BY page, k, day)
			SELECT page, lower(k), COALESCE(sum(c) FILTER (WHERE day >= $5), 0), COALESCE(sum(c) FILTER (WHERE day < $5), 0)
			FROM d GROUP BY page, k`, ws, brand, prevFrom, to, from, urls)
		if err != nil {
			return err
		}
		for rows.Next() {
			var page string
			var sp declineSplit
			if err := rows.Scan(&page, &sp.Key, &sp.Clicks, &sp.PreviousClicks); err != nil {
				rows.Close()
				return err
			}
			p := by[page]
			if p == nil {
				continue
			}
			if dim == "country" {
				p.Countries = append(p.Countries, sp)
				p.TopCountry = biggerLoss(p.TopCountry, sp)
			} else {
				p.TopDevice = biggerLoss(p.TopDevice, sp)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	for _, p := range pages {
		slices.SortFunc(p.Countries, func(x, y declineSplit) int {
			dx, dy := x.PreviousClicks-x.Clicks, y.PreviousClicks-y.Clicks
			switch {
			case dx > dy:
				return -1
			case dx < dy:
				return 1
			}
			return strings.Compare(x.Key, y.Key)
		})
		if len(p.Countries) > 5 {
			p.Countries = p.Countries[:5]
		}
	}

	// The query that lost the most.
	rows, err := s.pool.Query(ctx, `
		WITH x AS (
			SELECT integration_id, page, query, day, sum(clicks)::float8 c FROM search_daily
			WHERE workspace_id = $1 AND brand_id = $2 AND day BETWEEN $3 AND $4 AND page = ANY($6) AND query <> ''
			GROUP BY integration_id, page, query, day
		),
		d AS (SELECT page, query, day, max(c) c FROM x GROUP BY page, query, day)
		SELECT page, query, COALESCE(sum(c) FILTER (WHERE day >= $5), 0), COALESCE(sum(c) FILTER (WHERE day < $5), 0)
		FROM d GROUP BY page, query`, ws, brand, prevFrom, to, from, urls)
	if err != nil {
		return err
	}
	for rows.Next() {
		var page string
		var sp declineSplit
		if err := rows.Scan(&page, &sp.Key, &sp.Clicks, &sp.PreviousClicks); err != nil {
			rows.Close()
			return err
		}
		if p := by[page]; p != nil {
			p.TopQuery = biggerLoss(p.TopQuery, sp)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Other pages shown for each page's biggest losing query.
	var qPages, qs []string
	for _, p := range pages {
		if p.TopQuery != nil {
			qPages, qs = append(qPages, p.URL), append(qs, p.TopQuery.Key)
		}
	}
	if len(qs) > 0 {
		rows, err := s.pool.Query(ctx, `
			WITH t AS (SELECT * FROM unnest($5::text[], $6::text[]) AS t(page, query))
			SELECT t.page FROM t
			JOIN search_daily d ON d.query = t.query AND d.page <> t.page
			WHERE d.workspace_id = $1 AND d.brand_id = $2 AND d.day BETWEEN $3 AND $4
			GROUP BY t.page, d.page HAVING sum(d.impressions) >= $7`, ws, brand, from, to, qPages, qs, competeFloorImpr)
		if err != nil {
			return err
		}
		for rows.Next() {
			var page string
			if err := rows.Scan(&page); err != nil {
				rows.Close()
				return err
			}
			if p := by[page]; p != nil {
				p.CompetingPages++
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}

	// Conversions by landing page.
	rows, err = s.pool.Query(ctx, `
		WITH d AS (SELECT day, host, path, max(signups)::float8 sg, max(revenue_usd)::float8 r FROM organic_conversions
			WHERE workspace_id = $1 AND brand_id = $2 AND day BETWEEN $3 AND $4 GROUP BY day, host, path)
		SELECT host, path, sum(sg) FILTER (WHERE day >= $5), sum(r) FILTER (WHERE day >= $5), sum(sg) FILTER (WHERE day < $5), sum(r) FILTER (WHERE day < $5)
		FROM d GROUP BY host, path`, ws, brand, prevFrom, to, from)
	if err != nil {
		return err
	}
	byKey := map[[2]string]*declinePage{}
	for _, p := range pages {
		byKey[[2]string{p.Host, p.Path}] = p
	}
	for rows.Next() {
		var host, path string
		var sg, rev, psg, prev *float64
		if err := rows.Scan(&host, &path, &sg, &rev, &psg, &prev); err != nil {
			rows.Close()
			return err
		}
		h, pth := boardPageKey(host, path)
		if p := byKey[[2]string{h, pth}]; p != nil {
			p.Signups, p.Revenue, p.PreviousSignups, p.PreviousRevenue = sg, rev, psg, prev
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// The latest finished crawl's view of each page.
	rows, err = s.pool.Query(ctx, `
		SELECT DISTINCT ON (p.url) p.url, p.http_status, p.redirect_to, p.noindex
		FROM crawl_pages p JOIN crawl_runs c ON c.id = p.run_id AND c.workspace_id = p.workspace_id
		JOIN seo_sites s ON s.id = c.site_id AND s.workspace_id = c.workspace_id
		WHERE p.workspace_id = $1 AND s.brand_id = $2 AND c.status = 'done' AND p.url = ANY($3)
		ORDER BY p.url, c.created_at DESC`, ws, brand, urls)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var url, redirect string
		var status *int
		var noindex bool
		if err := rows.Scan(&url, &status, &redirect, &noindex); err != nil {
			return err
		}
		p := by[url]
		if p == nil {
			continue
		}
		switch {
		case status != nil && *status >= 500:
			p.Technical = "server_error"
		case status != nil && *status >= 400:
			p.Technical = "not_found"
		case redirect != "":
			p.Technical = "redirect"
		case noindex:
			p.Technical = "noindex"
		}
	}
	return rows.Err()
}

func biggerLoss(cur *declineSplit, sp declineSplit) *declineSplit {
	if cur == nil || sp.PreviousClicks-sp.Clicks > cur.PreviousClicks-cur.Clicks {
		v := sp
		return &v
	}
	return cur
}
