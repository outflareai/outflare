package insights

// Trend series from data Outflare already stores beside product events,
// so a dashboard can show a brand's search and link-building numbers next
// to its sign-ups: the daily totals of the brand's analytics and search
// tools (Search Console, Bing Webmaster Tools, Google Analytics and the
// other analytics connectors), its referring domains, and its link orders.
// They read stored rows only and never call a provider.

import (
	"slices"
	"strconv"
	"strings"
)

const (
	// KindAnalytics is the daily totals of the brand's analytics and
	// search tools (analytics_daily, report traffic).
	KindAnalytics = "analytics"
	// KindBacklinks is the brand's referring domains (backlinks).
	KindBacklinks = "backlinks"
	// KindLinkOrders is the brand's paid link orders.
	KindLinkOrders = "link_orders"
	// KindSearch is Search Console's detail: clicks, impressions, CTR and
	// position by query, page, country and device, as its own report
	// shows them (search_daily and search_segments).
	KindSearch = "search"
)

// SearchMetrics are the measures of a search series.
var SearchMetrics = []string{"clicks", "impressions", "ctr", "position"}

// SearchFields are what a search series can be filtered and broken down
// by. Search Console keeps queries apart from countries and devices, so
// one series uses query (with page), or country and device (with page),
// never both.
var SearchFields = map[string]string{
	"query":   `d.query`,
	"page":    `d.page`,
	"country": `d.country`,
	"device":  `d.device`,
}

// searchTable is the table a search series reads: the country and device
// rows when it asks for either, else the query rows.
func searchTable(s Series, breakdown string) (table string, err error) {
	uses := map[string]bool{breakdown: true}
	for _, f := range s.Filters {
		uses[f.Property] = true
	}
	segments := uses["country"] || uses["device"]
	if segments && uses["query"] {
		return "", Invalid("Search Console keeps queries apart from countries and devices: use query, or country and device, in one series, not both")
	}
	if segments {
		return "search_segments", nil
	}
	return "search_daily", nil
}

// SourceKinds are the series kinds read from stored tool data.
var SourceKinds = []string{KindAnalytics, KindBacklinks, KindLinkOrders, KindSearch}

// AnalyticsMetrics are the measures of an analytics series. clicks,
// impressions, visitors, pageviews, sessions and conversions add up over
// days (visitors is a sum of daily visitors); ctr is clicks over
// impressions and position the impression-weighted average position.
var AnalyticsMetrics = []string{"clicks", "impressions", "ctr", "position", "visitors", "pageviews", "sessions", "conversions"}

// BacklinkMetrics are the measures of a backlinks series. referring_domains
// and dofollow_referring_domains count the domains with a live link at the
// end of each period (the total is at the end of the range);
// new_referring_domains counts domains first seen in the period and
// lost_referring_domains domains whose last live link was lost in it.
var BacklinkMetrics = []string{"referring_domains", "dofollow_referring_domains", "new_referring_domains", "lost_referring_domains"}

// LinkOrderMetrics are the measures of a link_orders series: spend is the
// US-dollar amount marked Paid in the period (the ECB equivalent for an
// invoice in another currency), committed the amount approved in it, and
// published and paid count the orders published or paid in it.
var LinkOrderMetrics = []string{"spend", "committed", "published", "paid"}

// sourceMetrics lists a source kind's measures; the first is the default.
func sourceMetrics(kind string) []string {
	switch kind {
	case KindAnalytics:
		return AnalyticsMetrics
	case KindBacklinks:
		return BacklinkMetrics
	case KindLinkOrders:
		return LinkOrderMetrics
	case KindSearch:
		return SearchMetrics
	}
	return nil
}

func isSourceKind(kind string) bool { return slices.Contains(SourceKinds, kind) }

// sourceMeasure is the series' measure, or its kind's default.
func sourceMeasure(s Series) string {
	if s.Measure != "" {
		return s.Measure
	}
	return sourceMetrics(s.Kind)[0]
}

