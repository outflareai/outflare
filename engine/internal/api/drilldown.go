package api

import (
	"cmp"
	"context"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/fxrates"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// The drill-down report (plans/ad-tracker.md, B5a): a brand's visits, the
// conversions and revenue credited to them and, where it can be split the
// same way, ad spend, grouped by up to five levels of any visit dimension,
// ad id or day, hour and weekday in the brand's time zone. Every row has
// the same metrics; parents carry their children's subtotals. The tracker
// screens (B5c) and every entity tab are built on it.

const (
	drilldownMaxLevels = 5
	drilldownMaxRows   = 5000
)

// drilldownDims are the dimensions a report can group and filter by, as
// SQL over a visit (s) with the report's time zone as $tz. Never text from
// a request: names are looked up here.
var drilldownDims = map[string]string{
	"channel": `s.channel`, "source": `s.source`, "campaign": `s.campaign`,
	"utm_source": `s.utm_source`, "utm_medium": `s.utm_medium`, "utm_campaign": `s.utm_campaign`, "utm_term": `s.utm_term`, "utm_content": `s.utm_content`,
	"landing_path": `s.landing_path`, "referrer_host": `s.referrer_host`,
	"country": `s.country`, "region": `s.region`, "city": `s.city`, "isp": `s.isp`, "asn": `NULLIF(s.asn, 0)::text`, "language": `s.language`,
	"device": `s.device`, "device_brand": `s.device_brand`, "os": `s.os`, "browser": `s.browser`,
	"ad_campaign": `s.ad->>'campaign'`, "ad_adset": `s.ad->>'adset'`, "ad_ad": `s.ad->>'ad'`, "ad_placement": `s.ad->>'placement'`,
	"tracking_campaign": `s.tracking_campaign_id::text`,
	"day":               `to_char(s.started_at AT TIME ZONE $tz, 'YYYY-MM-DD')`, "hour": `to_char(s.started_at AT TIME ZONE $tz, 'HH24')`,
	"weekday": `lower(to_char(s.started_at AT TIME ZONE $tz, 'Dy'))`,
}

// drilldownSpendDims are the dimensions spend can be split by: the ad ids
// (one level at a time, at the matching ad entities) and the day (the ad
// account's day, as platforms report it).
var drilldownSpendDims = map[string]string{"ad_campaign": "campaign", "ad_adset": "ad_group", "ad_ad": "ad", "day": ""}

var drilldownMetrics = []string{"visits", "visitors", "conversions", "revenue", "cost", "profit", "roi", "roas", "cr", "epv", "cpa"}

func (s *Server) routesDrilldown(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/drilldown", s.onBrand(auth.ScopeAnalyticsRead, s.drilldown))
}

type drilldownMetricsView struct {
	Visits      int64    `json:"visits"`
	Visitors    int64    `json:"visitors"`
	Conversions float64  `json:"conversions"`
	Revenue     float64  `json:"revenue"`
	Cost        *float64 `json:"cost"`
	Profit      *float64 `json:"profit"`
	ROI         *float64 `json:"roi"`
	ROAS        *float64 `json:"roas"`
	CR          *float64 `json:"cr"`
	EPV         *float64 `json:"epv"`
	CPA         *float64 `json:"cpa"`
	// Custom are the report's custom columns (B5b), by name.
	Custom map[string]*float64 `json:"custom"`
}

type drilldownRow struct {
	// Keys are the row's value at each level so far: a parent's are a
	// prefix of its children's. "" is a visit with no value there.
	Keys    []string             `json:"keys"`
	Metrics drilldownMetricsView `json:"metrics"`
	Rows    []*drilldownRow      `json:"rows"`
	cost    float64
	hasCost bool
}

type drilldownView struct {
	From           string               `json:"from"`
	To             string               `json:"to"`
	Timezone       string               `json:"timezone"`
	Model          string               `json:"model"`
	ConversionTime string               `json:"conversion_time"`
	Currency       string               `json:"currency"`
	GroupBy        []string             `json:"group_by"`
	CostAvailable  bool                 `json:"cost_available"`
	CostNote       *string              `json:"cost_note"`
	Totals         drilldownMetricsView `json:"totals"`
	Rows           []*drilldownRow      `json:"rows"`
	// Truncated says rows past the limit were left out; the totals still
	// count them.
	Truncated bool `json:"truncated"`
}

type drilldownFilter struct {
	dim    string
	values []string
	not    bool
}

// drilldownColumn is a custom column (B5b): a name and its formula.
type drilldownColumn struct {
	name    string
	formula colFormula
}

// drilldownSpec is what a report asks for, besides its dates and model.
type drilldownSpec struct {
	groups   []string
	filters  []drilldownFilter
	sortBy   string
	desc     bool
	limit    int
	convTime string
	columns  []drilldownColumn
}

const drilldownMaxColumns = 10

var columnName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// drilldownQuery reads group_by, filter, exclude, column, sort, limit and
// conversion_time.
func drilldownQuery(r *http.Request) (drilldownSpec, error) {
	return drilldownQueryValues(r.URL.Query())
}

func drilldownQueryValues(q url.Values) (sp drilldownSpec, err error) {
	var groups []string
	var filters []drilldownFilter
	var sortBy, convTime string
	var desc bool
	var limit int
	defer func() {
		if err == nil {
			sp.groups, sp.filters, sp.sortBy, sp.desc, sp.limit, sp.convTime = groups, filters, sortBy, desc, limit, convTime
		}
	}()
	// Custom columns, as name:formula.
	for _, c := range q["column"] {
		name, src, ok := strings.Cut(c, ":")
		if !ok || !columnName.MatchString(name) || slices.Contains(drilldownMetrics, name) {
			return sp, errInvalid("column is name:formula, such as rpv:revenue/visits, with a lowercase name that isn't a metric's")
		}
		if slices.ContainsFunc(sp.columns, func(x drilldownColumn) bool { return x.name == name }) {
			return sp, errInvalid("column " + name + " is named twice")
		}
		f, err := parseColumnFormula(src)
		if err != nil {
			return sp, err
		}
		sp.columns = append(sp.columns, drilldownColumn{name, f})
	}
	if len(sp.columns) > drilldownMaxColumns {
		return sp, errInvalid("a report has at most 10 custom columns")
	}
	groups = []string{"channel"}
	if v := q.Get("group_by"); v != "" {
		groups = strings.Split(v, ",")
	}
	if len(groups) > drilldownMaxLevels {
		return sp, errInvalid("group_by has at most 5 levels")
	}
	for _, g := range groups {
		if _, ok := drilldownDims[g]; !ok {
			return sp, errInvalid("group_by " + g + " isn't a dimension; see the list in the API reference")
		}
	}
	// Values of one dimension are OR'd; dimensions are AND'd.
	byDim := map[string]*drilldownFilter{}
	for _, kind := range []string{"filter", "exclude"} {
		for _, f := range q[kind] {
			dim, val, ok := strings.Cut(f, ":")
			if _, known := drilldownDims[dim]; !ok || !known {
				return sp, errInvalid(kind + " is dimension:value, such as country:US")
			}
			key := kind + ":" + dim
			if byDim[key] == nil {
				byDim[key] = &drilldownFilter{dim: dim, not: kind == "exclude"}
			}
			byDim[key].values = append(byDim[key].values, val)
		}
	}
	for _, f := range byDim {
		filters = append(filters, *f)
	}
	slices.SortFunc(filters, func(a, b drilldownFilter) int {
		return cmp.Or(cmp.Compare(a.dim, b.dim), cmp.Compare(strconv.FormatBool(a.not), strconv.FormatBool(b.not)))
	})
	sortBy, desc = "visits", true
	if v := q.Get("sort"); v != "" {
		desc = strings.HasPrefix(v, "-")
		sortBy = strings.TrimPrefix(v, "-")
		if !slices.Contains(drilldownMetrics, sortBy) && !slices.ContainsFunc(sp.columns, func(c drilldownColumn) bool { return c.name == sortBy }) {
			return sp, errInvalid("sort is a metric (" + strings.Join(drilldownMetrics, ", ") + "), with - for largest first")
		}
	}
	limit = 100
	if v := q.Get("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit < 1 || limit > 1000 {
			return sp, errInvalid("limit is 1 to 1000 rows a level")
		}
	}
	convTime = q.Get("conversion_time")
	switch convTime {
	case "":
		convTime = "visit"
	case "visit", "conversion":
	default:
		return sp, errInvalid("conversion_time is visit (the default) or conversion")
	}
	return sp, nil
}

