package abtests

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// Planning: before a test starts, how long it will take. The answer comes
// from the site's own recent traffic and the goal's rate among those
// visitors, and from the same sequential test the results use, so "this
// needs 6 weeks" and "safe to stop" agree.

// TrafficWindow is how far back the planner and the recorded baseline
// look.
const TrafficWindow = 28 * 24 * time.Hour

// MinBaselineVisitors is the fewest visitors a recorded baseline is based
// on; below it, a test tunes itself with its live control rate.
const MinBaselineVisitors = 200

// Traffic is a page's recent visitors and, for a goal, how many reached it
// after their first view and the total and sum of squares of their values
// (for a count or value goal, capped as the goal says).
type Traffic struct {
	Visitors  int64   `json:"visitors"`
	Converted int64   `json:"converted"`
	Sum       float64 `json:"sum,omitempty"`
	SumSq     float64 `json:"-"`
}

// Rate is the share of visitors who reached the goal.
func (t Traffic) Rate() float64 {
	if t.Visitors == 0 {
		return 0
	}
	return float64(t.Converted) / float64(t.Visitors)
}

// Moments is one visitor's mean value and its variance for metric.
func (t Traffic) Moments(metric string) Moments {
	return Arm{Visitors: t.Visitors, Conversions: t.Converted, Sum: t.Sum, SumSq: t.SumSq}.Moments(metric)
}

// ReadTraffic counts visitors with a page view in [from, to), on page when
// it is set (a path, or a prefix ending in *), and what they did towards
// goal g after their first such view: how many reached it and, for a count
// or value goal, the total of their values.
func ReadTraffic(ctx context.Context, q Querier, ws string, g Goal, page string, from, to time.Time) (Traffic, error) {
	pageCond, pageArg := "", any(nil)
	if page != "" {
		pageCond, pageArg = " AND context->'page'->>'path' = $8", page
		if p, ok := strings.CutSuffix(page, "*"); ok {
			pageCond, pageArg = " AND starts_with(context->'page'->>'path', $8)", p
		}
	}
	arg, window, prop, capv := goalArgs(g)
	args := []any{ws, from, to, arg, window, prop, capv}
	if pageArg != nil {
		args = append(args, pageArg)
	}
	rows, err := q.Query(ctx, `
		WITH params AS (SELECT $4::text, $5::int, $6::text, $7::float8), seen AS (
			SELECT anonymous_id, ''::text AS variant, min(occurred_at) AS occurred_at
			FROM customer_events
			WHERE workspace_id = $1 AND name = 'page.viewed' AND occurred_at >= $2 AND occurred_at < $3
				AND anonymous_id IS NOT NULL`+pageCond+`
			GROUP BY anonymous_id
		), hits AS (`+hitsSQL(g, "$1", "$3", "$4", "$5", "$6")+`
		), per AS (
			SELECT anonymous_id, least(sum(val), $7::float8) AS v FROM hits GROUP BY anonymous_id
		)
		SELECT (SELECT count(*) FROM seen), count(*) FILTER (WHERE v > 0), coalesce(sum(v), 0)::float8, coalesce(sum(v * v), 0)::float8
		FROM per`, args...)
	if err != nil {
		return Traffic{}, err
	}
	defer rows.Close()
	var t Traffic
	if rows.Next() {
		if err := rows.Scan(&t.Visitors, &t.Converted, &t.Sum, &t.SumSq); err != nil {
			return t, err
		}
	}
	return t, rows.Err()
}

// Baseline is the mean to record for a goal when its test starts (its
// rate, for a conversion goal), from the site's recent traffic, or 0 when
// there is too little of it.
func Baseline(t Traffic, metric string) float64 {
	if t.Visitors < MinBaselineVisitors || t.Converted == 0 {
		return 0
	}
	return t.Moments(metric).Mean
}

// PlanRow is how many visitors each version needs, and how long that
// takes, if the change truly moves the goal by Lift.
type PlanRow struct {
	Lift               float64 `json:"lift"`
	VisitorsPerVersion int64   `json:"visitors_per_version"`
	Weeks              float64 `json:"weeks"`
}

// Plan is how long a test is likely to take.
type Plan struct {
	// Baseline is the goal's mean per visitor today (its rate, for a
	// conversion goal).
	Baseline       float64 `json:"baseline"`
	Metric         string  `json:"metric"`
	WeeklyVisitors float64 `json:"weekly_visitors"`
	// The weekly visitors of the smallest version: the comparison with
	// the control waits on it.
	WeeklyPerVersion float64 `json:"weekly_per_version"`
	MinEffect        float64 `json:"min_effect"`
	// Versions is how many versions compete with the control; past one,
	// each needs more visitors, since the results are corrected for the
	// extra chances of a false winner.
	Versions int       `json:"versions"`
	Rows     []PlanRow `json:"rows"`
	// The smallest lift likely to be found safe to stop within 4 and 8
	// weeks; 0 when even +300% would take longer.
	Detectable4Weeks float64 `json:"detectable_4_weeks"`
	Detectable8Weeks float64 `json:"detectable_8_weeks"`
	// Weeks the test is likely to need if the change moves the goal by
	// MinEffect; 0 when it can't be planned.
	Weeks float64 `json:"weeks"`
}

