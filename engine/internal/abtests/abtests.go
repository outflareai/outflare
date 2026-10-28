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
	From, To  time.Time
}

// GoalResult is one goal's arms and their comparison with the control.
type GoalResult struct {
	Goal        Goal         `json:"goal"`
	Arms        []Arm        `json:"arms"`
	Comparisons []Comparison `json:"comparisons"`
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

// Results counts each variant's visitors and conversions for every goal
// and compares them with the control.
func Results(ctx context.Context, q Querier, ws string, s Spec) (Result, error) {
	order, weights := controlFirst(s)
	res := Result{ComputedAt: time.Now().UTC(), MinEffect: s.MinEffect}
	if res.MinEffect <= 0 {
		res.MinEffect = DefaultMinEffect
	}
	for i, g := range s.Goals {
		arms, err := count(ctx, q, ws, s, g)
		if err != nil {
			return res, err
		}
		list := make([]Arm, len(order))
		for j, v := range order {
			list[j] = arms[v]
			list[j].Variant = v
		}
		if i == 0 {
			res.Arms = make([]Arm, len(list))
			for j, a := range list {
				res.Arms[j] = Arm{Variant: a.Variant, Visitors: a.Visitors}
			}
		}
		res.Goals = append(res.Goals, GoalResult{Goal: g, Arms: list, Comparisons: Compare(list, s.MinEffect)})
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

// count runs one goal's query. A visitor is their anonymous id; their
// variant is the one they first saw. Visitors who saw a variant the flag
// no longer has still count under it, so a removed version doesn't vanish
// from the results.
func count(ctx context.Context, q Querier, ws string, s Spec, g Goal) (map[string]Arm, error) {
	goal, args := "name = $5", []any{ws, ExposureEvent, s.FlagKey, []time.Time{s.From, s.To}, g.Name}
	if g.Kind == "page" {
		goal, args[4] = "name = 'page.viewed' AND context->'page'->>'path' = $5", g.Path
		if p, ok := strings.CutSuffix(g.Path, "*"); ok {
			goal, args[4] = "name = 'page.viewed' AND starts_with(context->'page'->>'path', $5)", p
		}
	}
	rows, err := q.Query(ctx, `
		WITH seen AS (
			SELECT DISTINCT ON (anonymous_id) anonymous_id, properties->>'variant' AS variant, occurred_at
			FROM customer_events
			WHERE workspace_id = $1 AND name = $2 AND properties->>'flag' = $3
				AND occurred_at >= ($4::timestamptz[])[1] AND occurred_at < ($4::timestamptz[])[2]
				AND anonymous_id IS NOT NULL AND properties->>'variant' IS NOT NULL
			ORDER BY anonymous_id, occurred_at
		), hit AS (
			SELECT anonymous_id, max(occurred_at) AS last
			FROM customer_events
			WHERE workspace_id = $1 AND `+goal+`
				AND occurred_at >= ($4::timestamptz[])[1] AND occurred_at < ($4::timestamptz[])[2]
				AND anonymous_id IS NOT NULL
			GROUP BY anonymous_id
		)
		SELECT seen.variant, count(*), count(*) FILTER (WHERE hit.last >= seen.occurred_at)
		FROM seen LEFT JOIN hit USING (anonymous_id)
		GROUP BY seen.variant`, args...)
	if err != nil {
		return nil, err
	}
	out := map[string]Arm{}
	for rows.Next() {
		var a Arm
		if err := rows.Scan(&a.Variant, &a.Visitors, &a.Conversions); err != nil {
			rows.Close()
			return nil, err
		}
		out[a.Variant] = a
	}
	return out, rows.Err()
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
