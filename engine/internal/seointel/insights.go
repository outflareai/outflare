package seointel

import (
	"math"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// InsightRow is a measured result or explicitly labelled model estimate.
type InsightRow struct {
	Key             string         `json:"key"`
	Page            string         `json:"page,omitempty"`
	Query           string         `json:"query,omitempty"`
	Date            string         `json:"date,omitempty"`
	Country         string         `json:"country,omitempty"`
	Device          string         `json:"device,omitempty"`
	Status          string         `json:"status,omitempty"`
	Detail          string         `json:"detail,omitempty"`
	Source          string         `json:"source,omitempty"`
	Target          string         `json:"target,omitempty"`
	Metric          string         `json:"metric,omitempty"`
	Clicks          float64        `json:"clicks,omitempty"`
	Impressions     float64        `json:"impressions,omitempty"`
	CTR             float64        `json:"ctr,omitempty"`
	Position        float64        `json:"position,omitempty"`
	ExpectedCTR     float64        `json:"expected_ctr,omitempty"`
	MissedClicks    float64        `json:"missed_clicks,omitempty"`
	PotentialClicks float64        `json:"potential_clicks,omitempty"`
	Before          float64        `json:"before,omitempty"`
	After           float64        `json:"after,omitempty"`
	Change          float64        `json:"change,omitempty"`
	Score           float64        `json:"score,omitempty"`
	Lower           float64        `json:"lower,omitempty"`
	Upper           float64        `json:"upper,omitempty"`
	Sessions        float64        `json:"sessions,omitempty"`
	Conversions     float64        `json:"conversions,omitempty"`
	RevenueMicros   int64          `json:"revenue_micros,omitempty"`
	Currency        string         `json:"currency,omitempty"`
	Share           float64        `json:"share,omitempty"`
	Value           float64        `json:"value,omitempty"`
	GoodShare       float64        `json:"good_share,omitempty"`
	Values          []InsightPoint `json:"values,omitempty"`
}
type InsightPoint struct {
	Date  string  `json:"date"`
	Value float64 `json:"value"`
}
type InsightReport struct {
	Report  string             `json:"report"`
	From    string             `json:"from,omitempty"`
	To      string             `json:"to,omitempty"`
	Status  string             `json:"status"`
	Notes   []string           `json:"notes"`
	Source  *Source            `json:"source"`
	Data    []InsightRow       `json:"data"`
	Summary map[string]float64 `json:"summary"`
}
type GroupRule struct {
	Operator string `json:"operator"`
	Value    string `json:"value"`
}
type SearchGroup struct {
	ID       string      `json:"id,omitempty"`
	Name     string      `json:"name"`
	Kind     string      `json:"kind"`
	Rules    []GroupRule `json:"rules"`
	compiled []*regexp.Regexp
}

func ValidRules(g SearchGroup) bool {
	if strings.TrimSpace(g.Name) == "" || len(g.Name) > 120 || (g.Kind != "content" && g.Kind != "topic") || len(g.Rules) == 0 || len(g.Rules) > 100 {
		return false
	}
	for _, r := range g.Rules {
		if len(r.Value) == 0 || len(r.Value) > 500 {
			return false
		}
		switch r.Operator {
		case "prefix", "contains", "equals":
		case "regex":
			if _, e := regexp.Compile(r.Value); e != nil {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// Rules are ORed. Content rules accept a full URL or path; topic rules
// match queries case-insensitively. Regex uses Go's bounded RE2 engine.
func PrepareGroup(g *SearchGroup) {
	g.compiled = make([]*regexp.Regexp, len(g.Rules))
	for i, r := range g.Rules {
		if r.Operator == "regex" {
			pattern := r.Value
			if g.Kind == "topic" {
				pattern = "(?i:" + pattern + ")"
			}
			g.compiled[i], _ = regexp.Compile(pattern)
		}
	}
}
func Matches(g SearchGroup, page, query string) bool {
	value := page
	if g.Kind == "topic" {
		value = query
	}
	for i, r := range g.Rules {
		v, x := r.Value, value
		if g.Kind == "topic" && r.Operator != "regex" {
			v = strings.ToLower(v)
			x = strings.ToLower(x)
		} else if g.Kind == "content" && strings.HasPrefix(v, "/") {
			if u, e := url.Parse(value); e == nil {
				x = u.Path
			}
		}
		switch r.Operator {
		case "equals":
			if x == v {
				return true
			}
		case "prefix":
			if strings.HasPrefix(x, v) {
				return true
			}
		case "contains":
			if strings.Contains(x, v) {
				return true
			}
		case "regex":
			var rx *regexp.Regexp
			if i < len(g.compiled) {
				rx = g.compiled[i]
			} else {
				pattern := v
				if g.Kind == "topic" {
					pattern = "(?i:" + v + ")"
				}
				rx, _ = regexp.Compile(pattern)
			}
			if rx != nil && rx.MatchString(x) {
				return true
			}
		}
	}
	return false
}

// Generic reference curve is a modelling assumption, not a Google fact.
// A site's observed curve replaces it only where >=1000 impressions exist.
var ReferenceCTR = []float64{.28, .16, .11, .08, .065, .05, .04, .033, .028, .024, .02, .018, .016, .014, .012, .011, .01, .009, .008, .007}

func ExpectedCTR(position float64) float64 {
	if position < 1 {
		return 0
	}
	i := int(math.Round(position)) - 1
	if i >= len(ReferenceCTR) {
		return .005
	}
	return ReferenceCTR[i]
}
func Opportunities(rows []InsightRow) ([]InsightRow, []InsightRow) {
	curves := make([]InsightRow, 20)
	for i := range curves {
		curves[i] = InsightRow{Key: stringKey(i + 1), Position: float64(i + 1), ExpectedCTR: ReferenceCTR[i], Status: "reference"}
	}
	for _, r := range rows {
		b := int(math.Round(r.Position)) - 1
		if b >= 0 && b < 20 {
			curves[b].Clicks += r.Clicks
			curves[b].Impressions += r.Impressions
		}
	}
	for i := range curves {
		r := &curves[i]
		if r.Impressions > 0 {
			r.CTR = r.Clicks / r.Impressions
		}
		if r.Impressions >= 1000 {
			r.ExpectedCTR = r.CTR
			r.Status = "site_observed"
		}
	}
	out := []InsightRow{}
	for _, r := range rows {
		if r.Impressions < 50 || r.Position < 1 || r.Position > 30 {
			continue
		}
		r.CTR = r.Clicks / r.Impressions
		r.ExpectedCTR = ExpectedCTR(r.Position)
		b := int(math.Round(r.Position)) - 1
		if b >= 0 && b < 20 {
			r.ExpectedCTR = curves[b].ExpectedCTR
		}
		r.MissedClicks = math.Max(0, r.Impressions*r.ExpectedCTR-r.Clicks)
		if r.Position >= 4 && r.Position <= 20 {
			target := curves[2].ExpectedCTR
			r.PotentialClicks = math.Max(0, r.Impressions*target-r.Clicks)
		}
		r.Score = r.MissedClicks + r.PotentialClicks*.5
		r.Status = "estimate"
		if r.Score > 0 {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out, curves
}
func stringKey(n int) string {
	const digits = "0123456789"
	if n < 10 {
		return string(digits[n])
	}
	return string(digits[n/10]) + string(digits[n%10])
}

// Forecast uses robust weekday seasonality and a bounded linear trend.
// Yearly seasonality is only used with two observations of each week.
func Forecast(points []InsightPoint, weeks int, growth, conversionRate, orderValue float64) []InsightRow {
	points = ContiguousTail(points)
	out := []InsightRow{}
	if len(points) < 56 {
		return out
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Date < points[j].Date })
	last, err := time.Parse(time.DateOnly, points[len(points)-1].Date)
	if err != nil {
		return out
	}
	var sums [7]float64
	var counts [7]int
	var mean float64
	n := len(points)
	start := max(0, n-84)
	for _, p := range points[start:] {
		d, e := time.Parse(time.DateOnly, p.Date)
		if e == nil {
			sums[d.Weekday()] += p.Value
			counts[d.Weekday()]++
			mean += p.Value
		}
	}
	mean /= float64(n - start)
	if mean == 0 {
		return out
	}
	var xs, ys, x2, xy float64
	for i, p := range points[start:] {
		d, _ := time.Parse(time.DateOnly, p.Date)
		season := sums[d.Weekday()] / float64(max(1, counts[d.Weekday()]))
		v := p.Value
		if season > 0 {
			v = v * mean / season
		}
		x := float64(i)
		xs += x
		ys += v
		x2 += x * x
		xy += x * v
	}
	count := float64(n - start)
	slope := (count*xy - xs*ys) / math.Max(1, count*x2-xs*xs)
	slope = math.Max(-mean/180, math.Min(mean/180, slope))
	base := ys/count + slope*(count-1)/2
	var variance float64
	for _, p := range points[start:] {
		d, _ := time.Parse(time.DateOnly, p.Date)
		avg := sums[d.Weekday()] / float64(max(1, counts[d.Weekday()]))
		variance += (p.Value - avg) * (p.Value - avg)
	}
	sigma := math.Sqrt(variance / count)
	for i := 1; i <= weeks*7; i++ {
		d := last.AddDate(0, 0, i)
		season := sums[d.Weekday()] / float64(max(1, counts[d.Weekday()])) / mean
		value := math.Max(0, (base+slope*float64(i))*season) * math.Pow(1+growth, float64(i)/365)
		width := 1.96 * sigma * math.Sqrt(1+float64(i)/count)
		revenue := value * conversionRate * orderValue * 1e6
		if revenue >= float64(math.MaxInt64) {
			return []InsightRow{}
		}
		out = append(out, InsightRow{Key: d.Format(time.DateOnly), Date: d.Format(time.DateOnly), Value: value, Lower: math.Max(0, value-width), Upper: value + width, Conversions: value * conversionRate, RevenueMicros: int64(math.Round(revenue)), Status: "estimate"})
	}
	return out
}

// ChangePoints requires two settled weeks on either side of a break.
// Comparing equal weekday windows avoids ordinary weekly seasonality.
func ChangePoints(points []InsightPoint) []InsightRow {
	out := []InsightRow{}
	last := -28
	for i := 14; i+14 <= len(points); i++ {
		if len(ContiguousTail(points[i-14:i+14])) != 28 {
			continue
		}
		if i-last < 14 {
			continue
		}
		var b, a, variance float64
		for _, p := range points[i-14 : i] {
			b += p.Value
		}
		for _, p := range points[i : i+14] {
			a += p.Value
		}
		b /= 14
		a /= 14
		for _, p := range points[i-14 : i] {
			variance += (p.Value - b) * (p.Value - b)
		}
		if b < 5 || math.Abs(a-b) < math.Max(.2*b, 3*math.Sqrt(variance/14)/math.Sqrt(14)) {
			continue
		}
		out = append(out, InsightRow{Key: points[i].Date, Date: points[i].Date, Before: b, After: a, Change: (a - b) / b, Status: "sustained_change", Detail: "Two weeks either side; timing overlap does not establish cause."})
		last = i
	}
	return out
}

// ContiguousTail refuses to treat uncollected dates as zero or compress gaps.
func ContiguousTail(points []InsightPoint) []InsightPoint {
	if len(points) == 0 {
		return points
	}
	start := len(points) - 1
	for start > 0 {
		d, e := time.Parse(time.DateOnly, points[start].Date)
		if e != nil || d.AddDate(0, 0, -1).Format(time.DateOnly) != points[start-1].Date {
			break
		}
		start--
	}
	return points[start:]
}
