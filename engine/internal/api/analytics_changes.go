package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/analyticsync"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Answers shaped for agents: what changed on each website, and what each
// website can be asked. Both read stored data only, so they are free, and
// they say which tool, period and unit every number comes from.

func (s *Server) routesAnalyticsAgents(mux *http.ServeMux) {
	read := auth.ScopeAnalyticsRead
	mux.HandleFunc("GET /v1/workspaces/{ws}/analytics/changes", s.inWorkspace(read, s.analyticsChanges))
	mux.HandleFunc("GET /v1/workspaces/{ws}/analytics/catalog", s.inWorkspace(read, s.analyticsCatalog))
}

// metricUnits names what each metric counts, once per answer.
var metricUnits = map[string]string{
	"visitors": "unique visitors (a returning visitor counts once per day)", "pageviews": "page views", "sessions": "visits",
	"bounce_rate": "fraction of visits, 0 to 1", "avg_duration": "seconds per visit",
	"clicks": "search result clicks", "impressions": "search result views", "ctr": "clicks per impression, 0 to 1",
	"position": "average search position, 1 is the top (lower is better)", "events": "events", "conversions": "conversions (key events, goals)",
	"domain_rating": "link strength, 0 to 100 (each SEO tool has its own scale)", "backlinks": "live links", "referring_domains": "domains linking",
	"organic_keywords": "keywords ranking in the top 100", "top3": "keywords in the top 3", "organic_traffic": "estimated visits a month from search",
	"traffic_value": "USD a month that traffic would cost in ads",
}

var metricOrder = []string{"visitors", "pageviews", "sessions", "conversions", "events", "clicks", "impressions", "ctr", "position", "bounce_rate", "avg_duration"}

func metricOf(m connectors.Metrics, name string) *float64 {
	switch name {
	case "visitors":
		return m.Visitors
	case "pageviews":
		return m.Pageviews
	case "sessions":
		return m.Sessions
	case "bounce_rate":
		return m.BounceRate
	case "avg_duration":
		return m.AvgDuration
	case "clicks":
		return m.Clicks
	case "impressions":
		return m.Impressions
	case "ctr":
		return m.CTR
	case "position":
		return m.Position
	case "events":
		return m.Events
	case "conversions":
		return m.Conversions
	}
	return nil
}

func seoMetricOf(m connectors.SEOMetrics, name string) *float64 {
	switch name {
	case "domain_rating":
		return m.DomainRating
	case "backlinks":
		return m.Backlinks
	case "referring_domains":
		return m.ReferringDomains
	case "organic_keywords":
		return m.OrganicKeywords
	case "top3":
		return m.Top3
	case "organic_traffic":
		return m.OrganicTraffic
	case "traffic_value":
		return m.TrafficValue
	}
	return nil
}

var seoOrder = []string{"domain_rating", "referring_domains", "backlinks", "organic_keywords", "top3", "organic_traffic", "traffic_value"}

// tidy rounds a number for reading: rates to 4 places, positions and
// durations to 1, counts to whole numbers once they are big.
func tidy(metric string, v float64) float64 {
	places := 0.0
	switch {
	case metric == "ctr" || metric == "bounce_rate":
		places = 4
	case metric == "position" || metric == "avg_duration" || metric == "domain_rating":
		places = 1
	case math.Abs(v) < 100:
		places = 1
	}
	p := math.Pow(10, places)
	return math.Round(v*p) / p
}

// change is one metric now and in the period before.
type change struct {
	Metric string   `json:"metric"`
	Now    *float64 `json:"now"`
	Before *float64 `json:"before,omitempty"`
	Change *float64 `json:"change,omitempty"`
	// ChangePct is the change in percent; missing when before was 0.
	ChangePct *float64 `json:"change_pct,omitempty"`
}

func makeChange(metric string, now, before *float64) *change {
	if now == nil {
		return nil
	}
	c := &change{Metric: metric, Now: connectors.Num(tidy(metric, *now))}
	if before != nil {
		c.Before = connectors.Num(tidy(metric, *before))
		c.Change = connectors.Num(tidy(metric, *now-*before))
		if *before != 0 {
			c.ChangePct = connectors.Num(math.Round((*now-*before)/math.Abs(*before)*1000) / 10)
		}
	}
	return c
}

type window struct {
	From string `json:"from"`
	To   string `json:"to"`
	// DaysStored is how many of its days the sync has stored.
	DaysStored int `json:"days_stored"`
}

type toolChange struct {
	Integration string    `json:"integration_id"`
	Name        string    `json:"name"`
	Connector   string    `json:"connector"`
	Now         window    `json:"now"`
	Before      window    `json:"before"`
	Metrics     []*change `json:"metrics"`
	// primary is the metric unusual days and headlines use.
	primary string
}

type moverRow struct {
	Key    string  `json:"key"`
	Now    float64 `json:"now"`
	Before float64 `json:"before"`
	Change float64 `json:"change"`
}

type movers struct {
	Report string     `json:"report"`
	Tools  []string   `json:"tools"`
	Metric string     `json:"metric"`
	Up     []moverRow `json:"up"`
	Down   []moverRow `json:"down"`
}

type unusualDay struct {
	Day       string  `json:"day"`
	Connector string  `json:"connector"`
	Name      string  `json:"name"`
	Metric    string  `json:"metric"`
	Value     float64 `json:"value"`
	// Typical is the median of the 28 days before the period.
	Typical float64 `json:"typical"`
	What    string  `json:"what"`
}

