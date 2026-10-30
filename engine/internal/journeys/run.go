package journeys

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/secrets"
)

// JobRun moves one run along until it waits or ends.
const JobRun = customers.JobJourneyRun

// Service moves runs along: it starts them from events and goes through
// their steps in jobs.
type Service struct {
	Pool   *pgxpool.Pool
	Emails *emails.Service
	Log    *slog.Logger
	// Fields writes record fields for set_field and tag steps (the API
	// server). Without it those steps fail.
	Fields FieldWriter
	// AI asks the workspace's own AI model, for ai steps (the API server).
	// Without it those steps fail.
	AI AI
	// Store opens the workspace's own Slack connection for slack steps, and
	// Box opens webhook keys and the signing secret. Without them those
	// steps fail.
	Store *connectors.Store
	Box   *secrets.Box
	// AllowPrivate lets webhook steps call private, loopback and plain
	// http addresses, for self-hosted servers whose receivers are on the
	// same network (WEBHOOKS_ALLOW_PRIVATE). Off, only public
	// https URLs work.
	AllowPrivate bool
	// Client overrides the HTTP client webhook steps call with, for tests.
	Client *http.Client
	// Deals moves deals for move_deal steps (the API server). Without it
	// those steps fail.
	Deals DealMover
}

// Register adds the job handlers to q.
func (s *Service) Register(q *jobs.Queue) {
	q.Handle(customers.JobJourneyEvent, s.eventJob)
	q.Handle(JobRun, s.runJob)
	q.Handle(JobCall, s.callJob)
}

// journey is a journey as runs read it.
type journey struct {
	id, ws, status, triggerKind, entry string
	version                            int
	brandID                            *string
	filter                             []Condition
	steps                              []Step
	// Exit criteria: these events end a run at once; these conditions
	// (any of them) end it before its next step.
	exitEvents     []string
	exitConditions []Condition
	// Entry limits: at most entryLimit entries per entryLimitPer, and
	// reentryAfter seconds after someone's last run before they start
	// again.
	entryLimit    *int
	entryLimitPer *string
	reentryAfter  *int
	// Who never enters.
	exclude customers.Exclusion
}

func loadJourney(ctx context.Context, q jobs.Querier, ws, id string, lock bool) (*journey, error) {
	var j journey
	var filter, steps, exitConds, exclude []byte
	sql := `SELECT id, workspace_id, status, trigger_kind, entry, version, brand_id, trigger_filter, steps, exit_events, exit_conditions, exclude,
		entry_limit, entry_limit_per, reentry_after_seconds FROM journeys WHERE workspace_id = $1 AND id = $2`
	if lock {
		sql += ` FOR SHARE`
	}
	err := q.QueryRow(ctx, sql, ws, id).Scan(&j.id, &j.ws, &j.status, &j.triggerKind, &j.entry, &j.version, &j.brandID, &filter, &steps, &j.exitEvents, &exitConds, &exclude,
		&j.entryLimit, &j.entryLimitPer, &j.reentryAfter)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(filter, &j.filter); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(steps, &j.steps); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(exitConds, &j.exitConditions); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(exclude, &j.exclude); err != nil {
		return nil, err
	}
	return &j, nil
}

// pin swaps in the steps of the version a run goes through, when it is not
// the journey's current one. A version that was never kept (a run from
// before versions) leaves the current steps.
func (j *journey) pin(ctx context.Context, q jobs.Querier, version *int) error {
	if version == nil || *version == j.version {
		return nil
	}
	var steps []byte
	err := q.QueryRow(ctx, `SELECT steps FROM journey_versions WHERE workspace_id = $1 AND journey_id = $2 AND status = 'published' AND version = $3`,
		j.ws, j.id, *version).Scan(&steps)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	j.steps = nil
	return json.Unmarshal(steps, &j.steps)
}

func (j *journey) step(id string) *Step {
	for i := range j.steps {
		if j.steps[i].ID == id {
			return &j.steps[i]
		}
	}
	return nil
}

func record(ctx context.Context, q jobs.Querier, ws, action, journeyID string, data map[string]any) error {
	return audit.Record(ctx, q, audit.Event{WorkspaceID: ws, ActorType: "system", Action: action, TargetType: "journey", TargetID: journeyID, Data: data})
}

