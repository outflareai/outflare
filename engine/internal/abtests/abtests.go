// Package abtests measures A/B tests: which version of a feature flag
// (package flags) did best at a goal. The flag splits visitors; a visitor
// joins the test at their first Experiment Viewed event for the flag
// (stored as experiment_viewed, with properties flag and variant, sent by
// the site script when the page reads the flag, or by a server), and
// converts when a goal event of theirs follows it before the test ends.
// A goal can also be measured per visitor: how many times they reached it,
// or the value they brought (a number on the event, or won deals).
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

// MaxGoals is the most goals a test watches, and MaxGuardrails the most
// guardrails.
const (
	MaxGoals      = 5
	MaxGuardrails = 5
)

var (
	eventRe    = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$`)
	propertyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	Kinds      = []string{"event", "page", "deal"}
	Metrics    = []string{MetricConversion, MetricCount, MetricValue}
)

// What a goal measures per visitor.
const (
	// MetricConversion: whether the visitor reached the goal (the rate).
	MetricConversion = "conversion"
	// MetricCount: how many times they reached it.
	MetricCount = "count"
	// MetricValue: the total of a number on the goal's events, such as
	// revenue, or for a deal goal the value of won deals in US dollars.
	MetricValue = "value"
)

// MaxWindowHours is the longest conversion window: a year.
const MaxWindowHours = 24 * 366

// Goal is what counts as success: a product event by name, a view of a
// page by path (a trailing * matches every path under it), or a won deal
// of the visitor's (their customer record, once the site knows who they
// are). Metric says what is measured per visitor; Property names the
// number on the event a value goal adds up. WindowHours, when set, counts
// only what happens within that many hours of the visitor first seeing
// their version; Cap, when set, caps each visitor's count or value so a
// few huge ones can't swing the result.
type Goal struct {
	Kind        string  `json:"kind"`
	Name        string  `json:"name,omitempty"`
	Path        string  `json:"path,omitempty"`
	Metric      string  `json:"metric,omitempty"`
	Property    string  `json:"property,omitempty"`
	WindowHours int     `json:"window_hours,omitempty"`
	Cap         float64 `json:"cap,omitempty"`
	// Better is which way is good, for a guardrail: "higher" (the default)
	// or "lower", such as for an error event or a refund.
	Better string `json:"better,omitempty"`
}

// MetricOf is the goal's metric, MetricConversion when unset.
func (g Goal) MetricOf() string {
	if g.Metric == "" {
		return MetricConversion
	}
	return g.Metric
}

// CheckGoals says what is wrong with a test's goals, or "".
func CheckGoals(gs []Goal) string {
	if len(gs) == 0 || len(gs) > MaxGoals {
		return fmt.Sprintf("give 1 to %d goals; the first is the one the test is judged on", MaxGoals)
	}
	for _, g := range gs {
		if g.Better != "" {
			return "better is for guardrails only: a goal is always better higher"
		}
		if msg := checkGoal(g); msg != "" {
			return msg
		}
	}
	return ""
}

// CheckGuardrails says what is wrong with a test's guardrails, or "".
func CheckGuardrails(gs []Goal) string {
	if len(gs) > MaxGuardrails {
		return fmt.Sprintf("give at most %d guardrails", MaxGuardrails)
	}
	for _, g := range gs {
		if g.Better != "" && g.Better != "higher" && g.Better != "lower" {
			return `a guardrail's better is "higher" or "lower"`
		}
		if msg := checkGoal(g); msg != "" {
			return "guardrail: " + msg
		}
	}
	return ""
}

