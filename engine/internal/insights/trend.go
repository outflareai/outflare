package insights

// Trends: one or more series over time, from the same sources as funnel
// steps plus the ad networks' daily numbers, with a formula between
// series (cost per sign-up) and the period before for comparison. A
// number tile is a trend read as its total.

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// KindAd is a series from the ad networks' daily numbers.
const KindAd = "ad"

// Measures of a series.
const (
	// MeasureCount counts rows: events, visits, payments.
	MeasureCount = "count"
	// MeasurePeople counts distinct people.
	MeasurePeople = "people"
	// MeasureSum adds a property up: payment amounts, an event's value.
	MeasureSum = "sum"
	// MeasureAverage averages a property.
	MeasureAverage = "average"
)

// Measures lists the measures of event, page, visit, conversion and
// payment series.
var Measures = []string{MeasureCount, MeasurePeople, MeasureSum, MeasureAverage}

// AdMetrics are the measures of an ad series.
var AdMetrics = []string{"clicks", "impressions", "spend", "conversions"}

// Series is one line of a trend.
type Series struct {
	// Kind is a step kind, or ad.
	Kind string `json:"kind"`
	// Event, Conversion and Filters pick the rows as a funnel step does.
	Event      string   `json:"event,omitempty"`
	Conversion string   `json:"conversion,omitempty"`
	Filters    []Filter `json:"filters,omitempty"`
	// First keeps only each person's first ever event or payment.
	First bool `json:"first,omitempty"`
	// Measure is count (default), people, sum or average; for an ad
	// series it is clicks (default), impressions, spend or conversions.
	Measure string `json:"measure,omitempty"`
	// Property is what sum and average add up: amount for payments,
	// a property for events (property:value or value).
	Property string `json:"property,omitempty"`
	// Network and Campaign narrow an ad series.
	Network  string `json:"network,omitempty"`
	Campaign string `json:"campaign,omitempty"`
	Label    string `json:"label,omitempty"`
}

// Trend is one or more series over time.
type Trend struct {
	Series []Series `json:"series"`
	// From and To bound the rows: From inclusive, To exclusive.
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	// Interval is day, week or month.
	Interval string `json:"interval"`
	// Breakdown splits every series by a field of its rows; an ad
	// series splits by network or campaign.
	Breakdown string `json:"breakdown,omitempty"`
	// Formula combines the series as A, B, C with + - * / and brackets:
	// "A / B" for cost per sign-up.
	Formula string `json:"formula,omitempty"`
	// Compare adds the same numbers for the period just before.
	Compare bool `json:"compare,omitempty"`
	// Location is the time zone days, weeks and months are in: the
	// brand's reporting time zone (B4a), UTC when nil.
	Location *time.Location `json:"-"`
}

// MaxSeries is how many series one trend has.
const MaxSeries = 6

// DefaultLabel is the series' name on the chart.
func (s Series) DefaultLabel(conversionName string) string {
	if s.Label != "" {
		return s.Label
	}
	var what string
	switch s.Kind {
	case KindAd:
		m := s.Measure
		if m == "" {
			m = "clicks"
		}
		what = "Ad " + m
		if s.Network != "" {
			if n, ok := networkByID(s.Network); ok {
				what = n.Label + " " + m
			}
		}
		return what
	case KindPage:
		what = "Page views"
	case KindVisit:
		what = "Visits"
	case KindConversion:
		what = conversionName
		if what == "" {
			what = s.Conversion
		}
	case KindPayment:
		what = "Payments"
		if s.Measure == MeasureSum && (s.Property == "" || s.Property == "amount") {
			return "Revenue"
		}
	default:
		what = s.Event
	}
	switch s.Measure {
	case MeasurePeople:
		return what + " (people)"
	case MeasureSum:
		return what + ": total " + strings.TrimPrefix(s.Property, "property:")
	case MeasureAverage:
		return what + ": average " + strings.TrimPrefix(s.Property, "property:")
	}
	return what
}