// eventJob starts runs for a new event on someone's timeline, and ends
// the runs whose goal it is.
func (s *Service) eventJob(ctx context.Context, job jobs.Job) error {
	var in struct {
		EventID string `json:"event_id"`
	}
	if err := json.Unmarshal(job.Payload, &in); err != nil || job.WorkspaceID == nil {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var rec *string
		var name string
		var props []byte
		err := tx.QueryRow(ctx, `SELECT record_id, name, properties FROM customer_events WHERE workspace_id = $1 AND id = $2`, ws, in.EventID).
			Scan(&rec, &name, &props)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && rec == nil) {
			return nil
		}
		if err != nil {
			return err
		}
		// Only approved events drive journeys: a name the workspace never
		// defined arrives quarantined (the event catalog).
		var approved bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM event_definitions WHERE workspace_id = $1 AND name = $2 AND status = 'approved')`,
			ws, name).Scan(&approved); err != nil || !approved {
			return err
		}
		// Goals first: an event that ends a run never also restarts it.
		// Exit events too: they end a run at once, even mid-wait.
		rows, err := tx.Query(ctx, `UPDATE journey_runs r SET status = 'exited',
				exit_reason = CASE WHEN j.goal_event = $2 THEN 'goal' ELSE 'exit_event' END, finished_at = now(), next_at = NULL, updated_at = now()
			FROM journeys j WHERE j.id = r.journey_id AND j.workspace_id = $1 AND j.status IN ('active', 'paused')
			  AND (j.goal_event = $2 OR $2 = ANY (j.exit_events))
			  AND r.record_id = $3 AND r.status = 'active' RETURNING r.journey_id::text, r.exit_reason`, ws, name, *rec)
		if err != nil {
			return err
		}
		type ended struct{ journey, reason string }
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ended, error) {
			var e ended
			return e, row.Scan(&e.journey, &e.reason)
		})
		if err != nil {
			return err
		}
		for _, e := range list {
			if err := record(ctx, tx, ws, "journey_run.exited", e.journey, map[string]any{"reason": e.reason}); err != nil {
				return err
			}
		}
		var event map[string]any
		_ = json.Unmarshal(props, &event)
		// Runs waiting for this event on a wait_until step go on.
		rows, err = tx.Query(ctx, `SELECT id::text FROM journey_runs WHERE workspace_id = $1 AND record_id = $2 AND status = 'active'
			AND wait_events @> ARRAY[$3]::text[]`, ws, *rec, name)
		if err != nil {
			return err
		}
		waiting, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, runID := range waiting {
			if err := s.waitEvent(ctx, tx, ws, runID, in.EventID, name, event); err != nil {
				return err
			}
		}
		rows, err = tx.Query(ctx, `SELECT id::text FROM journeys WHERE workspace_id = $1 AND status = 'active' AND trigger_kind = 'event' AND trigger_event = $2`, ws, name)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, jid := range ids {
			if _, _, err := Enroll(ctx, tx, ws, jid, *rec, &in.EventID, event, false); err != nil {
				return err
			}
		}
		return nil
	})
}

// Enroll starts a run of a journey for a person, and reports its id, or
// why not: the journey is not active, the person is not in its brand or
// does not meet its filter (unless manual), or its entry rule says they
// have been through it or are in it now.
func Enroll(ctx context.Context, tx pgx.Tx, ws, journeyID, recordID string, eventID *string, context map[string]any, manual bool) (string, string, error) {
	j, err := loadJourney(ctx, tx, ws, journeyID, true)
	if err != nil || j == nil {
		return "", "journey not found", err
	}
	if j.status != "active" {
		return "", "the journey is not active", nil
	}
	if context == nil {
		context = map[string]any{}
	}
	why, err := refusal(ctx, tx, ws, j, recordID, context, manual)
	if err != nil || why != "" {
		if strings.HasPrefix(why, customers.ExcludedPrefix) {
			metrics.Excluded("journey")
		}
		return "", why, err
	}
	if !manual {
		if why, err := entryRefused(ctx, tx, j, recordID); err != nil || why != "" {
			return "", why, err
		}
	}
	first := ""
	if len(j.steps) > 0 {
		first = j.steps[0].ID
	}
	ctxJSON, _ := json.Marshal(context)
	var id string
	// The run goes through the version it entered on, even after a new one
	// is published.
	err = tx.QueryRow(ctx, `INSERT INTO journey_runs (workspace_id, journey_id, record_id, step, trigger_event_id, context, journey_version)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7) ON CONFLICT DO NOTHING RETURNING id::text`, ws, journeyID, recordID, first, eventID, ctxJSON, j.version).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "already in the journey", nil
	}
	if err != nil {
		return "", "", err
	}
	if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobRun, WorkspaceID: &ws, Payload: map[string]string{"run_id": id},
		IdempotencyKey: "journey-run:" + id + ":0"}); err != nil {
		return "", "", err
	}
	if manual {
		return id, "", nil // the caller records who added them
	}
	return id, "", record(ctx, tx, ws, "journey_run.entered", journeyID, map[string]any{"via": "event"})
}

const notPerson = "not a person on a customer record"

// refusal is why a person can't start a run of j now, or "": they are not
// a person in its brand, are excluded, already meet its exit criteria,
// don't meet its filter (unless manual), or its entry rule says no.
func refusal(ctx context.Context, tx pgx.Tx, ws string, j *journey, recordID string, context map[string]any, manual bool) (string, error) {
	var brand *string
	var attrs []byte
	err := tx.QueryRow(ctx, `SELECT brand_id, attributes FROM records WHERE workspace_id = $1 AND id = $2 AND record_type = 'person' AND merged_into IS NULL AND archived_at IS NULL`,
		ws, recordID).Scan(&brand, &attrs)
	if errors.Is(err, pgx.ErrNoRows) {
		return notPerson, nil
	}
	if err != nil {
		return "", err
	}
	if j.brandID != nil && (brand == nil || *brand != *j.brandID) {
		return "not in the journey's brand", nil
	}
	// Exclusions keep people out even when added by hand.
	if why, err := customers.Excluded(ctx, tx, ws, recordID, j.exclude); err != nil || why != "" {
		return why, err
	}
	var attributes map[string]any
	_ = json.Unmarshal(attrs, &attributes)
	// Someone who already meets the exit criteria never starts.
	if len(j.exitConditions) > 0 && Holds(attributes, context, j.exitConditions, "any") {
		return "already meets the journey's exit criteria", nil
	}
	if !manual && len(j.filter) > 0 {
		ok, err := holds(ctx, tx, subject{ws: ws, recordID: recordID, attributes: attributes, context: context, entered: time.Now()}, j.filter, "all")
		if err != nil {
			return "", err
		}
		if !ok {
			return "does not meet the journey's conditions", nil
		}
	}
	var before, now bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM journey_runs WHERE journey_id = $1 AND record_id = $2),
		EXISTS (SELECT 1 FROM journey_runs WHERE journey_id = $1 AND record_id = $2 AND status = 'active')`, j.id, recordID).Scan(&before, &now); err != nil {
		return "", err
	}
	switch {
	case now:
		return "already in the journey", nil
	case before && j.entry == "once":
		return "already went through the journey, which people go through once", nil
	}
	return "", nil
}

