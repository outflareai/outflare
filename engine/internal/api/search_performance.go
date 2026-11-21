package api

import (
	"context"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/seointel"
)

// Search Console's Performance report for one SEO site, asked live with
// the workspace's own Search Console sign-in, so the numbers match what
// Search Console shows for the same dates and filters: the totals, the
// days, and the rows of one dimension, for the period and a period to
// compare with. Stored search data (GET .../search) cannot answer every
// mix of query, country and device; Google can, for free.

var (
	perfTypes      = []string{"web", "image", "video", "news", "discover", "googleNews"}
	perfDimensions = []string{"query", "page", "country", "device", "searchAppearance", "date"}
	perfFilterDims = []string{"query", "page", "country", "device", "searchAppearance"}
	perfOperators  = []string{"equals", "notEquals", "contains", "notContains", "includingRegex", "excludingRegex"}
)

const (
	perfMaxFilters = 20
	perfMaxRows    = 25000
	// Search Console keeps 16 months.
	perfMaxDays = 500
	// Search Console keeps hourly data for the last ten days or so.
	perfMaxHours = 72
)

type perfMetrics struct {
	Clicks      float64 `json:"clicks"`
	Impressions float64 `json:"impressions"`
	CTR         float64 `json:"ctr"`
	Position    float64 `json:"position"`
}

type perfPoint struct {
	Date string `json:"date"`
	perfMetrics
}

type perfRow struct {
	Key string `json:"key"`
	perfMetrics
	// The same key in the period compared with; null when it had none.
	Previous *perfMetrics `json:"previous"`
}

type perfAnswer struct {
	Source         *seointel.Source                   `json:"source"`
	Type           string                             `json:"type"`
	Dimension      string                             `json:"dimension"`
	From           string                             `json:"from"`
	To             string                             `json:"to"`
	CompareFrom    *string                            `json:"compare_from"`
	CompareTo      *string                            `json:"compare_to"`
	Filters        []connectors.SearchAnalyticsFilter `json:"filters"`
	Totals         perfMetrics                        `json:"totals"`
	PreviousTotals *perfMetrics                       `json:"previous_totals"`
	Series         []perfPoint                        `json:"series"`
	PreviousSeries []perfPoint                        `json:"previous_series"`
	Rows           []perfRow                          `json:"rows"`
	Notes          []string                           `json:"notes"`
	Cached         bool                               `json:"cached"`
	FetchedAt      *time.Time                         `json:"fetched_at"`
}

func metricsOf(r connectors.SearchAnalyticsRow) perfMetrics {
	return perfMetrics{Clicks: r.Clicks, Impressions: r.Impressions, CTR: r.CTR, Position: r.Position}
}

// perfDate reads one date parameter, YYYY-MM-DD.
func perfDate(v string) (time.Time, bool) {
	d, err := time.Parse(time.DateOnly, dateOnly(v))
	return d, err == nil
}

// perfFilter reads one filter, dimension:operator:expression; the
// expression may itself hold colons.
func perfFilter(v string) (connectors.SearchAnalyticsFilter, string) {
	parts := strings.SplitN(v, ":", 3)
	if len(parts) != 3 {
		return connectors.SearchAnalyticsFilter{}, "a filter is dimension:operator:expression, such as query:contains:proxy"
	}
	f := connectors.SearchAnalyticsFilter{Dimension: parts[0], Operator: parts[1], Expression: parts[2]}
	switch {
	case !slices.Contains(perfFilterDims, f.Dimension):
		return f, "a filter's dimension is " + strings.Join(perfFilterDims, ", ")
	case !slices.Contains(perfOperators, f.Operator):
		return f, "a filter's operator is " + strings.Join(perfOperators, ", ")
	case f.Expression == "" || len(f.Expression) > 4096:
		return f, "a filter's expression is 1 to 4096 characters"
	}
	return f, ""
}

