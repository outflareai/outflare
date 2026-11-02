package api

import (
	"context"
	"math"
	"net/http"
	"sort"
	"strconv"

	"github.com/outflareai/outflare/engine/internal/journeys"
)

// Incrementality: what a journey changed. A random split with a holdout
// path keeps some people out on purpose, so comparing what each path's
// people did afterwards against the holdout's says what the journey added
// beyond what would have happened anyway.

// liftPath is one path of the split.
type liftPath struct {
	ID      string `json:"id"`
	Holdout bool   `json:"holdout"`
	// People who went this way.
	People int `json:"people"`
	// Of them, people with a conversion within the window after the split,
	// and that as a share.
	Converted int     `json:"converted"`
	Rate      float64 `json:"rate"`
	// Revenue within the window, net of refunds, per currency, in total and
	// per person.
	Revenue          []money `json:"revenue"`
	RevenuePerPerson []money `json:"revenue_per_person"`
	// Against the control: the relative change in rate, the chance that
	// difference is noise, and the verdict. Null on the control itself.
	Lift    *float64 `json:"lift"`
	PValue  *float64 `json:"p_value"`
	Verdict *string  `json:"verdict"`
	// What the path's people paid beyond what the same number of control
	// people paid, per currency. Null on the control.
	IncrementalRevenue []money `json:"incremental_revenue"`
	revenue            map[string]float64
}

// liftMinPeople is the fewest people in each group before a verdict.
const liftMinPeople = 30

// liftQuery is what a lift report reads: the days and event from the
// query string.
func liftQuery(w http.ResponseWriter, r *http.Request) (days int, event string, ok bool) {
	q := r.URL.Query()
	days, event = 30, q.Get("event")
	if v := q.Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 180 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "days must be 1 to 180")
			return 0, "", false
		}
		days = n
	}
	if len(event) > 200 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "event is at most 200 characters")
		return 0, "", false
	}
	return days, event, true
}

// liftPaths compares each group in taken with the control. taken is a
// SELECT of (run_id text, outcome text, at timestamptz, record_id uuid),
// one row per person per exposure, whose $1 is the workspace; paths are
// the groups in order, with whether each is a holdout. A conversion counts
// for a row when it comes after at and within days.
func (s *Server) liftPaths(ctx context.Context, taken string, args []any, days int, event string,
	paths []journeys.Path, control string) ([]*liftPath, error) {
	n := len(args)
	args = append(args, days, event)
	cte := `WITH taken AS (` + taken + `),
		won AS (
			SELECT t.run_id, t.outcome, c.currency, c.value_minor, c.name
			FROM taken t JOIN attribution_conversions c ON c.workspace_id = $1 AND c.record_id = t.record_id
				AND c.occurred_at > t.at AND c.occurred_at <= t.at + make_interval(days => $` + strconv.Itoa(n+1) + `)
				AND ($` + strconv.Itoa(n+2) + ` = '' OR c.name = $` + strconv.Itoa(n+2) + ` OR c.name = 'payment.refunded'))`
	byID := map[string]*liftPath{}
	path := func(id string) *liftPath {
		if byID[id] == nil {
			byID[id] = &liftPath{ID: id, revenue: map[string]float64{}}
		}
		return byID[id]
	}
	for _, p := range paths {
		path(p.ID).Holdout = p.Holdout
	}
	rows, err := s.pool.Query(ctx, cte+`
		SELECT t.outcome, count(*)::int,
			count(*) FILTER (WHERE EXISTS (SELECT 1 FROM won x WHERE x.run_id = t.run_id AND x.name <> 'payment.refunded'))::int
		FROM taken t GROUP BY t.outcome`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var people, converted int
		if err := rows.Scan(&id, &people, &converted); err != nil {
			rows.Close()
			return nil, err
		}
		x := path(id)
		x.People, x.Converted = people, converted
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.pool.Query(ctx, cte+`
		SELECT outcome, currency, sum(value_minor)::float8 FROM won
		WHERE value_minor IS NOT NULL AND currency <> '' GROUP BY outcome, currency`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, cur string
		var v float64
		if err := rows.Scan(&id, &cur, &v); err != nil {
			rows.Close()
			return nil, err
		}
		path(id).revenue[cur] += v
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	perPerson := func(x *liftPath) map[string]float64 {
		out := map[string]float64{}
		for cur, v := range x.revenue {
			if x.People > 0 {
				out[cur] = v / float64(x.People)
			}
		}
		return out
	}
	c := path(control)
	cPer := perPerson(c)
	out := make([]*liftPath, 0, len(byID))
	for _, x := range byID {
		if x.People > 0 {
			x.Rate = round4(float64(x.Converted) / float64(x.People))
		}
		per := perPerson(x)
		x.Revenue, x.RevenuePerPerson = moneyList(x.revenue), moneyList(per)
		if x.ID != control {
			x.Lift, x.PValue, x.Verdict = liftVerdict(x.Converted, x.People, c.Converted, c.People)
			inc := map[string]float64{}
			for _, cur := range currencyKeys(per, cPer) {
				inc[cur] = (per[cur] - cPer[cur]) * float64(x.People)
			}
			x.IncrementalRevenue = moneyList(inc)
		}
		out = append(out, x)
	}
	order := map[string]int{}
	for i, p := range paths {
		order[p.ID] = i
	}
	sort.Slice(out, func(i, k int) bool {
		oi, iok := order[out[i].ID]
		ok2, kok := order[out[k].ID]
		if iok != kok {
			return iok
		}
		if oi != ok2 {
			return oi < ok2
		}
		return out[i].ID < out[k].ID
	})
	return out, nil
}

// journeyLift compares a random split's paths with its control.
func (s *Server) journeyLift(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	j, ok := s.findJourney(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	q := r.URL.Query()
	stepID, control := q.Get("step"), q.Get("control")
	var split *journeys.Step
	for i := range j.Steps {
		if j.Steps[i].ID == stepID {
			split = &j.Steps[i]
		}
	}
	if split == nil || split.Kind != journeys.KindSplit || split.SplitBy != "random" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "step is the id of one of the journey's random split steps")
		return
	}
	if control == "" {
		for _, p := range split.Paths {
			if p.Holdout {
				control = p.ID
			}
		}
	}
	known := false
	for _, p := range split.Paths {
		known = known || p.ID == control
	}
	if !known {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "control is one of the step's paths; it defaults to its holdout path, and this step has none")
		return
	}
	days, event, ok := liftQuery(w, r)
	if !ok {
		return
	}
	// Each run counts once, on the first time it took the step; a person
	// in the journey more than once counts each time.
	taken := `SELECT DISTINCT ON (rs.run_id) rs.run_id::text AS run_id, rs.outcome, rs.at, jr.record_id
		FROM journey_run_steps rs JOIN journey_runs jr ON jr.id = rs.run_id
		WHERE rs.workspace_id = $1 AND rs.journey_id = $2 AND rs.step = $3 AND rs.kind = 'split'
		ORDER BY rs.run_id, rs.at, rs.id`
	out, err := s.liftPaths(r.Context(), taken, []any{a.WorkspaceID, j.ID, stepID}, days, event, split.Paths, control)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"step": stepID, "control": control, "event": event, "days": days, "paths": out})
}

