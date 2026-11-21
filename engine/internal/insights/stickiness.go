package insights

import (
	"context"
	"slices"
	"time"
)

// Stickiness: of the people who did something in the dates, how many
// did it on one day, on two days, on three and so on (or in one week,
// two weeks...). A product people come back to has a long tail to the
// right; one they try once has nearly everyone at 1.

// MaxStickinessBuckets is how many counts are told apart; people with
// more periods than this share the last bucket.
const MaxStickinessBuckets = 31

// Stickiness is one stickiness question.
type Stickiness struct {
	// Step is what counts as being active, as in a lifecycle question.
	Step     Step `json:"step"`
	From, To time.Time
	// Interval is what is counted: the days, weeks or months a person
	// was active in.
	Interval string `json:"interval"`
	// Location is the time zone periods are in: the brand's reporting
	// time zone (B4a), UTC when nil.
	Location *time.Location `json:"-"`
}

// Validate checks the question before it is compiled.
func (s Stickiness) Validate() error {
	if err := validRetentionStep(s.Step, "step"); err != nil {
		return err
	}
	if s.From.IsZero() || s.To.IsZero() || !s.To.After(s.From) || s.To.Sub(s.From) > MaxRange {
		return Invalid("from and to are dates, from before to, at most 366 days apart")
	}
	if !slices.Contains(Intervals, s.Interval) {
		return Invalid("interval is day, week or month")
	}
	return nil
}

// StickinessBucket is the people active in exactly Periods periods.
type StickinessBucket struct {
	Periods int `json:"periods"`
	People  int `json:"people"`
	// Share is People of everyone active in the dates, 0 to 1.
	Share float64 `json:"share"`
	// OrMore marks the last bucket when it also holds people with more
	// periods than it says.
	OrMore bool `json:"or_more,omitempty"`
}

// StickinessResult is every bucket from 1 up to the most periods anyone
// had (or the number of periods in the dates), in order.
type StickinessResult struct {
	Label    string             `json:"label"`
	Interval string             `json:"interval"`
	People   int                `json:"people"`
	Buckets  []StickinessBucket `json:"buckets"`
	// AveragePeriods is how many periods an active person had on average.
	AveragePeriods *float64  `json:"average_periods"`
	From           time.Time `json:"from"`
	To             time.Time `json:"to"`
}

// CompiledStickiness is a stickiness question as one query.
type CompiledStickiness struct {
	s     Stickiness
	label string
	sql   string
	args  args
}

// CompileStickiness turns the question into SQL: each person's number
// of distinct active periods, then how many people have each number.
func CompileStickiness(ctx context.Context, q Querier, ws, brand string, s Stickiness) (*CompiledStickiness, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	c := &CompiledStickiness{s: s}
	a := &c.args
	if err := a.leaveOutInternal(ctx, q, ws, brand); err != nil {
		return nil, err
	}
	pWS, pBrand := a.add(ws), a.add(brand)
	conv, err := loadConversion(ctx, q, ws, brand, s.Step.Kind, s.Step.Conversion, "step")
	if err != nil {
		return nil, err
	}
	c.label = s.Step.DefaultLabel(conv.Name)
	pFrom, pTo := a.add(s.From), a.add(s.To)
	src, err := source(s.Step.Kind, s.Step.Event, conv, s.Step.Filters, s.Step.First, pFrom, pTo, pWS, pBrand, a)
	if err != nil {
		return nil, err
	}
	pInterval, pZone := a.add(s.Interval), a.add(zoneName(s.Location))
	c.sql = `WITH act AS (
		SELECT ` + src.actor + ` AS actor, count(DISTINCT date_trunc(` + pInterval + `, ` + src.ts + ` AT TIME ZONE ` + pZone + `))::int AS n
		FROM ` + src.from + ` WHERE ` + joinAnd(src.where) + ` GROUP BY 1
	)
	SELECT n, count(*)::int FROM act GROUP BY n`
	return c, nil
}

// Run answers the question.
func (c *CompiledStickiness) Run(ctx context.Context, q Querier) (StickinessResult, error) {
	s := c.s
	res := StickinessResult{Label: c.label, Interval: s.Interval, From: s.From, To: s.To, Buckets: []StickinessBucket{}}
	rows, err := q.Query(ctx, c.sql, c.args.list...)
	if err != nil {
		return res, err
	}
	defer rows.Close()
	counts := map[int]int{}
	most, sum := 0, 0
	for rows.Next() {
		var n, people int
		if err := rows.Scan(&n, &people); err != nil {
			return res, err
		}
		res.People += people
		sum += n * people
		most = max(most, n)
		counts[min(n, MaxStickinessBuckets)] += people
	}
	if err := rows.Err(); err != nil {
		return res, err
	}
	// Show at least a week of buckets (or every period of the dates when
	// there are fewer), so an empty tail reads as empty.
	last := min(MaxStickinessBuckets, max(most, min(7, len(periods(s.From, s.To, s.Interval, s.Location)))))
	for n := 1; n <= last; n++ {
		b := StickinessBucket{Periods: n, People: counts[n], OrMore: n == MaxStickinessBuckets && most > MaxStickinessBuckets}
		if res.People > 0 {
			b.Share = float64(b.People) / float64(res.People)
		}
		res.Buckets = append(res.Buckets, b)
	}
	if res.People > 0 {
		avg := float64(sum) / float64(res.People)
		res.AveragePeriods = &avg
	}
	return res, nil
}