// PlanLifts are the true lifts a plan shows.
var PlanLifts = []float64{0.05, 0.10, 0.25, 0.50}

// maxPlanVisitors caps the search: a version needing more is "too long".
const maxPlanVisitors = 50_000_000

// MakePlan estimates how long a test takes: base is one visitor's value
// today for metric, and weeklyVisitors are those the flag will show a
// version to each week, split by weights (the control's included). The
// estimate is the typical point where the always-valid test, corrected for
// the number of versions, reaches p < 0.05 if the observed means equal the
// true ones; half of tests stop sooner, half later.
func MakePlan(base Moments, metric string, weeklyVisitors float64, weights []int, minEffect float64) Plan {
	if minEffect <= 0 {
		minEffect = DefaultMinEffect
	}
	p := Plan{Baseline: base.Mean, Metric: metric, WeeklyVisitors: weeklyVisitors, MinEffect: minEffect, Rows: []PlanRow{}}
	total, least, arms := 0, 0, 0
	for _, w := range weights {
		total += w
		if w > 0 {
			arms++
			if least == 0 || w < least {
				least = w
			}
		}
	}
	p.Versions = max(1, arms-1)
	if total == 0 || base.Mean <= 0 || weeklyVisitors <= 0 || !plannable(base, metric) {
		return p
	}
	alpha := Alpha / float64(p.Versions)
	p.WeeklyPerVersion = weeklyVisitors * float64(least) / float64(total)
	weeks := func(n int64) float64 {
		if n <= 0 {
			return 0
		}
		return math.Round(float64(n)/p.WeeklyPerVersion*10) / 10
	}
	for _, l := range PlanLifts {
		n := VisitorsNeeded(base, metric, l, minEffect, alpha)
		p.Rows = append(p.Rows, PlanRow{Lift: l, VisitorsPerVersion: n, Weeks: weeks(n)})
	}
	p.Weeks = weeks(VisitorsNeeded(base, metric, minEffect, minEffect, alpha))
	p.Detectable4Weeks = Detectable(base, metric, int64(p.WeeklyPerVersion*4), minEffect, alpha)
	p.Detectable8Weeks = Detectable(base, metric, int64(p.WeeklyPerVersion*8), minEffect, alpha)
	return p
}

// plannable says whether base can be planned for: a rate strictly between
// 0 and 1, or a positive mean with some spread.
func plannable(base Moments, metric string) bool {
	if metric == "" || metric == MetricConversion {
		return base.Mean > 0 && base.Mean < 1
	}
	return base.Mean > 0 && base.Var > 0
}

// moved is base with its mean moved by lift: for a rate the variance
// follows from the new rate; for a count or value the spread is assumed to
// grow with the mean (the same coefficient of variation).
func moved(base Moments, metric string, lift float64) (Moments, bool) {
	m := base.Mean * (1 + lift)
	if metric == "" || metric == MetricConversion {
		return Moments{m, m * (1 - m)}, m > 0 && m < 1
	}
	return Moments{m, base.Var * (1 + lift) * (1 + lift)}, m > 0
}