// Resume moves on the active runs of a journey that was paused.
func Resume(ctx context.Context, tx pgx.Tx, ws, journeyID string) error {
	rows, err := tx.Query(ctx, `SELECT id::text, steps_taken FROM journey_runs WHERE journey_id = $1 AND status = 'active' AND (next_at IS NULL OR next_at <= now())`, journeyID)
	if err != nil {
		return err
	}
	type pending struct {
		id    string
		taken int
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (pending, error) {
		var p pending
		err := row.Scan(&p.id, &p.taken)
		return p, err
	})
	if err != nil {
		return err
	}
	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	for _, p := range list {
		if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobRun, WorkspaceID: &ws, Payload: map[string]string{"run_id": p.id},
			IdempotencyKey: "journey-run:" + p.id + ":resume:" + stamp}); err != nil {
			return err
		}
	}
	return nil
}

// ExitRuns ends a journey's active runs, or one person's, with a reason
// (archived, manual), and reports how many. The caller records it.
func ExitRuns(ctx context.Context, tx pgx.Tx, ws, journeyID, recordID, reason string) (int, error) {
	tag, err := tx.Exec(ctx, `UPDATE journey_runs SET status = 'exited', exit_reason = $4, finished_at = now(), next_at = NULL, updated_at = now()
		WHERE workspace_id = $1 AND journey_id = $2 AND ($3 = '' OR record_id::text = $3) AND status = 'active'`, ws, journeyID, recordID, reason)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *Service) runJob(ctx context.Context, job jobs.Job) error {
	var in struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(job.Payload, &in); err != nil || job.WorkspaceID == nil {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	// A run goes through its steps one transaction each until it waits or
	// ends; 50 is far more than a journey without a wait can have.
	for range 50 {
		more, err := s.advance(ctx, *job.WorkspaceID, in.RunID)
		if err != nil || !more {
			return err
		}
	}
	return nil
}

// run is a run as advance reads it.
type run struct {
	id, journeyID, recordID string
	step                    *string
	version                 *int
	nextAt                  *time.Time
	context                 map[string]any
	vars                    map[string]any
	// When a wait_until step it is on times out.
	waitDeadline *time.Time
	entered      time.Time
	taken        int
}

// advance takes one step of a run, and reports whether to go on.
func (s *Service) advance(ctx context.Context, ws, runID string) (bool, error) {
	more := false
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		r, found, err := loadRun(ctx, tx, ws, runID)
		if err != nil || !found {
			return err
		}
		j, err := loadJourney(ctx, tx, ws, r.journeyID, false)
		if err != nil {
			return err
		}
		switch {
		case j == nil || j.status == "archived":
			return s.finish(ctx, tx, ws, r, "exited", "archived")
		case j.status != "active":
			return nil // paused: Resume picks it up
		case r.nextAt != nil && r.nextAt.After(time.Now()):
			return nil // waiting; its own job comes then
		case r.step == nil:
			return s.finish(ctx, tx, ws, r, "completed", "")
		case r.taken >= 500:
			return s.finish(ctx, tx, ws, r, "failed", "too many steps")
		}
		if err := j.pin(ctx, tx, r.version); err != nil {
			return err
		}
		st := j.step(*r.step)
		if st == nil {
			return s.finish(ctx, tx, ws, r, "exited", "step_removed")
		}
		if len(j.exitConditions) > 0 && r.nextAt == nil {
			attrs, err := attributes(ctx, tx, ws, r.recordID)
			if err != nil {
				return err
			}
			if Holds(attrs, r.context, j.exitConditions, "any") {
				return s.finish(ctx, tx, ws, r, "exited", "exit_condition")
			}
		}
		if st.Kind == KindWait && r.nextAt == nil {
			at := time.Now().Add(time.Duration(st.WaitSeconds) * time.Second)
			if _, err := tx.Exec(ctx, `UPDATE journey_runs SET next_at = $2, updated_at = now() WHERE id = $1`, r.id, at); err != nil {
				return err
			}
			if err := logStep(ctx, tx, ws, r, st, "waiting", nil, "until "+at.UTC().Format(time.RFC3339)); err != nil {
				return err
			}
			_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobRun, WorkspaceID: &ws, Payload: map[string]string{"run_id": r.id}, RunAt: at,
				IdempotencyKey: "journey-run:" + r.id + ":" + strconv.Itoa(r.taken+1)})
			return err
		}
		t, err := s.take(ctx, tx, ws, j, r, st, nil)
		if err != nil || t.wait {
			return err
		}
		if t.call != nil {
			// A call runs in a job of its own, outside this transaction;
			// the run waits for it.
			return s.startCall(ctx, tx, ws, r, st, t.call)
		}
		more, err = s.moveOn(ctx, tx, ws, r, st, t)
		return err
	})
	return more, err
}