func checkGoal(g Goal) string {
	switch g.Kind {
	case "event":
		if !eventRe.MatchString(g.Name) || len(g.Name) > 100 || g.Name == ExposureEvent {
			return "an event goal names a product event such as signup_started, as it appears in the event catalog"
		}
	case "page":
		if !strings.HasPrefix(g.Path, "/") || len(g.Path) > 500 || strings.Contains(strings.TrimSuffix(g.Path, "*"), "*") {
			return "a page goal is a path such as /thanks, or /docs/* for every page under /docs"
		}
	case "deal":
	default:
		return "a goal's kind is " + strings.Join(Kinds, ", ")
	}
	m := g.MetricOf()
	switch {
	case !slices.Contains(Metrics, m):
		return "a goal's metric is " + strings.Join(Metrics, ", ")
	case g.Kind == "page" && m == MetricValue:
		return "a page goal measures conversion or count; a value comes from an event's property or from deals"
	case g.Kind == "deal" && m == MetricCount:
		return "a deal goal measures conversion (won a deal) or value (the deals' value in US dollars)"
	case g.Kind == "event" && m == MetricValue && !propertyRe.MatchString(g.Property):
		return "a value goal names the event property to add up, such as revenue"
	case m != MetricValue && g.Property != "":
		return "only a value goal on an event takes a property"
	case g.WindowHours < 0 || g.WindowHours > MaxWindowHours:
		return fmt.Sprintf("window_hours is 1 to %d, or 0 for until the test ends", MaxWindowHours)
	case g.Cap < 0 || (g.Cap > 0 && m == MetricConversion):
		return "cap is a positive number, for count and value goals only"
	}
	return ""
}

// Querier is what Results reads with: a pool, a connection or a
// transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
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
	// Each goal's expected control mean (its rate, for a conversion goal),
	// recorded when the test started (Baseline), which fixes the
	// sequential test's tuning. A missing or zero one falls back to the
	// live control mean.
	Baselines []float64
	// Guardrails are watched alongside, to catch a version that hurts
	// something else.
	Guardrails []Goal
	From, To   time.Time
	// JoinUntil, when set, is when new visitors stopped joining (the test
	// was frozen); those already in keep counting until To.
	JoinUntil time.Time
	// ExcludeDomains leaves out visitors from these companies (the
	// workspace's own team), as on the Visitors page.
	ExcludeDomains []string
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
	// Metric is what the arms measure (MetricConversion, MetricCount or
	// MetricValue).
	Metric string `json:"metric"`
	// Baseline is the control mean the test was tuned with; 0 when none
	// was recorded.
	Baseline float64 `json:"baseline"`
	// Exploratory is true for every goal after the first: the winner is
	// decided on the first, and the others are for learning, since
	// checking many goals turns up chance differences.
	Exploratory bool `json:"exploratory"`
	// Days is how the goal's arms grew, one point a day, for the first
	// goal only.
	Days []Day `json:"days,omitempty"`
}

// GuardrailResult is a guardrail's arms and comparisons, and the versions
// that clearly hurt it: worse than the control in its bad direction, with
// the same proof as a winner (at least 95% likely, and safe to stop).
type GuardrailResult struct {
	GoalResult
	Harmed []string `json:"harmed"`
}

// Result is a test's results at a moment.
type Result struct {
	// Visitors per variant who joined the test, control first.
	Arms       []Arm             `json:"arms"`
	Goals      []GoalResult      `json:"goals"`
	Guardrails []GuardrailResult `json:"guardrails"`
	// Crossovers is how many visitors saw more than one version: they count
	// under the first, but many of them means something (a cache, a
	// server that assigns differently) is showing versions inconsistently.
	Crossovers int64 `json:"crossovers"`
	// The chance of a split this uneven by luck; below 0.001 the results
	// shouldn't be trusted (SampleRatioP).
	SampleRatioP float64 `json:"sample_ratio_p"`
	SampleRatio  bool    `json:"sample_ratio_mismatch"`
	// The smallest change the p-values were tuned to detect.
	MinEffect float64 `json:"min_effect"`
	// CorrectedFor is how many versions are compared with the control; past
	// one, every p-value and range is corrected for that many chances.
	CorrectedFor int `json:"corrected_for"`
	// FlagChanged says how the flag changed since the test started (its
	// split, content, rollout or versions): results that mix the before
	// and after need care.
	FlagChanged []string  `json:"flag_changed"`
	ComputedAt  time.Time `json:"computed_at"`
}

