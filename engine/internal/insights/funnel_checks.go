package insights

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// Funnel checks look at the data behind each step for signs that a
// drop-off is a tracking problem, not people leaving. A funnel across
// several systems (a website, a download server, an installer, an app)
// only works when every system reports under an id that leads back to the
// same person and keeps reporting; when one doesn't, the chart shows a
// drop that never happened. The checks say so on the step.

// Check kinds.
const (
	// CheckNeverReceived: nothing matching the step has arrived.
	CheckNeverReceived = "never_received"
	// CheckStopped: the step's data used to arrive and has stopped.
	CheckStopped = "stopped"
	// CheckUnlinked: many people did the step with nothing on record
	// before it, so it is probably sent under a different id.
	CheckUnlinked = "unlinked"
)

const (
	// checkLookback is how far before the funnel's dates a step's data is
	// looked at, to know whether it ever arrived and how often.
	checkLookback = 90 * 24 * time.Hour
	// A step is quiet only after this long without data.
	checkQuietAfter = 36 * time.Hour
	// A quiet step has stopped when, at its usual rate, at least this many
	// would have arrived by now.
	checkMissedAtLeast = 10
	// The same stretch of each of the two weeks before must have had at
	// least this many, so a step that is always quiet at weekends isn't
	// called stopped every Monday.
	checkWeekBeforeAtLeast = 3
	// checkSample is how many of a step's people are looked up for
	// earlier activity; the count is scaled up from them.
	checkSample = 5000
	// A step is unlinked when at least this many of its people, and this
	// share of them, have nothing on record before it.
	checkUnlinkedPeople = 20
	checkUnlinkedShare  = 0.15
)

// Check is something in a step's data that makes its numbers doubtful.
type Check struct {
	// Step is counted from 1.
	Step int    `json:"step"`
	Kind string `json:"kind"`
	// LastReceivedAt and DailyBefore say when a stopped step's data last
	// arrived and about how many a day came before.
	LastReceivedAt *time.Time `json:"last_received_at,omitempty"`
	DailyBefore    int        `json:"daily_before,omitempty"`
	// People is how many did an unlinked step in the dates with nothing
	// on record before it.
	People int `json:"people,omitempty"`
}

// StepData is one step looked at on its own, apart from the funnel: who
// did it at all, how it follows the step before when only those two are
// compared, and how its data has been arriving. Set beside the funnel's
// own counts, it shows whether a drop is people leaving or a link the
// tracking lost.
type StepData struct {
	// Step is counted from 1.
	Step int `json:"step"`
	// People is everyone who did the step in the dates, whether or not
	// they did the steps before it.
	People int `json:"people"`
	// LastReceivedAt is when the step's data last arrived, nil when it
	// never has; PerDay is about how many arrived a day in the two weeks
	// up to then.
	LastReceivedAt *time.Time `json:"last_received_at"`
	PerDay         int        `json:"per_day"`
	// FromPrevious compares only this step and the one before it.
	FromPrevious *StepPair `json:"from_previous,omitempty"`
	// NothingBefore is how many of People have no event, page view or
	// visit on record before this step. Not for step 1 or companies.
	NothingBefore *int `json:"nothing_before,omitempty"`
}

// StepPair is a two-step funnel of a step and the one before it: Started
// did the step before in the dates, Reached then did this one in time.
type StepPair struct {
	Started int `json:"started"`
	Reached int `json:"reached"`
}

// ChecksResult is every step on its own and what the checks found.
type ChecksResult struct {
	Steps []StepData `json:"steps"`
	// Data is in step order, and empty when nothing was found.
	Data []Check `json:"data"`
}

// Checks looks at each step of a funnel in order on its own and runs the
// data checks, as of now.
func Checks(ctx context.Context, q Querier, ws, brand string, f Funnel, now time.Time) (ChecksResult, error) {
	res := ChecksResult{Steps: []StepData{}, Data: []Check{}}
	data, checks, err := runChecks(ctx, q, ws, brand, f, now)
	if err != nil {
		return res, err
	}
	sortChecks(checks)
	res.Steps, res.Data = data, checks
	return res, nil
}