type keywordMove struct {
	Keyword  string  `json:"keyword"`
	Position float64 `json:"position"`
	Previous float64 `json:"previous"`
	Volume   float64 `json:"volume,omitempty"`
}

type seoChange struct {
	Integration  string        `json:"integration_id"`
	Name         string        `json:"name"`
	Connector    string        `json:"connector"`
	Day          string        `json:"day"`
	PreviousDay  string        `json:"previous_day,omitempty"`
	Metrics      []*change     `json:"metrics"`
	KeywordsUp   []keywordMove `json:"keywords_up,omitempty"`
	KeywordsDown []keywordMove `json:"keywords_down,omitempty"`
	KeywordsNew  int           `json:"keywords_new,omitempty"`
	KeywordsLost int           `json:"keywords_lost,omitempty"`
}

// gap is something an agent could not answer, and how to fix it.
type gap struct {
	What string `json:"what"`
	Why  string `json:"why"`
	// Connectors are ids for request_connection (POST /connect-requests).
	Connectors []string `json:"connectors,omitempty"`
	// IntegrationID is the connection to fix, when one is broken.
	IntegrationID string `json:"integration_id,omitempty"`
	Action        string `json:"action"`
}

type websiteChanges struct {
	Host     string        `json:"host"`
	Headline []string      `json:"headline"`
	Tools    []*toolChange `json:"tools"`
	// Search adds clicks and impressions up across search consoles.
	Search  []*change    `json:"search,omitempty"`
	Movers  []movers     `json:"movers,omitempty"`
	Unusual []unusualDay `json:"unusual_days,omitempty"`
	SEO     []seoChange  `json:"seo,omitempty"`
	Gaps    []gap        `json:"gaps,omitempty"`
}

type dayRow struct {
	integration, name, connector, host, day, key string
	m                                            connectors.Metrics
}

// sumRows adds stored days up the way /analytics/history does: counts
// summed, bounce rate weighted by visits, click-through and position by
// impressions, duration averaged.
func sumRows(rows []dayRow) connectors.Metrics {
	var out connectors.Metrics
	var bounceW, bounceN, durSum, durN, posW, ctrSum, ctrN float64
	for _, r := range rows {
		out = out.Add(r.m)
		if r.m.BounceRate != nil {
			w := 1.0
			if r.m.Sessions != nil && *r.m.Sessions > 0 {
				w = *r.m.Sessions
			}
			bounceW += *r.m.BounceRate * w
			bounceN += w
		}
		if r.m.AvgDuration != nil {
			durSum += *r.m.AvgDuration
			durN++
		}
		if r.m.Position != nil && r.m.Impressions != nil {
			posW += *r.m.Position * *r.m.Impressions
		}
		if r.m.CTR != nil {
			ctrSum += *r.m.CTR
			ctrN++
		}
	}
	if bounceN > 0 {
		out.BounceRate = connectors.Num(bounceW / bounceN)
	}
	if durN > 0 {
		out.AvgDuration = connectors.Num(durSum / durN)
	}
	if out.Impressions != nil && *out.Impressions > 0 {
		if out.Clicks != nil {
			out.CTR = connectors.Num(*out.Clicks / *out.Impressions)
		}
		if posW > 0 {
			out.Position = connectors.Num(posW / *out.Impressions)
		}
	} else if ctrN > 0 {
		out.CTR = connectors.Num(ctrSum / ctrN)
	}
	return out
}

func primaryMetric(m connectors.Metrics) string {
	for _, name := range []string{"visitors", "pageviews", "sessions", "clicks", "events"} {
		if metricOf(m, name) != nil {
			return name
		}
	}
	return ""
}

func addDays(day string, n int) string {
	t, _ := time.Parse(time.DateOnly, day)
	return t.AddDate(0, 0, n).Format(time.DateOnly)
}

func inWindow(day string, w window) bool { return day >= w.From && day <= w.To }