// validSource checks a series from stored tool data.
func validSource(t Trend, s Series, at string) error {
	ms := sourceMetrics(s.Kind)
	if s.Measure != "" && !slices.Contains(ms, s.Measure) {
		return Invalid(at + ".measure for " + s.Kind + " is one of " + strings.Join(ms, ", "))
	}
	if s.Kind == KindSearch {
		if s.Event != "" || s.Conversion != "" || s.First || s.Property != "" || s.Network != "" || s.Campaign != "" || s.Connector != "" {
			return Invalid(at + ": a search series takes measure and filters only")
		}
		if len(s.Filters) > 10 {
			return Invalid(at + ".filters has at most 10 filters")
		}
		for j, f := range s.Filters {
			if _, ok := SearchFields[f.Property]; !ok {
				return Invalid(at + ".filters[" + strconv.Itoa(j) + "].property for a search series is query, page, country or device")
			}
			if err := validFilter(KindSearch, f, at+".filters["+strconv.Itoa(j)+"]"); err != nil {
				return err
			}
		}
		if t.Breakdown != "" {
			if _, ok := SearchFields[t.Breakdown]; !ok {
				return Invalid("breakdown with a search series is query, page, country or device")
			}
		}
		if t.Breakdown2 != "" {
			return Invalid("breakdown_2 isn't for search series")
		}
		if _, err := searchTable(s, t.Breakdown); err != nil {
			return err
		}
		if len(s.Label) > 80 {
			return Invalid(at + ".label is at most 80 characters")
		}
		return nil
	}
	if s.Event != "" || s.Conversion != "" || len(s.Filters) > 0 || s.First || s.Property != "" || s.Network != "" || s.Campaign != "" {
		return Invalid(at + ": a " + s.Kind + " series takes measure" + map[bool]string{true: " and connector", false: ""}[s.Kind == KindAnalytics] + " only")
	}
	if s.Kind != KindAnalytics && s.Connector != "" {
		return Invalid(at + ".connector is only for an analytics series")
	}
	if len(s.Connector) > 60 || strings.ContainsFunc(s.Connector, func(r rune) bool { return !(r == '_' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') }) {
		return Invalid(at + ".connector is a connector id, such as google_search_console")
	}
	if t.Breakdown != "" && !(s.Kind == KindAnalytics && t.Breakdown == "connector") {
		return Invalid("breakdown with an " + s.Kind + " series is connector (analytics only) or none")
	}
	if t.Breakdown2 != "" {
		return Invalid("breakdown_2 isn't for " + s.Kind + " series")
	}
	if len(s.Label) > 80 {
		return Invalid(at + ".label is at most 80 characters")
	}
	return nil
}

// sourceLabel names a source series on the chart.
func sourceLabel(s Series) string {
	m := strings.ReplaceAll(sourceMeasure(s), "_", " ")
	switch s.Kind {
	case KindAnalytics:
		tool := map[string]string{
			"google_search_console": "Search Console", "bing_webmaster": "Bing", "google_analytics": "Google Analytics",
		}[s.Connector]
		if tool == "" {
			tool = s.Connector
		}
		if tool == "" {
			return "Search and analytics " + m
		}
		return tool + " " + m
	case KindBacklinks:
		return strings.ToUpper(m[:1]) + m[1:]
	case KindLinkOrders:
		return "Link orders " + m
	case KindSearch:
		return "Search " + m
	}
	return m
}

// sourceCountable says whether a period with no rows is 0: everything but
// ctr and position, which are ratios of nothing.
func sourceCountable(s Series) bool {
	m := sourceMeasure(s)
	return m != "ctr" && m != "position"
}

// sourceSeries is the SQL for one series from stored tool data, in the
// shape every trend part has: i, gp (1 for the total), gb (1 for the rows
// across every group), p, bk, v, cur.
func sourceSeries(i int, s Series, breakdown, pWS, pBrand, pFrom, pTo, pInterval, pZone string, a *args) string {
	idx := strconv.Itoa(i)
	m := sourceMeasure(s)
	switch s.Kind {
	case KindAnalytics:
		agg := map[string]string{
			"clicks":      `sum(clicks)`,
			"impressions": `sum(impressions)`,
			"ctr":         `sum(clicks) / NULLIF(sum(impressions), 0)`,
			"position":    `sum(position * impressions) / NULLIF(sum(impressions) FILTER (WHERE position IS NOT NULL), 0)`,
			"visitors":    `sum(visitors)`,
			"pageviews":   `sum(pageviews)`,
			"sessions":    `sum(sessions)`,
			"conversions": `sum(conversions)`,
		}[m]
		bk, sets, gb := `''`, `GROUPING SETS ((i, p), (i))`, `1`
		if breakdown == "connector" {
			bk, sets, gb = `d.connector`, `GROUPING SETS ((i, p, bk), (i, bk), (i, p), (i))`, `GROUPING(bk)::int`
		}
		conn := a.add(s.Connector)
		// One row per tool, site and day: a property read through two
		// connections of the same tool counts once (the latest sync).
		return `SELECT ` + idx + ` AS i, GROUPING(p)::int AS gp, ` + gb + ` AS gb, p, ` + groupedBK(breakdown != "") + `, (` + agg + `)::float8 AS v, '' AS cur FROM (
		SELECT ` + idx + ` AS i, date_trunc(` + pInterval + `, d.day::timestamp) AT TIME ZONE ` + pZone + ` AS p, ` + bk + ` AS bk,
			d.clicks, d.impressions, d.position, d.visitors, d.pageviews, d.sessions, d.conversions
		FROM (
			SELECT DISTINCT ON (connector, host, site, day) connector, day, clicks, impressions, position, visitors, pageviews, sessions, conversions
			FROM analytics_daily
			WHERE workspace_id = ` + pWS + ` AND brand_id = ` + pBrand + ` AND report = 'traffic' AND key = ''
				AND day >= (` + pFrom + ` AT TIME ZONE ` + pZone + `)::date AND day < (` + pTo + ` AT TIME ZONE ` + pZone + `)::date
				AND (` + conn + ` = '' OR connector = ` + conn + `)
			ORDER BY connector, host, site, day, synced_at DESC
		) d) r GROUP BY ` + sets

	case KindBacklinks:
		alive := func(at string) string {
			return `b.first_seen_at <= ` + at + ` AND (b.lost_at IS NULL OR b.lost_at > ` + at + ` OR (b.regained_at IS NOT NULL AND b.regained_at >= b.lost_at AND b.regained_at <= ` + at + `))`
		}
		base := `b.workspace_id = ` + pWS + `::uuid AND b.brand_id = ` + pBrand + `::uuid AND b.first_seen_at IS NOT NULL`
		if m == "dofollow_referring_domains" {
			base += ` AND NOT b.nofollow AND NOT b.sponsored AND NOT b.ugc`
		}
		// Periods as the trend counts them, from the range's own start.
		gen := `SELECT g.p AT TIME ZONE ` + pZone + ` AS p,
			LEAST((g.p + ('1 ' || ` + pInterval + `)::interval) AT TIME ZONE ` + pZone + `, ` + pTo + `::timestamptz) AS e
			FROM generate_series(date_trunc(` + pInterval + `, ` + pFrom + `::timestamptz AT TIME ZONE ` + pZone + `),
				(` + pTo + `::timestamptz AT TIME ZONE ` + pZone + `) - interval '1 microsecond', ('1 ' || ` + pInterval + `)::interval) AS g(p)`
		// The brand's links once, then counted per period.
		bl := `(SELECT b.source_domain, b.first_seen_at, b.lost_at, b.regained_at, b.status FROM backlinks b WHERE ` + base + `)`
		var per, total string
		switch m {
		case "referring_domains", "dofollow_referring_domains":
			per = `(SELECT count(DISTINCT b.source_domain) FROM bl b WHERE ` + alive("w.e") + `)`
			total = `(SELECT count(DISTINCT b.source_domain) FROM bl b WHERE ` + alive(pTo+`::timestamptz`) + `)`
		case "new_referring_domains":
			first := `(SELECT b.source_domain, min(b.first_seen_at) AS f FROM bl b GROUP BY 1)`
			per = `(SELECT count(*) FROM ` + first + ` n WHERE n.f >= w.p AND n.f < w.e)`
			total = `(SELECT count(*) FROM ` + first + ` n WHERE n.f >= ` + pFrom + `::timestamptz AND n.f < ` + pTo + `::timestamptz)`
		case "lost_referring_domains":
			// A domain is lost when no link from it is live any more; it
			// counts in the period its last link was lost.
			lost := `(SELECT b.source_domain, max(b.lost_at) AS l FROM bl b GROUP BY 1
				HAVING bool_and(b.status = 'lost') AND max(b.lost_at) IS NOT NULL)`
			per = `(SELECT count(*) FROM ` + lost + ` n WHERE n.l >= w.p AND n.l < w.e)`
			total = `(SELECT count(*) FROM ` + lost + ` n WHERE n.l >= ` + pFrom + `::timestamptz AND n.l < ` + pTo + `::timestamptz)`
		}
		// A CTE can't sit inside the UNION ALL of every series, so the
		// links are a subquery each part reads.
		return `SELECT * FROM (WITH bl AS ` + bl + `
		SELECT ` + idx + ` AS i, 0 AS gp, 1 AS gb, w.p, ''::text AS bk, ` + per + `::float8 AS v, '' AS cur FROM (` + gen + `) w
		UNION ALL
		SELECT ` + idx + `, 1, 1, NULL::timestamptz, '', ` + total + `::float8, '') x`

	case KindSearch:
		table, _ := searchTable(s, breakdown)
		agg := map[string]string{
			"clicks":      `sum(clicks)`,
			"impressions": `sum(impressions)`,
			"ctr":         `sum(clicks)::float8 / NULLIF(sum(impressions), 0)`,
			"position":    `sum(position * impressions) / NULLIF(sum(impressions), 0)`,
		}[m]
		where := `d.workspace_id = ` + pWS + `::uuid AND d.brand_id = ` + pBrand + `::uuid
			AND d.day >= (` + pFrom + `::timestamptz AT TIME ZONE ` + pZone + `)::date AND d.day < (` + pTo + `::timestamptz AT TIME ZONE ` + pZone + `)::date`
		// One connection per property: a site read through two Search
		// Console sign-ins counts once (the one with the newest day).
		pick := `(SELECT DISTINCT ON (site) site, integration_id FROM (
				SELECT d.site, d.integration_id, max(d.day) AS last FROM ` + table + ` d WHERE ` + where + ` GROUP BY 1, 2) x
			ORDER BY site, last DESC, integration_id)`
		for _, f := range s.Filters {
			cond, err := filterSQL(KindSearch, f, a)
			if err != nil {
				// Validate checked every filter; nothing reaches here.
				cond = `false`
			}
			where += ` AND ` + cond
		}
		bk := `''::text`
		if breakdown != "" {
			bk = `left(` + SearchFields[breakdown] + `, 200)`
		}
		rows := `SELECT date_trunc(` + pInterval + `, d.day::timestamp) AT TIME ZONE ` + pZone + ` AS p, ` + bk + ` AS bk, d.clicks, d.impressions, d.position
			FROM ` + table + ` d JOIN ` + pick + ` k ON k.site = d.site AND k.integration_id = d.integration_id WHERE ` + where
		if breakdown == "" {
			return `SELECT ` + idx + ` AS i, GROUPING(p)::int AS gp, 1 AS gb, p, ''::text AS bk, (` + agg + `)::float8 AS v, '' AS cur FROM (` + rows + `) r GROUP BY GROUPING SETS ((p), ())`
		}
		// Only the biggest groups come back (a site has tens of thousands
		// of queries); the rows across every group still count them all.
		return `SELECT * FROM (WITH r AS (` + rows + `), top AS (
				SELECT bk FROM r GROUP BY bk ORDER BY sum(clicks) DESC, sum(impressions) DESC, bk LIMIT ` + strconv.Itoa(MaxGroups) + `)
			SELECT ` + idx + ` AS i, GROUPING(p)::int AS gp, GROUPING(bk)::int AS gb, p, COALESCE(bk, '') AS bk, (` + agg + `)::float8 AS v, '' AS cur
			FROM r GROUP BY GROUPING SETS ((p, bk), (bk), (p), ())
			HAVING GROUPING(bk) = 1 OR bk IN (SELECT bk FROM top)) x`

	case KindLinkOrders:
		usd := `COALESCE(o.usd_amount_minor, CASE WHEN o.currency = 'USD' THEN o.donor_amount_minor END)::float8 / 100`
		quotedUSD := `COALESCE(o.quoted_usd_minor, CASE WHEN COALESCE(o.currency, o.quoted_currency) = 'USD' THEN COALESCE(o.donor_amount_minor, o.quoted_amount_minor) END)::float8 / 100`
		pick := map[string][2]string{
			"spend":     {`o.paid_at`, usd},
			"committed": {`o.approved_at`, quotedUSD},
			"published": {`o.published_at`, `1::float8`},
			"paid":      {`o.paid_at`, `1::float8`},
		}[m]
		ts, val := pick[0], pick[1]
		cur := `''`
		if m == "spend" || m == "committed" {
			cur = `'USD'`
		}
		return `SELECT ` + idx + ` AS i, GROUPING(p)::int AS gp, 1 AS gb, p, ''::text AS bk, COALESCE(sum(v), 0)::float8 AS v, ` + cur + ` AS cur FROM (
		SELECT date_trunc(` + pInterval + `, ` + ts + ` AT TIME ZONE ` + pZone + `) AT TIME ZONE ` + pZone + ` AS p, ` + val + ` AS v
		FROM coverage_link_orders o
		WHERE o.workspace_id = ` + pWS + `::uuid AND o.brand_id = ` + pBrand + `::uuid AND ` + ts + ` >= ` + pFrom + `::timestamptz AND ` + ts + ` < ` + pTo + `::timestamptz
		) r GROUP BY GROUPING SETS ((p), ())`
	}
	return ""
}