func (s *Server) siteSearchPerformance(w http.ResponseWriter, r *http.Request) {
	_, src, ok := s.searchSource(w, r)
	if !ok {
		return
	}
	v := r.URL.Query()
	out := perfAnswer{Type: v.Get("type"), Dimension: v.Get("dimension"), Filters: []connectors.SearchAnalyticsFilter{},
		Series: []perfPoint{}, PreviousSeries: []perfPoint{}, Rows: []perfRow{}, Notes: []string{}}
	if out.Type == "" {
		out.Type = "web"
	}
	if out.Dimension == "" {
		out.Dimension = "query"
	}
	if !slices.Contains(perfTypes, out.Type) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "type is "+strings.Join(perfTypes, ", "))
		return
	}
	if !slices.Contains(perfDimensions, out.Dimension) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "dimension is "+strings.Join(perfDimensions, ", "))
		return
	}
	// The default is the last 28 days that Search Console has, as its own
	// report opens.
	end := time.Now().UTC().AddDate(0, 0, -1)
	from, to := end.AddDate(0, 0, -27), end
	for name, dst := range map[string]*time.Time{"from": &from, "to": &to} {
		if x := v.Get(name); x != "" {
			d, ok := perfDate(x)
			if !ok {
				writeError(w, r, http.StatusBadRequest, "invalid_request", name+" is a date, YYYY-MM-DD")
				return
			}
			*dst = d
		}
	}
	if from.After(to) || to.Sub(from) > perfMaxDays*24*time.Hour {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "from is on or before to, at most 16 months apart")
		return
	}
	out.From, out.To = from.Format(time.DateOnly), to.Format(time.DateOnly)
	if cf, ct := v.Get("compare_from"), v.Get("compare_to"); cf != "" || ct != "" {
		a, okA := perfDate(cf)
		b, okB := perfDate(ct)
		if !okA || !okB || a.After(b) || b.Sub(a) > perfMaxDays*24*time.Hour {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "compare_from and compare_to are two dates, YYYY-MM-DD, the first on or before the second")
			return
		}
		x, y := a.Format(time.DateOnly), b.Format(time.DateOnly)
		out.CompareFrom, out.CompareTo = &x, &y
	}
	filters := v["filter"]
	if len(filters) > perfMaxFilters {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "at most "+strconv.Itoa(perfMaxFilters)+" filters")
		return
	}
	for _, x := range filters {
		f, msg := perfFilter(x)
		if msg != "" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
			return
		}
		out.Filters = append(out.Filters, f)
	}
	// Discover and Google News have no queries or positions.
	if out.Type == "discover" || out.Type == "googleNews" {
		if out.Dimension == "query" || slices.ContainsFunc(out.Filters, func(f connectors.SearchAnalyticsFilter) bool { return f.Dimension == "query" }) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "Search Console has no queries for "+out.Type+": group or filter by page, country, device or search appearance")
			return
		}
	}
	limit, err := limitParam(r, 1000, perfMaxRows)
	if s.allocError(w, r, err) {
		return
	}
	// hours asks for the last so many hours, hour by hour, as Search
	// Console's "24 hours" does; compare_previous sets them against the
	// hours before.
	hours := 0
	if x := v.Get("hours"); x != "" {
		n, err := strconv.Atoi(x)
		if err != nil || n < 1 || n > perfMaxHours {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "hours is 1 to "+strconv.Itoa(perfMaxHours))
			return
		}
		hours = n
		if v.Get("compare_previous") == "true" {
			x, y := "", ""
			out.CompareFrom, out.CompareTo = &x, &y
		} else {
			out.CompareFrom, out.CompareTo = nil, nil
		}
	}
	if src == nil {
		out.Notes = append(out.Notes, "No Search Console property covers this site yet. Connect Google Search Console with an account that can see it.")
		writeJSON(w, http.StatusOK, out)
		return
	}
	out.Source = src
	ws := accessFrom(r.Context()).WorkspaceID
	o, err := s.integrations.Open(r.Context(), s.pool, ws, src.IntegrationID)
	if err != nil {
		if connectors.KindOf(err) != connectors.ErrUnavailable {
			connectorError(w, r, err)
		} else {
			s.internal(w, r, err)
		}
		return
	}
	reader, ok := o.Connector.(connectors.SearchAnalyticsReader)
	if !ok {
		writeError(w, r, http.StatusConflict, "connector_needed", o.Name+" can't answer Search Console's Performance report; connect Google Search Console")
		return
	}
	key := strings.Join(append([]string{"performance", src.Property, out.Type, out.Dimension, out.From, out.To, deref(out.CompareFrom), deref(out.CompareTo), strconv.Itoa(limit), "h" + strconv.Itoa(hours)}, filters...), "|")
	if v.Get("fresh") != "true" {
		var kept perfAnswer
		if at, ok := s.cached(r.Context(), o, key, analyticsCacheFor, &kept); ok {
			kept.Cached, kept.FetchedAt = true, &at
			writeJSON(w, http.StatusOK, kept)
			return
		}
	}
	ask := s.askPerformance
	if hours > 0 {
		ask = func(ctx context.Context, o *connectors.Opened, reader connectors.SearchAnalyticsReader, property string, limit int, out *perfAnswer) error {
			return askHourly(ctx, o, reader, property, limit, hours, out)
		}
	}
	if err := ask(r.Context(), o, reader, src.Property, limit, &out); err != nil {
		if connectors.KindOf(err) == connectors.ErrInvalid {
			connectorError(w, r, err)
		} else {
			s.analyticsFailed(w, r, o, err)
		}
		return
	}
	if hours > 0 {
		out.Notes = append(out.Notes, "Hour by hour, live from Search Console; the newest hours may still change.")
	} else {
		out.Notes = append(out.Notes, "Live from Search Console, which is usually two to three days behind; the newest days may still change.")
	}
	if out.Dimension == "query" {
		out.Notes = append(out.Notes, "Google leaves out rare queries for privacy, so the query rows add up to less than the totals.")
	}
	now := time.Now().UTC()
	out.FetchedAt = &now
	s.keep(r.Context(), o, key, out)
	writeJSON(w, http.StatusOK, out)
}