func (s *Server) drilldown(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if !s.applyReportView(w, r) {
		return
	}
	sp, err := drilldownQuery(r)
	if s.allocError(w, r, err) {
		return
	}
	f, ok := s.attributionQuery(w, r)
	if !ok {
		return
	}
	v, err := s.runDrilldown(r.Context(), f, sp)
	if err != nil {
		metrics.ReportDrilldown("error", time.Since(start))
		s.internal(w, r, err)
		return
	}
	metrics.ReportDrilldown("success", time.Since(start))
	if r.URL.Query().Get("format") == "csv" {
		header := append(slices.Clone(sp.groups), drilldownMetrics...)
		for _, c := range sp.columns {
			header = append(header, c.name)
		}
		var out [][]string
		var walk func(rows []*drilldownRow)
		walk = func(rows []*drilldownRow) {
			for _, row := range rows {
				if len(row.Rows) > 0 {
					walk(row.Rows)
					continue
				}
				line := append(slices.Clone(row.Keys), row.Metrics.csv()...)
				for _, c := range sp.columns {
					line = append(line, csvNumber(row.Metrics.Custom[c.name]))
				}
				out = append(out, line)
			}
		}
		walk(v.Rows)
		writeCSV(w, "report", header, out)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (m drilldownMetricsView) csv() []string {
	num := func(x *float64) string {
		if x == nil {
			return ""
		}
		return strconv.FormatFloat(*x, 'f', -1, 64)
	}
	f := func(x float64) string { return strconv.FormatFloat(x, 'f', -1, 64) }
	return []string{strconv.FormatInt(m.Visits, 10), strconv.FormatInt(m.Visitors, 10), f(m.Conversions), f(m.Revenue),
		num(m.Cost), num(m.Profit), num(m.ROI), num(m.ROAS), num(m.CR), num(m.EPV), num(m.CPA)}
}

// sortValue is a metric for sorting; a missing one sorts last.
func (m drilldownMetricsView) sortValue(name string) float64 {
	p := func(x *float64) float64 {
		if x == nil {
			return math.Inf(-1)
		}
		return *x
	}
	switch name {
	case "visitors":
		return float64(m.Visitors)
	case "conversions":
		return m.Conversions
	case "revenue":
		return m.Revenue
	case "cost":
		return p(m.Cost)
	case "profit":
		return p(m.Profit)
	case "roi":
		return p(m.ROI)
	case "roas":
		return p(m.ROAS)
	case "cr":
		return p(m.CR)
	case "epv":
		return p(m.EPV)
	case "cpa":
		return p(m.CPA)
	}
	if x, ok := m.Custom[name]; ok {
		return p(x)
	}
	return float64(m.Visits)
}

func (s *Server) runDrilldown(ctx context.Context, f attributionFilter, sp drilldownSpec) (drilldownView, error) {
	groups, filters, sortBy, desc, limit, convTime := sp.groups, sp.filters, sp.sortBy, sp.desc, sp.limit, sp.convTime
	a := accessFrom(ctx)
	v := drilldownView{From: f.from, To: f.to, Timezone: f.loc.String(), Model: f.ref, ConversionTime: convTime, GroupBy: groups, Rows: []*drilldownRow{}}
	if err := s.pool.QueryRow(ctx, `SELECT currency FROM brands WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, a.Brand).Scan(&v.Currency); err != nil {
		return v, err
	}

	// Rows by their keys joined; "" is the totals.
	nodes := map[string]*drilldownRow{"": {Keys: []string{}}}
	node := func(keys []string) *drilldownRow {
		k := strings.Join(keys, "\x00")
		if len(keys) == 0 {
			k = ""
		}
		if nodes[k] == nil {
			nodes[k] = &drilldownRow{Keys: slices.Clone(keys)}
		}
		return nodes[k]
	}
	// The SQL for the levels, filters and grouping sets, with the time
	// zone, workspace, brand and range as $1 to $5.
	args := []any{f.loc.String(), a.WorkspaceID, a.Brand, f.start, f.end}
	dim := func(name string) string { return strings.ReplaceAll(drilldownDims[name], "$tz", "$1") }
	var cols, sets []string
	for i, g := range groups {
		cols = append(cols, `COALESCE(`+dim(g)+`, '') AS d`+strconv.Itoa(i))
	}
	for n := len(groups); n >= 0; n-- {
		var set []string
		for i := range n {
			set = append(set, "d"+strconv.Itoa(i))
		}
		sets = append(sets, "("+strings.Join(set, ", ")+")")
	}
	where := ""
	for _, fl := range filters {
		args = append(args, fl.values)
		op := ` = ANY($` + strconv.Itoa(len(args)) + `::text[])`
		if fl.not {
			where += ` AND NOT (COALESCE(` + dim(fl.dim) + `, '')` + op + `)`
		} else {
			where += ` AND COALESCE(` + dim(fl.dim) + `, '')` + op
		}
	}
	// The inner query names each level d0, d1, …; the outer one groups
	// them (GROUPING only takes grouping expressions, not output names).
	inKeys, outKeys, grouping := "", "", "0"
	if len(groups) > 0 {
		inKeys = strings.Join(cols, ", ") + ", "
		var ds []string
		for i := range groups {
			ds = append(ds, "d"+strconv.Itoa(i))
		}
		outKeys = strings.Join(ds, ", ") + ", "
		grouping = "GROUPING(" + strings.Join(ds, ", ") + ")"
	}
	groupBy := " GROUP BY GROUPING SETS (" + strings.Join(sets, ", ") + ")"
	if len(groups) == 0 {
		groupBy = ""
	}
	// level is how many leading keys a grouping-sets row has.
	level := func(bits int) int {
		n := 0
		for i := len(groups) - 1; i >= 0; i-- {
			if bits&(1<<(len(groups)-1-i)) == 0 {
				n = i + 1
				break
			}
		}
		return n
	}
	scanKeys := func(vals []any, bits int) []string {
		n := level(bits)
		keys := make([]string, n)
		for i := range n {
			if p := *(vals[i].(**string)); p != nil {
				keys[i] = *p
			}
		}
		return keys
	}
	keyDest := func() []any {
		// A level a grouping set leaves out is NULL.
		out := make([]any, len(groups))
		for i := range out {
			out[i] = new(*string)
		}
		return out
	}

	// Visits and visitors, exactly at every level.
	rows, err := s.pool.Query(ctx, `SELECT `+outKeys+grouping+`, count(*), count(DISTINCT anon) FROM (
		SELECT `+inKeys+`s.anonymous_id AS anon FROM site_sessions s
		WHERE s.workspace_id = $2 AND s.brand_id = $3 AND s.started_at >= $4 AND s.started_at < $5 AND $1::text <> ''`+f.visitsSQL("s.")+where+`) x`+groupBy, args...)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		keys := keyDest()
		var bits int
		var visits, visitors int64
		if err := rows.Scan(append(keys, &bits, &visits, &visitors)...); err != nil {
			rows.Close()
			return v, err
		}
		n := node(scanKeys(keys, bits))
		n.Metrics.Visits, n.Metrics.Visitors = visits, visitors
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return v, err
	}

	// Conversions and revenue credited to those visits under the model,
	// by the visit's time (default) or the conversion's.
	cargs := append(slices.Clone(args), f.model)
	pm := "$" + strconv.Itoa(len(cargs))
	creditJoin := ` JOIN site_sessions s ON s.id = cr.session_id AND s.started_at >= $4 AND s.started_at < $5`
	when := ``
	if convTime == "conversion" {
		creditJoin = ` LEFT JOIN site_sessions s ON s.id = cr.session_id`
		when = ` AND c.occurred_at >= $4 AND c.occurred_at < $5`
	}
	j, cw := f.creditSQL()
	withCurrency := func(sets []string) string {
		out := " GROUP BY GROUPING SETS (" + strings.ReplaceAll(strings.Join(sets, ", "), ")", ", cur)") + ")"
		return strings.ReplaceAll(out, "(, cur)", "(cur)")
	}
	crows, err := s.pool.Query(ctx, `SELECT `+outKeys+grouping+`, cur, sum(w)::float8, sum(v)::float8 FROM (
		SELECT `+inKeys+`c.currency AS cur, cr.weight AS w, cr.weight * c.value_minor AS v
		FROM attribution_credits cr JOIN attribution_conversions c ON c.event_id = cr.event_id`+creditJoin+j+`
		WHERE cr.workspace_id = $2 AND COALESCE(c.brand_id, outflare_first_brand(c.workspace_id)) = $3 AND cr.model = `+pm+` AND $1::text <> ''`+when+cw+where+`) x`+
		withCurrency(sets), cargs...)
	if err != nil {
		return v, err
	}
	type money struct {
		n        *drilldownRow
		currency string
		minor    float64
	}
	var revenue []money
	for crows.Next() {
		keys := keyDest()
		var bits int
		var currency string
		var weight float64
		var value *float64
		if err := crows.Scan(append(keys, &bits, &currency, &weight, &value)...); err != nil {
			crows.Close()
			return v, err
		}
		n := node(scanKeys(keys, bits))
		n.Metrics.Conversions += weight
		if value != nil && currency != "" {
			revenue = append(revenue, money{n, currency, *value})
		}
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return v, err
	}

	// Spend, where every level and filter can split it the same way.
	note := ""
	adLevel := ""
	for _, g := range groups {
		lvl, ok := drilldownSpendDims[g]
		switch {
		case !ok:
			note = "Spend can't be split by " + g + ": group by day and one ad level (ad_campaign, ad_adset or ad_ad) to see cost."
		case lvl != "" && adLevel != "":
			note = "Spend splits by one ad level at a time."
		case lvl != "":
			adLevel = lvl
		}
	}
	for _, fl := range filters {
		if _, ok := drilldownSpendDims[fl.dim]; !ok && note == "" {
			note = "Spend can't be filtered by " + fl.dim + "."
		}
	}
	var spend []money
	if note == "" {
		entityLevel := adLevel
		if entityLevel == "" {
			entityLevel = "campaign"
		}
		var scols []string
		for i, g := range groups {
			x := `e.external_id`
			if g == "day" {
				x = `to_char(m.day, 'YYYY-MM-DD')`
			}
			scols = append(scols, x+` AS d`+strconv.Itoa(i))
		}
		sargs := []any{a.WorkspaceID, a.Brand, f.from, f.to, entityLevel}
		swhere := ""
		for _, fl := range filters {
			sargs = append(sargs, fl.values)
			x := `e.external_id`
			if fl.dim == "day" {
				x = `to_char(m.day, 'YYYY-MM-DD')`
			}
			op := x + ` = ANY($` + strconv.Itoa(len(sargs)) + `::text[])`
			if fl.not {
				op = `NOT (` + op + `)`
			}
			swhere += ` AND ` + op
		}
		ssel := ""
		if len(groups) > 0 {
			ssel = strings.Join(scols, ", ") + ", "
		}
		srows, err := s.pool.Query(ctx, `SELECT `+outKeys+grouping+`, cur, sum(v)::float8 FROM (
			SELECT `+ssel+`a.currency AS cur, m.spend_micros AS v
			FROM ad_metrics_daily m JOIN ad_entities e ON e.id = m.ad_entity_id JOIN ad_accounts a ON a.id = e.ad_account_id
			WHERE e.workspace_id = $1 AND COALESCE(e.brand_id, a.brand_id) = $2 AND e.level = $5 AND m.day BETWEEN $3::date AND $4::date`+swhere+`) x`+
			withCurrency(sets), sargs...)
		if err != nil {
			return v, err
		}
		for srows.Next() {
			keys := keyDest()
			var bits int
			var currency string
			var micros float64
			if err := srows.Scan(append(keys, &bits, &currency, &micros)...); err != nil {
				srows.Close()
				return v, err
			}
			spend = append(spend, money{node(scanKeys(keys, bits)), currency, micros})
		}
		srows.Close()
		if err := srows.Err(); err != nil {
			return v, err
		}
	} else {
		v.CostNote = &note
	}
	v.CostAvailable = note == ""

	// Money in the brand's currency, at the latest rates.
	var currencies []string
	for _, m := range append(slices.Clone(revenue), spend...) {
		if !slices.Contains(currencies, m.currency) {
			currencies = append(currencies, m.currency)
		}
	}
	rates, err := s.brandRates(ctx, v.Currency, currencies)
	if err != nil {
		return v, err
	}
	for _, m := range revenue {
		if rate, ok := rates[m.currency]; ok {
			m.n.Metrics.Revenue += m.minor / math.Pow10(fxrates.MinorDigits(m.currency)) * rate
		}
	}
	for _, m := range spend {
		if rate, ok := rates[m.currency]; ok {
			m.n.cost += m.minor / 1e6 * rate
		}
	}

	// Ratios, then the tree: each row under its parent, sorted, and cut
	// at the limit per level.
	for _, n := range nodes {
		n.Metrics.finish(n.cost, v.CostAvailable)
		n.Metrics.custom(sp.columns)
	}
	v.Totals = nodes[""].Metrics
	var all []*drilldownRow
	for k, n := range nodes {
		if k != "" {
			all = append(all, n)
		}
	}
	slices.SortFunc(all, func(x, y *drilldownRow) int { return cmp.Compare(len(x.Keys), len(y.Keys)) })
	total := 0
	for _, n := range all {
		parent := nodes[strings.Join(n.Keys[:len(n.Keys)-1], "\x00")]
		if len(n.Keys) == 1 {
			parent = nodes[""]
		}
		if parent == nil {
			continue
		}
		parent.Rows = append(parent.Rows, n)
	}
	var trim func(n *drilldownRow)
	trim = func(n *drilldownRow) {
		slices.SortStableFunc(n.Rows, func(x, y *drilldownRow) int {
			c := cmp.Compare(x.Metrics.sortValue(sortBy), y.Metrics.sortValue(sortBy))
			if desc {
				c = -c
			}
			if c == 0 {
				c = cmp.Compare(strings.Join(x.Keys, "\x00"), strings.Join(y.Keys, "\x00"))
			}
			return c
		})
		if len(n.Rows) > limit {
			n.Rows, v.Truncated = n.Rows[:limit], true
		}
		for _, c := range n.Rows {
			total++
			if total > drilldownMaxRows {
				v.Truncated = true
			}
			trim(c)
		}
		if n.Rows == nil {
			n.Rows = []*drilldownRow{}
		}
	}
	trim(nodes[""])
	v.Rows = nodes[""].Rows
	if total > drilldownMaxRows {
		v.Truncated = true
	}
	return v, nil
}

// finish works out the ratios from the counts and money.
func (m *drilldownMetricsView) finish(cost float64, costKnown bool) {
	round := func(x float64) *float64 {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil
		}
		r := math.Round(x*1e4) / 1e4
		return &r
	}
	m.Revenue = math.Round(m.Revenue*1e4) / 1e4
	m.Conversions = math.Round(m.Conversions*1e4) / 1e4
	if m.Visits > 0 {
		m.CR = round(m.Conversions / float64(m.Visits))
		m.EPV = round(m.Revenue / float64(m.Visits))
	}
	if !costKnown {
		return
	}
	m.Cost = round(cost)
	m.Profit = round(m.Revenue - cost)
	if cost > 0 {
		m.ROI = round((m.Revenue - cost) / cost)
		m.ROAS = round(m.Revenue / cost)
	}
	if m.Conversions > 0 {
		m.CPA = round(cost / m.Conversions)
	}
}

// value is a metric by name, for custom columns.
func (m drilldownMetricsView) value(name string) *float64 {
	f := func(x float64) *float64 { return &x }
	switch name {
	case "visits":
		return f(float64(m.Visits))
	case "visitors":
		return f(float64(m.Visitors))
	case "conversions":
		return f(m.Conversions)
	case "revenue":
		return f(m.Revenue)
	case "cost":
		return m.Cost
	case "profit":
		return m.Profit
	case "roi":
		return m.ROI
	case "roas":
		return m.ROAS
	case "cr":
		return m.CR
	case "epv":
		return m.EPV
	case "cpa":
		return m.CPA
	}
	return nil
}

// custom works out the custom columns, rounded as the other ratios are.
func (m *drilldownMetricsView) custom(cols []drilldownColumn) {
	m.Custom = map[string]*float64{}
	for _, c := range cols {
		v := c.formula.eval(m.value)
		if v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) {
			r := math.Round(*v*1e4) / 1e4
			v = &r
		} else {
			v = nil
		}
		m.Custom[c.name] = v
	}
}

func csvNumber(x *float64) string {
	if x == nil {
		return ""
	}
	return strconv.FormatFloat(*x, 'f', -1, 64)
}