func (s *Server) dailyRows(ctx context.Context, ws, report, from, to, host string, sel brandSel) ([]dayRow, error) {
	sql := `SELECT d.integration_id::text, i.name, d.connector, d.host, to_char(d.day, 'YYYY-MM-DD'), d.key,
			d.visitors, d.pageviews, d.sessions, d.bounce_rate, d.avg_duration, d.clicks, d.impressions, d.ctr, d.position, d.events, d.conversions
		FROM analytics_daily d JOIN integrations i ON i.id = d.integration_id
		WHERE d.workspace_id = $1 AND d.report = $2 AND d.day BETWEEN $3 AND $4`
	args := []any{ws, report, from, to}
	if report == connectors.ReportTraffic {
		sql += ` AND d.key = ''`
	} else {
		sql += ` AND d.key <> ''`
	}
	if host != "" {
		args = append(args, host)
		sql += ` AND d.host = $5`
	}
	sql += sel.sql(`analytics_site_brand(d.workspace_id, d.integration_id, d.site, d.host)`, &args)
	rows, err := s.pool.Query(ctx, sql+` ORDER BY d.day`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dayRow
	for rows.Next() {
		var r dayRow
		if err := rows.Scan(append([]any{&r.integration, &r.name, &r.connector, &r.host, &r.day, &r.key}, scanMetrics(&r.m)...)...); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Server) analyticsChanges(w http.ResponseWriter, r *http.Request) {
	ws := accessFrom(r.Context()).WorkspaceID
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	days, top := 7, 5
	if v := q.Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 90 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "days is 1 to 90")
			return
		}
		days = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 25 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit is 1 to 25")
			return
		}
		top = n
	}
	host := analyticsync.Host(q.Get("host"))
	if q.Get("host") != "" && host == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "host is a domain, like example.com")
		return
	}
	if host != "" && !s.needHostInBrand(w, r, ws, host, sel) {
		return
	}
	to := time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	if v := q.Get("to"); v != "" {
		if dateOnly(v) == "" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "to is a date, YYYY-MM-DD")
			return
		}
		to = dateOnly(v)
	}
	// Enough history for the period before and a 28-day baseline.
	back := 2 * days
	if days+28 > back {
		back = days + 28
	}
	from := addDays(to, -back+1)
	ctx := r.Context()
	traffic, err := s.dailyRows(ctx, ws, connectors.ReportTraffic, from, to, host, sel)
	if err != nil {
		s.internal(w, r, err)
		return
	}

	sites := map[string]*websiteChanges{}
	var order []string
	site := func(h string) *websiteChanges {
		if sites[h] == nil {
			sites[h] = &websiteChanges{Host: h, Headline: []string{}, Tools: []*toolChange{}}
			order = append(order, h)
		}
		return sites[h]
	}
	byTool := map[string][]dayRow{}
	var toolOrder []string
	for _, row := range traffic {
		if byTool[row.integration] == nil {
			toolOrder = append(toolOrder, row.integration)
		}
		byTool[row.integration] = append(byTool[row.integration], row)
	}
	tools := map[string]*toolChange{}
	for _, id := range toolOrder {
		rows := byTool[id]
		// Each tool is compared over its own latest days, so a tool that
		// runs behind (Search Console is two to three days late) is not
		// read as a drop.
		end := rows[len(rows)-1].day
		now := window{From: addDays(end, -days+1), To: end}
		before := window{From: addDays(end, -2*days+1), To: addDays(end, -days)}
		var nowRows, beforeRows, baseRows []dayRow
		for _, row := range rows {
			switch {
			case inWindow(row.day, now):
				nowRows = append(nowRows, row)
			case inWindow(row.day, before):
				beforeRows = append(beforeRows, row)
			}
			if row.day < now.From && row.day >= addDays(now.From, -28) {
				baseRows = append(baseRows, row)
			}
		}
		if len(nowRows) == 0 {
			continue
		}
		now.DaysStored, before.DaysStored = len(nowRows), len(beforeRows)
		t := &toolChange{Integration: id, Name: rows[0].name, Connector: rows[0].connector, Now: now, Before: before, Metrics: []*change{}}
		cur := sumRows(nowRows)
		var prev *connectors.Metrics
		if len(beforeRows) > 0 {
			p := sumRows(beforeRows)
			prev = &p
		}
		for _, name := range metricOrder {
			var b *float64
			if prev != nil {
				b = metricOf(*prev, name)
			}
			if c := makeChange(name, metricOf(cur, name), b); c != nil {
				t.Metrics = append(t.Metrics, c)
			}
		}
		t.primary = primaryMetric(cur)
		tools[id] = t
		sc := site(rows[0].host)
		sc.Tools = append(sc.Tools, t)
		sc.Unusual = append(sc.Unusual, unusualDays(t, nowRows, baseRows)...)
	}

	for _, h := range order {
		sc := sites[h]
		var nowSearch, beforeSearch connectors.Metrics
		var hasBefore bool
		for _, t := range sc.Tools {
			for _, c := range t.Metrics {
				if c.Metric == "clicks" || c.Metric == "impressions" {
					add := func(m *connectors.Metrics, v *float64) {
						if c.Metric == "clicks" {
							*m = m.Add(connectors.Metrics{Clicks: v})
						} else {
							*m = m.Add(connectors.Metrics{Impressions: v})
						}
					}
					add(&nowSearch, c.Now)
					if c.Before != nil {
						add(&beforeSearch, c.Before)
						hasBefore = true
					}
				}
			}
		}
		if nowSearch.Clicks != nil && searchTools(sc.Tools) > 1 {
			for _, name := range []string{"clicks", "impressions"} {
				var b *float64
				if hasBefore {
					b = metricOf(beforeSearch, name)
				}
				if c := makeChange(name, metricOf(nowSearch, name), b); c != nil {
					sc.Search = append(sc.Search, c)
				}
			}
		}
	}

	notes := []string{
		"Each tool is compared over its own latest stored days against the same number of days before, so tools that run behind are not read as drops. A tool's now and before give the exact dates.",
		"Movers come from the top 50 rows stored per day, so a page, source or query that was below the top 50 can look new.",
	}
	for _, report := range []string{connectors.ReportPages, connectors.ReportSources, connectors.ReportSearchQueries} {
		rows, err := s.dailyRows(ctx, ws, report, addDays(to, -2*days-7), to, host, sel)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		for h, mv := range moversFor(report, rows, tools, top) {
			if sc := sites[h]; sc != nil && mv != nil {
				sc.Movers = append(sc.Movers, *mv)
			}
		}
	}

	seo, err := s.seoChanges(ctx, ws, host, top, sel)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for h, list := range seo {
		site(h).SEO = list
	}

	syncs, err := s.syncStatuses(ctx, ws, sel)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	seoSources, err := s.seoSources(ctx, ws, sel)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// A connected tool with nothing stored yet still belongs to its site.
	for _, st := range syncs {
		if st.Host != "" && (host == "" || st.Host == host) {
			site(st.Host)
		}
	}
	out := []*websiteChanges{}
	sort.SliceStable(order, func(i, j int) bool { return order[i] != "" && order[j] == "" })
	for _, h := range order {
		sc := sites[h]
		sort.Slice(sc.Unusual, func(i, j int) bool { return sc.Unusual[i].Day > sc.Unusual[j].Day })
		if len(sc.Unusual) > top {
			sc.Unusual = sc.Unusual[:top]
		}
		sc.Gaps = siteGaps(h, sc.Tools, syncs, seoSources, len(sc.SEO) > 0)
		sc.Headline = headline(sc, days)
		out = append(out, sc)
	}
	resp := map[string]any{"days": days, "to": to, "websites": out, "units": metricUnits, "notes": notes}
	if len(out) == 0 {
		resp["gaps"] = siteGaps("", nil, syncs, seoSources, false)
	}
	writeJSON(w, http.StatusOK, resp)
}

