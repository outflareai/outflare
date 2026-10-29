package journeys

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/track"
)

// A test run takes one person through a journey to show the path they
// would take and what each step would do. It runs the same step code as a
// real run, in a transaction that is always rolled back with its events
// muted, so nothing is sent, written or recorded: a wait passes at once,
// an email is composed but not queued, and an action's changes vanish
// with the transaction. That holds because steps only ever touch the
// database in their run's transaction; anything outside it (sending,
// calling a provider) is a job, and the job is rolled back too.

// TestInput is who to test with, and how.
type TestInput struct {
	RecordID string
	// Draft tests the journey's draft version instead of its live one.
	Draft bool
	// Event is the properties of the event that starts the run. Nil uses
	// the person's latest trigger event, if they have one.
	Event map[string]any
	// Paths sends branch steps (by id) down yes or no whatever the data
	// says, to see the other way.
	Paths map[string]string
}

// TestStep is one step of a test run.
type TestStep struct {
	Step    string `json:"step"`
	Kind    string `json:"kind"`
	Outcome string `json:"outcome"`
	Detail  string `json:"detail,omitempty"`
	Next    string `json:"next,omitempty"`
	// Subject is an email step's subject, as the person would get it.
	Subject string `json:"subject,omitempty"`
	// Data is the way a branch's data points (yes or no); it differs from
	// outcome when the test chose the other path.
	Data string `json:"data,omitempty"`
	// CanChange is true for a branch that tests something the person may
	// still do (an event, an opened or clicked email): the data says no
	// now, but by then it may say yes.
	CanChange bool `json:"can_change,omitempty"`
}

// TestResult is a test run: whether the person would start, the steps
// they would go through, and how the run would end.
type TestResult struct {
	Version string `json:"version"`
	// Refusal is why the person wouldn't start a run now (the test goes
	// through the steps anyway).
	Refusal string     `json:"refusal,omitempty"`
	EventAt *time.Time `json:"event_at"`
	Steps   []TestStep `json:"steps"`
	End     string     `json:"end"`
	Reason  string     `json:"end_reason,omitempty"`
}

// trial is what a test run tells take, and take tells it back.
type trial struct {
	paths   map[string]string
	data    bool
	subject string
}

// ErrNoDraft is returned when a test asks for a draft the journey doesn't
// have.
var ErrNoDraft = errors.New("the journey has no draft version")

// Test takes a person through journeyID without doing anything. It reports
// nil for a journey that isn't the workspace's.
func (s *Service) Test(ctx context.Context, ws, journeyID string, in TestInput) (*TestResult, error) {
	ctx = track.Mute(ctx)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	j, err := loadJourney(ctx, tx, ws, journeyID, false)
	if err != nil || j == nil {
		return nil, err
	}
	var triggerEvent *string
	if err := tx.QueryRow(ctx, `SELECT trigger_event FROM journeys WHERE id = $1`, j.id).Scan(&triggerEvent); err != nil {
		return nil, err
	}
	out := &TestResult{Version: "live", Steps: []TestStep{}}
	if in.Draft {
		var filter, steps, exitConds []byte
		err := tx.QueryRow(ctx, `SELECT trigger_event, trigger_filter, entry, steps, exit_events, exit_conditions FROM journey_versions
			WHERE workspace_id = $1 AND journey_id = $2 AND status = 'draft'`, ws, j.id).
			Scan(&triggerEvent, &filter, &j.entry, &steps, &j.exitEvents, &exitConds)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoDraft
		}
		if err != nil {
			return nil, err
		}
		j.filter, j.steps, j.exitConditions = nil, nil, nil
		for _, p := range []struct {
			raw []byte
			to  any
		}{{filter, &j.filter}, {steps, &j.steps}, {exitConds, &j.exitConditions}} {
			if err := json.Unmarshal(p.raw, p.to); err != nil {
				return nil, err
			}
		}
		out.Version = "draft"
	}
	event := in.Event
	if event == nil && triggerEvent != nil {
		var props []byte
		var at time.Time
		err := tx.QueryRow(ctx, `SELECT properties, occurred_at FROM customer_events WHERE workspace_id = $1 AND record_id = $2 AND name = $3
			ORDER BY occurred_at DESC LIMIT 1`, ws, in.RecordID, *triggerEvent).Scan(&props, &at)
		switch {
		case err == nil:
			_ = json.Unmarshal(props, &event)
			out.EventAt = &at
		case !errors.Is(err, pgx.ErrNoRows):
			return nil, err
		}
	}
	if event == nil {
		event = map[string]any{}
	}
	why, err := refusal(ctx, tx, ws, j, in.RecordID, event, false)
	if err != nil {
		return nil, err
	}
	if why == notPerson {
		return nil, ErrNotPerson
	}
	out.Refusal = why
	r := run{recordID: in.RecordID, context: event, entered: time.Now()}
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&r.id); err != nil {
		return nil, err
	}
	cur := ""
	if len(j.steps) > 0 {
		cur = j.steps[0].ID
	}
	for {
		if cur == "" {
			out.End = "completed"
			return out, nil
		}
		if len(out.Steps) >= 100 {
			out.End, out.Reason = "failed", "too many steps"
			return out, nil
		}
		st := j.step(cur)
		if st == nil {
			out.End, out.Reason = "failed", "unknown_step"
			return out, nil
		}
		if len(j.exitConditions) > 0 {
			attrs, err := attributes(ctx, tx, ws, r.recordID)
			if err != nil {
				return nil, err
			}
			if Holds(attrs, r.context, j.exitConditions, "any") {
				out.End, out.Reason = "exited", "exit_condition"
				return out, nil
			}
		}
		t := &trial{paths: in.Paths}
		got, err := s.take(ctx, tx, ws, j, r, st, t)
		if err != nil {
			return nil, err
		}
		step := TestStep{Step: st.ID, Kind: st.Kind, Outcome: got.outcome, Detail: got.detail, Next: got.next, Subject: t.subject}
		if st.Kind == KindBranch {
			step.Data = "no"
			if t.data {
				step.Data = "yes"
			}
			step.CanChange = !t.data && canChange(st.Conditions)
		}
		if got.end != "" {
			if got.outcome != "" {
				out.Steps = append(out.Steps, step)
			}
			out.End, out.Reason = got.end, got.reason
			return out, nil
		}
		out.Steps = append(out.Steps, step)
		r.taken++
		cur = got.next
	}
}

// ErrNotPerson is returned when a test names a record that isn't a person.
var ErrNotPerson = errors.New(notPerson)

// canChange reports whether conditions test something a person can still
// do after they enter: an event, or opening or clicking an email.
func canChange(conds []Condition) bool {
	for _, c := range conds {
		if c.Event != "" || c.Email != "" {
			return true
		}
	}
	return false
}