// askPerformance asks Google for the totals, the days and the rows of each
// period at once, so the answer takes as long as the slowest call.
func (s *Server) askPerformance(ctx context.Context, o *connectors.Opened, reader connectors.SearchAnalyticsReader, property string, limit int, out *perfAnswer) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	type period struct{ from, to string }
	periods := []period{{out.From, out.To}}
	if out.CompareFrom != nil {
		periods = append(periods, period{*out.CompareFrom, *out.CompareTo})
	}
	type ask struct {
		period int
		dims   []string
		limit  int
		rows   []connectors.SearchAnalyticsRow
		err    error
	}
	var asks []*ask
	for i := range periods {
		asks = append(asks, &ask{period: i, limit: 1}, &ask{period: i, dims: []string{"date"}, limit: perfMaxDays})
		if out.Dimension != "date" {
			asks = append(asks, &ask{period: i, dims: []string{out.Dimension}, limit: limit})
		}
	}
	var wg sync.WaitGroup
	for _, a := range asks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := periods[a.period]
			a.rows, a.err = reader.SearchAnalytics(ctx, o.Client, o.Creds, connectors.SearchAnalyticsQuery{
				Site: property, From: p.from, To: p.to, Type: out.Type, Dimensions: a.dims, Filters: out.Filters, Limit: a.limit})
		}()
	}
	wg.Wait()
	rows := make([][]connectors.SearchAnalyticsRow, len(periods))
	for _, a := range asks {
		if a.err != nil {
			return a.err
		}
		switch {
		case a.dims == nil:
			t := perfMetrics{}
			if len(a.rows) > 0 {
				t = metricsOf(a.rows[0])
			}
			if a.period == 0 {
				out.Totals = t
			} else {
				out.PreviousTotals = &t
			}
		case a.dims[0] == "date":
			pts := make([]perfPoint, 0, len(a.rows))
			for _, x := range a.rows {
				if len(x.Keys) > 0 {
					pts = append(pts, perfPoint{Date: x.Keys[0], perfMetrics: metricsOf(x)})
				}
			}
			sort.Slice(pts, func(i, j int) bool { return pts[i].Date < pts[j].Date })
			if a.period == 0 {
				out.Series = pts
			} else {
				out.PreviousSeries = pts
			}
			if out.Dimension == "date" {
				rows[a.period] = a.rows
			}
		default:
			rows[a.period] = a.rows
		}
	}
	out.Rows = perfRows(rows, limit, out.Dimension == "date")
	return nil
}