func searchTools(tools []*toolChange) int {
	n := 0
	for _, t := range tools {
		for _, c := range t.Metrics {
			if c.Metric == "impressions" {
				n++
			}
		}
	}
	return n
}

// unusualDays flags days in the period far from the 28 days before it:
// over half again or under half the median, and well outside the usual
// spread. Small sites need at least 20 a day of difference.
func unusualDays(t *toolChange, nowRows, baseRows []dayRow) []unusualDay {
	if t.primary == "" || len(baseRows) < 14 {
		return nil
	}
	var base []float64
	for _, row := range baseRows {
		if v := metricOf(row.m, t.primary); v != nil {
			base = append(base, *v)
		}
	}
	if len(base) < 14 {
		return nil
	}
	med := median(base)
	var dev []float64
	for _, v := range base {
		dev = append(dev, math.Abs(v-med))
	}
	spread := 1.4826 * median(dev)
	var out []unusualDay
	for _, row := range nowRows {
		v := metricOf(row.m, t.primary)
		if v == nil {
			continue
		}
		diff := *v - med
		if math.Abs(diff) < 20 || math.Abs(diff) < 3*spread {
			continue
		}
		u := unusualDay{Day: row.day, Connector: t.Connector, Name: t.Name, Metric: t.primary, Value: tidy(t.primary, *v), Typical: tidy(t.primary, med)}
		switch {
		case *v == 0:
			u.What = "nothing recorded: check the tracking script or the connection"
		case *v >= 1.5*med:
			u.What = fmt.Sprintf("%.1f times the usual", *v/med)
		case *v <= 0.5*med:
			u.What = fmt.Sprintf("%.0f%% of the usual", *v/med*100)
		default:
			continue
		}
		out = append(out, u)
	}
	return out
}

func median(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// moversFor finds the pages, sources or search queries that changed most
// on each website. Search queries add Google and Bing up; pages and
// sources come from the tool that stores the most of them, since adding
// two tools' counts of the same visits up would double them.
func moversFor(report string, rows []dayRow, tools map[string]*toolChange, top int) map[string]*movers {
	metric := map[string][]string{
		connectors.ReportPages:         {"pageviews", "visitors", "sessions"},
		connectors.ReportSources:       {"visitors", "sessions", "pageviews"},
		connectors.ReportSearchQueries: {"clicks", "impressions"},
	}[report]
	type agg struct{ now, before float64 }
	byHostTool := map[string]map[string][]dayRow{}
	for _, row := range rows {
		t := tools[row.integration]
		if t == nil {
			continue
		}
		if byHostTool[row.host] == nil {
			byHostTool[row.host] = map[string][]dayRow{}
		}
		byHostTool[row.host][row.integration] = append(byHostTool[row.host][row.integration], row)
	}
	out := map[string]*movers{}
	for host, perTool := range byHostTool {
		var use []string
		if report == connectors.ReportSearchQueries {
			for id := range perTool {
				use = append(use, id)
			}
			sort.Strings(use)
		} else {
			best, bestN := "", 0
			for id, list := range perTool {
				if len(list) > bestN || (len(list) == bestN && id < best) {
					best, bestN = id, len(list)
				}
			}
			use = []string{best}
		}
		// The metric every chosen tool measures.
		pick := ""
		for _, m := range metric {
			for _, row := range perTool[use[0]] {
				if metricOf(row.m, m) != nil {
					pick = m
					break
				}
			}
			if pick != "" {
				break
			}
		}
		if pick == "" {
			continue
		}
		keys := map[string]*agg{}
		hasBefore := false
		var names []string
		for _, id := range use {
			t := tools[id]
			names = append(names, t.Name)
			for _, row := range perTool[id] {
				v := metricOf(row.m, pick)
				if v == nil {
					continue
				}
				a := keys[row.key]
				if a == nil {
					a = &agg{}
					keys[row.key] = a
				}
				switch {
				case inWindow(row.day, t.Now):
					a.now += *v
				case inWindow(row.day, t.Before):
					a.before += *v
					hasBefore = true
				}
			}
		}
		if !hasBefore {
			continue
		}
		mv := &movers{Report: report, Tools: names, Metric: pick, Up: []moverRow{}, Down: []moverRow{}}
		var list []moverRow
		for k, a := range keys {
			if d := a.now - a.before; d != 0 {
				list = append(list, moverRow{Key: k, Now: tidy(pick, a.now), Before: tidy(pick, a.before), Change: tidy(pick, d)})
			}
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].Change != list[j].Change {
				return list[i].Change > list[j].Change
			}
			return list[i].Key < list[j].Key
		})
		for _, m := range list {
			if m.Change > 0 && len(mv.Up) < top {
				mv.Up = append(mv.Up, m)
			}
		}
		for i := len(list) - 1; i >= 0 && len(mv.Down) < top; i-- {
			if list[i].Change < 0 {
				mv.Down = append(mv.Down, list[i])
			}
		}
		out[host] = mv
	}
	return out
}

