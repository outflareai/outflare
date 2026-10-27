// Package pipeline moves work through stages one item at a time.
//
// A run (a lead finder search, a lead list build) is many items. Each item
// is a row that sits in one stage at a time, waiting, being worked on, or
// finished with a reason. Stage workers claim due items in small batches
// with FOR UPDATE SKIP LOCKED, share them fairly between workspaces, hold
// a lease that they renew while they work, and write every outcome in one
// short transaction. So one slow or failing item never holds a whole run,
// a crash loses at most one batch, and the backlog of every stage can be
// counted: the stage with the oldest waiting item is the bottleneck.
//
// A feature registers its stages (Stage) and keeps its own data in its own
// tables, keyed by the item's ref. It creates a run with Start, adds items
// with Add, and its stage functions say what happens to each item with a
// Result: move on, finish, skip, hold for a person, retry, wait, pause the
// run or fail. Limits (rate limits and breakers per outside account) let a
// stage wait instead of failing when a provider slows it down.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is satisfied by *pgxpool.Pool, *pgx.Conn and pgx.Tx.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Run states.
const (
	RunRunning   = "running"
	RunPaused    = "paused"
	RunCancelled = "cancelled"
	RunFinished  = "finished"
)

// Item states.
const (
	StateWaiting = "waiting"
	StateWorking = "working"
	StateDone    = "done"
	StateSkipped = "skipped"
	StateHeld    = "held"
	StateFailed  = "failed"
)

// Item is a claimed item handed to a stage.
type Item struct {
	ID          string
	WorkspaceID string
	RunID       string
	Ref         string
	Stage       string
	// Attempt is 1 the first time the item is worked on in this stage.
	Attempt int
	// Entered is when the item came into this stage.
	Entered time.Time
}

// Outcome is what a stage decided for an item.
type Outcome int

const (
	// Advance moves the item to Result.Next, due now.
	Advance Outcome = iota
	// Done finishes the item: it went through every stage.
	Done
	// Skip finishes the item on purpose (a reject, a duplicate, an
	// opt-out), with Code and Reason.
	Skip
	// Hold stops the item until a person acts (Release), with Code.
	Hold
	// Retry tries the item again in this stage after RetryAfter (or the
	// stage's backoff), counting an attempt. After the stage's
	// MaxAttempts it fails.
	Retry
	// Wait tries the item again after RetryAfter without counting an
	// attempt: a rate limit, an open breaker, a slow check still running.
	Wait
	// Pause puts the item back and pauses its whole run with Code: out of
	// budget, a missing connection. ResumeRun carries on.
	Pause
	// Fail gives up on the item with Code.
	Fail
)

var outcomeNames = [...]string{"advanced", "done", "skipped", "held", "retried", "waited", "paused", "failed"}

func (o Outcome) String() string {
	if int(o) < len(outcomeNames) {
		return outcomeNames[o]
	}
	return "unknown"
}

// Result is a stage's decision for one item. Results name items by ID; an
// item a stage leaves out of its results is retried.
type Result struct {
	ItemID  string
	Outcome Outcome
	// Next is the stage to move to, for Advance.
	Next string
	// Code is an error code for anything but Advance and Done: a catalog
	// code (connector_quota, ai_budget_reached) or the stage's own
	// (no_website, rejected). Events and metrics carry it.
	Code string
	// Reason is a short note for staff and the run page. It may quote
	// customer data, so it never goes into events or metrics.
	Reason string
	// RetryAfter is how long to wait, for Retry and Wait.
	RetryAfter time.Duration
}

// Stage is one step of a pipeline.
type Stage struct {
	Pipeline string
	Name     string
	// Work decides each claimed item. It runs without a transaction open;
	// it may take as long as it needs (the lease is renewed), but a batch
	// should aim to finish within a minute. An error retries the whole
	// batch.
	Work func(ctx context.Context, items []Item) ([]Result, error)
	// Batch is how many items one call to Work gets. Default 25.
	Batch int
	// Concurrency is how many batches of this stage one process works on
	// at once. Default 2.
	Concurrency int
	// PerWorkspace caps one workspace's items in progress in this stage
	// across every process, so a big run never takes every slot. Default 50.
	PerWorkspace int
	// MaxAttempts before a retried item fails. Default 5.
	MaxAttempts int
	// Lease is how long a claim lasts without renewal. Default 2 minutes.
	Lease time.Duration
	// Backoff is the delay before retry number attempt when a Result
	// gives none. Default 10s doubling to 15 minutes.
	Backoff func(attempt int) time.Duration
}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var pipelineRe = regexp.MustCompile(`^[a-z][a-z0-9_.]{0,63}$`)

