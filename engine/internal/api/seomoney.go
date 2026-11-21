package api

import (
	"context"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The money view turns search gaps into dollars, from stored data only:
// the pages that lost organic revenue or clicks against the 28 days
// before, the queries in positions 4 to 20 worth moving to position 3,
// and the queries shown often but clicked less than their position
// earns. Every dollar is clicks times one rate, revenue per organic click
// (the brand's first-touch organic revenue over its Search Console clicks
// in the same 28 days), unless the caller sets its own; a page's own
// revenue change is shown beside it and never mixed in.

// expectedCTR is the share of impressions a result usually gets by its
// rounded position, for organic results on desktop and mobile together.
// It is a planning curve, not a measurement: the figures are shown on the
// page and can be read against the site's own CTR by position.
var expectedCTR = []float64{
	0,                                  // unused
	0.28, 0.15, 0.10, 0.07, 0.05, 0.04, // 1-6
	0.03, 0.025, 0.02, 0.018, // 7-10
	0.012, 0.011, 0.010, 0.009, 0.008, // 11-15
	0.007, 0.006, 0.005, 0.005, 0.004, // 16-20
}

// ctrAt is the expected CTR for an average position; past 20 it keeps
// falling slowly.
func ctrAt(pos float64) float64 {
	p := int(math.Round(pos))
	switch {
	case p < 1:
		p = 1
	case p > 20:
		return 0.003
	}
	return expectedCTR[p]
}

const (
	moneyDays          = 28
	opportunityFloor   = 100  // impressions in 28 days for a 4-20 row
	lowCTRFloor        = 1000 // impressions in 28 days for a low-CTR row
	lowCTRShare        = 0.7  // CTR under this share of expected is low
	moneyRows          = 100
	linkOrderLookback  = 90 // days an order still counts as a campaign
	minLostClicksShown = 10
)

type moneyQuery struct {
	Query           string  `json:"query"`
	URL             string  `json:"url"`
	Impressions     float64 `json:"impressions"`
	Clicks          float64 `json:"clicks"`
	CTR             float64 `json:"ctr"`
	Position        float64 `json:"position"`
	ExpectedCTR     float64 `json:"expected_ctr"`
	ExtraClicks     float64 `json:"extra_clicks"`
	ExtraRevenue    float64 `json:"extra_revenue"`
	LinkOrders      int     `json:"link_orders"`
	TargetPosition  *int    `json:"target_position,omitempty"`
	TargetCTR       float64 `json:"target_ctr,omitempty"`
	pageKey         [2]string
	brandOrNavQuery bool
}

type moneyPage struct {
	Host            string   `json:"host"`
	Path            string   `json:"path"`
	Clicks          float64  `json:"clicks"`
	PreviousClicks  float64  `json:"previous_clicks"`
	LostClicks      float64  `json:"lost_clicks"`
	ModeledLoss     float64  `json:"modeled_loss"`
	Revenue         *float64 `json:"revenue"`
	PreviousRevenue *float64 `json:"previous_revenue"`
	RevenueChange   *float64 `json:"revenue_change"`
	Position        *float64 `json:"position"`
	PreviousPos     *float64 `json:"previous_position"`
	LinkOrders      int      `json:"link_orders"`
}

func (s *Server) seoMoney(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a := accessFrom(ctx)
	ws, brand := a.WorkspaceID, a.Brand
	var rpcSet *float64
	if v := r.URL.Query().Get("rpc"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 || f > 10000 || math.IsNaN(f) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "rpc is revenue per click in USD, 0 to 10000")
			return
		}
		rpcSet = &f
	}
	withBrand := r.URL.Query().Get("branded") == "true"

	var name, handle string
	var domains []string
	if err := s.pool.QueryRow(ctx, `SELECT name, handle, domains FROM brands WHERE id = $1 AND workspace_id = $2`, brand, ws).Scan(&name, &handle, &domains); err != nil {
		s.internal(w, r, err)
		return
	}

	// The newest Search Console day stored for the brand ends both windows.
	var newest *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT max(day) FROM search_daily WHERE workspace_id = $1 AND brand_id = $2`, ws, brand).Scan(&newest); err != nil {
		s.internal(w, r, err)
		return
	}
	notes := []string{
		"Every dollar is clicks times revenue per organic click: the brand's first-touch organic revenue over its Search Console clicks in the same 28 days, unless you set your own rate. It is an estimate, not revenue already earned or lost.",
		"Positions 4 to 20: queries with at least 100 impressions in 28 days, with the clicks they would get at position 3 by the expected CTR curve, less the clicks they get now.",
		"Low CTR: queries with at least 1000 impressions in 28 days whose CTR is under 70% of the expected CTR for their position; the gap is the clicks they would get at the expected CTR.",
		"Losing pages: pages with fewer Search Console clicks than in the 28 days before (at least 10 fewer), with the modeled loss and, beside it, the change in the page's own first-touch revenue.",
		"Queries that name the brand are left out unless branded is on. A link order counts when it was placed in the last 90 days for the page and is not lost.",
	}
	empty := map[string]any{
		"search_through": nil, "from": nil, "to": nil, "previous_from": nil, "previous_to": nil, "rpc": rpcSet, "rpc_source": "none",
		"organic_revenue": nil, "organic_clicks": nil,
		"totals":       map[string]float64{"lost": 0, "positions_4_20": 0, "low_ctr": 0},
		"losing_pages": []moneyPage{},
		"positions":    []moneyQuery{},
		"low_ctr":      []moneyQuery{},
		"expected_ctr": expectedCTR[1:],
		"notes":        notes,
	}
	if newest == nil {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	to := *newest
	from := to.AddDate(0, 0, -(moneyDays - 1))
	prevTo := from.AddDate(0, 0, -1)
	prevFrom := prevTo.AddDate(0, 0, -(moneyDays - 1))

	// Revenue per organic click over the same 28 days.
	var revenue, clicks *float64
	if err := s.pool.QueryRow(ctx, `
		WITH d AS (SELECT day, host, path, max(revenue_usd)::float8 r FROM organic_conversions
			WHERE workspace_id = $1 AND brand_id = $2 AND day BETWEEN $3 AND $4 GROUP BY day, host, path)
		SELECT sum(r) FROM d`, ws, brand, from, to).Scan(&revenue); err != nil {
		s.internal(w, r, err)
		return
	}
	if err := s.pool.QueryRow(ctx, `
		WITH d AS (SELECT host, day, max(clicks) c FROM analytics_daily
			WHERE workspace_id = $1 AND brand_id = $2 AND report = 'traffic' AND key = '' AND host <> '' AND day BETWEEN $3 AND $4 AND clicks IS NOT NULL
			GROUP BY host, day)
		SELECT sum(c)::float8 FROM d`, ws, brand, from, to).Scan(&clicks); err != nil {
		s.internal(w, r, err)
		return
	}
	rpc, source := 0.0, "none"
	switch {
	case rpcSet != nil:
		rpc, source = *rpcSet, "set"
	case revenue != nil && clicks != nil && *clicks > 0:
		rpc, source = *revenue / *clicks, "measured"
	}

	orders, err := s.linkOrdersByPage(ctx, ws, brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	brandWords := brandTerms(name, handle, domains)

	queries, err := s.moneyQueries(ctx, ws, brand, from, to, brandWords)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var positions, lowCTR []moneyQuery
	var sumPositions, sumLow float64
	for _, q := range queries {
		if !withBrand && q.brandOrNavQuery {
			continue
		}
		q.LinkOrders = orders[q.pageKey]
		q.ExpectedCTR = ctrAt(q.Position)
		if q.Position >= 3.5 && q.Position < 20.5 && q.Impressions >= opportunityFloor {
			three := 3
			p := q
			p.TargetPosition, p.TargetCTR = &three, ctrAt(3)
			p.ExtraClicks = math.Max(0, q.Impressions*ctrAt(3)-q.Clicks)
			p.ExtraRevenue = p.ExtraClicks * rpc
			if p.ExtraClicks >= 1 {
				positions = append(positions, p)
				sumPositions += p.ExtraRevenue
			}
		}
		if q.Position < 20.5 && q.Impressions >= lowCTRFloor && q.CTR < lowCTRShare*q.ExpectedCTR {
			l := q
			l.ExtraClicks = q.Impressions*q.ExpectedCTR - q.Clicks
			l.ExtraRevenue = l.ExtraClicks * rpc
			lowCTR = append(lowCTR, l)
			sumLow += l.ExtraRevenue
		}
	}
	byExtra := func(x, y moneyQuery) int {
		if x.ExtraClicks != y.ExtraClicks {
			if x.ExtraClicks > y.ExtraClicks {
				return -1
			}
			return 1
		}
		return strings.Compare(x.Query+x.URL, y.Query+y.URL)
	}
	slices.SortFunc(positions, byExtra)
	slices.SortFunc(lowCTR, byExtra)
	positions, lowCTR = firstN(positions, moneyRows), firstN(lowCTR, moneyRows)
	if positions == nil {
		positions = []moneyQuery{}
	}
	if lowCTR == nil {
		lowCTR = []moneyQuery{}
	}

	pages, err := s.losingPages(ctx, ws, brand, from, to, prevFrom, prevTo, rpc, orders)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var sumLost float64
	for _, p := range pages {
		sumLost += p.ModeledLoss
	}
	pages = firstN(pages, moneyRows)
	if pages == nil {
		pages = []moneyPage{}
	}

	out := empty
	out["search_through"] = to.Format(time.DateOnly)
	out["from"], out["to"] = from.Format(time.DateOnly), to.Format(time.DateOnly)
	out["previous_from"], out["previous_to"] = prevFrom.Format(time.DateOnly), prevTo.Format(time.DateOnly)
	out["rpc"], out["rpc_source"] = rpc, source
	out["organic_revenue"], out["organic_clicks"] = revenue, clicks
	out["totals"] = map[string]float64{"lost": sumLost, "positions_4_20": sumPositions, "low_ctr": sumLow}
	out["losing_pages"], out["positions"], out["low_ctr"] = pages, positions, lowCTR
	writeJSON(w, http.StatusOK, out)
}

func firstN[T any](list []T, n int) []T {
	if len(list) > n {
		return list[:n]
	}
	return list
}

// queryNamesBrand says whether a query names the brand (brandTerms).
func queryNamesBrand(query string, terms []string) bool {
	for _, t := range terms {
		if strings.Contains(query, t) {
			return true
		}
	}
	return false
}

// moneyQueries reads the brand's queries by page over the 28 days: one
// row per query and page, each connection's day counted once.
func (s *Server) moneyQueries(ctx context.Context, ws, brand string, from, to time.Time, terms []string) ([]moneyQuery, error) {
	rows, err := s.pool.Query(ctx, `
		WITH x AS (
			SELECT integration_id, query, page, day, sum(clicks)::float8 c, sum(impressions)::float8 i, sum(position * impressions)::float8 pw
			FROM search_daily
			WHERE workspace_id = $1 AND brand_id = $2 AND day BETWEEN $3 AND $4 AND query <> ''
			GROUP BY integration_id, query, page, day
		),
		d AS (SELECT query, page, day, max(c) c, max(i) i, max(pw) pw FROM x GROUP BY query, page, day)
		SELECT query, page, sum(c), sum(i), sum(pw) / NULLIF(sum(i), 0)
		FROM d GROUP BY query, page HAVING sum(i) >= $5
		ORDER BY sum(i) DESC LIMIT 20000`, ws, brand, from, to, opportunityFloor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []moneyQuery
	for rows.Next() {
		var q moneyQuery
		var pos *float64
		if err := rows.Scan(&q.Query, &q.URL, &q.Clicks, &q.Impressions, &pos); err != nil {
			return nil, err
		}
		if pos == nil || q.Impressions <= 0 {
			continue
		}
		q.Position, q.CTR = *pos, q.Clicks/q.Impressions
		h, p := boardURLKey(q.URL)
		q.pageKey = [2]string{h, p}
		q.brandOrNavQuery = queryNamesBrand(q.Query, terms)
		out = append(out, q)
	}
	return out, rows.Err()
}

// losingPages lists the pages with fewer clicks than in the 28 days
// before, most lost first, with their own revenue change beside.
func (s *Server) losingPages(ctx context.Context, ws, brand string, from, to, prevFrom, prevTo time.Time, rpc float64, orders map[[2]string]int) ([]moneyPage, error) {
	type acc struct {
		moneyPage
		pw, pi, ppw, ppi float64
	}
	by := map[[2]string]*acc{}
	get := func(k [2]string) *acc {
		if by[k] == nil {
			by[k] = &acc{moneyPage: moneyPage{Host: k[0], Path: k[1]}}
		}
		return by[k]
	}
	segments := true
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM search_segments WHERE workspace_id = $1 AND brand_id = $2 AND day BETWEEN $3 AND $4)`,
		ws, brand, prevFrom, to).Scan(&segments); err != nil {
		return nil, err
	}
	table := "search_segments"
	if !segments {
		table = "search_daily"
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
		SELECT page, day >= $5 AS cur, sum(c), sum(i), sum(pw) FROM d GROUP BY page, cur`, ws, brand, prevFrom, to, from)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var page string
		var cur bool
		var c, i, pw float64
		if err := rows.Scan(&page, &cur, &c, &i, &pw); err != nil {
			rows.Close()
			return nil, err
		}
		h, p := boardURLKey(page)
		a := get([2]string{h, p})
		if cur {
			a.Clicks += c
			a.pw, a.pi = a.pw+pw, a.pi+i
		} else {
			a.PreviousClicks += c
			a.ppw, a.ppi = a.ppw+pw, a.ppi+i
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.pool.Query(ctx, `
		WITH d AS (SELECT day, host, path, max(revenue_usd)::float8 r FROM organic_conversions
			WHERE workspace_id = $1 AND brand_id = $2 AND day BETWEEN $3 AND $4 GROUP BY day, host, path)
		SELECT host, path, day >= $5 AS cur, sum(r) FROM d GROUP BY host, path, cur`, ws, brand, prevFrom, to, from)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var host, path string
		var cur bool
		var rev float64
		if err := rows.Scan(&host, &path, &cur, &rev); err != nil {
			rows.Close()
			return nil, err
		}
		h, p := boardPageKey(host, path)
		k := [2]string{h, p}
		// Revenue only for pages Search Console also shows.
		a := by[k]
		if a == nil {
			continue
		}
		v := rev
		if cur {
			a.Revenue = &v
		} else {
			a.PreviousRevenue = &v
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var out []moneyPage
	for k, a := range by {
		lost := a.PreviousClicks - a.Clicks
		if lost < minLostClicksShown {
			continue
		}
		a.LostClicks = lost
		a.ModeledLoss = lost * rpc
		if a.pi > 0 {
			p := a.pw / a.pi
			a.Position = &p
		}
		if a.ppi > 0 {
			p := a.ppw / a.ppi
			a.PreviousPos = &p
		}
		if a.Revenue != nil || a.PreviousRevenue != nil {
			cur, prev := 0.0, 0.0
			if a.Revenue != nil {
				cur = *a.Revenue
			}
			if a.PreviousRevenue != nil {
				prev = *a.PreviousRevenue
			}
			d := cur - prev
			a.RevenueChange = &d
		}
		a.LinkOrders = orders[k]
		out = append(out, a.moneyPage)
	}
	slices.SortFunc(out, func(x, y moneyPage) int {
		switch {
		case x.LostClicks > y.LostClicks:
			return -1
		case x.LostClicks < y.LostClicks:
			return 1
		}
		return strings.Compare(x.Host+x.Path, y.Host+y.Path)
	})
	return out, nil
}

// linkOrdersByPage counts the brand's link orders of the last 90 days
// that are not lost, by target page.
func (s *Server) linkOrdersByPage(ctx context.Context, ws, brand string) (map[[2]string]int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT target_url, count(*) FROM coverage_link_orders
		WHERE workspace_id = $1 AND brand_id = $2 AND status <> 'lost' AND created_at > now() - make_interval(days => $3)
		GROUP BY target_url`, ws, brand, linkOrderLookback)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[[2]string]int{}
	for rows.Next() {
		var u string
		var n int
		if err := rows.Scan(&u, &n); err != nil {
			return nil, err
		}
		h, p := boardURLKey(u)
		out[[2]string{h, p}] += n
	}
	return out, rows.Err()
}
