package insights

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// Lifecycle: in each day, week or month, how many of the people who did
// something were doing it for the first time (new), had also done it the
// period before (returning), came back after a gap (resurrected), and how
// many who did it the period before did not this time (dormant). New,
// returning and resurrected add up to everyone active in the period;
// dormant is the people lost, shown below the line.

// Lifecycle is one lifecycle question.
type Lifecycle struct {
	// Step is what counts as being active: an event, a page view, a visit,
	// a conversion or a payment, with filters. No time limit.
	Step     Step `json:"step"`
	From, To time.Time
	Interval string `json:"interval"`
	// Location is the time zone periods are in: the brand's reporting
	// time zone (B4a), UTC when nil.
	Location *time.Location `json:"-"`
}

// Validate checks the question before it is compiled.
func (l Lifecycle) Validate() error {
	if err := validRetentionStep(l.Step, "step"); err != nil {
		return err
	}
	if l.From.IsZero() || l.To.IsZero() || !l.To.After(l.From) || l.To.Sub(l.From) > MaxRange {
		return Invalid("from and to are dates, from before to, at most 366 days apart")
	}
	if !slices.Contains(Intervals, l.Interval) {
		return Invalid("interval is day, week or month")
	}
	return nil
}

// LifecyclePeriod is one period's people by what they were doing.
type LifecyclePeriod struct {
	Start time.Time `json:"start"`
	// New did it for the first time ever in this period.
	New int `json:"new"`
	// Returning did it in this period and the one before.
	Returning int `json:"returning"`
	// Resurrected did it in this period, not the one before, but some
	// time earlier.
	Resurrected int `json:"resurrected"`
	// Dormant did it the period before but not in this one. Shown as a
	// loss: it is not part of the period's active people.
	Dormant int `json:"dormant"`
}

// LifecycleResult is the periods in order.
type LifecycleResult struct {
	Label    string            `json:"label"`
	Interval string            `json:"interval"`
	Periods  []LifecyclePeriod `json:"periods"`
	From     time.Time         `json:"from"`
	To       time.Time         `json:"to"`
}

// CompiledLifecycle is a lifecycle question as one query.
type CompiledLifecycle struct {
	l     Lifecycle
	label string
	sql   string
	args  args
}

// lifecycleEpoch is how far back "ever" looks for a person's first time.
var lifecycleEpoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// CompileLifecycle turns the question into SQL. Whether someone is new
// needs their whole history, so the query reads every one of their rows
// before the dates end, not only the dates.
func CompileLifecycle(ctx context.Context, q Querier, ws, brand string, l Lifecycle) (*CompiledLifecycle, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	c := &CompiledLifecycle{l: l}
	a := &c.args
	pWS, pBrand := a.add(ws), a.add(brand)
	conv, err := loadConversion(ctx, q, ws, brand, l.Step.Kind, l.Step.Conversion, "step")
	if err != nil {
		return nil, err
	}
	c.label = l.Step.DefaultLabel(conv.Name)
	pEpoch, pTo := a.add(lifecycleEpoch), a.add(l.To)
	src, err := source(l.Step.Kind, l.Step.Event, conv, l.Step.Filters, l.Step.First, pEpoch, pTo, pWS, pBrand, a)
	if err != nil {
		return nil, err
	}
	pInterval := a.add(l.Interval)
	pStep := a.add("1 " + l.Interval)
	pFrom := a.add(truncate(l.From, l.Interval, l.Location))
	pZone := a.add(zoneName(l.Location))
	// Periods are wall-clock times in the zone, so a period plus a step is
	// the next one across daylight-saving changes; they become instants
	// on the way out.
	period := `date_trunc(` + pInterval + `, ` + src.ts + ` AT TIME ZONE ` + pZone + `)`
	from, to := `(`+pFrom+` AT TIME ZONE `+pZone+`)`, `(`+pTo+` AT TIME ZONE `+pZone+`)`
	// act is each person's active periods; seq adds the period before and
	// after each, so one pass tells new (no earlier period), returning
	// (the period just before), resurrected (an earlier one, with a gap)
	// and dormant (active, then nothing in the period after).
	c.sql = `WITH act AS (
		SELECT DISTINCT ` + src.actor + ` AS actor, ` + period + ` AS p FROM ` + src.from + ` WHERE ` + joinAnd(src.where) + `
	), seq AS (
		SELECT actor, p, lag(p) OVER (PARTITION BY actor ORDER BY p) AS prev, lead(p) OVER (PARTITION BY actor ORDER BY p) AS nxt FROM act
	)
	SELECT p AT TIME ZONE ` + pZone + `,
		count(*) FILTER (WHERE prev IS NULL)::int AS new,
		count(*) FILTER (WHERE prev = p - ` + pStep + `::interval)::int AS returning,
		count(*) FILTER (WHERE prev IS NOT NULL AND prev < p - ` + pStep + `::interval)::int AS resurrected,
		0 AS dormant
	FROM seq WHERE p >= ` + from + ` AND p < ` + to + ` GROUP BY p
	UNION ALL
	SELECT (p + ` + pStep + `::interval) AT TIME ZONE ` + pZone + `, 0, 0, 0, count(*)::int
	FROM seq WHERE (nxt IS NULL OR nxt > p + ` + pStep + `::interval)
		AND p + ` + pStep + `::interval >= ` + from + ` AND p + ` + pStep + `::interval < ` + to + `
	GROUP BY 1`
	return c, nil
}

func joinAnd(where []string) string {
	out := ""
	for i, w := range where {
		if i > 0 {
			out += " AND "
		}
		out += w
	}
	return out
}

// Run answers the question, with every period of the dates present, in
// order, even when nobody was active in it.
func (c *CompiledLifecycle) Run(ctx context.Context, q Querier) (LifecycleResult, error) {
	l := c.l
	res := LifecycleResult{Label: c.label, Interval: l.Interval, From: l.From, To: l.To, Periods: []LifecyclePeriod{}}
	ps := periods(l.From, l.To, l.Interval, l.Location)
	byStart := map[int64]int{}
	for i, p := range ps {
		res.Periods = append(res.Periods, LifecyclePeriod{Start: p})
		byStart[p.Unix()] = i
	}
	now := time.Now().UTC()
	rows, err := q.Query(ctx, c.sql, c.args.list...)
	if err != nil {
		return res, err
	}
	defer rows.Close()
	for rows.Next() {
		var p time.Time
		var n, ret, resur, dorm int
		if err := rows.Scan(&p, &n, &ret, &resur, &dorm); err != nil {
			return res, err
		}
		i, ok := byStart[p.UTC().Unix()]
		if !ok {
			continue
		}
		// A period that hasn't begun has lost nobody yet.
		if p.After(now) {
			dorm = 0
		}
		pr := &res.Periods[i]
		pr.New += n
		pr.Returning += ret
		pr.Resurrected += resur
		pr.Dormant += dorm
	}
	if err := rows.Err(); err != nil {
		return res, err
	}
	if len(res.Periods) == 0 {
		return res, fmt.Errorf("insights: lifecycle without periods")
	}
	return res, nil
}