func (s *Stage) defaults() error {
	if !pipelineRe.MatchString(s.Pipeline) || !nameRe.MatchString(s.Name) || s.Work == nil {
		return fmt.Errorf("pipeline: stage %q of %q needs a lowercase pipeline, name and Work", s.Name, s.Pipeline)
	}
	if s.Batch <= 0 {
		s.Batch = 25
	}
	if s.Concurrency <= 0 {
		s.Concurrency = 2
	}
	if s.PerWorkspace <= 0 {
		s.PerWorkspace = 50
	}
	if s.MaxAttempts <= 0 {
		s.MaxAttempts = 5
	}
	if s.Lease <= 0 {
		s.Lease = 2 * time.Minute
	}
	if s.Backoff == nil {
		s.Backoff = DefaultBackoff
	}
	return nil
}

// DefaultBackoff is 10s doubling, capped at 15 minutes.
func DefaultBackoff(attempt int) time.Duration {
	d := 10 * time.Second
	for i := 1; i < attempt && d < 15*time.Minute; i++ {
		d *= 2
	}
	return min(d, 15*time.Minute)
}

// NewItem is an item to add to a run.
type NewItem struct {
	Ref   string
	Stage string
	// At is when it is due; zero is now.
	At time.Time
}

// ErrNotRunning means the run is cancelled or finished.
var ErrNotRunning = errors.New("pipeline: the run is not running")

// Start creates a run with the caller's id (usually the feature's own run
// id), in tx with the feature's own run row.
func Start(ctx context.Context, q Querier, workspaceID, pipeline, runID string) error {
	if !pipelineRe.MatchString(pipeline) {
		return fmt.Errorf("pipeline: bad pipeline name %q", pipeline)
	}
	_, err := q.Exec(ctx, `INSERT INTO pipeline_runs (id, workspace_id, pipeline) VALUES ($1, $2, $3)`, runID, workspaceID, pipeline)
	return err
}

// Add puts items in a run, each in its first stage. A ref already in the
// run is left as it is. It returns how many were added. Workers are woken
// when q's transaction commits.
func Add(ctx context.Context, q Querier, workspaceID, runID string, items []NewItem) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	refs := make([]string, len(items))
	stages := make([]string, len(items))
	ats := make([]time.Time, len(items))
	now := time.Now()
	for i, it := range items {
		if !nameRe.MatchString(it.Stage) || it.Ref == "" || len(it.Ref) > 300 {
			return 0, fmt.Errorf("pipeline: bad item %q in stage %q", it.Ref, it.Stage)
		}
		refs[i], stages[i], ats[i] = it.Ref, it.Stage, it.At
		if it.At.IsZero() {
			ats[i] = now
		}
	}
	var pipeline, state string
	err := q.QueryRow(ctx, `SELECT pipeline, state FROM pipeline_runs WHERE id = $1 AND workspace_id = $2`, runID, workspaceID).Scan(&pipeline, &state)
	if err != nil {
		return 0, err
	}
	if state == RunCancelled || state == RunFinished {
		return 0, ErrNotRunning
	}
	var n int
	err = q.QueryRow(ctx, `
		WITH added AS (
			INSERT INTO pipeline_items (workspace_id, run_id, pipeline, ref, stage, next_at)
			SELECT $1, $2, $3, ref, stage, at FROM unnest($4::text[], $5::text[], $6::timestamptz[]) AS t (ref, stage, at)
			ON CONFLICT (run_id, ref) DO NOTHING
			RETURNING stage
		), counted AS (
			INSERT INTO pipeline_stage_stats (run_id, stage, workspace_id, entered)
			SELECT $2, stage, $1, count(*) FROM added GROUP BY stage
			ON CONFLICT (run_id, stage) DO UPDATE SET entered = pipeline_stage_stats.entered + excluded.entered, updated_at = now()
		)
		SELECT count(*) FROM added`,
		workspaceID, runID, pipeline, refs, stages, ats).Scan(&n)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	return n, notify(ctx, q)
}