// sendGroups are a broadcast's or sequence's two sides.
var sendGroups = []journeys.Path{{ID: "sent"}, {ID: "held_out", Holdout: true}}

// emailBroadcastLift compares the people a broadcast was sent to with the
// people it held out, from when each was reached.
func (s *Server) emailBroadcastLift(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	b, ok := s.findEmailBroadcast(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	days, event, ok := liftQuery(w, r)
	if !ok {
		return
	}
	taken := `SELECT x.record_id::text AS run_id, CASE WHEN x.skipped = 'holdout' THEN 'held_out' ELSE 'sent' END AS outcome,
			x.created_at AS at, x.record_id
		FROM email_broadcast_recipients x
		WHERE x.workspace_id = $1 AND x.broadcast_id = $2 AND (x.skipped = 'holdout' OR x.email_id IS NOT NULL)`
	out, err := s.liftPaths(r.Context(), taken, []any{a.WorkspaceID, b.ID}, days, event, sendGroups, "held_out")
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"holdout_percent": b.HoldoutPercent, "control": "held_out", "event": event, "days": days, "paths": out})
}

// sequenceLift compares the leads a sequence emailed with the ones it held
// out, from when each was enrolled. Leads without a customer record can't
// be followed, so they are left out.
func (s *Server) sequenceLift(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q, ok := s.findSequence(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	days, event, ok := liftQuery(w, r)
	if !ok {
		return
	}
	taken := `SELECT en.id::text AS run_id,
			CASE WHEN en.status = 'stopped' AND en.status_reason = 'holdout' THEN 'held_out' ELSE 'sent' END AS outcome,
			en.created_at AS at, l.record_id
		FROM enrollments en JOIN leads l ON l.id = en.lead_id
		WHERE en.workspace_id = $1 AND en.sequence_id = $2 AND l.record_id IS NOT NULL
		  AND ((en.status = 'stopped' AND en.status_reason = 'holdout')
		       OR EXISTS (SELECT 1 FROM messages m WHERE m.enrollment_id = en.id AND m.status = 'sent'))`
	out, err := s.liftPaths(r.Context(), taken, []any{a.WorkspaceID, q.ID}, days, event, sendGroups, "held_out")
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"holdout_percent": q.HoldoutPercent, "control": "held_out", "event": event, "days": days, "paths": out})
}

// liftVerdict compares a path's conversion rate with the control's: the
// relative change, a two-sided two-proportion z-test, and better, worse,
// no_clear_change or too_early (fewer than liftMinPeople in either group,
// or no conversions at all).
func liftVerdict(conv, n, cConv, cN int) (*float64, *float64, *string) {
	verdict := "too_early"
	if n < liftMinPeople || cN < liftMinPeople || conv+cConv == 0 {
		var lift *float64
		if cN > 0 && cConv > 0 && n > 0 {
			l := round4((float64(conv)/float64(n))/(float64(cConv)/float64(cN)) - 1)
			lift = &l
		}
		return lift, nil, &verdict
	}
	p1, p2 := float64(conv)/float64(n), float64(cConv)/float64(cN)
	var lift *float64
	if cConv > 0 {
		l := round4(p1/p2 - 1)
		lift = &l
	}
	pool := float64(conv+cConv) / float64(n+cN)
	se := math.Sqrt(pool * (1 - pool) * (1/float64(n) + 1/float64(cN)))
	pv := 1.0
	if se > 0 {
		pv = math.Erfc(math.Abs(p1-p2) / se / math.Sqrt2)
	}
	pv = round4(pv)
	switch {
	case pv < 0.05 && p1 > p2:
		verdict = "better"
	case pv < 0.05:
		verdict = "worse"
	default:
		verdict = "no_clear_change"
	}
	return lift, &pv, &verdict
}

func round4(v float64) float64 { return math.Round(v*10000) / 10000 }

// currencyKeys are the currencies in either map.
func currencyKeys(a, b map[string]float64) []string {
	out := []string{}
	for k := range a {
		out = append(out, k)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