// perfRows joins the period's rows with the same keys in the period
// compared with, and keeps the keys only the earlier period had (what was
// lost), most clicks first; days newest first.
func perfRows(periods [][]connectors.SearchAnalyticsRow, limit int, byDate bool) []perfRow {
	out := []perfRow{}
	index := map[string]int{}
	for _, x := range periods[0] {
		if len(x.Keys) == 0 {
			continue
		}
		index[x.Keys[0]] = len(out)
		out = append(out, perfRow{Key: x.Keys[0], perfMetrics: metricsOf(x)})
	}
	if len(periods) > 1 {
		for _, x := range periods[1] {
			if len(x.Keys) == 0 {
				continue
			}
			m := metricsOf(x)
			if i, ok := index[x.Keys[0]]; ok {
				out[i].Previous = &m
			} else {
				out = append(out, perfRow{Key: x.Keys[0], Previous: &m})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if byDate {
			return out[i].Key > out[j].Key
		}
		if out[i].Clicks != out[j].Clicks {
			return out[i].Clicks > out[j].Clicks
		}
		return out[i].Impressions > out[j].Impressions
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// askHourly answers the last hours hour by hour: Search Console's hourly
// data for the last few days, cut to the hours up to the newest one it has
// (and the same number of hours before it, when compared), with the totals
// and rows added up from those hours. Each point's date is the hour, as
// Search Console names it (2026-11-17T10:00:00-08:00).
func askHourly(ctx context.Context, o *connectors.Opened, reader connectors.SearchAnalyticsReader, property string, limit, hours int, out *perfAnswer) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	now := time.Now().UTC()
	from, to := now.AddDate(0, 0, -(hours*2)/24-2).Format(time.DateOnly), now.AddDate(0, 0, 1).Format(time.DateOnly)
	ask := func(dims []string, n int) ([]connectors.SearchAnalyticsRow, error) {
		return reader.SearchAnalytics(ctx, o.Client, o.Creds, connectors.SearchAnalyticsQuery{
			Site: property, From: from, To: to, Type: out.Type, Dimensions: dims, Filters: out.Filters, Limit: n, Hourly: true})
	}
	var (
		wg               sync.WaitGroup
		hourRows, byKey  []connectors.SearchAnalyticsRow
		hourErr, keysErr error
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		hourRows, hourErr = ask([]string{"hour"}, perfMaxDays)
	}()
	if out.Dimension != "date" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			byKey, keysErr = ask([]string{out.Dimension, "hour"}, perfMaxRows)
		}()
	}
	wg.Wait()
	if hourErr != nil {
		return hourErr
	}
	if keysErr != nil {
		return keysErr
	}
	hourOf := func(s string) (time.Time, bool) {
		t, err := time.Parse(time.RFC3339, s)
		return t.UTC(), err == nil
	}
	var end time.Time
	for _, x := range hourRows {
		if len(x.Keys) > 0 {
			if h, ok := hourOf(x.Keys[0]); ok && h.After(end) {
				end = h
			}
		}
	}
	start := end.Add(-time.Duration(hours-1) * time.Hour)
	prevStart := start.Add(-time.Duration(hours) * time.Hour)
	compare := out.CompareFrom != nil
	// which period an hour falls in: 0 now, 1 before, -1 neither.
	period := func(key string) int {
		h, ok := hourOf(key)
		switch {
		case !ok || end.IsZero() || h.After(end):
			return -1
		case !h.Before(start):
			return 0
		case compare && !h.Before(prevStart):
			return 1
		}
		return -1
	}
	if end.IsZero() {
		end, start, prevStart = now.Truncate(time.Hour), now.Truncate(time.Hour), now.Truncate(time.Hour)
	}
	out.From, out.To = start.Format(time.DateOnly), end.Format(time.DateOnly)
	if compare {
		a, b := prevStart.Format(time.DateOnly), start.Add(-time.Hour).Format(time.DateOnly)
		out.CompareFrom, out.CompareTo = &a, &b
	}
	type sum struct{ clicks, impressions, positions float64 }
	add := func(s *sum, x connectors.SearchAnalyticsRow) {
		s.clicks += x.Clicks
		s.impressions += x.Impressions
		s.positions += x.Position * x.Impressions
	}
	metrics := func(s sum) perfMetrics {
		m := perfMetrics{Clicks: s.clicks, Impressions: s.impressions}
		if s.impressions > 0 {
			m.CTR, m.Position = s.clicks/s.impressions, s.positions/s.impressions
		}
		return m
	}
	var totals [2]sum
	rows := make([][]connectors.SearchAnalyticsRow, 1, 2)
	if compare {
		rows = append(rows, nil)
	}
	for _, x := range hourRows {
		if len(x.Keys) == 0 {
			continue
		}
		p := period(x.Keys[0])
		if p < 0 {
			continue
		}
		add(&totals[p], x)
		pt := perfPoint{Date: x.Keys[0], perfMetrics: metricsOf(x)}
		if p == 0 {
			out.Series = append(out.Series, pt)
		} else {
			out.PreviousSeries = append(out.PreviousSeries, pt)
		}
		if out.Dimension == "date" {
			rows[p] = append(rows[p], x)
		}
	}
	sort.Slice(out.Series, func(i, j int) bool { return out.Series[i].Date < out.Series[j].Date })
	sort.Slice(out.PreviousSeries, func(i, j int) bool { return out.PreviousSeries[i].Date < out.PreviousSeries[j].Date })
	out.Totals = metrics(totals[0])
	if compare {
		t := metrics(totals[1])
		out.PreviousTotals = &t
	}
	if out.Dimension != "date" {
		sums := [2]map[string]*sum{{}, {}}
		order := [2][]string{}
		for _, x := range byKey {
			if len(x.Keys) < 2 {
				continue
			}
			p := period(x.Keys[1])
			if p < 0 {
				continue
			}
			s, ok := sums[p][x.Keys[0]]
			if !ok {
				s = &sum{}
				sums[p][x.Keys[0]] = s
				order[p] = append(order[p], x.Keys[0])
			}
			add(s, x)
		}
		for p := range rows {
			for _, k := range order[p] {
				m := metrics(*sums[p][k])
				rows[p] = append(rows[p], connectors.SearchAnalyticsRow{Keys: []string{k}, Clicks: m.Clicks, Impressions: m.Impressions, CTR: m.CTR, Position: m.Position})
			}
		}
	}
	out.Rows = perfRows(rows, limit, out.Dimension == "date")
	return nil
}