// Validate checks a trend's shape without the database.
func (t Trend) Validate() error {
	if len(t.Series) < 1 || len(t.Series) > MaxSeries {
		return Invalid(fmt.Sprintf("series has 1 to %d series", MaxSeries))
	}
	if t.From.IsZero() || t.To.IsZero() || !t.To.After(t.From) || t.To.Sub(t.From) > MaxRange {
		return Invalid("from and to are dates, from before to, at most 366 days apart")
	}
	if !slices.Contains(Intervals, t.Interval) {
		return Invalid("interval is day, week or month")
	}
	for i, s := range t.Series {
		at := fmt.Sprintf("series[%d]", i)
		if s.Kind == KindAd {
			if s.Measure != "" && !slices.Contains(AdMetrics, s.Measure) {
				return Invalid(at + ".measure for ads is one of " + strings.Join(AdMetrics, ", "))
			}
			if s.Network != "" {
				if _, ok := networkByID(s.Network); !ok {
					return Invalid(at + ".network is one of the ad networks (google, meta, x)")
				}
			}
			if len(s.Campaign) > 200 || s.Event != "" || s.Conversion != "" || len(s.Filters) > 0 || s.First || s.Property != "" {
				return Invalid(at + ": an ad series takes measure, network and campaign only")
			}
			if t.Breakdown != "" && t.Breakdown != "network" && t.Breakdown != "campaign" {
				return Invalid("breakdown with an ad series is network or campaign")
			}
			continue
		}
		if !slices.Contains(Kinds, s.Kind) {
			return Invalid(at + ".kind is one of " + strings.Join(Kinds, ", ") + ", ad")
		}
		if s.Measure != "" && !slices.Contains(Measures, s.Measure) {
			return Invalid(at + ".measure is one of " + strings.Join(Measures, ", "))
		}
		switch s.Measure {
		case MeasureSum, MeasureAverage:
			switch s.Kind {
			case KindPayment:
				if s.Property != "" && s.Property != "amount" {
					return Invalid(at + ".property for a payment total is amount")
				}
			case KindVisit:
				return Invalid(at + ": visits have nothing to add up; use count or people")
			default:
				if s.Property == "" || s.Property == "amount" {
					return Invalid(at + ".property names the event property to add up")
				}
			}
		default:
			if s.Property != "" {
				return Invalid(at + ".property is only for sum and average")
			}
		}
		if s.Kind == KindEvent && !eventName(s.Event) {
			return Invalid(at + ".event is the event's name, such as signed_up")
		}
		if s.Kind == KindConversion && (s.Conversion == "" || len(s.Conversion) > 100) {
			return Invalid(at + ".conversion is a saved conversion's key")
		}
		if s.Kind != KindEvent && s.Event != "" {
			return Invalid(at + ".event is only for an event series")
		}
		if s.First && s.Kind != KindEvent && s.Kind != KindPayment && s.Kind != KindConversion {
			return Invalid(at + ".first is for events, conversions and payments")
		}
		if len(s.Filters) > 10 {
			return Invalid(at + ".filters has at most 10 filters")
		}
		for j, f := range s.Filters {
			if err := validFilter(s.Kind, f, fmt.Sprintf("%s.filters[%d]", at, j)); err != nil {
				return err
			}
		}
		if t.Breakdown != "" {
			if _, err := breakdownExpr(s.Kind, t.Breakdown); err != nil {
				return err
			}
		}
		if len(s.Label) > 80 {
			return Invalid(at + ".label is at most 80 characters")
		}
	}
	if t.Formula != "" {
		if _, err := parseFormula(t.Formula, len(t.Series)); err != nil {
			return err
		}
	}
	return nil
}

// Point is one period's value; nil when nothing was there to measure.
type Point struct {
	Start time.Time `json:"start"`
	Value *float64  `json:"value"`
}

// SeriesResult is one series' numbers.
type SeriesResult struct {
	Label string `json:"label"`
	// Total is the whole range: a sum, or for people and averages the
	// value over the whole range (not the sum of points).
	Total  *float64 `json:"total"`
	Points []Point  `json:"points"`
	// Currency is set for money: the brand's for payments, the ad
	// accounts' for spend ("mixed" when they differ).
	Currency string `json:"currency,omitempty"`
}

// TrendGroup is one breakdown value's series.
type TrendGroup struct {
	Value  string         `json:"value"`
	Series []SeriesResult `json:"series"`
}

// TrendResult is a trend's answer.
type TrendResult struct {
	Series    []SeriesResult `json:"series"`
	Formula   *SeriesResult  `json:"formula"`
	Breakdown []TrendGroup   `json:"breakdown"`
	// Previous is the period just before, when asked for.
	Previous *TrendResult `json:"previous,omitempty"`
	From     time.Time    `json:"from"`
	To       time.Time    `json:"to"`
}