// taken is what one step did: where the run goes next, or how it ends.
type taken struct {
	next, outcome, detail string
	emailID               *string
	// end and reason are set when the run ends at this step: its status
	// and why.
	end, reason string
	// call is set when the step is a call to start: the run waits for it.
	call *actionDef
	// wait is set when the run stays on the step (a wait_until that hasn't
	// happened yet): it has already been saved, and nothing moves on.
	wait bool
}

// take does one step of a run. A test run (t set) goes through the same
// code in a transaction that is rolled back: a wait passes at once, an
// email is composed but not queued, and a branch goes the way t says.
func (s *Service) take(ctx context.Context, tx pgx.Tx, ws string, j *journey, r run, st *Step, t *trial) (taken, error) {
	switch st.Kind {
	case KindWait:
		return taken{next: st.Next, outcome: "waited"}, nil
	case KindBranch:
		attrs, err := attributes(ctx, tx, ws, r.recordID)
		if err != nil {
			return taken{}, err
		}
		yes, err := holds(ctx, tx, subject{ws: ws, runID: r.id, recordID: r.recordID, attributes: attrs, context: r.context, vars: r.vars, entered: r.entered}, st.Conditions, st.Match)
		if err != nil {
			return taken{}, err
		}
		if t != nil {
			t.data = yes
			switch t.paths[st.ID] {
			case "yes":
				yes = true
			case "no":
				yes = false
			}
		}
		if yes {
			return taken{next: st.Then, outcome: "yes"}, nil
		}
		return taken{next: st.Else, outcome: "no"}, nil
	case KindEmail:
		id, why, failed, err := s.email(ctx, tx, ws, j, r, st, t)
		if err != nil {
			return taken{}, err
		}
		if failed != "" {
			return taken{outcome: "failed", detail: failed, end: "failed", reason: "email_failed"}, nil
		}
		if why != "" {
			return taken{next: st.Next, outcome: "skipped", detail: why}, nil
		}
		return taken{next: st.Next, outcome: "sent", emailID: id}, nil
	case KindExit:
		return taken{outcome: "exited", end: "exited", reason: "exit_step"}, nil
	case KindWaitUntil:
		if t != nil {
			return s.tryWaitUntil(ctx, tx, ws, r, st, t)
		}
		next, outcome, done, err := s.waitUntil(ctx, tx, ws, r, st)
		if err != nil || !done {
			return taken{wait: true}, err
		}
		return taken{next: next, outcome: outcome}, nil
	case KindSplit:
		p, ok, err := s.split(ctx, tx, ws, j, r, st)
		if err != nil {
			return taken{}, err
		}
		if t != nil {
			if id, set := t.paths[st.ID]; set {
				p, ok = Path{}, false
				for _, q := range st.Paths {
					if q.ID == id {
						p, ok = q, true
					}
				}
			}
		}
		switch {
		case !ok:
			return taken{next: st.Else, outcome: "else"}, nil
		case p.Holdout:
			return taken{outcome: p.ID, detail: "holdout", end: "exited", reason: "holdout"}, nil
		}
		return taken{next: p.Next, outcome: p.ID}, nil
	}
	a := action(st.Kind)
	if a == nil {
		return taken{end: "failed", reason: "unknown_step"}, nil
	}
	if a.call != nil {
		switch {
		case t != nil:
			// A test never calls out: the call would be a job, after
			// the test is rolled back.
			return taken{next: st.Next, outcome: "not_called", detail: "a test doesn't make calls"}, nil
		case r.nextAt == nil:
			return taken{call: a}, nil
		}
		return onError(st, taken{next: st.Next, outcome: "failed", detail: "the call did not finish in time"}), nil
	}
	outcome, detail, err := a.run(ctx, &act{s: s, tx: tx, ws: ws, j: j, r: r, st: st})
	if err != nil {
		return taken{}, err
	}
	return onError(st, taken{next: st.Next, outcome: outcome, detail: detail}), nil
}

