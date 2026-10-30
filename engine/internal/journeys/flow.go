package journeys

import (
	"context"
	"errors"
	"hash/fnv"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// Flow control: wait_until waits for one of its paths (an event, or
// conditions coming true) or its timeout; split sends people down one of
// its paths by value or at random. Each path's id is the step's outcome,
// so a journey's step results count people per path.

// recheck is how often a wait_until step with condition paths looks
// again, besides whenever one of the person's fields changes.
const recheck = time.Hour

// waitUntil takes a wait_until step: it goes down the first condition
// path that holds, or the timeout once it has passed, or else keeps the
// run waiting and reports done false.
func (s *Service) waitUntil(ctx context.Context, tx pgx.Tx, ws string, r run, st *Step) (next, outcome string, done bool, err error) {
	now := time.Now()
	first := r.waitDeadline == nil
	var conds bool
	var events []string
	for _, p := range st.Paths {
		if p.Event != "" {
			events = append(events, p.Event)
			continue
		}
		conds = true
	}
	if conds {
		attrs, err := attributes(ctx, tx, ws, r.recordID)
		if err != nil {
			return "", "", false, err
		}
		for _, p := range st.Paths {
			if p.Event != "" {
				continue
			}
			ok, err := holds(ctx, tx, subject{ws: ws, runID: r.id, recordID: r.recordID, attributes: attrs, context: r.context, vars: r.vars, entered: r.entered}, p.Conditions, p.Match)
			if err != nil || ok {
				return p.Next, p.ID, ok, err
			}
		}
	}
	deadline := now.Add(time.Duration(st.TimeoutSeconds) * time.Second)
	if !first {
		deadline = *r.waitDeadline
		if !now.Before(deadline) {
			return st.TimeoutNext, "timed_out", true, nil
		}
	}
	at := deadline
	if conds && now.Add(recheck).Before(deadline) {
		at = now.Add(recheck)
	}
	if _, err := tx.Exec(ctx, `UPDATE journey_runs SET next_at = $2, wait_deadline = $3, wait_events = $4, wait_fields = $5, updated_at = now() WHERE id = $1`,
		r.id, at, deadline, events, conds); err != nil {
		return "", "", false, err
	}
	if first {
		if err := logStep(ctx, tx, ws, r, st, "waiting", nil, "until "+deadline.UTC().Format(time.RFC3339)); err != nil {
			return "", "", false, err
		}
	}
	_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobRun, WorkspaceID: &ws, Payload: map[string]string{"run_id": r.id}, RunAt: at,
		IdempotencyKey: "journey-run:" + r.id + ":wait:" + strconv.Itoa(r.taken) + ":" + strconv.FormatInt(at.UnixNano(), 36)})
	return "", "", false, err
}

// tryWaitUntil takes a wait_until step in a test, which can't wait: it goes
// down the path the test names, else the first condition path that holds
// now, else the timeout.
func (s *Service) tryWaitUntil(ctx context.Context, tx pgx.Tx, ws string, r run, st *Step, t *trial) (taken, error) {
	if id, set := t.paths[st.ID]; set {
		if id == "timed_out" {
			return taken{next: st.TimeoutNext, outcome: "timed_out"}, nil
		}
		for _, p := range st.Paths {
			if p.ID == id {
				return taken{next: p.Next, outcome: p.ID}, nil
			}
		}
	}
	attrs, err := attributes(ctx, tx, ws, r.recordID)
	if err != nil {
		return taken{}, err
	}
	for _, p := range st.Paths {
		if p.Event != "" {
			continue
		}
		ok, err := holds(ctx, tx, subject{ws: ws, runID: r.id, recordID: r.recordID, attributes: attrs, context: r.context, vars: r.vars, entered: r.entered}, p.Conditions, p.Match)
		if err != nil || ok {
			return taken{next: p.Next, outcome: p.ID}, err
		}
	}
	return taken{next: st.TimeoutNext, outcome: "timed_out", detail: "a test doesn't wait"}, nil
}

