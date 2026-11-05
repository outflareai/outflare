package insights

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Retention: of the people who did one thing in a period (the cohort),
// how many came back and did another in each of the periods after. The
// start is a person's first start in the dates, so each person is in one
// cohort; a return counts once per period. Shown as a triangle of cohorts
// and the average curve across them.

// MaxRetentionPeriods is how many periods after the start are followed.
const MaxRetentionPeriods = 24

// Retention is one retention question.
type Retention struct {
	// Start is what puts a person in a cohort; Return is what counts as
	// coming back (the start again when nil).
	Start  Step  `json:"start"`
	Return *Step `json:"return,omitempty"`
	// From and To bound the cohorts' starts; returns are followed for
	// Periods after each.
	From, To time.Time
	Interval string `json:"interval"`
	Periods  int    `json:"periods"`
}

func validRetentionStep(s Step, at string) error {
	switch s.Kind {
	case KindEvent:
		if !eventName(s.Event) {
			return Invalid(at + ".event is an event's catalog name, such as signed_up")
		}
	case KindConversion:
		if s.Conversion == "" {
			return Invalid(at + ".conversion is a conversion definition's key")
		}
	case KindPage, KindVisit, KindPayment:
	default:
		return Invalid(at + ".kind is event, page, visit, conversion or payment")
	}
	if s.First && s.Kind != KindEvent && s.Kind != KindPayment {
		return Invalid(at + ".first only applies to event and payment steps")
	}
	if s.Within != nil {
		return Invalid(at + ".within: retention has no time limit; set periods")
	}
	if len(s.Label) > 80 {
		return Invalid(at + ".label is at most 80 characters")
	}
	if len(s.Filters) > 20 {
		return Invalid(at + ".filters has at most 20 filters")
	}
	for j, fl := range s.Filters {
		if err := validFilter(s.Kind, fl, fmt.Sprintf("%s.filters[%d]", at, j)); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks the question before it is compiled.
func (r Retention) Validate() error {
	if err := validRetentionStep(r.Start, "start"); err != nil {
		return err
	}
	if r.Return != nil {
		if err := validRetentionStep(*r.Return, "return"); err != nil {
			return err
		}
	}
	if r.From.IsZero() || r.To.IsZero() || !r.To.After(r.From) || r.To.Sub(r.From) > MaxRange {
		return Invalid("from and to are dates, from before to, at most 366 days apart")
	}
	if !slices.Contains(Intervals, r.Interval) {
		return Invalid("interval is day, week or month")
	}
	if r.Periods < 1 || r.Periods > MaxRetentionPeriods {
		return Invalid(fmt.Sprintf("periods is 1 to %d", MaxRetentionPeriods))
	}
	return nil
}

// Cohort is the people who started in one period and how many came back
// in each period after it (Counts[0] is the start period itself).
type Cohort struct {
	Start time.Time `json:"start"`
	Size  int       `json:"size"`
	// Counts has Periods+1 entries; nil for a period that hasn't begun.
	Counts []*int `json:"counts"`
}

// RetentionResult is the triangle and its average curve.
type RetentionResult struct {
	StartLabel  string   `json:"start_label"`
	ReturnLabel string   `json:"return_label"`
	Interval    string   `json:"interval"`
	Periods     int      `json:"periods"`
	Cohorts     []Cohort `json:"cohorts"`
	// Average is the share who came back in each period, over the cohorts
	// whose period had begun; nil where none had.
	Average []*float64 `json:"average"`
	From    time.Time  `json:"from"`
	To      time.Time  `json:"to"`
}

// CompiledRetention is a retention question as one query.
type CompiledRetention struct {
	r                       Retention
	startLabel, returnLabel string
	sql                     string
	args                    args
}

// CompileRetention turns the question into SQL.
func CompileRetention(ctx context.Context, q Querier, ws, brand string, r Retention) (*CompiledRetention, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	ret := r.Start
	if r.Return != nil {
		ret = *r.Return
	}
	c := &CompiledRetention{r: r}
	a := &c.args
	pWS, pBrand := a.add(ws), a.add(brand)
	pFrom, pTo := a.add(r.From), a.add(r.To)
	// Returns are followed Periods after the last cohort's start.
	last := truncate(r.To.Add(-time.Nanosecond), r.Interval)
	for i := 0; i <= r.Periods; i++ {
		last = next(last, r.Interval)
	}
	pEnd := a.add(last)
	pInterval := a.add(r.Interval)

	startConv, err := loadConversion(ctx, q, ws, brand, r.Start.Kind, r.Start.Conversion, "start")
	if err != nil {
		return nil, err
	}
	retConv, err := loadConversion(ctx, q, ws, brand, ret.Kind, ret.Conversion, "return")
	if err != nil {
		return nil, err
	}
	c.startLabel = r.Start.DefaultLabel(startConv.Name)
	c.returnLabel = ret.DefaultLabel(retConv.Name)
	if r.Return == nil && ret.Label == "" {
		c.returnLabel = "Did it again"
	}
	s, err := source(r.Start.Kind, r.Start.Event, startConv, r.Start.Filters, r.Start.First, pFrom, pTo, pWS, pBrand, a)
	if err != nil {
		return nil, err
	}
	rs, err := source(ret.Kind, ret.Event, retConv, ret.Filters, ret.First, pFrom, pEnd, pWS, pBrand, a)
	if err != nil {
		return nil, err
	}
	period := func(ts string) string {
		return `date_trunc(` + pInterval + `, ` + ts + ` AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`
	}
	// n is how many periods after the cohort's start a return fell.
	var n string
	switch r.Interval {
	case "month":
		n = `((EXTRACT(year FROM r.p) - EXTRACT(year FROM s.p0)) * 12 + EXTRACT(month FROM r.p) - EXTRACT(month FROM s.p0))::int`
	case "week":
		n = `(EXTRACT(epoch FROM (r.p - s.p0)) / 604800)::int`
	default:
		n = `(EXTRACT(epoch FROM (r.p - s.p0)) / 86400)::int`
	}
	c.sql = `WITH s AS (
		SELECT actor, min(p) AS p0 FROM (SELECT ` + s.actor + ` AS actor, ` + period(s.ts) + ` AS p FROM ` + s.from + ` WHERE ` + strings.Join(s.where, " AND ") + `) x GROUP BY actor
	), r AS (
		SELECT DISTINCT ` + rs.actor + ` AS actor, ` + period(rs.ts) + ` AS p FROM ` + rs.from + ` WHERE ` + strings.Join(rs.where, " AND ") + `
	)
	SELECT s.p0, -1 AS n, count(*)::int FROM s GROUP BY s.p0
	UNION ALL
	SELECT s.p0, ` + n + ` AS n, count(DISTINCT s.actor)::int FROM s JOIN r ON r.actor = s.actor AND r.p >= s.p0 GROUP BY s.p0, 2
	ORDER BY 1, 2`
	return c, nil
}

// Run answers the question.
func (c *CompiledRetention) Run(ctx context.Context, q Querier) (RetentionResult, error) {
	r := c.r
	res := RetentionResult{StartLabel: c.startLabel, ReturnLabel: c.returnLabel, Interval: r.Interval, Periods: r.Periods, From: r.From, To: r.To, Cohorts: []Cohort{}}
	now := time.Now().UTC()
	ps := periods(r.From, r.To, r.Interval)
	// Every cohort is made first, so the pointers into the slice hold.
	res.Cohorts = make([]Cohort, 0, len(ps))
	byStart := map[int64]*Cohort{}
	for _, p := range ps {
		co := Cohort{Start: p, Counts: make([]*int, r.Periods+1)}
		at := p
		for i := range co.Counts {
			if !at.After(now) {
				zero := 0
				co.Counts[i] = &zero
			}
			at = next(at, r.Interval)
		}
		res.Cohorts = append(res.Cohorts, co)
		byStart[p.Unix()] = &res.Cohorts[len(res.Cohorts)-1]
	}
	rows, err := q.Query(ctx, c.sql, c.args.list...)
	if err != nil {
		return res, err
	}
	defer rows.Close()
	for rows.Next() {
		var p0 time.Time
		var n, count int
		if err := rows.Scan(&p0, &n, &count); err != nil {
			return res, err
		}
		co := byStart[p0.Unix()]
		if co == nil {
			continue
		}
		switch {
		case n < 0:
			co.Size = count
		case n <= r.Periods && co.Counts[n] != nil:
			*co.Counts[n] = count
		}
	}
	if err := rows.Err(); err != nil {
		return res, err
	}
	res.Average = make([]*float64, r.Periods+1)
	for i := range res.Average {
		var came, size int
		for _, co := range res.Cohorts {
			if co.Counts[i] != nil && co.Size > 0 {
				came += *co.Counts[i]
				size += co.Size
			}
		}
		if size > 0 {
			v := float64(came) / float64(size)
			res.Average[i] = &v
		}
	}
	return res, nil
}