// onError applies a failed action's on_error: stop (the run fails),
// continue, or a step to go to.
func onError(st *Step, t taken) taken {
	if t.outcome != "failed" {
		return t
	}
	switch st.OnError {
	case "", "stop":
		t.end, t.reason = "failed", "action_failed"
	case "continue":
	default:
		t.next = st.OnError
	}
	return t
}

// loadRun reads an active run, locking it until tx ends.
func loadRun(ctx context.Context, tx pgx.Tx, ws, runID string) (run, bool, error) {
	var r run
	var ctxJSON, varsJSON []byte
	err := tx.QueryRow(ctx, `SELECT id, journey_id, record_id, step, journey_version, next_at, context, vars, wait_deadline, entered_at, steps_taken FROM journey_runs
		WHERE workspace_id = $1 AND id = $2 AND status = 'active' FOR UPDATE`, ws, runID).
		Scan(&r.id, &r.journeyID, &r.recordID, &r.step, &r.version, &r.nextAt, &ctxJSON, &varsJSON, &r.waitDeadline, &r.entered, &r.taken)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	_ = json.Unmarshal(ctxJSON, &r.context)
	_ = json.Unmarshal(varsJSON, &r.vars)
	if r.vars == nil {
		r.vars = map[string]any{}
	}
	return r, true, nil
}