// VisitorsNeeded is the visitors each version needs before the
// always-valid test reaches p < alpha, if the version truly moves a goal
// whose visitors' values have base by lift and the observed means equal
// the true ones. 0 means more than maxPlanVisitors.
func VisitorsNeeded(base Moments, metric string, lift, minEffect, alpha float64) int64 {
	b, ok := moved(base, metric, lift)
	if !plannable(base, metric) || !ok || lift == 0 {
		return 0
	}
	t := tau(minEffect, base.Mean, 0)
	reached := func(n int64) bool {
		a, bn := base, b
		if metric == "" || metric == MetricConversion {
			// Whole conversions, as the results will count them.
			a = Arm{Visitors: n, Conversions: int64(math.Round(base.Mean * float64(n)))}.Moments(metric)
			bn = Arm{Visitors: n, Conversions: int64(math.Round(b.Mean * float64(n)))}.Moments(metric)
		}
		p, _, ok := sequential(a, n, bn, n, t, alpha)
		return ok && p < alpha
	}
	hi := int64(100)
	for !reached(hi) {
		if hi >= maxPlanVisitors {
			return 0
		}
		hi = min(hi*2, maxPlanVisitors)
	}
	lo := hi / 2
	for lo+1 < hi {
		mid := (lo + hi) / 2
		if reached(mid) {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi
}

// Detectable is the smallest lift (up to +300%) likely to be found safe
// to stop with n visitors per version, or 0.
func Detectable(base Moments, metric string, n int64, minEffect, alpha float64) float64 {
	top := 3.0
	if metric == "" || metric == MetricConversion {
		top = math.Min(3, 1/base.Mean-1.0001)
	}
	fits := func(l float64) bool {
		need := VisitorsNeeded(base, metric, l, minEffect, alpha)
		return need > 0 && need <= n
	}
	if n <= 0 || base.Mean <= 0 || top <= 0 || !fits(top) {
		return 0
	}
	lo, hi := 0.001, top
	for range 40 {
		mid := (lo + hi) / 2
		if fits(mid) {
			hi = mid
		} else {
			lo = mid
		}
	}
	// Rounded up, so the lift shown fits in n.
	return math.Ceil(hi*1000) / 1000
}

// Recommendations a results summary can make. The engine decides; an AI
// only puts it into words.
const (
	RecommendShip        = "ship"         // a version is the winner
	RecommendKeepControl = "keep_control" // every version is worse
	RecommendFixSplit    = "fix_split"    // sample ratio mismatch
	RecommendWait        = "wait"         // too few visitors to say anything
	RecommendExtend      = "extend"       // keep it running
	RecommendStop        = "stop"         // unlikely to finish in 8 more weeks
	// The winner clearly hurts a guardrail: a person weighs the trade.
	RecommendCheckGuardrails = "check_guardrails"
)

// Recommend says what to do with a running or finished test's results, and
// why, in a sentence: started is when it began. For a test without a
// winner, it estimates from the traffic so far whether the smallest
// change worth finding can still be found within 8 more weeks.
func Recommend(r Result, control string, minEffect float64, started, now time.Time) (string, string) {
	if minEffect <= 0 {
		minEffect = DefaultMinEffect
	}
	switch w := Winner(r, control); {
	case r.SampleRatio:
		return RecommendFixSplit, "Visitors split between versions far from the flag's weights, so the results can't be trusted until the cause (a redirect, a cache, bots) is fixed."
	case w != "" && w != control:
		for _, g := range r.Guardrails {
			if slices.Contains(g.Harmed, w) {
				return RecommendCheckGuardrails, fmt.Sprintf("%s is at least 95%% likely to beat %s and safe to stop, but it clearly hurts a guardrail. Weigh the gain against that cost before shipping it.", w, control)
			}
		}
		return RecommendShip, fmt.Sprintf("%s is at least 95%% likely to beat %s and the result is safe to stop.", w, control)
	case w == control:
		return RecommendKeepControl, fmt.Sprintf("Every version is at least 95%% likely to do worse than %s, and the result is safe to stop.", control)
	}
	least, base, metric := int64(0), Moments{}, MetricConversion
	for i, a := range r.Arms {
		if i == 0 || a.Visitors < least {
			least = a.Visitors
		}
	}
	if len(r.Goals) > 0 && len(r.Goals[0].Arms) > 0 {
		metric = r.Goals[0].Metric
		base = r.Goals[0].Arms[0].Moments(metric)
		if b := r.Goals[0].Baseline; b > 0 && base.Mean > 0 {
			// Keep the observed spread, scaled to the recorded mean.
			base.Var *= (b / base.Mean) * (b / base.Mean)
			if metric == "" || metric == MetricConversion {
				base.Var = b * (1 - b)
			}
			base.Mean = b
		}
	}
	weeks := now.Sub(started).Hours() / (24 * 7)
	if least < 100 || base.Mean <= 0 || weeks < 1.0/7 {
		return RecommendWait, "Too few visitors or conversions yet to say anything; check again in a few days."
	}
	need := VisitorsNeeded(base, metric, minEffect, minEffect, Alpha/float64(max(1, r.CorrectedFor)))
	perWeek := float64(least) / weeks
	if need == 0 || perWeek <= 0 || float64(need-least)/perWeek > 8 {
		return RecommendStop, fmt.Sprintf("At the traffic so far, a change of +%.0f%% is unlikely to be safe to stop within 8 more weeks. If no version stands out, end it and test a bolder change.", minEffect*100)
	}
	left := math.Max(0, float64(need-least)/perWeek)
	return RecommendExtend, fmt.Sprintf("No clear winner yet. At the traffic so far, a change of +%.0f%% would typically be safe to stop in about %.0f more week(s).", minEffect*100, math.Ceil(left))
}
