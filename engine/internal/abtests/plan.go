package abtests

import (
	"context"
	"fmt"
	"math"
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

// Traffic is a page's recent visitors and how many reached a goal after
// their first view.
type Traffic struct {
	Visitors  int64 `json:"visitors"`
	Converted int64 `json:"converted"`
}

// Rate is the share of visitors who reached the goal.
func (t Traffic) Rate() float64 {
	if t.Visitors == 0 {
		return 0
	}
	return float64(t.Converted) / float64(t.Visitors)
}

// ReadTraffic counts visitors with a page view in [from, to), on page when
// it is set (a path, or a prefix ending in *), and how many of them reached
// goal after their first such view.
func ReadTraffic(ctx context.Context, q Querier, ws string, g Goal, page string, from, to time.Time) (Traffic, error) {
	cond, arg := goalSQL(g)
	cond = fmt.Sprintf(cond, "$4")
	pageCond, pageArg := "", any(nil)
	if page != "" {
		pageCond, pageArg = " AND context->'page'->>'path' = $5", page
		if p, ok := strings.CutSuffix(page, "*"); ok {
			pageCond, pageArg = " AND starts_with(context->'page'->>'path', $5)", p
		}
	}
	args := []any{ws, from, to, arg}
	if pageArg != nil {
		args = append(args, pageArg)
	}
	rows, err := q.Query(ctx, `
		WITH v AS (
			SELECT anonymous_id, min(occurred_at) AS first
			FROM customer_events
			WHERE workspace_id = $1 AND name = 'page.viewed' AND occurred_at >= $2 AND occurred_at < $3
				AND anonymous_id IS NOT NULL`+pageCond+`
			GROUP BY anonymous_id
		), hit AS (
			SELECT DISTINCT v.anonymous_id
			FROM customer_events e JOIN v ON v.anonymous_id = e.anonymous_id AND e.occurred_at >= v.first
			WHERE e.workspace_id = $1 AND `+cond+` AND e.occurred_at < $3
		)
		SELECT (SELECT count(*) FROM v), (SELECT count(*) FROM hit)`, args...)
	if err != nil {
		return Traffic{}, err
	}
	defer rows.Close()
	var t Traffic
	if rows.Next() {
		if err := rows.Scan(&t.Visitors, &t.Converted); err != nil {
			return t, err
		}
	}
	return t, rows.Err()
}

// Baseline is the rate to record for a goal when its test starts, from
// the site's recent traffic, or 0 when there is too little of it.
func Baseline(t Traffic) float64 {
	if t.Visitors < MinBaselineVisitors || t.Converted == 0 {
		return 0
	}
	return t.Rate()
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
	Baseline       float64 `json:"baseline"`
	WeeklyVisitors float64 `json:"weekly_visitors"`
	// The weekly visitors of the smallest version: the comparison with
	// the control waits on it.
	WeeklyPerVersion float64   `json:"weekly_per_version"`
	MinEffect        float64   `json:"min_effect"`
	Rows             []PlanRow `json:"rows"`
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

// MakePlan estimates how long a test takes: weeklyVisitors are those the
// flag will show a version to each week, split by weights. The estimate is
// the typical point where the always-valid test reaches p < 0.05 if the
// observed rates equal the true ones; half of tests stop sooner, half
// later.
func MakePlan(baseline, weeklyVisitors float64, weights []int, minEffect float64) Plan {
	if minEffect <= 0 {
		minEffect = DefaultMinEffect
	}
	p := Plan{Baseline: baseline, WeeklyVisitors: weeklyVisitors, MinEffect: minEffect, Rows: []PlanRow{}}
	total, least := 0, 0
	for _, w := range weights {
		total += w
		if w > 0 && (least == 0 || w < least) {
			least = w
		}
	}
	if total == 0 || baseline <= 0 || baseline >= 1 || weeklyVisitors <= 0 {
		return p
	}
	p.WeeklyPerVersion = weeklyVisitors * float64(least) / float64(total)
	weeks := func(n int64) float64 {
		if n <= 0 {
			return 0
		}
		return math.Round(float64(n)/p.WeeklyPerVersion*10) / 10
	}
	for _, l := range PlanLifts {
		n := VisitorsNeeded(baseline, l, minEffect)
		p.Rows = append(p.Rows, PlanRow{Lift: l, VisitorsPerVersion: n, Weeks: weeks(n)})
	}
	p.Weeks = weeks(VisitorsNeeded(baseline, minEffect, minEffect))
	p.Detectable4Weeks = Detectable(baseline, int64(p.WeeklyPerVersion*4), minEffect)
	p.Detectable8Weeks = Detectable(baseline, int64(p.WeeklyPerVersion*8), minEffect)
	return p
}

// VisitorsNeeded is the visitors each version needs before the
// always-valid test reaches p < Alpha, if the version truly moves a goal
// with rate baseline by lift and the observed rates equal the true ones.
// 0 means more than maxPlanVisitors.
func VisitorsNeeded(baseline, lift, minEffect float64) int64 {
	pb := baseline * (1 + lift)
	if baseline <= 0 || pb >= 1 || lift == 0 {
		return 0
	}
	t := tau(minEffect, baseline, Arm{})
	reached := func(n int64) bool {
		a := Arm{Visitors: n, Conversions: int64(math.Round(baseline * float64(n)))}
		b := Arm{Visitors: n, Conversions: int64(math.Round(pb * float64(n)))}
		p, _, ok := sequential(a, b, t)
		return ok && p < Alpha
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
func Detectable(baseline float64, n int64, minEffect float64) float64 {
	fits := func(l float64) bool {
		need := VisitorsNeeded(baseline, l, minEffect)
		return need > 0 && need <= n
	}
	if n <= 0 || baseline <= 0 || !fits(math.Min(3, 1/baseline-1.0001)) {
		return 0
	}
	lo, hi := 0.001, math.Min(3, 1/baseline-1.0001)
	for range 40 {
		mid := (lo + hi) / 2
		if fits(mid) {
			hi = mid
		} else {
			lo = mid
		}
	}
	return math.Round(hi*1000) / 1000
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
		return RecommendShip, fmt.Sprintf("%s is at least 95%% likely to beat %s and the result is safe to stop.", w, control)
	case w == control:
		return RecommendKeepControl, fmt.Sprintf("Every version is at least 95%% likely to do worse than %s, and the result is safe to stop.", control)
	}
	least, base := int64(0), 0.0
	for i, a := range r.Arms {
		if i == 0 || a.Visitors < least {
			least = a.Visitors
		}
	}
	if len(r.Goals) > 0 {
		base = r.Goals[0].Baseline
		if base <= 0 && len(r.Goals[0].Arms) > 0 {
			base = r.Goals[0].Arms[0].Rate()
		}
	}
	weeks := now.Sub(started).Hours() / (24 * 7)
	if least < 100 || base <= 0 || weeks < 1.0/7 {
		return RecommendWait, "Too few visitors or conversions yet to say anything; check again in a few days."
	}
	need := VisitorsNeeded(base, minEffect, minEffect)
	perWeek := float64(least) / weeks
	if need == 0 || perWeek <= 0 || float64(need-least)/perWeek > 8 {
		return RecommendStop, fmt.Sprintf("At the traffic so far, a change of +%.0f%% is unlikely to be safe to stop within 8 more weeks. If no version stands out, end it and test a bolder change.", minEffect*100)
	}
	left := math.Max(0, float64(need-least)/perWeek)
	return RecommendExtend, fmt.Sprintf("No clear winner yet. At the traffic so far, a change of +%.0f%% would typically be safe to stop in about %.0f more week(s).", minEffect*100, math.Ceil(left))
}