// CompiledTrend is a trend turned into SQL.
type CompiledTrend struct {
	ws, brand string
	t         Trend
	labels    []string
	currency  []string
	sql       string
	args      args
	// from and to are the indexes of the date parameters, so the
	// previous period can reuse the SQL.
	from, to int
}

// CompileTrend turns a trend into one grouped query.
func CompileTrend(ctx context.Context, q Querier, ws, brand string, t Trend) (*CompiledTrend, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	c := &CompiledTrend{ws: ws, brand: brand, t: t}
	a := &c.args
	pWS, pBrand := a.add(ws), a.add(brand)
	pFrom, pTo := a.add(t.From), a.add(t.To)
	c.from, c.to = len(a.list)-2, len(a.list)-1
	pInterval, pZone := a.add(t.Interval), a.add(zoneName(t.Location))
	var parts []string
	for i, s := range t.Series {
		conv, err := loadConversion(ctx, q, ws, brand, s.Kind, s.Conversion, fmt.Sprintf("series[%d]", i))
		if err != nil {
			return nil, err
		}
		c.labels = append(c.labels, s.DefaultLabel(conv.Name))
		c.currency = append(c.currency, "")
		if s.Kind == KindAd {
			parts = append(parts, adSeries(i, s, t.Breakdown, pWS, pBrand, pFrom, pTo, pInterval, pZone, a))
			if s.Measure == "spend" {
				c.currency[i] = "?"
			}
			continue
		}
		src, err := source(s.Kind, s.Event, conv, s.Filters, s.First, pFrom, pTo, pWS, pBrand, a)
		if err != nil {
			return nil, err
		}
		bk := `''`
		if t.Breakdown != "" {
			x, err := fieldExpr(s.Kind, t.Breakdown, a)
			if err != nil {
				return nil, err
			}
			bk = `COALESCE(NULLIF(left((` + x + `)::text, 200), ''), '')`
		}
		var val string
		switch s.Measure {
		case MeasureSum, MeasureAverage:
			prop := s.Property
			if s.Kind == KindPayment {
				prop = "amount"
				c.currency[i] = "brand"
			}
			x, err := fieldExpr(s.Kind, prop, a)
			if err != nil {
				return nil, err
			}
			val = `(CASE WHEN (` + x + `)::text ~ '^\s*-?[0-9]+(\.[0-9]+)?\s*$' THEN trim((` + x + `)::text)::float8 END)`
		}
		// Every row carries its period, its person and its value; the
		// outer query adds them up per period and over the range.
		row := `SELECT ` + strconv.Itoa(i) + ` AS i, date_trunc(` + pInterval + `, ` + src.ts + ` AT TIME ZONE ` + pZone + `) AT TIME ZONE ` + pZone + ` AS p, ` +
			bk + ` AS bk, ` + src.actor + ` AS actor, `
		if val == "" {
			row += `NULL::float8 AS v`
		} else {
			row += val + ` AS v`
		}
		row += ` FROM ` + src.from + ` WHERE ` + strings.Join(src.where, " AND ")
		agg := `count(*)::float8`
		switch s.Measure {
		case MeasurePeople:
			agg = `count(DISTINCT actor)::float8`
		case MeasureSum:
			agg = `sum(v)`
		case MeasureAverage:
			agg = `avg(v)`
		}
		parts = append(parts, `SELECT i, GROUPING(p)::int AS gp, p, bk, `+agg+` AS v, '' AS cur FROM (`+row+`) r GROUP BY GROUPING SETS ((i, p, bk), (i, bk))`)
	}
	c.sql = strings.Join(parts, "\nUNION ALL\n")
	return c, nil
}