// Results counts each variant's visitors and conversions for every goal,
// day by day, and compares them with the control.
func Results(ctx context.Context, q Querier, ws string, s Spec) (Result, error) {
	order, weights := controlFirst(s)
	res := Result{ComputedAt: time.Now().UTC(), MinEffect: s.MinEffect, CorrectedFor: max(1, len(order)-1)}
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
		gr := GoalResult{Goal: g, Arms: list, Metric: g.MetricOf(), Baseline: base, Exploratory: i > 0,
			Comparisons: CompareSeries(looks, g.MetricOf(), s.MinEffect, base)}
		if i == 0 {
			gr.Days = days
		}
		res.Goals = append(res.Goals, gr)
	}
	res.Guardrails = []GuardrailResult{}
	for _, g := range s.Guardrails {
		days, err := count(ctx, q, ws, s, g, order)
		if err != nil {
			return res, err
		}
		looks := make([][]Arm, len(days))
		for j, d := range days {
			looks[j] = d.Arms
		}
		list := make([]Arm, len(order))
		for j, v := range order {
			list[j] = Arm{Variant: v}
		}
		if len(days) > 0 {
			list = days[len(days)-1].Arms
		} else {
			looks = [][]Arm{list}
		}
		gr := GuardrailResult{GoalResult: GoalResult{Goal: g, Arms: list, Metric: g.MetricOf(), Exploratory: true,
			Comparisons: CompareSeries(looks, g.MetricOf(), s.MinEffect, 0)}, Harmed: []string{}}
		for _, c := range gr.Comparisons {
			worse := c.ChanceToBeat <= 0.05
			if g.Better == "lower" {
				worse = c.ChanceToBeat >= 0.95
			}
			if worse && c.PValue < Alpha {
				gr.Harmed = append(gr.Harmed, c.Variant)
			}
		}
		res.Guardrails = append(res.Guardrails, gr)
	}
	var err error
	if res.Crossovers, err = crossovers(ctx, q, ws, s); err != nil {
		return res, err
	}
	res.SampleRatioP = SampleRatioP(res.Arms, weights)
	res.SampleRatio = res.SampleRatioP < 0.001
	return res, nil
}

// crossovers counts visitors who saw more than one of the flag's versions
// while the test ran.
func crossovers(ctx context.Context, q Querier, ws string, s Spec) (int64, error) {
	var n int64
	err := q.QueryRow(ctx, `
		SELECT count(*) FROM (
			SELECT anonymous_id FROM customer_events
			WHERE workspace_id = $1 AND name = $2 AND properties->>'flag' = $3 AND occurred_at >= $4 AND occurred_at < $5
				AND anonymous_id IS NOT NULL AND properties->>'variant' IS NOT NULL
			GROUP BY anonymous_id HAVING count(DISTINCT properties->>'variant') > 1
		) x`, ws, ExposureEvent, s.FlagKey, s.From, joinUntil(s)).Scan(&n)
	return n, err
}