// PauseRun stops a run's items from being claimed, with a code and a reason
// for the run page. Items being worked on finish their batch.
func PauseRun(ctx context.Context, q Querier, workspaceID, runID, code, reason string) error {
	_, err := q.Exec(ctx, `UPDATE pipeline_runs SET state = 'paused', pause_code = $3, pause_reason = left($4, 500), updated_at = now()
		WHERE id = $1 AND workspace_id = $2 AND state = 'running'`, runID, workspaceID, code, reason)
	return err
}

// ResumeRun lets a paused run carry on.
func ResumeRun(ctx context.Context, q Querier, workspaceID, runID string) error {
	tag, err := q.Exec(ctx, `UPDATE pipeline_runs SET state = 'running', pause_code = NULL, pause_reason = NULL, updated_at = now()
		WHERE id = $1 AND workspace_id = $2 AND state IN ('running', 'paused')`, runID, workspaceID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotRunning
	}
	return notify(ctx, q)
}

// CancelRun ends a run: its unfinished items are skipped as cancelled.
func CancelRun(ctx context.Context, q Querier, workspaceID, runID string) error {
	tag, err := q.Exec(ctx, `UPDATE pipeline_runs SET state = 'cancelled', updated_at = now(), finished_at = now()
		WHERE id = $1 AND workspace_id = $2 AND state IN ('running', 'paused')`, runID, workspaceID)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	_, err = q.Exec(ctx, `UPDATE pipeline_items SET state = 'skipped', error_code = 'cancelled', lease_until = NULL, updated_at = now()
		WHERE run_id = $1 AND state IN ('waiting', 'working', 'held')`, runID)
	return err
}

// Release sends held items of a run on to a stage (a person picked them)
// or skips them (stage ""), returning how many moved.
func Release(ctx context.Context, q Querier, workspaceID, runID string, refs []string, stage, code string) (int, error) {
	var tag pgconn.CommandTag
	var err error
	if stage == "" {
		tag, err = q.Exec(ctx, `UPDATE pipeline_items SET state = 'skipped', error_code = $4, updated_at = now()
			WHERE run_id = $1 AND workspace_id = $2 AND ref = ANY($3) AND state = 'held'`, runID, workspaceID, refs, code)
	} else {
		if !nameRe.MatchString(stage) {
			return 0, fmt.Errorf("pipeline: bad stage %q", stage)
		}
		tag, err = q.Exec(ctx, `UPDATE pipeline_items SET state = 'waiting', stage = $4, next_at = now(), attempts = 0,
				error_code = NULL, reason = NULL, stage_entered_at = now(), updated_at = now()
			WHERE run_id = $1 AND workspace_id = $2 AND ref = ANY($3) AND state = 'held'`, runID, workspaceID, refs, stage)
	}
	if err != nil {
		return 0, err
	}
	if stage != "" && tag.RowsAffected() > 0 {
		if _, err := q.Exec(ctx, `INSERT INTO pipeline_stage_stats (run_id, stage, workspace_id, entered) VALUES ($1, $2, $3, $4)
			ON CONFLICT (run_id, stage) DO UPDATE SET entered = pipeline_stage_stats.entered + excluded.entered, updated_at = now()`,
			runID, stage, workspaceID, tag.RowsAffected()); err != nil {
			return 0, err
		}
		return int(tag.RowsAffected()), notify(ctx, q)
	}
	return int(tag.RowsAffected()), nil
}

// RunView is a run's state and its counts per stage.
type RunView struct {
	ID          string       `json:"id"`
	Pipeline    string       `json:"pipeline"`
	State       string       `json:"state"`
	PauseCode   *string      `json:"pause_code,omitempty"`
	PauseReason *string      `json:"pause_reason,omitempty"`
	Stages      []StageCount `json:"stages"`
	CreatedAt   time.Time    `json:"created_at"`
	FinishedAt  *time.Time   `json:"finished_at,omitempty"`
}