// seoChanges compares each SEO connection's latest snapshot of a domain
// with the one before.
func (s *Server) seoChanges(ctx context.Context, ws, host string, top int, sel brandSel) (map[string][]seoChange, error) {
	inBrand, err := s.hostBrandFilter(ctx, ws, sel)
	if err != nil {
		return nil, err
	}
	sql := `SELECT s.integration_id::text, i.name, s.connector, s.host, s.report, to_char(s.day, 'YYYY-MM-DD'), s.key, s.metrics
		FROM seo_snapshots s JOIN integrations i ON i.id = s.integration_id
		JOIN (SELECT integration_id, host, report, day, dense_rank() OVER (PARTITION BY integration_id, host, report ORDER BY day DESC) AS n
			FROM (SELECT DISTINCT integration_id, host, report, day FROM seo_snapshots WHERE workspace_id = $1) d) r
			ON r.integration_id = s.integration_id AND r.host = s.host AND r.report = s.report AND r.day = s.day AND r.n <= 2
		WHERE s.workspace_id = $1 AND s.report IN ('overview', 'keywords')`
	args := []any{ws}
	if host != "" {
		sql += ` AND s.host = $2`
		args = append(args, host)
	}
	rows, err := s.pool.Query(ctx, sql+` ORDER BY s.day DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type snap struct {
		day  string
		rows map[string]connectors.SEOMetrics
	}
	type item struct {
		name, connector, host string
		overview, keywords    []*snap
	}
	items := map[string]*item{}
	var order []string
	for rows.Next() {
		var id, name, conn, h, report, day, key string
		var raw []byte
		if err := rows.Scan(&id, &name, &conn, &h, &report, &day, &key, &raw); err != nil {
			return nil, err
		}
		if !inBrand(h) {
			continue
		}
		k := id + "|" + h
		it := items[k]
		if it == nil {
			it = &item{name: name, connector: conn, host: h}
			items[k] = it
			order = append(order, k)
		}
		list := &it.overview
		if report == "keywords" {
			list = &it.keywords
		}
		if len(*list) == 0 || (*list)[len(*list)-1].day != day {
			*list = append(*list, &snap{day: day, rows: map[string]connectors.SEOMetrics{}})
		}
		var m connectors.SEOMetrics
		_ = json.Unmarshal(raw, &m)
		(*list)[len(*list)-1].rows[key] = m
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := map[string][]seoChange{}
	for _, k := range order {
		it := items[k]
		if len(it.overview) == 0 {
			continue
		}
		c := seoChange{Integration: strings.SplitN(k, "|", 2)[0], Name: it.name, Connector: it.connector, Day: it.overview[0].day, Metrics: []*change{}}
		cur := it.overview[0].rows[""]
		var prev *connectors.SEOMetrics
		if len(it.overview) > 1 {
			p := it.overview[1].rows[""]
			prev = &p
			c.PreviousDay = it.overview[1].day
		}
		for _, name := range seoOrder {
			var b *float64
			if prev != nil {
				b = seoMetricOf(*prev, name)
			}
			if ch := makeChange(name, seoMetricOf(cur, name), b); ch != nil {
				c.Metrics = append(c.Metrics, ch)
			}
		}
		if len(it.keywords) > 1 {
			now, before := it.keywords[0].rows, it.keywords[1].rows
			var moves []keywordMove
			for kw, m := range now {
				p, ok := before[kw]
				if !ok {
					c.KeywordsNew++
					continue
				}
				if m.Position != nil && p.Position != nil && *m.Position != *p.Position {
					mv := keywordMove{Keyword: kw, Position: *m.Position, Previous: *p.Position}
					if m.Volume != nil {
						mv.Volume = *m.Volume
					}
					moves = append(moves, mv)
				}
			}
			for kw := range before {
				if _, ok := now[kw]; !ok {
					c.KeywordsLost++
				}
			}
			// Biggest moves first; ties go to the keyword more people search.
			sort.Slice(moves, func(i, j int) bool {
				di, dj := moves[i].Previous-moves[i].Position, moves[j].Previous-moves[j].Position
				if di != dj {
					return di > dj
				}
				if moves[i].Volume != moves[j].Volume {
					return moves[i].Volume > moves[j].Volume
				}
				return moves[i].Keyword < moves[j].Keyword
			})
			for _, m := range moves {
				if m.Position < m.Previous && len(c.KeywordsUp) < top {
					c.KeywordsUp = append(c.KeywordsUp, m)
				}
			}
			for i := len(moves) - 1; i >= 0 && len(c.KeywordsDown) < top; i-- {
				if moves[i].Position > moves[i].Previous {
					c.KeywordsDown = append(c.KeywordsDown, moves[i])
				}
			}
		}
		out[it.host] = append(out[it.host], c)
	}
	return out, nil
}

// siteGaps says what a website is missing for a full picture, and how to
// fix it. host "" is the whole workspace.
func siteGaps(host string, tools []*toolChange, syncs []syncStatus, seo []seoSource, hasSEO bool) []gap {
	out := []gap{}
	var traffic, search, conversions bool
	for _, t := range tools {
		for _, c := range t.Metrics {
			switch c.Metric {
			case "visitors", "pageviews", "sessions":
				traffic = true
			case "impressions":
				search = true
			case "conversions":
				conversions = true
			}
		}
	}
	for _, st := range syncs {
		if st.Host != host {
			continue
		}
		switch {
		case st.Paused:
		case st.LastError != nil && *st.LastError != "":
			out = append(out, gap{What: st.Name + " is not syncing", Why: *st.LastError, IntegrationID: st.Integration,
				Action: "Fix the connection in Connectors (or test_integration), then sync_analytics."})
		case st.LastOK == nil:
			out = append(out, gap{What: st.Name + " has not synced yet", Why: "The first sync starts within a few minutes of connecting.", IntegrationID: st.Integration,
				Action: "Wait, or sync_analytics with this integration_id."})
		case time.Since(*st.LastOK) > 48*time.Hour:
			out = append(out, gap{What: st.Name + " last synced " + st.LastOK.UTC().Format(time.DateOnly), Why: "Its numbers may be out of date.", IntegrationID: st.Integration,
				Action: "Run sync_analytics with this integration_id."})
		}
	}
	if host == "" && len(tools) > 0 {
		return append(out, gap{What: "Tools without a domain", Why: "These tools' sites show no domain, so they cannot line up with the other tools for a website.",
			Action: "Set each one's domain with update_analytics_site (domain), e.g. example.com."})
	}
	if !traffic {
		out = append(out, gap{What: "No website traffic", Why: "No analytics tool reports visitors for this website.",
			Connectors: []string{"google_analytics", "plausible", "posthog", "vercel_analytics", "cloudflare_web_analytics"},
			Action:     "request_connection for the tool the website already uses, then set its domain with update_analytics_site."})
	}
	if !search {
		out = append(out, gap{What: "No search console", Why: "Without one there are no search queries, clicks or positions from Google or Bing.",
			Connectors: []string{"google_search_console", "bing_webmaster"}, Action: "request_connection; one Google sign-in connects Search Console and Google Analytics."})
	}
	if traffic && !conversions {
		out = append(out, gap{What: "No conversions", Why: "The analytics tools report no key events or goals, so traffic cannot be tied to signups or demos.",
			Action: "Mark signups or demo requests as key events (GA4) or goals (Plausible, Fathom, Matomo) in the tool itself."})
	}
	if !hasSEO {
		if len(seo) == 0 {
			out = append(out, gap{What: "No SEO tool", Why: "Domain rating, backlinks and keyword rankings come from DataForSEO or Ahrefs.",
				Connectors: []string{"dataforseo", "ahrefs"}, Action: "request_connection. Both cost money per call, within a monthly budget."})
		} else if host != "" {
			out = append(out, gap{What: "No SEO snapshot for " + host, Why: "The SEO tools do not follow this domain yet, or the first weekly snapshot has not run.",
				IntegrationID: seo[0].ID, Action: "Add the domain with update_seo_settings, or take one now with sync_seo (costs money)."})
		}
	}
	return out
}

func fmtNum(metric string, v float64) string {
	switch metric {
	case "ctr", "bounce_rate":
		return strconv.FormatFloat(v*100, 'f', 1, 64) + "%"
	case "position", "avg_duration", "domain_rating":
		return strconv.FormatFloat(v, 'f', 1, 64)
	}
	s := strconv.FormatInt(int64(math.Round(math.Abs(v))), 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if v < 0 {
		s = "-" + s
	}
	return s
}

var metricLabel = map[string]string{
	"visitors": "Visitors", "pageviews": "Page views", "sessions": "Visits", "clicks": "Search clicks", "impressions": "Search impressions",
	"events": "Events", "conversions": "Conversions", "domain_rating": "Domain rating", "referring_domains": "Referring domains",
	"organic_keywords": "Ranking keywords", "top3": "Keywords in the top 3", "organic_traffic": "Estimated search visits",
}

func sayChange(label string, c *change, period, source string) string {
	s := label + " " + fmtNum(c.Metric, *c.Now)
	switch {
	case c.Before == nil:
		s += ", nothing stored for " + period + " before"
	case c.ChangePct != nil && math.Abs(*c.ChangePct) >= 0.5:
		dir := "up"
		if *c.ChangePct < 0 {
			dir = "down"
		}
		s += fmt.Sprintf(", %s %s%% on %s before (%s)", dir, strconv.FormatFloat(math.Abs(*c.ChangePct), 'f', -1, 64), period, fmtNum(c.Metric, *c.Before))
	case *c.Change == 0 || (c.ChangePct != nil && math.Abs(*c.ChangePct) < 0.5):
		s += ", about the same as " + period + " before"
	default:
		s += fmt.Sprintf(", from %s", fmtNum(c.Metric, *c.Before))
	}
	return s + " (" + source + ")."
}

// headline is a few plain sentences an agent can repeat as they are.
func headline(sc *websiteChanges, days int) []string {
	period := fmt.Sprintf("the %d days", days)
	if days == 1 {
		period = "the day"
	}
	out := []string{}
	// The tool with the most traffic speaks for the website.
	var lead *toolChange
	var leadV float64
	for _, t := range sc.Tools {
		if t.primary == "" || t.primary == "clicks" || t.primary == "events" {
			continue
		}
		for _, c := range t.Metrics {
			if c.Metric == t.primary && *c.Now > leadV {
				lead, leadV = t, *c.Now
			}
		}
	}
	if lead != nil {
		for _, c := range lead.Metrics {
			if c.Metric == lead.primary || c.Metric == "conversions" {
				out = append(out, sayChange(metricLabel[c.Metric], c, period, lead.Name+", "+lead.Now.From+" to "+lead.Now.To))
			}
		}
	}
	search := sc.Search
	source := "Google and Bing added up"
	if search == nil {
		for _, t := range sc.Tools {
			for _, c := range t.Metrics {
				if c.Metric == "clicks" {
					search, source = []*change{c}, t.Name+", "+t.Now.From+" to "+t.Now.To
				}
			}
		}
	}
	for _, c := range search {
		if c.Metric == "clicks" {
			out = append(out, sayChange("Search clicks", c, period, source))
		}
	}
	for _, mv := range sc.Movers {
		what := map[string]string{connectors.ReportPages: "page", connectors.ReportSources: "source", connectors.ReportSearchQueries: "search query"}[mv.Report]
		if len(mv.Up) > 0 {
			m := mv.Up[0]
			out = append(out, fmt.Sprintf("Biggest rise by %s: %s, %s %s (from %s).", what, m.Key, fmtNum(mv.Metric, m.Now), strings.ReplaceAll(metricLabel[mv.Metric], "Search ", ""), fmtNum(mv.Metric, m.Before)))
		}
		if len(mv.Down) > 0 {
			m := mv.Down[0]
			out = append(out, fmt.Sprintf("Biggest fall by %s: %s, %s %s (from %s).", what, m.Key, fmtNum(mv.Metric, m.Now), strings.ReplaceAll(metricLabel[mv.Metric], "Search ", ""), fmtNum(mv.Metric, m.Before)))
		}
	}
	for _, u := range sc.Unusual {
		out = append(out, fmt.Sprintf("Unusual day: %s had %s %s in %s, %s (typical %s).", u.Day, fmtNum(u.Metric, u.Value), strings.ToLower(metricLabel[u.Metric]), u.Name, u.What, fmtNum(u.Metric, u.Typical)))
		break
	}
	for _, c := range sc.SEO {
		since := "first snapshot, nothing to compare yet"
		if c.PreviousDay != "" {
			since = "since " + c.PreviousDay
		}
		var parts []string
		for _, m := range c.Metrics {
			if m.Metric != "domain_rating" && m.Metric != "organic_keywords" && m.Metric != "top3" {
				continue
			}
			p := strings.ToLower(metricLabel[m.Metric]) + " " + fmtNum(m.Metric, *m.Now)
			if m.Change != nil && *m.Change != 0 {
				sign := "+"
				if *m.Change < 0 {
					sign = ""
				}
				p += " (" + sign + fmtNum(m.Metric, *m.Change) + ")"
			}
			parts = append(parts, p)
		}
		if len(parts) > 0 {
			s := fmt.Sprintf("SEO (%s, %s, %s): %s", c.Name, c.Day, since, strings.Join(parts, ", "))
			if len(c.KeywordsUp)+len(c.KeywordsDown) > 0 {
				s += fmt.Sprintf("; %d keywords moved up and %d down", len(c.KeywordsUp), len(c.KeywordsDown))
			}
			out = append(out, s+".")
		}
	}
	if len(out) == 0 {
		out = append(out, "Nothing stored for this website yet.")
	}
	return out
}

// catalog: what each website can be asked, and from which tool.

type catalogTool struct {
	Integration string `json:"integration_id"`
	Name        string `json:"name"`
	Connector   string `json:"connector"`
	Site        string `json:"site,omitempty"`
	Status      string `json:"status"`
	// Measures lists, per report, the metrics this tool has stored.
	Measures   map[string][]string `json:"measures"`
	FirstDay   *string             `json:"first_day,omitempty"`
	LastDay    *string             `json:"last_day,omitempty"`
	LastSynced *time.Time          `json:"last_synced_at,omitempty"`
	Error      *string             `json:"error,omitempty"`
}

type catalogSEO struct {
	Integration   string   `json:"integration_id"`
	Name          string   `json:"name"`
	Connector     string   `json:"connector"`
	Reports       []string `json:"reports"`
	Snapshots     int      `json:"snapshots"`
	FirstSnapshot string   `json:"first_snapshot,omitempty"`
	LastSnapshot  string   `json:"last_snapshot,omitempty"`
	BudgetLeft    float64  `json:"budget_left"`
	BudgetUnit    string   `json:"budget_unit"`
}

type catalogSite struct {
	Host  string        `json:"host"`
	Tools []catalogTool `json:"tools"`
	SEO   []catalogSEO  `json:"seo"`
	Gaps  []gap         `json:"gaps"`
}

func (s *Server) analyticsCatalog(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws := accessFrom(ctx).WorkspaceID
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	syncs, err := s.syncStatuses(ctx, ws, sel)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	sources, err := s.analyticsSources(ctx, ws, sel)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	status := map[string]analyticsSource{}
	for _, src := range sources {
		status[src.ID] = src
	}
	measures := map[string]map[string][]string{}
	rows, err := s.pool.Query(ctx, `
		SELECT integration_id::text, report, bool_or(visitors IS NOT NULL), bool_or(pageviews IS NOT NULL), bool_or(sessions IS NOT NULL),
			bool_or(bounce_rate IS NOT NULL), bool_or(avg_duration IS NOT NULL), bool_or(clicks IS NOT NULL), bool_or(impressions IS NOT NULL),
			bool_or(ctr IS NOT NULL), bool_or(position IS NOT NULL), bool_or(events IS NOT NULL), bool_or(conversions IS NOT NULL)
		FROM analytics_daily WHERE workspace_id = $1 AND day > now() - interval '35 days'
		GROUP BY integration_id, report`, ws)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	names := []string{"visitors", "pageviews", "sessions", "bounce_rate", "avg_duration", "clicks", "impressions", "ctr", "position", "events", "conversions"}
	for rows.Next() {
		var id, report string
		has := make([]bool, len(names))
		dst := []any{&id, &report}
		for i := range has {
			dst = append(dst, &has[i])
		}
		if err := rows.Scan(dst...); err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		if measures[id] == nil {
			measures[id] = map[string][]string{}
		}
		list := []string{}
		for i, ok := range has {
			if ok {
				list = append(list, names[i])
			}
		}
		measures[id][report] = list
	}
	rows.Close()
	if rows.Err() != nil {
		s.internal(w, r, rows.Err())
		return
	}

	sites := map[string]*catalogSite{}
	var order []string
	site := func(h string) *catalogSite {
		if sites[h] == nil {
			sites[h] = &catalogSite{Host: h, Tools: []catalogTool{}, SEO: []catalogSEO{}}
			order = append(order, h)
		}
		return sites[h]
	}
	var changes = map[string][]*toolChange{}
	for _, st := range syncs {
		src := status[st.Integration]
		t := catalogTool{Integration: st.Integration, Name: st.Name, Connector: st.Connector, Site: st.Site, Status: src.Status,
			Measures: measures[st.Integration], FirstDay: st.FirstDay, LastDay: st.LastDay, LastSynced: st.LastOK, Error: st.LastError}
		if t.Measures == nil {
			t.Measures = map[string][]string{}
		}
		sc := site(st.Host)
		sc.Tools = append(sc.Tools, t)
		// The gap check reads measured metrics the same way changes does.
		tc := &toolChange{}
		for _, m := range t.Measures[connectors.ReportTraffic] {
			tc.Metrics = append(tc.Metrics, &change{Metric: m})
		}
		changes[st.Host] = append(changes[st.Host], tc)
	}
	seoSources, err := s.seoSources(ctx, ws, sel)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for _, src := range seoSources {
		for _, d := range src.Domains {
			c := catalogSEO{Integration: src.ID, Name: src.Name, Connector: src.Connector, Reports: src.Reports,
				BudgetLeft: tidy("", src.Budget.Left()), BudgetUnit: src.Budget.Unit}
			var first, last *string
			if err := s.pool.QueryRow(ctx, `SELECT count(DISTINCT day), to_char(min(day), 'YYYY-MM-DD'), to_char(max(day), 'YYYY-MM-DD')
				FROM seo_snapshots WHERE integration_id = $1 AND host = $2 AND report = 'overview'`, src.ID, d.Host).Scan(&c.Snapshots, &first, &last); err != nil {
				s.internal(w, r, err)
				return
			}
			if first != nil {
				c.FirstSnapshot, c.LastSnapshot = *first, *last
			}
			sc := site(d.Host)
			sc.SEO = append(sc.SEO, c)
		}
	}
	out := []*catalogSite{}
	sort.SliceStable(order, func(i, j int) bool { return order[i] != "" && order[j] == "" })
	for _, h := range order {
		sc := sites[h]
		hasSEO := false
		for _, c := range sc.SEO {
			hasSEO = hasSEO || c.Snapshots > 0
		}
		sc.Gaps = siteGaps(h, changes[h], syncs, seoSources, hasSEO)
		out = append(out, sc)
	}
	resp := map[string]any{"data": out, "units": metricUnits,
		"reports": map[string]string{
			connectors.ReportTraffic:       "totals per day",
			connectors.ReportPages:         "page paths",
			connectors.ReportSources:       "referrers and channels",
			connectors.ReportSearchQueries: "search queries (search consoles only)",
			connectors.ReportEvents:        "events and conversions",
		},
		"ask": map[string]string{
			"what changed":                       "get_analytics_changes (GET /analytics/changes): this period against the one before, top movers, unusual days, SEO moves",
			"totals by website":                  "get_analytics_overview (GET /analytics/overview)",
			"trend, top pages or queries":        "query_analytics_history (GET /analytics/history)",
			"do the tools agree":                 "compare_analytics_tools (GET /analytics/compare)",
			"today, or a report not stored":      "get_analytics_report (live, spends the provider's rate limit)",
			"SEO over time":                      "get_seo_history (free)",
			"backlinks, competitors, any domain": "get_seo_report (live, costs money within the budget)",
		}}
	if len(out) == 0 {
		resp["gaps"] = siteGaps("", nil, syncs, seoSources, false)
	}
	writeJSON(w, http.StatusOK, resp)
}
