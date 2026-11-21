package api

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// The SEO board is the one-glance view of a brand's search money: tiles
// for today, yesterday, the month so far, the month's forecast and last
// month, each set against the period it is usually compared with, then
// the pages and countries behind the picked tile. Like the overview it
// reads only what is stored (Search Console days and segments, and first-
// touch organic conversions from product analytics), so it costs nothing.
//
// Search Console runs two or three days behind, so today and yesterday
// usually have conversions but no clicks yet; search_through says which
// day the clicks reach, and a period with no stored day is null, not 0.

type seoBoardMetrics struct {
	Revenue         *float64 `json:"revenue"`
	Payments        *float64 `json:"payments"`
	Signups         *float64 `json:"signups"`
	OrganicSessions *float64 `json:"organic_sessions"`
	Clicks          *float64 `json:"clicks"`
	Impressions     *float64 `json:"impressions"`
	CTR             *float64 `json:"ctr"`
	Position        *float64 `json:"position"`
}

type seoBoardPeriod struct {
	ID          string          `json:"id"`
	From        string          `json:"from"`
	To          string          `json:"to"`
	CompareFrom string          `json:"compare_from"`
	CompareTo   string          `json:"compare_to"`
	Forecast    bool            `json:"forecast"`
	Metrics     seoBoardMetrics `json:"metrics"`
	Previous    seoBoardMetrics `json:"previous"`
}

type seoBoardPage struct {
	Host            string   `json:"host"`
	Path            string   `json:"path"`
	Clicks          *float64 `json:"clicks"`
	Impressions     *float64 `json:"impressions"`
	CTR             *float64 `json:"ctr"`
	Position        *float64 `json:"position"`
	OrganicSessions *float64 `json:"organic_sessions"`
	Signups         *float64 `json:"signups"`
	Payments        *float64 `json:"payments"`
	Revenue         *float64 `json:"revenue"`
	PreviousClicks  *float64 `json:"previous_clicks"`
	PreviousRevenue *float64 `json:"previous_revenue"`
}

type seoBoardCountry struct {
	Country        string   `json:"country"`
	Clicks         float64  `json:"clicks"`
	Impressions    float64  `json:"impressions"`
	Position       *float64 `json:"position"`
	PreviousClicks float64  `json:"previous_clicks"`
	// From the brand's analytics tool (first-touch Organic Search), by the
	// visitor's country: sessions, their average time on the site,
	// signups, payments and revenue in US dollars.
	Sessions   int      `json:"organic_sessions"`
	AvgSeconds *float64 `json:"avg_session_seconds"`
	Signups    int      `json:"signups"`
	Payments   int      `json:"payments"`
	RevenueUSD float64  `json:"revenue_usd"`
}

// seoBoardPeriods are the tiles, in the order they are shown.
var seoBoardPeriods = []string{"today", "yesterday", "month_to_date", "month_forecast", "last_month"}

// maxBoardPages is how many pages the table lists: the ones with the most
// revenue, then the most clicks.
const maxBoardPages = 200

// One day of a brand's stored numbers.
type boardDay struct {
	revenue, payments, signups, sessions *float64
	clicks, impressions, posWeight       *float64
}

