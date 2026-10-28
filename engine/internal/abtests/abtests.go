// Package abtests measures A/B tests: which version of a feature flag
// (package flags) did best at a goal. The flag splits visitors; a visitor
// joins the test at their first Experiment Viewed event for the flag
// (stored as experiment_viewed, with properties flag and variant, sent by
// the site script when the page reads the flag, or by a server), and
// converts when a goal event of theirs follows it before the test ends.
//
// The numbers come from the workspace's own product events
// (customer_events), so the results, the reports and the event stream
// agree. The maths is in stats.go.
package abtests

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ExposureEvent is the catalog name of Experiment Viewed.
const ExposureEvent = "experiment_viewed"

// MaxGoals is the most goals a test watches.
const MaxGoals = 5

var (
	eventRe = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$`)
	Kinds   = []string{"event", "page"}
)

// Goal is what counts as success: a product event by name, or a view of a
// page by path (a trailing * matches every path under it).
type Goal struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
	Path string `json:"path,omitempty"`
}

// CheckGoals says what is wrong with a test's goals, or "".
func CheckGoals(gs []Goal) string {
	if len(gs) == 0 || len(gs) > MaxGoals {
		return fmt.Sprintf("give 1 to %d goals; the first is the one the test is judged on", MaxGoals)
	}
	for _, g := range gs {
		switch g.Kind {
		case "event":
			if !eventRe.MatchString(g.Name) || len(g.Name) > 100 || g.Name == ExposureEvent {
				return "an event goal names a product event such as signup_started, as it appears in the event catalog"
			}
		case "page":
			if !strings.HasPrefix(g.Path, "/") || len(g.Path) > 500 || strings.Contains(strings.TrimSuffix(g.Path, "*"), "*") {
				return "a page goal is a path such as /thanks, or /docs/* for every page under /docs"
			}
		default:
			return "a goal's kind is " + strings.Join(Kinds, " or ")
		}
	}
	return ""
}

// Querier is what Results reads with: a pool, a connection or a
// transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Spec is what Results needs of a test.
type Spec struct {
	FlagKey string
	// The flag's variants in order with their weights, control first or
	// not; Control names it.
	Variants []string
	Weights  []int
	Control  string
	Goals    []Goal
	// The smallest relative change worth detecting, such as 0.25 for
	// +25%; 0 means DefaultMinEffect.
	MinEffect float64
	// Each goal's expected control rate, recorded when the test started
	// (Baseline), which fixes the sequential test's tuning. A missing or
	// zero one falls back to the live control rate.
	Baselines []float64
	From, To  time.Time
}

// Day is the cumulative arms at the end of one day (UTC), control first;
// the last day of a running test is today so far.
type Day struct {
	Date string `json:"date"`
	Arms []Arm  `json:"arms"`
}

// GoalResult is one goal's arms and their comparison with the control.
type GoalResult struct {
	Goal        Goal         `json:"goal"`
	Arms        []Arm        `json:"arms"`
	Comparisons []Comparison `json:"comparisons"`
	// Baseline is the control rate the test was tuned with; 0 when none
	// was recorded.
	Baseline float64 `json:"baseline"`
	// Days is how the goal's arms grew, one point a day, for the first
	// goal only.
	Days []Day `json:"days,omitempty"`
}

// Result is a test's results at a moment.
type Result struct {
	// Visitors per variant who joined the test, control first.
	Arms  []Arm        `json:"arms"`
	Goals []GoalResult `json:"goals"`
	// The chance of a split this uneven by luck; below 0.001 the results
	// shouldn't be trusted (SampleRatioP).
	SampleRatioP float64 `json:"sample_ratio_p"`
	SampleRatio  bool    `json:"sample_ratio_mismatch"`
	// The smallest change the p-values were tuned to detect.
	MinEffect  float64   `json:"min_effect"`
	ComputedAt time.Time `json:"computed_at"`
}

// Results counts each variant's visitors and conversions for every goal,
// day by day, and compares them with the control.
func Results(ctx context.Context, q Querier, ws string, s Spec) (Result, error) {
	order, weights := controlFirst(s)
	res := Result{ComputedAt: time.Now().UTC(), MinEffect: s.MinEffect}
	if res.MinEffect <= 0 {
		res.MinEffect = DefaultMinEffect
	}
	for i, g := range s.Goals {
		days, err := count(ctx, q, ws, s, g, order)
		if err != nil {
			return res, err
		}
		list := make([]Arm, len(order))
		for j, v := range order {
			list[j] = Arm{Variant: v}
		}
		if len(days) > 0 {
			list = days[len(days)-1].Arms
		}
		if i == 0 {
			res.Arms = make([]Arm, len(list))
			for j, a := range list {
				res.Arms[j] = Arm{Variant: a.Variant, Visitors: a.Visitors}
			}
		}
		base := 0.0
		if i < len(s.Baselines) {
			base = s.Baselines[i]
		}
		looks := make([][]Arm, len(days))
		for j, d := range days {
			looks[j] = d.Arms
		}
		if len(looks) == 0 {
			looks = [][]Arm{list}
		}
		gr := GoalResult{Goal: g, Arms: list, Comparisons: CompareSeries(looks, s.MinEffect, base), Baseline: base}
		if i == 0 {
			gr.Days = days
		}
		res.Goals = append(res.Goals, gr)
	}
	res.SampleRatioP = SampleRatioP(res.Arms, weights)
	res.SampleRatio = res.SampleRatioP < 0.001
	return res, nil
}

func controlFirst(s Spec) ([]string, []int) {
	order, weights := []string{s.Control}, []int{0}
	for i, v := range s.Variants {
		w := 0
		if i < len(s.Weights) {
			w = s.Weights[i]
		}
		if v == s.Control {
			weights[0] = w
			continue
		}
		order = append(order, v)
		weights = append(weights, w)
	}
	return order, weights
}

// goalSQL is the condition on customer_events (aliased e) that makes an
// event reach goal g, with its argument.
func goalSQL(g Goal) (string, any) {
	if g.Kind == "page" {
		if p, ok := strings.CutSuffix(g.Path, "*"); ok {
			return "e.name = 'page.viewed' AND starts_with(e.context->'page'->>'path', %s)", p
		}
		return "e.name = 'page.viewed' AND e.context->'page'->>'path' = %s", g.Path
	}
	return "e.name = %s", g.Name
}

// count runs one goal's query and returns the cumulative arms (in order)
// at the end of each day that saw a visitor join or convert. A visitor is
// their anonymous id; their variant is the one they first saw; they
// convert on their first goal event after that. Visitors of a variant the
// flag no longer has are left out of the comparison.
func count(ctx context.Context, q Querier, ws string, s Spec, g Goal, order []string) ([]Day, error) {
	cond, arg := goalSQL(g)
	cond = fmt.Sprintf(cond, "$5")
	rows, err := q.Query(ctx, `
		WITH seen AS (
			SELECT DISTINCT ON (anonymous_id) anonymous_id, properties->>'variant' AS variant, occurred_at
			FROM customer_events
			WHERE workspace_id = $1 AND name = $2 AND properties->>'flag' = $3
				AND occurred_at >= ($4::timestamptz[])[1] AND occurred_at < ($4::timestamptz[])[2]
				AND anonymous_id IS NOT NULL AND properties->>'variant' IS NOT NULL
			ORDER BY anonymous_id, occurred_at
		), hit AS (
			SELECT seen.anonymous_id, min(e.occurred_at) AS at
			FROM customer_events e JOIN seen ON seen.anonymous_id = e.anonymous_id AND e.occurred_at >= seen.occurred_at
			WHERE e.workspace_id = $1 AND `+cond+`
				AND e.occurred_at >= ($4::timestamptz[])[1] AND e.occurred_at < ($4::timestamptz[])[2]
			GROUP BY seen.anonymous_id
		)
		SELECT seen.variant, to_char(seen.occurred_at AT TIME ZONE 'UTC', 'YYYY-MM-DD'), false, count(*) FROM seen GROUP BY 1, 2
		UNION ALL
		SELECT seen.variant, to_char(hit.at AT TIME ZONE 'UTC', 'YYYY-MM-DD'), true, count(*) FROM seen JOIN hit USING (anonymous_id) GROUP BY 1, 2`,
		ws, ExposureEvent, s.FlagKey, []time.Time{s.From, s.To}, arg)
	if err != nil {
		return nil, err
	}
	idx := map[string]int{}
	for i, v := range order {
		idx[v] = i
	}
	type step struct{ visitors, conversions []int64 }
	byDay := map[string]*step{}
	for rows.Next() {
		var variant, day string
		var conv bool
		var n int64
		if err := rows.Scan(&variant, &day, &conv, &n); err != nil {
			rows.Close()
			return nil, err
		}
		i, ok := idx[variant]
		if !ok {
			continue
		}
		st := byDay[day]
		if st == nil {
			st = &step{make([]int64, len(order)), make([]int64, len(order))}
			byDay[day] = st
		}
		if conv {
			st.conversions[i] += n
		} else {
			st.visitors[i] += n
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	dates := make([]string, 0, len(byDay))
	for d := range byDay {
		dates = append(dates, d)
	}
	slices.Sort(dates)
	out := make([]Day, len(dates))
	total := make([]Arm, len(order))
	for i, v := range order {
		total[i].Variant = v
	}
	for j, d := range dates {
		for i := range order {
			total[i].Visitors += byDay[d].visitors[i]
			total[i].Conversions += byDay[d].conversions[i]
		}
		out[j] = Day{Date: d, Arms: slices.Clone(total)}
	}
	return out, nil
}

// Winner is the variant the results favour, or "" while none is clear: a
// version whose chance to beat the control is at least 95% and whose
// always-valid p-value is under 0.05 on the primary goal, or the control
// when every version is at most 5% likely to beat it with the same proof.
// A sample ratio mismatch means no winner.
func Winner(r Result, control string) string {
	if r.SampleRatio || len(r.Goals) == 0 {
		return ""
	}
	best, bestChance := "", 0.0
	allWorse := len(r.Goals[0].Comparisons) > 0
	for _, c := range r.Goals[0].Comparisons {
		if c.ChanceToBeat >= 0.95 && c.PValue < 0.05 && c.ChanceToBeat > bestChance {
			best, bestChance = c.Variant, c.ChanceToBeat
		}
		if !(c.ChanceToBeat <= 0.05 && c.PValue < 0.05) {
			allWorse = false
		}
	}
	if best != "" {
		return best
	}
	if allWorse {
		return control
	}
	return ""
}

// HasVariant reports whether v is one of a flag's variants.
func HasVariant(variants []string, v string) bool { return slices.Contains(variants, v) }