func runChecks(ctx context.Context, q Querier, ws, brand string, f Funnel, now time.Time) ([]StepData, []Check, error) {
	if err := f.Validate(); err != nil {
		return nil, nil, err
	}
	if f.Order == OrderAny {
		return nil, nil, Invalid("the data checks are for funnels in order")
	}
	c := &Compiled{ws: ws, brand: brand, f: f}
	a := &c.args
	if err := a.leaveOutInternal(ctx, q, ws, brand); err != nil {
		return nil, nil, err
	}
	pWS, pBrand := a.add(ws), a.add(brand)
	overall := DefaultWindow
	if f.Window != nil {
		overall = f.Window.duration()
	}
	end := f.To.Add(overall)
	if now.Before(end) {
		end = now.Add(time.Minute)
	}
	pFrom, pTo, pBack, pEnd, pNow := a.add(f.From), a.add(f.To), a.add(f.From.Add(-checkLookback)), a.add(end), a.add(now)
	pOverall := a.add(fmt.Sprintf("%d seconds", int64(overall.Seconds())))

	var ctes, union []string
	for i, s := range f.Steps {
		conv, err := loadConversion(ctx, q, ws, brand, s.Kind, s.Conversion, fmt.Sprintf("steps[%d]", i))
		if err != nil {
			return nil, nil, err
		}
		src, err := source(s.Kind, s.Event, conv, s.Filters, s.First, pBack, pEnd, pWS, pBrand, a)
		if err != nil {
			return nil, nil, err
		}
		ctes = append(ctes, fmt.Sprintf("s%d AS (%s)", i+1, c.rows(src, `''`, pWS)))
		union = append(union, fmt.Sprintf(`SELECT %d AS step, ts, actor FROM s%d`, i+1, i+1))
	}
	// The three queries below share one argument list, and each must be
	// able to tell every argument's type, used by it or not.
	ctes = append(ctes, `args AS (SELECT `+pFrom+`::timestamptz, `+pTo+`::timestamptz, `+pNow+`::timestamptz, `+pOverall+`::interval)`)
	with := `WITH ` + strings.Join(ctes, ",\n")

	// How each step's data has been arriving: its first and last row, how
	// many in the two weeks up to the last, and how many in the same
	// stretch (from the last row's time of week to now's) of each of the
	// two weeks before.
	rows, err := q.Query(ctx, with+`,
		u AS (`+strings.Join(union, " UNION ALL ")+`),
		l AS (SELECT step, min(ts) AS first, max(ts) AS last FROM u GROUP BY step)
		SELECT l.step, l.first, l.last,
			count(*) FILTER (WHERE u.ts > l.last - interval '14 days')::int,
			count(*) FILTER (WHERE u.ts > l.last - interval '7 days' AND u.ts <= `+pNow+`::timestamptz - interval '7 days')::int,
			count(*) FILTER (WHERE u.ts > l.last - interval '14 days' AND u.ts <= `+pNow+`::timestamptz - interval '14 days')::int,
			count(DISTINCT u.actor) FILTER (WHERE u.ts >= `+pFrom+` AND (l.step > 1 OR u.ts < `+pTo+`))::int
		FROM l JOIN u ON u.step = l.step
		GROUP BY l.step, l.first, l.last`, a.list...)
	if err != nil {
		return nil, nil, err
	}
	type arrival struct {
		first, last                time.Time
		recent, week1, wk2, people int
	}
	seen := map[int]arrival{}
	for rows.Next() {
		var step int
		var x arrival
		if err := rows.Scan(&step, &x.first, &x.last, &x.recent, &x.week1, &x.wk2, &x.people); err != nil {
			rows.Close()
			return nil, nil, err
		}
		seen[step] = x
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	// A funnel over dates that ended a while ago says nothing about what
	// is arriving now.
	current := !f.To.Before(now.Add(-24 * time.Hour))
	out := []Check{}
	data := make([]StepData, len(f.Steps))
	for i := range f.Steps {
		n := i + 1
		data[i] = StepData{Step: n}
		x, ok := seen[n]
		if ok {
			last := x.last
			data[i].People, data[i].LastReceivedAt, data[i].PerDay = x.people, &last, int(math.Round(float64(x.recent)/14))
		}
		if !ok {
			out = append(out, Check{Step: n, Kind: CheckNeverReceived})
			continue
		}
		if stopped(x.first, x.last, x.recent, x.week1, x.wk2, now) && current {
			last := x.last
			out = append(out, Check{Step: n, Kind: CheckStopped, LastReceivedAt: &last, DailyBefore: int(math.Round(float64(x.recent) / 14))})
		}
	}

	// Each step against the one before it, as a funnel of just those two:
	// everyone who did the step before in the dates, and how many of them
	// then did this one in time.
	var pairs []string
	for i := 1; i < len(f.Steps); i++ {
		n := i + 1
		after := `>=`
		if sameStep(f.Steps[i-1], f.Steps[i]) {
			after = `>`
		}
		pairs = append(pairs, fmt.Sprintf(`SELECT %d AS step, count(*)::int, count(z.hit)::int FROM
			(SELECT d.actor, max(1) FILTER (WHERE x.ts %s d.t AND x.ts <= d.t + %s::interval) AS hit
			FROM (SELECT actor, min(ts) AS t FROM s%d WHERE ts >= %s AND ts < %s GROUP BY actor) d
			LEFT JOIN s%d x ON x.actor = d.actor GROUP BY d.actor) z`,
			n, after, pOverall, i, pFrom, pTo, n))
	}
	rows, err = q.Query(ctx, with+"\n"+strings.Join(pairs, "\nUNION ALL\n"), a.list...)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var step int
		var p StepPair
		if err := rows.Scan(&step, &p.Started, &p.Reached); err != nil {
			rows.Close()
			return nil, nil, err
		}
		data[step-1].FromPrevious = &p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	// Who did each later step with nothing on record before it: no
	// event, page view or visit under their id. A company funnel counts
	// companies, which have no id of their own to lose.
	if f.By == ByCompany {
		return data, out, nil
	}
	var parts []string
	for i := 1; i < len(f.Steps); i++ {
		n := i + 1
		if _, ok := seen[n]; !ok {
			continue
		}
		parts = append(parts, fmt.Sprintf(`SELECT %d AS step, count(*)::int AS sampled, count(*) FILTER (WHERE %s)::int AS fresh
			FROM (SELECT actor, min(ts) AS t FROM s%d WHERE ts >= %s GROUP BY actor LIMIT %d) d`, n, nothingBefore(pWS, pBrand), n, pFrom, checkSample))
	}
	if len(parts) == 0 {
		return data, out, nil
	}
	rows, err = q.Query(ctx, with+"\n"+strings.Join(parts, "\nUNION ALL\n"), a.list...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var step, sampled, fresh int
		if err := rows.Scan(&step, &sampled, &fresh); err != nil {
			return nil, nil, err
		}
		if sampled == 0 {
			continue
		}
		share := float64(fresh) / float64(sampled)
		estimate := int(math.Round(share * float64(data[step-1].People)))
		data[step-1].NothingBefore = &estimate
		if estimate >= checkUnlinkedPeople && share >= checkUnlinkedShare {
			out = append(out, Check{Step: step, Kind: CheckUnlinked, People: estimate})
		}
	}
	return data, out, rows.Err()
}

// stopped says whether a step whose data last arrived at last has gone
// quiet for longer than its own history explains.
func stopped(first, last time.Time, recent, week1, week2 int, now time.Time) bool {
	gap := now.Sub(last)
	if gap < checkQuietAfter {
		return false
	}
	// At the rate of the two weeks up to the last row, how many would
	// have arrived since.
	missed := float64(recent) / 14 * gap.Hours() / 24
	if missed < checkMissedAtLeast {
		return false
	}
	// With enough history, the same stretch of both weeks before must
	// have had data too: otherwise this is the step's usual quiet time.
	if !first.After(last.Add(-14 * 24 * time.Hour)) {
		return week1 >= checkWeekBeforeAtLeast && week2 >= checkWeekBeforeAtLeast
	}
	return true
}

// nothingBefore is true for the person d (actor, t) with no event, page
// view or visit on record before t. An anonymous browser and a record are
// looked up apart, so each uses its own index.
func nothingBefore(pWS, pBrand string) string {
	anon := `substr(d.actor, 3)`
	record := `(CASE WHEN d.actor LIKE 'a:%' THEN NULL ELSE d.actor END)::uuid`
	return `NOT EXISTS (SELECT 1 FROM customer_events e WHERE d.actor LIKE 'a:%' AND e.workspace_id = ` + pWS + ` AND e.brand_id = ` + pBrand + `
			AND e.record_id IS NULL AND e.anonymous_id = ` + anon + ` AND e.occurred_at < d.t)
		AND NOT EXISTS (SELECT 1 FROM customer_events e WHERE e.record_id = ` + record + ` AND e.brand_id = ` + pBrand + ` AND e.occurred_at < d.t)
		AND NOT EXISTS (SELECT 1 FROM site_sessions s WHERE d.actor LIKE 'a:%' AND s.workspace_id = ` + pWS + ` AND s.brand_id = ` + pBrand + `
			AND s.anonymous_id = ` + anon + ` AND s.started_at < d.t)
		AND NOT EXISTS (SELECT 1 FROM site_sessions s WHERE s.workspace_id = ` + pWS + ` AND s.brand_id = ` + pBrand + `
			AND s.record_id = ` + record + ` AND s.started_at < d.t)`
}

// sortChecks puts checks in step order, a step's own in a fixed order.
func sortChecks(list []Check) {
	rank := map[string]int{CheckNeverReceived: 0, CheckStopped: 1, CheckUnlinked: 2}
	for i := 1; i < len(list); i++ {
		for j := i; j > 0; j-- {
			x, y := list[j-1], list[j]
			if x.Step < y.Step || (x.Step == y.Step && rank[x.Kind] <= rank[y.Kind]) {
				break
			}
			list[j-1], list[j] = y, x
		}
	}
}