func (s *Server) seoBoard(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ws, brand := a.WorkspaceID, a.Brand
	picked := r.URL.Query().Get("period")
	if picked == "" {
		picked = "month_to_date"
	}
	if !slices.Contains(seoBoardPeriods, picked) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "period should be one of "+strings.Join(seoBoardPeriods, ", "))
		return
	}
	var tz string
	if err := s.pool.QueryRow(r.Context(), `SELECT COALESCE(timezone, '') FROM brands WHERE id = $1 AND workspace_id = $2`, brand, ws).Scan(&tz); err != nil {
		s.internal(w, r, err)
		return
	}
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc, tz = time.UTC, "UTC"
	}
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	periods := boardPeriods(today)

	// Every day the tiles need: from the start of the month before last.
	start := periods[len(periods)-1].compareFrom
	days, err := s.boardDays(r.Context(), ws, brand, start, today)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var searchThrough, convThrough *string
	for d, v := range days {
		if v.clicks != nil && (searchThrough == nil || d > *searchThrough) {
			searchThrough = &d
		}
		if v.sessions != nil && (convThrough == nil || d > *convThrough) {
			convThrough = &d
		}
	}

	var out []seoBoardPeriod
	var sel boardRange
	for _, p := range periods {
		o := seoBoardPeriod{ID: p.id, From: boardDate(p.from), To: boardDate(p.to), CompareFrom: boardDate(p.compareFrom), CompareTo: boardDate(p.compareTo), Forecast: p.id == "month_forecast"}
		if o.Forecast {
			// The month so far, scaled to the whole month by the days each
			// kind of number has: conversions through yesterday at the
			// latest (today is not over), clicks through the last Search
			// Console day.
			mtd := sumBoardDays(days, p.from, today)
			whole := float64(p.to.Sub(p.from).Hours()/24 + 1)
			scale := func(through *string, limit time.Time, vs ...**float64) {
				if through == nil {
					for _, v := range vs {
						*v = nil
					}
					return
				}
				last, _ := time.Parse(time.DateOnly, *through)
				if last.After(limit) {
					last = limit
				}
				n := last.Sub(p.from).Hours()/24 + 1
				for _, v := range vs {
					if n < 1 || *v == nil {
						*v = nil
						continue
					}
					x := **v / n * whole
					*v = &x
				}
			}
			yesterday := today.AddDate(0, 0, -1)
			c := sumBoardDays(days, p.from, yesterday)
			mtd.Revenue, mtd.Payments, mtd.Signups, mtd.OrganicSessions = c.Revenue, c.Payments, c.Signups, c.OrganicSessions
			scale(convThrough, yesterday, &mtd.Revenue, &mtd.Payments, &mtd.Signups, &mtd.OrganicSessions)
			scale(searchThrough, today, &mtd.Clicks, &mtd.Impressions)
			o.Metrics = mtd
		} else {
			o.Metrics = sumBoardDays(days, p.from, p.to)
		}
		o.Previous = sumBoardDays(days, p.compareFrom, p.compareTo)
		out = append(out, o)
		if p.id == picked {
			sel = p
			if sel.id == "month_forecast" {
				// The table behind the forecast is the month so far.
				sel.to = today
				sel.compareTo = sel.compareFrom.AddDate(0, 0, int(today.Sub(sel.from).Hours()/24))
				if end := endOfMonth(sel.compareFrom); sel.compareTo.After(end) {
					sel.compareTo = end
				}
			}
		}
	}

	pages, err := s.boardPages(r.Context(), ws, brand, sel)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	countries, err := s.boardCountries(r.Context(), ws, brand, sel)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"timezone":            tz,
		"today":               boardDate(today),
		"period":              picked,
		"search_through":      searchThrough,
		"conversions_through": convThrough,
		"periods":             out,
		"pages":               pages,
		"countries":           countries,
		"notes": []string{
			"Revenue, payments and sign-ups are first-touch Organic Search conversions from product analytics, by the page the person first landed on. Revenue is USD only.",
			"Clicks, impressions, CTR and position are Search Console's, which runs two or three days behind: today and yesterday usually have none yet.",
			"Today and yesterday are set against the same weekday a week before; the month so far against the same days of last month; the forecast and last month against the whole month before.",
			"The forecast scales the month so far to the whole month by the days each number has: conversions through yesterday, clicks through the last Search Console day.",
			"Clicks and impressions by country are Search Console's; sessions, time on site, signups, payments and revenue by country are from the analytics tool, by the visitor's first country.",
		},
	})
}

type boardRange struct {
	id                               string
	from, to, compareFrom, compareTo time.Time
}

func boardDate(t time.Time) string { return t.Format(time.DateOnly) }

func endOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
}

// boardPeriods are the tiles' dates, today being the brand's own date.
// The last one reaches furthest back.
func boardPeriods(today time.Time) []boardRange {
	month := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	last := month.AddDate(0, -1, 0)
	before := month.AddDate(0, -2, 0)
	// The same days of last month, at most to its end.
	sameDays := last.AddDate(0, 0, today.Day()-1)
	if end := endOfMonth(last); sameDays.After(end) {
		sameDays = end
	}
	yesterday := today.AddDate(0, 0, -1)
	return []boardRange{
		{id: "today", from: today, to: today, compareFrom: today.AddDate(0, 0, -7), compareTo: today.AddDate(0, 0, -7)},
		{id: "yesterday", from: yesterday, to: yesterday, compareFrom: yesterday.AddDate(0, 0, -7), compareTo: yesterday.AddDate(0, 0, -7)},
		{id: "month_to_date", from: month, to: today, compareFrom: last, compareTo: sameDays},
		{id: "month_forecast", from: month, to: endOfMonth(month), compareFrom: last, compareTo: endOfMonth(last)},
		{id: "last_month", from: last, to: endOfMonth(last), compareFrom: before, compareTo: endOfMonth(before)},
	}
}