// joinUntil is when visitors stopped joining: JoinUntil, or To.
func joinUntil(s Spec) time.Time {
	if !s.JoinUntil.IsZero() && s.JoinUntil.Before(s.To) {
		return s.JoinUntil
	}
	return s.To
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
// event reach an event or page goal g, with %s for its argument.
func goalSQL(g Goal) (string, any) {
	if g.Kind == "page" {
		if p, ok := strings.CutSuffix(g.Path, "*"); ok {
			return "e.name = 'page.viewed' AND starts_with(e.context->'page'->>'path', %s)", p
		}
		return "e.name = 'page.viewed' AND e.context->'page'->>'path' = %s", g.Path
	}
	return "e.name = %s", g.Name
}

// hitsSQL is a query of every time a visitor in seen (anonymous_id,
// variant, occurred_at: when they joined) reached goal g after joining and
// before $end, within the goal's window: (anonymous_id, variant, at, val),
// where val is what the hit adds to their value. $ws is the workspace and
// $arg the goal's argument; they and $end, $window and $prop are numbered
// placeholders the caller fills in.
func hitsSQL(g Goal, ws, end, arg, window, prop string) string {
	within := func(at string) string {
		return "AND (" + window + "::int IS NULL OR " + at + " < seen.occurred_at + " + window + "::int * interval '1 hour')"
	}
	if g.Kind == "deal" {
		val := "1"
		if g.MetricOf() == MetricValue {
			val = "coalesce(d.amount_cents, 0) / 100.0"
		}
		// A won deal counts for each browser linked to its person, closed
		// after that browser joined.
		return `SELECT seen.anonymous_id, seen.variant, d.closed_at AS at, ` + val + `::float8 AS val
			FROM seen
			JOIN site_visitors sv ON sv.workspace_id = ` + ws + ` AND sv.anonymous_id = seen.anonymous_id AND sv.record_id IS NOT NULL
			JOIN deals d ON d.workspace_id = ` + ws + ` AND d.record_id = sv.record_id AND d.state = 'won' AND d.currency = 'USD'
			WHERE d.closed_at >= seen.occurred_at AND d.closed_at < ` + end + ` ` + within("d.closed_at")
	}
	cond, _ := goalSQL(g)
	cond = fmt.Sprintf(cond, arg)
	val := "1"
	if g.MetricOf() == MetricValue {
		val = "CASE WHEN jsonb_typeof(e.properties->" + prop + "::text) = 'number' THEN greatest((e.properties->>" + prop + "::text)::float8, 0) ELSE 0 END"
	}
	return `SELECT seen.anonymous_id, seen.variant, e.occurred_at AS at, ` + val + ` AS val
		FROM customer_events e JOIN seen ON seen.anonymous_id = e.anonymous_id AND e.occurred_at >= seen.occurred_at
		WHERE e.workspace_id = ` + ws + ` AND ` + cond + ` AND e.occurred_at < ` + end + ` ` + within("e.occurred_at")
}

// goalArgs are the values hitsSQL's $arg, $window, $prop and $cap stand
// for: the goal's argument, its window in hours or nil, the property and
// the cap on a visitor's value (1 for a conversion goal) or nil.
func goalArgs(g Goal) (arg, window, prop, capv any) {
	_, arg = goalSQL(g)
	if g.Kind == "deal" {
		arg = ""
	}
	if g.WindowHours > 0 {
		window = g.WindowHours
	}
	prop = g.Property
	switch {
	case g.MetricOf() == MetricConversion:
		capv = 1.0
	case g.Cap > 0:
		capv = g.Cap
	}
	return
}

// count runs one goal's query and returns the cumulative arms (in order)
// at the end of each day that saw a visitor join or convert. A visitor is
// their anonymous id; their variant is the one they first saw. Each one's
// value is the total of their hits so far, capped at the goal's cap (1 for
// a conversion goal, so it says whether they reached it); a day adds what
// it changed to each arm's sum and sum of squares, so the cap applies to
// the running total. Visitors of a variant the flag no longer has are left
// out of the comparison.
func count(ctx context.Context, q Querier, ws string, s Spec, g Goal, order []string) ([]Day, error) {
	by, err := tally(ctx, q, ws, s, g, order, "")
	return by[""], err
}

// segmentSQL is the segment of a visitor, by dimension, from the visit
// (site_sessions ss) they first saw their version in; "unknown" when that
// visit isn't known (an exposure sent by a server).
var segmentSQL = map[string]string{
	"device":  `coalesce(nullif(ss.device, ''), 'unknown')`,
	"country": `coalesce(nullif(ss.country, ''), 'unknown')`,
	"source": `CASE WHEN ss.id IS NULL THEN 'unknown' WHEN ss.utm_source <> '' THEN lower(ss.utm_source)
		WHEN ss.referrer_host <> '' THEN lower(ss.referrer_host) ELSE 'direct' END`,
	"visitor": `CASE WHEN ss.id IS NULL THEN 'unknown'
		WHEN EXISTS (SELECT 1 FROM site_sessions p WHERE p.workspace_id = $1 AND p.anonymous_id = e.anonymous_id AND p.started_at < ss.started_at) THEN 'returning'
		ELSE 'new' END`,
}

// tally is count for every segment of dim ("" for none): the cumulative
// arms per day, by segment.
func tally(ctx context.Context, q Querier, ws string, s Spec, g Goal, order []string, dim string) (map[string][]Day, error) {
	hits := hitsSQL(g, "$1", "($4::timestamptz[])[2]", "$5", "$6", "$7")
	arg, window, prop, capv := goalArgs(g)
	// A visitor's segment rides along with their variant, after a unit
	// separator, through every step below.
	seg, join := "''", ""
	if dim != "" {
		seg = segmentSQL[dim]
		join = `LEFT JOIN site_sessions ss ON ss.workspace_id = $1
				AND ss.id = CASE WHEN e.context->>'sessionId' ~ '^[0-9a-fA-F-]{36}$' THEN (e.context->>'sessionId')::uuid END`
	}
	// params gives every placeholder a type, used by the goal or not.
	rows, err := q.Query(ctx, `
		WITH params AS (SELECT $5::text, $6::int, $7::text, $8::float8, $9::timestamptz, $10::text[]), first AS (
			SELECT DISTINCT ON (e.anonymous_id) e.anonymous_id, (e.properties->>'variant') || chr(31) || `+seg+` AS variant, e.occurred_at
			FROM customer_events e `+join+`
			WHERE e.workspace_id = $1 AND e.name = $2 AND e.properties->>'flag' = $3
				AND e.occurred_at >= ($4::timestamptz[])[1] AND e.occurred_at < ($4::timestamptz[])[2]
				AND e.anonymous_id IS NOT NULL AND e.properties->>'variant' IS NOT NULL
			ORDER BY e.anonymous_id, e.occurred_at
		), seen AS (
			-- Joined before new visitors stopped, and not from a company
			-- the workspace leaves out.
			SELECT * FROM first f WHERE f.occurred_at < $9::timestamptz
				AND (cardinality($10::text[]) = 0 OR NOT EXISTS (
					SELECT 1 FROM site_visitors sv, unnest($10::text[]) x
					WHERE sv.workspace_id = $1 AND sv.anonymous_id = f.anonymous_id AND (
						sv.company_domain = x OR sv.company_domain LIKE '%.' || x
						OR EXISTS (SELECT 1 FROM record_identities d WHERE d.record_id IN (sv.account_id, sv.record_id)
							AND d.namespace = 'domain' AND d.valid_to IS NULL AND (d.value = x OR d.value LIKE '%.' || x)))))
		), hits AS (`+hits+`
		), daily AS (
			SELECT anonymous_id, variant, to_char(at AT TIME ZONE 'UTC', 'YYYY-MM-DD') AS day, sum(val) AS dv
			FROM hits GROUP BY 1, 2, 3
		), cum AS (
			SELECT anonymous_id, variant, day,
				least(sum(dv) OVER (PARTITION BY anonymous_id ORDER BY day), $8::float8) AS v
			FROM daily
		), inc AS (
			SELECT variant, day, v, coalesce(lag(v) OVER (PARTITION BY anonymous_id ORDER BY day), 0) AS pv FROM cum
		)
		SELECT variant, to_char(occurred_at AT TIME ZONE 'UTC', 'YYYY-MM-DD'), count(*), 0, 0::float8, 0::float8 FROM seen GROUP BY 1, 2
		UNION ALL
		SELECT variant, day, 0, count(*) FILTER (WHERE v > 0 AND pv = 0), sum(v - pv), sum(v * v - pv * pv) FROM inc GROUP BY 1, 2`,
		ws, ExposureEvent, s.FlagKey, []time.Time{s.From, s.To}, arg, window, prop, capv, joinUntil(s), nonNil(s.ExcludeDomains))
	if err != nil {
		return nil, err
	}
	idx := map[string]int{}
	for i, v := range order {
		idx[v] = i
	}
	bySeg := map[string]map[string][]Arm{}
	for rows.Next() {
		var key, day string
		var visitors, conversions int64
		var sum, sq float64
		if err := rows.Scan(&key, &day, &visitors, &conversions, &sum, &sq); err != nil {
			rows.Close()
			return nil, err
		}
		variant, segment, _ := strings.Cut(key, "\x1f")
		i, ok := idx[variant]
		if !ok {
			continue
		}
		byDay := bySeg[segment]
		if byDay == nil {
			byDay = map[string][]Arm{}
			bySeg[segment] = byDay
		}
		st := byDay[day]
		if st == nil {
			st = make([]Arm, len(order))
			byDay[day] = st
		}
		st[i].Visitors += visitors
		st[i].Conversions += conversions
		st[i].Sum += sum
		st[i].SumSq += sq
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := map[string][]Day{}
	for segment, byDay := range bySeg {
		out[segment] = cumulative(byDay, order, g.MetricOf() != MetricConversion)
	}
	return out, nil
}

// cumulative turns each day's arms into running totals, one Day per date
// in order.
func cumulative(byDay map[string][]Arm, order []string, measured bool) []Day {
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
			total[i].Visitors += byDay[d][i].Visitors
			total[i].Conversions += byDay[d][i].Conversions
			if measured {
				total[i].Sum += byDay[d][i].Sum
				total[i].SumSq += byDay[d][i].SumSq
				if total[i].Visitors > 0 {
					total[i].Mean = total[i].Sum / float64(total[i].Visitors)
				}
			}
		}
		out[j] = Day{Date: d, Arms: slices.Clone(total)}
	}
	return out
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

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