// StageCount is one stage of a run: its items now, by state, and what has
// happened in it so far.
type StageCount struct {
	Stage   string         `json:"stage"`
	Waiting int            `json:"waiting"`
	Working int            `json:"working"`
	Done    int            `json:"done"`
	Skipped int            `json:"skipped"`
	Held    int            `json:"held"`
	Failed  int            `json:"failed"`
	Entered int            `json:"entered"`
	Retried int            `json:"retried"`
	Waited  int            `json:"waited"`
	BusyMS  int64          `json:"busy_ms"`
	Errors  map[string]int `json:"errors"`
	// OldestWaiting is how long the longest-waiting due item has waited.
	OldestWaitingSeconds float64 `json:"oldest_waiting_seconds"`
}

// View reads a run in a workspace (pgx.ErrNoRows if it isn't there).
func View(ctx context.Context, q Querier, workspaceID, runID string) (RunView, error) {
	var v RunView
	err := q.QueryRow(ctx, `SELECT id, pipeline, state, pause_code, pause_reason, created_at, finished_at
		FROM pipeline_runs WHERE id = $1 AND workspace_id = $2`, runID, workspaceID).
		Scan(&v.ID, &v.Pipeline, &v.State, &v.PauseCode, &v.PauseReason, &v.CreatedAt, &v.FinishedAt)
	if err != nil {
		return v, err
	}
	rows, err := q.Query(ctx, `
		SELECT coalesce(s.stage, i.stage),
			coalesce(i.waiting, 0), coalesce(i.working, 0), coalesce(i.done, 0), coalesce(i.skipped, 0), coalesce(i.held, 0), coalesce(i.failed, 0),
			coalesce(s.entered, 0), coalesce(s.retried, 0), coalesce(s.waited, 0), coalesce(s.busy_ms, 0), coalesce(s.errors, '{}'),
			coalesce(i.oldest, 0)
		FROM (SELECT * FROM pipeline_stage_stats WHERE run_id = $1) s
		FULL JOIN (
			SELECT stage,
				count(*) FILTER (WHERE state = 'waiting') AS waiting,
				count(*) FILTER (WHERE state = 'working') AS working,
				count(*) FILTER (WHERE state = 'done') AS done,
				count(*) FILTER (WHERE state = 'skipped') AS skipped,
				count(*) FILTER (WHERE state = 'held') AS held,
				count(*) FILTER (WHERE state = 'failed') AS failed,
				extract(epoch FROM now() - min(next_at) FILTER (WHERE state = 'waiting' AND next_at <= now()))::float8 AS oldest
			FROM pipeline_items WHERE run_id = $1 GROUP BY stage
		) i ON i.stage = s.stage
		ORDER BY 1`, runID)
	if err != nil {
		return v, err
	}
	v.Stages, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (StageCount, error) {
		var c StageCount
		err := r.Scan(&c.Stage, &c.Waiting, &c.Working, &c.Done, &c.Skipped, &c.Held, &c.Failed,
			&c.Entered, &c.Retried, &c.Waited, &c.BusyMS, &c.Errors, &c.OldestWaitingSeconds)
		return c, err
	})
	return v, err
}

const notifyChannel = "outflare_pipeline"

// notify wakes idle stage workers; delivered on commit inside a transaction.
func notify(ctx context.Context, q Querier) error {
	_, err := q.Exec(ctx, `SELECT pg_notify($1, '')`, notifyChannel)
	return err
}

// FinishRun marks a run finished once its feature is done with it (all
// items settled and any review over). Its items stay until Prune.
func FinishRun(ctx context.Context, q Querier, workspaceID, runID string) error {
	_, err := q.Exec(ctx, `UPDATE pipeline_runs SET state = 'finished', updated_at = now(), finished_at = now()
		WHERE id = $1 AND workspace_id = $2 AND state IN ('running', 'paused')`, runID, workspaceID)
	return err
}

// Prune deletes the items of runs that ended more than keep ago. Their
// per-stage summaries stay, so a run's story outlives its items.
func Prune(ctx context.Context, q Querier, keep time.Duration) (int64, error) {
	tag, err := q.Exec(ctx, `DELETE FROM pipeline_items WHERE run_id IN (
		SELECT id FROM pipeline_runs WHERE state IN ('finished', 'cancelled') AND finished_at < now() - $1::interval)`, interval(keep))
	return tag.RowsAffected(), err
}