// adSeries is the ad networks' daily numbers as a series.
func adSeries(i int, s Series, breakdown, pWS, pBrand, pFrom, pTo, pInterval, pZone string, a *args) string {
	connector := ""
	if s.Network != "" {
		n, _ := networkByID(s.Network)
		connector = n.Connector
		if connector == "" {
			connector = "-"
		}
	}
	val := map[string]string{
		"":            `sum(m.clicks)::float8`,
		"clicks":      `sum(m.clicks)::float8`,
		"impressions": `sum(m.impressions)::float8`,
		"spend":       `sum(m.spend_micros)::float8 / 1000000`,
		"conversions": `sum(m.conversions)::float8`,
	}[s.Measure]
	// Only spend is money; clicks, impressions and conversions carry no
	// currency.
	cur := `''`
	if s.Measure == "spend" {
		cur = `m.currency`
	}
	bk := `''`
	switch breakdown {
	case "network":
		bk = `a.connector`
	case "campaign":
		bk = `left(e.name, 200)`
	}
	return `SELECT ` + strconv.Itoa(i) + ` AS i, GROUPING(p)::int AS gp, p, bk, sum(v) AS v, CASE WHEN count(DISTINCT cur) = 1 THEN min(cur) ELSE 'mixed' END AS cur FROM (
		SELECT date_trunc(` + pInterval + `, m.day::timestamp) AT TIME ZONE ` + pZone + ` AS p, ` + bk + ` AS bk, ` + val + ` AS v, ` + cur + ` AS cur
		FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id
		JOIN ad_metrics_daily m ON m.ad_entity_id = e.id AND m.day >= (` + pFrom + ` AT TIME ZONE ` + pZone + `)::date AND m.day < (` + pTo + ` AT TIME ZONE ` + pZone + `)::date
		WHERE e.workspace_id = ` + pWS + ` AND e.level = 'campaign' AND COALESCE(e.brand_id, a.brand_id) = ` + pBrand + `
			AND (` + a.add(connector) + ` = '' OR a.connector = ` + a.add(connector) + `)
			AND (` + a.add(s.Campaign) + ` = '' OR strpos(lower(e.name), lower(` + a.add(s.Campaign) + `)) > 0)
		GROUP BY 1, 2, m.currency) r GROUP BY GROUPING SETS ((i, p, bk), (i, bk))`
}

// periods lists the interval starts between from and to, in loc.
func periods(from, to time.Time, interval string, loc *time.Location) []time.Time {
	start := truncate(from, interval, loc)
	var out []time.Time
	for p := start; p.Before(to); p = next(p, interval) {
		out = append(out, p)
	}
	return out
}

// truncate is the start of t's day, week (from Monday, as Postgres's) or
// month in loc (UTC when nil).
func truncate(t time.Time, interval string, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	t = t.In(loc)
	switch interval {
	case "week":
		d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
		// Postgres weeks start on Monday.
		return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7))
	case "month":
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc)
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

// zoneOr is loc, UTC when nil.
func zoneOr(loc *time.Location) *time.Location {
	if loc == nil {
		return time.UTC
	}
	return loc
}

// zoneName is the Postgres name of loc, UTC when nil.
func zoneName(loc *time.Location) string {
	if loc == nil {
		return "UTC"
	}
	return loc.String()
}

func next(t time.Time, interval string) time.Time {
	switch interval {
	case "week":
		return t.AddDate(0, 0, 7)
	case "month":
		return t.AddDate(0, 1, 0)
	}
	return t.AddDate(0, 0, 1)
}

// Run answers a compiled trend, with the period before when asked.
func (c *CompiledTrend) Run(ctx context.Context, q Querier) (TrendResult, error) {
	res, err := c.run(ctx, q, c.t.From, c.t.To)
	if err != nil {
		return res, err
	}
	if c.t.Compare {
		span := c.t.To.Sub(c.t.From)
		prev, err := c.run(ctx, q, c.t.From.Add(-span), c.t.To.Add(-span))
		if err != nil {
			return res, err
		}
		prev.Breakdown = []TrendGroup{}
		res.Previous = &prev
	}
	return res, nil
}