// waitEvent hands an event to a run waiting for it on a wait_until step:
// the first event path it fits moves the run on.
func (s *Service) waitEvent(ctx context.Context, tx pgx.Tx, ws, runID, eventID, name string, props map[string]any) error {
	r, found, err := loadRun(ctx, tx, ws, runID)
	if err != nil || !found || r.step == nil || r.waitDeadline == nil {
		return err
	}
	j, err := loadJourney(ctx, tx, ws, r.journeyID, false)
	if err != nil || j == nil || j.status != "active" {
		return err // a paused journey's runs don't move
	}
	if err := j.pin(ctx, tx, r.version); err != nil {
		return err
	}
	st := j.step(*r.step)
	if st == nil || st.Kind != KindWaitUntil {
		return nil
	}
	attrs, err := attributes(ctx, tx, ws, r.recordID)
	if err != nil {
		return err
	}
	for _, p := range st.Paths {
		if p.Event != name {
			continue
		}
		// The path's conditions test the event it waited for.
		ok, err := holds(ctx, tx, subject{ws: ws, runID: r.id, recordID: r.recordID, attributes: attrs, context: props, vars: r.vars, entered: r.entered}, p.Conditions, p.Match)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		more, err := s.moveOn(ctx, tx, ws, r, st, taken{next: p.Next, outcome: p.ID, detail: "did " + name})
		if err != nil || !more {
			return err
		}
		_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobRun, WorkspaceID: &ws, Payload: map[string]string{"run_id": r.id},
			IdempotencyKey: "journey-run:" + r.id + ":event:" + eventID})
		return err
	}
	return nil
}

// split picks a split step's path: the first whose conditions hold, else
// else (by value), or one by weight (at random, the same each time for a
// person, or for everyone at their company).
func (s *Service) split(ctx context.Context, tx pgx.Tx, ws string, j *journey, r run, st *Step) (Path, bool, error) {
	if st.SplitBy == "random" {
		key := r.recordID
		if st.SplitKey == "company" {
			var company *string
			if err := tx.QueryRow(ctx, `SELECT to_id::text FROM record_relationships WHERE workspace_id = $1 AND from_id = $2 AND kind = 'companies'
				AND valid_to IS NULL ORDER BY valid_from, to_id LIMIT 1`, ws, r.recordID).Scan(&company); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return Path{}, false, err
			}
			if company != nil {
				key = *company
			}
		}
		total := 0
		for _, p := range st.Paths {
			total += p.Weight
		}
		h := fnv.New64a()
		h.Write([]byte(j.id + ":" + st.ID + ":" + key)) //nolint:errcheck
		pick := int(h.Sum64() % uint64(total))
		for _, p := range st.Paths {
			if pick < p.Weight {
				return p, true, nil
			}
			pick -= p.Weight
		}
	}
	attrs, err := attributes(ctx, tx, ws, r.recordID)
	if err != nil {
		return Path{}, false, err
	}
	for _, p := range st.Paths {
		ok, err := holds(ctx, tx, subject{ws: ws, runID: r.id, recordID: r.recordID, attributes: attrs, context: r.context, vars: r.vars, entered: r.entered}, p.Conditions, p.Match)
		if err != nil || ok {
			return p, ok, err
		}
	}
	return Path{}, false, nil
}

// entryRefused says why an automatic entry is turned away by the
// journey's limits, or "": the most people it takes an hour or a day, and
// how long after someone's last run ends they can start again.
func entryRefused(ctx context.Context, tx pgx.Tx, j *journey, recordID string) (string, error) {
	if j.reentryAfter != nil {
		var recent bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM journey_runs WHERE journey_id = $1 AND record_id = $2
			AND finished_at > now() - make_interval(secs => $3))`, j.id, recordID, *j.reentryAfter).Scan(&recent); err != nil {
			return "", err
		}
		if recent {
			metrics.JourneyEntryRefused("reentry")
			return "their last run ended too recently to start again", nil
		}
	}
	if j.entryLimit != nil {
		window := time.Hour
		if j.entryLimitPer != nil && *j.entryLimitPer == "day" {
			window = 24 * time.Hour
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM journey_runs WHERE journey_id = $1 AND entered_at > $2`,
			j.id, time.Now().Add(-window)).Scan(&n); err != nil {
			return "", err
		}
		if n >= *j.entryLimit {
			metrics.JourneyEntryRefused("limit")
			return "the journey's entry limit is reached", nil
		}
	}
	return "", nil
}