// boardDays reads the brand's stored days: organic conversions by day,
// and Search Console totals by day, each website's day once.
func (s *Server) boardDays(ctx context.Context, ws, brand string, from, to time.Time) (map[string]*boardDay, error) {
	out := map[string]*boardDay{}
	get := func(d string) *boardDay {
		if out[d] == nil {
			out[d] = &boardDay{}
		}
		return out[d]
	}
	// Two connections reading one page's day count it once.
	rows, err := s.pool.Query(ctx, `
		WITH d AS (
			SELECT day, host, path, max(revenue_usd)::float8 r, max(payments) p, max(signups) s, max(organic_sessions) o
			FROM organic_conversions
			WHERE workspace_id = $1 AND brand_id = $2 AND day BETWEEN $3 AND $4
			GROUP BY day, host, path
		)
		SELECT to_char(day, 'YYYY-MM-DD'), sum(r), sum(p)::float8, sum(s)::float8, sum(o)::float8 FROM d GROUP BY day`, ws, brand, from, to)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d string
		v := &boardDay{}
		if err := rows.Scan(&d, &v.revenue, &v.payments, &v.signups, &v.sessions); err != nil {
			rows.Close()
			return nil, err
		}
		g := get(d)
		g.revenue, g.payments, g.signups, g.sessions = v.revenue, v.payments, v.signups, v.sessions
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.pool.Query(ctx, `
		WITH d AS (
			SELECT host, day, max(clicks) c, max(impressions) i, max(position) p
			FROM analytics_daily
			WHERE workspace_id = $1 AND brand_id = $2 AND report = 'traffic' AND key = '' AND host <> ''
			  AND day BETWEEN $3 AND $4 AND (clicks IS NOT NULL OR impressions IS NOT NULL)
			GROUP BY host, day
		)
		SELECT to_char(day, 'YYYY-MM-DD'), sum(c), sum(i), sum(i * p) FILTER (WHERE p IS NOT NULL) FROM d GROUP BY day`, ws, brand, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d string
		v := &boardDay{}
		if err := rows.Scan(&d, &v.clicks, &v.impressions, &v.posWeight); err != nil {
			return nil, err
		}
		g := get(d)
		g.clicks, g.impressions, g.posWeight = v.clicks, v.impressions, v.posWeight
	}
	return out, rows.Err()
}

// sumBoardDays adds up the days from from to to. A number no day has is
// null: not measured, not zero.
func sumBoardDays(days map[string]*boardDay, from, to time.Time) seoBoardMetrics {
	var m seoBoardMetrics
	var posW, posI float64
	add := func(acc **float64, v *float64) {
		if v == nil {
			return
		}
		if *acc == nil {
			z := 0.0
			*acc = &z
		}
		**acc += *v
	}
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		v := days[boardDate(d)]
		if v == nil {
			continue
		}
		add(&m.Revenue, v.revenue)
		add(&m.Payments, v.payments)
		add(&m.Signups, v.signups)
		add(&m.OrganicSessions, v.sessions)
		add(&m.Clicks, v.clicks)
		add(&m.Impressions, v.impressions)
		if v.posWeight != nil && v.impressions != nil {
			posW += *v.posWeight
			posI += *v.impressions
		}
	}
	if m.Clicks != nil && m.Impressions != nil && *m.Impressions > 0 {
		c := *m.Clicks / *m.Impressions
		m.CTR = &c
	}
	if posI > 0 {
		p := posW / posI
		m.Position = &p
	}
	return m
}

// boardPageKey lines a Search Console URL up with a landing page: the
// host without www. and the path.
func boardPageKey(host, path string) (string, string) {
	host = strings.TrimPrefix(strings.ToLower(host), "www.")
	if path == "" {
		path = "/"
	}
	return host, path
}

func boardURLKey(raw string) (string, string) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", raw
	}
	return boardPageKey(u.Hostname(), u.EscapedPath())
}