// moveOn records what a step did and moves the run on to its next step,
// or ends it. It reports whether the run goes on.
func (s *Service) moveOn(ctx context.Context, tx pgx.Tx, ws string, r run, st *Step, t taken) (bool, error) {
	if t.end != "" {
		if t.outcome != "" {
			if err := logStep(ctx, tx, ws, r, st, t.outcome, nil, t.detail); err != nil {
				return false, err
			}
		}
		return false, s.finish(ctx, tx, ws, r, t.end, t.reason)
	}
	if err := logStep(ctx, tx, ws, r, st, t.outcome, t.emailID, t.detail); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE journey_runs SET step = NULLIF($2, ''), next_at = NULL, steps_taken = steps_taken + 1,
		wait_events = NULL, wait_fields = false, wait_deadline = NULL, updated_at = now() WHERE id = $1`,
		r.id, t.next); err != nil {
		return false, err
	}
	return true, nil
}

func attributes(ctx context.Context, q jobs.Querier, ws, recordID string) (map[string]any, error) {
	var raw []byte
	if err := q.QueryRow(ctx, `SELECT attributes FROM records WHERE workspace_id = $1 AND id = $2`, ws, recordID).Scan(&raw); err != nil {
		return nil, err
	}
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out, nil
}

func logStep(ctx context.Context, q jobs.Querier, ws string, r run, st *Step, outcome string, emailID *string, detail string) error {
	_, err := q.Exec(ctx, `INSERT INTO journey_run_steps (workspace_id, run_id, journey_id, step, kind, outcome, email_id, detail)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''))`, ws, r.id, r.journeyID, st.ID, st.Kind, outcome, emailID, detail)
	return err
}

func (s *Service) finish(ctx context.Context, tx pgx.Tx, ws string, r run, status, reason string) error {
	if _, err := tx.Exec(ctx, `UPDATE journey_runs SET status = $2, exit_reason = NULLIF($3, ''), finished_at = now(), next_at = NULL, updated_at = now() WHERE id = $1`,
		r.id, status, reason); err != nil {
		return err
	}
	data := map[string]any{}
	if reason != "" {
		data["reason"] = reason
	}
	return record(ctx, tx, ws, "journey_run."+status, r.journeyID, data)
}

// email sends an email step's template to the person, and reports the
// email's id, or why they were skipped (nil id), or why the step cannot
// work for anyone (failed). A test run (t set) only composes it.
func (s *Service) email(ctx context.Context, tx pgx.Tx, ws string, j *journey, r run, st *Step, t *trial) (*string, string, string, error) {
	var templateID string
	err := tx.QueryRow(ctx, `SELECT id::text FROM email_templates WHERE workspace_id = $1 AND (id::text = $2 OR handle = $2)`, ws, st.Template).Scan(&templateID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", "the template " + st.Template + " no longer exists", nil
	}
	if err != nil {
		return nil, "", "", err
	}
	var name, address string
	var attrs []byte
	err = tx.QueryRow(ctx, `SELECT r.name, COALESCE(i.value, ''), r.attributes FROM records r
		LEFT JOIN LATERAL (SELECT value FROM record_identities i WHERE i.record_id = r.id AND i.namespace = 'email' AND i.verified
			AND i.valid_to IS NULL ORDER BY i.valid_from, i.id LIMIT 1) i ON true
		WHERE r.workspace_id = $1 AND r.id = $2`, ws, r.recordID).Scan(&name, &address, &attrs)
	if err != nil {
		return nil, "", "", err
	}
	if address == "" {
		return nil, "no verified email address", "", nil
	}
	var attributes map[string]any
	_ = json.Unmarshal(attrs, &attributes)
	d := emails.Draft{NewEmail: emails.NewEmail{WorkspaceID: ws, BrandID: j.brandID, To: []string{address}, Purpose: "lifecycle",
		Trigger: "journey", TriggerRef: j.id, Surface: "job"},
		TemplateID: templateID, Data: map[string]any{"person": emails.PersonData(r.recordID, name, address, attributes), "event": r.context, "vars": r.vars}}
	e, err := s.Emails.Compose(ctx, tx, d)
	var ce *emails.ComposeError
	if errors.As(err, &ce) {
		if ce.Code == "template_variables_missing" || ce.Code == "invalid_request" {
			return nil, ce.Message, "", nil
		}
		return nil, "", ce.Message, nil
	}
	if err != nil {
		return nil, "", "", err
	}
	why, err := emails.SkipReason(ctx, tx, ws, r.recordID, e.TopicID)
	if err != nil || why != "" {
		return nil, why, "", err
	}
	if t != nil {
		t.subject = e.Subject
		return nil, "", "", nil
	}
	id, err := s.Emails.Queue(ctx, tx, emails.System, e)
	if err != nil {
		return nil, "", "", err
	}
	return &id, "", "", nil
}