func (c *CompiledTrend) run(ctx context.Context, q Querier, from, to time.Time) (TrendResult, error) {
	a := slices.Clone(c.args.list)
	a[c.from], a[c.to] = from, to
	rows, err := q.Query(ctx, c.sql, a...)
	if err != nil {
		return TrendResult{}, err
	}
	defer rows.Close()
	ps := periods(from, to, c.t.Interval, c.t.Location)
	n := len(c.t.Series)
	blank := func() []SeriesResult {
		out := make([]SeriesResult, n)
		for i := range out {
			out[i] = SeriesResult{Label: c.labels[i], Points: make([]Point, len(ps))}
			for j, p := range ps {
				out[i].Points[j].Start = p
			}
			if c.currency[i] == "brand" {
				out[i].Currency = "brand"
			}
		}
		return out
	}
	res := TrendResult{Series: blank(), Breakdown: []TrendGroup{}, From: from, To: to}
	groups := map[string]*TrendGroup{}
	for rows.Next() {
		var i, gp int
		var p *time.Time
		var bk, cur string
		var v *float64
		if err := rows.Scan(&i, &gp, &p, &bk, &v, &cur); err != nil {
			return TrendResult{}, err
		}
		set := func(sr *SeriesResult) {
			if cur != "" {
				sr.Currency = cur
			}
			if gp == 1 {
				sr.Total = v
				return
			}
			if p == nil {
				return
			}
			if j, ok := slices.BinarySearchFunc(ps, p.UTC(), func(a, b time.Time) int { return a.Compare(b) }); ok {
				sr.Points[j].Value = v
			}
		}
		if c.t.Breakdown == "" {
			set(&res.Series[i])
			continue
		}
		g, ok := groups[bk]
		if !ok {
			g = &TrendGroup{Value: bk, Series: blank()}
			groups[bk] = g
		}
		set(&g.Series[i])
	}
	if err := rows.Err(); err != nil {
		return TrendResult{}, err
	}
	if c.t.Breakdown != "" {
		// Totals across groups: sums add up; people and averages don't,
		// so they are recomputed from the points as a best effort.
		for _, g := range groups {
			res.Breakdown = append(res.Breakdown, *g)
			for i := range g.Series {
				res.Series[i] = addSeries(res.Series[i], g.Series[i], c.t.Series[i].Measure)
			}
		}
		res.Breakdown = topTrendGroups(res.Breakdown, c.labels)
	}
	if c.t.Formula != "" {
		f, _ := parseFormula(c.t.Formula, n)
		res.Formula = applyFormula(f, c.t.Formula, res.Series, ps)
		for gi := range res.Breakdown {
			fr := applyFormula(f, c.t.Formula, res.Breakdown[gi].Series, ps)
			res.Breakdown[gi].Series = append(res.Breakdown[gi].Series, *fr)
		}
	}
	return res, nil
}

// addSeries adds b into a, point by point; averages become the mean of
// the group averages, which is close enough for a chart.
func addSeries(a, b SeriesResult, measure string) SeriesResult {
	add := func(x, y *float64) *float64 {
		switch {
		case x == nil:
			return y
		case y == nil:
			return x
		}
		s := *x + *y
		if measure == MeasureAverage {
			s /= 2
		}
		return &s
	}
	a.Total = add(a.Total, b.Total)
	for j := range a.Points {
		a.Points[j].Value = add(a.Points[j].Value, b.Points[j].Value)
	}
	if b.Currency != "" {
		a.Currency = b.Currency
	}
	return a
}

// topTrendGroups keeps the MaxGroups biggest by the first series' total
// and adds the rest up as "$other".
func topTrendGroups(groups []TrendGroup, labels []string) []TrendGroup {
	total := func(g TrendGroup) float64 {
		if g.Series[0].Total == nil {
			return 0
		}
		return *g.Series[0].Total
	}
	slices.SortStableFunc(groups, func(x, y TrendGroup) int {
		if tx, ty := total(x), total(y); tx != ty {
			if ty > tx {
				return 1
			}
			return -1
		}
		return strings.Compare(x.Value, y.Value)
	})
	if len(groups) <= MaxGroups {
		return groups
	}
	other := TrendGroup{Value: "$other", Series: make([]SeriesResult, len(labels))}
	for i := range labels {
		other.Series[i] = SeriesResult{Label: labels[i], Points: slices.Clone(groups[MaxGroups].Series[i].Points)}
		for j := range other.Series[i].Points {
			other.Series[i].Points[j].Value = nil
		}
		for _, g := range groups[MaxGroups:] {
			other.Series[i] = addSeries(other.Series[i], g.Series[i], MeasureSum)
		}
	}
	return append(groups[:MaxGroups:MaxGroups], other)
}

// Formulas: A, B, C are the series; + - * / and brackets; numbers allowed.

type formula interface {
	eval(get func(i int) *float64) *float64
}

type fSeries int
type fNum float64
type fOp struct {
	op   byte
	l, r formula
}