// boardPages lists the pages behind a tile: Search Console's clicks by
// page (from the page, country and device segments, which keep the
// queries Google hides; the query rows when there are no segments) and
// organic conversions by landing page, for the tile and the period it is
// compared with.
func (s *Server) boardPages(ctx context.Context, ws, brand string, p boardRange) ([]seoBoardPage, error) {
	type acc struct {
		seoBoardPage
		posW, posI float64
	}
	by := map[[2]string]*acc{}
	get := func(host, path string) *acc {
		k := [2]string{host, path}
		if by[k] == nil {
			by[k] = &acc{seoBoardPage: seoBoardPage{Host: host, Path: path}}
		}
		return by[k]
	}
	add := func(acc **float64, v float64) {
		if *acc == nil {
			*acc = new(float64)
		}
		**acc += v
	}

	var segments bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM search_segments WHERE workspace_id = $1 AND brand_id = $2 AND day BETWEEN $3 AND $4)`,
		ws, brand, p.compareFrom, p.to).Scan(&segments); err != nil {
		return nil, err
	}
	search := `
		WITH d AS (
			SELECT page, day, max(clicks) c, max(impressions) i, max(position) pos FROM (
				SELECT integration_id, page, day, sum(clicks)::float8 clicks, sum(impressions)::float8 impressions,
				       sum(impressions * position) / NULLIF(sum(impressions), 0) position
				FROM search_segments WHERE workspace_id = $1 AND brand_id = $2 AND day BETWEEN $3 AND $4
				GROUP BY integration_id, page, day
			) x GROUP BY page, day
		)
		SELECT page, sum(c), sum(i), sum(i * pos) FILTER (WHERE pos IS NOT NULL) FROM d GROUP BY page`
	if !segments {
		search = `
		WITH d AS (
			SELECT page, day, max(clicks) c, max(impressions) i, max(position) pos FROM (
				SELECT integration_id, page, day, sum(clicks)::float8 clicks, sum(impressions)::float8 impressions,
				       sum(impressions * position) / NULLIF(sum(impressions), 0) position
				FROM search_daily WHERE workspace_id = $1 AND brand_id = $2 AND day BETWEEN $3 AND $4
				GROUP BY integration_id, page, day
			) x GROUP BY page, day
		)
		SELECT page, sum(c), sum(i), sum(i * pos) FILTER (WHERE pos IS NOT NULL) FROM d GROUP BY page`
	}
	for _, side := range []struct {
		from, to time.Time
		prev     bool
	}{{p.from, p.to, false}, {p.compareFrom, p.compareTo, true}} {
		rows, err := s.pool.Query(ctx, search, ws, brand, side.from, side.to)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var page string
			var c, i float64
			var pw *float64
			if err := rows.Scan(&page, &c, &i, &pw); err != nil {
				rows.Close()
				return nil, err
			}
			host, path := boardURLKey(page)
			a := get(host, path)
			if side.prev {
				add(&a.PreviousClicks, c)
				continue
			}
			add(&a.Clicks, c)
			add(&a.Impressions, i)
			if pw != nil {
				a.posW += *pw
				a.posI += i
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}

		rows, err = s.pool.Query(ctx, `
			WITH d AS (
				SELECT host, path, day, max(revenue_usd)::float8 r, max(payments) pay, max(signups) sg, max(organic_sessions) o
				FROM organic_conversions
				WHERE workspace_id = $1 AND brand_id = $2 AND day BETWEEN $3 AND $4
				GROUP BY host, path, day
			)
			SELECT host, path, sum(r), sum(pay)::float8, sum(sg)::float8, sum(o)::float8 FROM d GROUP BY host, path`, ws, brand, side.from, side.to)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var host, path string
			var rev, pay, sg, o float64
			if err := rows.Scan(&host, &path, &rev, &pay, &sg, &o); err != nil {
				rows.Close()
				return nil, err
			}
			a := get(boardPageKey(host, path))
			if side.prev {
				add(&a.PreviousRevenue, rev)
				continue
			}
			add(&a.Revenue, rev)
			add(&a.Payments, pay)
			add(&a.Signups, sg)
			add(&a.OrganicSessions, o)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	out := make([]seoBoardPage, 0, len(by))
	for _, a := range by {
		// A page seen only in the period before is not in this tile.
		if a.Clicks == nil && a.Impressions == nil && a.Revenue == nil && a.OrganicSessions == nil {
			continue
		}
		if a.Clicks != nil && a.Impressions != nil && *a.Impressions > 0 {
			c := *a.Clicks / *a.Impressions
			a.CTR = &c
		}
		if a.posI > 0 {
			pos := a.posW / a.posI
			a.Position = &pos
		}
		out = append(out, a.seoBoardPage)
	}
	val := func(v *float64) float64 {
		if v == nil {
			return 0
		}
		return *v
	}
	slices.SortFunc(out, func(x, y seoBoardPage) int {
		switch {
		case val(x.Revenue) != val(y.Revenue):
			if val(x.Revenue) > val(y.Revenue) {
				return -1
			}
			return 1
		case val(x.Clicks) != val(y.Clicks):
			if val(x.Clicks) > val(y.Clicks) {
				return -1
			}
			return 1
		}
		return strings.Compare(x.Host+x.Path, y.Host+y.Path)
	})
	if len(out) > maxBoardPages {
		out = out[:maxBoardPages]
	}
	return out, nil
}

// boardCountries is Search Console's clicks by country for a tile and
// the period it is compared with. Countries are ISO 3166-1 alpha-3, as
// Search Console names them.
func (s *Server) boardCountries(ctx context.Context, ws, brand string, p boardRange) ([]seoBoardCountry, error) {
	rows, err := s.pool.Query(ctx, `
		WITH x AS (
			SELECT integration_id, country, day, sum(clicks)::float8 c, sum(impressions)::float8 i, sum(impressions * position) pw,
			       day BETWEEN $3 AND $4 AS cur
			FROM search_segments
			WHERE workspace_id = $1 AND brand_id = $2 AND (day BETWEEN $3 AND $4 OR day BETWEEN $5 AND $6)
			GROUP BY integration_id, country, day
		),
		d AS (SELECT country, day, cur, max(c) c, max(i) i, max(pw) pw FROM x GROUP BY country, day, cur)
		SELECT lower(country),
		       COALESCE(sum(c) FILTER (WHERE cur), 0), COALESCE(sum(i) FILTER (WHERE cur), 0),
		       sum(pw) FILTER (WHERE cur) / NULLIF(sum(i) FILTER (WHERE cur), 0),
		       COALESCE(sum(c) FILTER (WHERE NOT cur), 0)
		FROM d GROUP BY 1 ORDER BY 2 DESC, 3 DESC`, ws, brand, p.from, p.to, p.compareFrom, p.compareTo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []seoBoardCountry{}
	for rows.Next() {
		var c seoBoardCountry
		if err := rows.Scan(&c.Country, &c.Clicks, &c.Impressions, &c.Position, &c.PreviousClicks); err != nil {
			return nil, err
		}
		if c.Clicks == 0 && c.Impressions == 0 {
			continue
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	return s.boardCountryMoney(ctx, ws, brand, p, out)
}

// boardCountryMoney adds organic sessions, time on the site, signups,
// payments and revenue by country to the Search Console countries, and
// adds the countries that paid without any stored clicks.
func (s *Server) boardCountryMoney(ctx context.Context, ws, brand string, p boardRange, out []seoBoardCountry) ([]seoBoardCountry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT country, sum(organic_sessions)::int, sum(session_seconds), sum(signups)::int, sum(payments)::int, sum(revenue_usd)::float8
		FROM organic_countries
		WHERE workspace_id = $1 AND brand_id = $2 AND day BETWEEN $3 AND $4 AND country <> ''
		GROUP BY country`, ws, brand, p.from, p.to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	at := map[string]int{}
	for i, c := range out {
		at[c.Country] = i
	}
	for rows.Next() {
		var cc string
		var sess, sign, pay int
		var secs, rev float64
		if err := rows.Scan(&cc, &sess, &secs, &sign, &pay, &rev); err != nil {
			return nil, err
		}
		code, ok := alpha2To3[strings.ToUpper(cc)]
		if !ok {
			continue
		}
		i, ok := at[code]
		if !ok {
			out = append(out, seoBoardCountry{Country: code})
			i = len(out) - 1
			at[code] = i
		}
		c := &out[i]
		c.Sessions += sess
		c.Signups += sign
		c.Payments += pay
		c.RevenueUSD += rev
		if c.Sessions > 0 {
			total := secs
			if c.AvgSeconds != nil {
				total += *c.AvgSeconds * float64(c.Sessions-sess)
			}
			avg := total / float64(c.Sessions)
			c.AvgSeconds = &avg
		}
	}
	return out, rows.Err()
}