func (f fSeries) eval(get func(int) *float64) *float64 { return get(int(f)) }
func (f fNum) eval(func(int) *float64) *float64        { v := float64(f); return &v }
func (f fOp) eval(get func(int) *float64) *float64 {
	l, r := f.l.eval(get), f.r.eval(get)
	if l == nil || r == nil {
		return nil
	}
	var v float64
	switch f.op {
	case '+':
		v = *l + *r
	case '-':
		v = *l - *r
	case '*':
		v = *l * *r
	case '/':
		if *r == 0 {
			return nil
		}
		v = *l / *r
	}
	return &v
}

type formulaParser struct {
	s   []rune
	pos int
	n   int
}

// parseFormula reads "A / B" over n series.
func parseFormula(src string, n int) (formula, error) {
	if len(src) > 200 {
		return nil, Invalid("formula is at most 200 characters")
	}
	p := &formulaParser{s: []rune(src), n: n}
	f, err := p.expr()
	if err != nil {
		return nil, err
	}
	p.skip()
	if p.pos != len(p.s) {
		return nil, Invalid("formula: unexpected " + string(p.s[p.pos:]))
	}
	return f, nil
}

func (p *formulaParser) skip() {
	for p.pos < len(p.s) && unicode.IsSpace(p.s[p.pos]) {
		p.pos++
	}
}

func (p *formulaParser) expr() (formula, error) {
	l, err := p.term()
	if err != nil {
		return nil, err
	}
	for {
		p.skip()
		if p.pos >= len(p.s) || (p.s[p.pos] != '+' && p.s[p.pos] != '-') {
			return l, nil
		}
		op := byte(p.s[p.pos])
		p.pos++
		r, err := p.term()
		if err != nil {
			return nil, err
		}
		l = fOp{op: op, l: l, r: r}
	}
}

func (p *formulaParser) term() (formula, error) {
	l, err := p.atom()
	if err != nil {
		return nil, err
	}
	for {
		p.skip()
		if p.pos >= len(p.s) || (p.s[p.pos] != '*' && p.s[p.pos] != '/') {
			return l, nil
		}
		op := byte(p.s[p.pos])
		p.pos++
		r, err := p.atom()
		if err != nil {
			return nil, err
		}
		l = fOp{op: op, l: l, r: r}
	}
}

func (p *formulaParser) atom() (formula, error) {
	p.skip()
	if p.pos >= len(p.s) {
		return nil, Invalid("formula ends early: it combines A, B, C with + - * / and brackets")
	}
	r := p.s[p.pos]
	switch {
	case r == '(':
		p.pos++
		f, err := p.expr()
		if err != nil {
			return nil, err
		}
		p.skip()
		if p.pos >= len(p.s) || p.s[p.pos] != ')' {
			return nil, Invalid("formula: a bracket isn't closed")
		}
		p.pos++
		return f, nil
	case r >= 'A' && r <= 'Z':
		p.pos++
		i := int(r - 'A')
		if i >= p.n {
			return nil, Invalid(fmt.Sprintf("formula names %c, and the trend has %d series", r, p.n))
		}
		return fSeries(i), nil
	case unicode.IsDigit(r) || r == '.':
		start := p.pos
		for p.pos < len(p.s) && (unicode.IsDigit(p.s[p.pos]) || p.s[p.pos] == '.') {
			p.pos++
		}
		v, err := strconv.ParseFloat(string(p.s[start:p.pos]), 64)
		if err != nil {
			return nil, Invalid("formula: " + string(p.s[start:p.pos]) + " isn't a number")
		}
		return fNum(v), nil
	}
	return nil, Invalid("formula: unexpected " + string(r))
}

// applyFormula evaluates f on every point and on the totals.
func applyFormula(f formula, label string, series []SeriesResult, ps []time.Time) *SeriesResult {
	out := &SeriesResult{Label: label, Points: make([]Point, len(ps))}
	out.Total = f.eval(func(i int) *float64 { return series[i].Total })
	for j, p := range ps {
		out.Points[j] = Point{Start: p, Value: f.eval(func(i int) *float64 { return series[i].Points[j].Value })}
	}
	return out
}

// TrendBreakdowns lists what a series of kind can be broken down by.
func TrendBreakdowns(kind string) []string {
	if kind == KindAd {
		return []string{"campaign", "network"}
	}
	return Breakdowns(kind)
}
