// Package jobs is a durable job queue on Postgres.
//
// Jobs are rows in the jobs table. Enqueue can run inside the same transaction
// as the domain write that caused it, so a job exists if and only if that write
// committed. Workers claim jobs with FOR UPDATE SKIP LOCKED, and enqueues send a
// NOTIFY so idle workers wake immediately instead of waiting for the next poll.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

const notifyChannel = "outflare_jobs"

// Querier is satisfied by *pgxpool.Pool, *pgx.Conn and pgx.Tx.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// NewJob describes a job to enqueue.
type NewJob struct {
	Kind           string
	Payload        any       // marshalled to JSON
	WorkspaceID    *string   // nil for system jobs
	RunAt          time.Time // zero means now
	IdempotencyKey string    // optional; a second enqueue with the same kind and key returns the first job
	MaxAttempts    int       // zero means 10
}

// Job is a claimed job handed to a handler.
type Job struct {
	ID          int64
	Kind        string
	Payload     json.RawMessage
	WorkspaceID *string
	Attempt     int // 1 on the first run
	MaxAttempts int

	waited time.Duration // from run_at to the claim, for metrics
}

// Handler processes one job. Returning an error schedules a retry with backoff
// until MaxAttempts is reached, after which the job is marked dead.
type Handler func(ctx context.Context, job Job) error

// ErrPermanent wraps an error that should not be retried.
var ErrPermanent = errors.New("permanent job failure")

// Enqueue inserts a job and returns its id.
func Enqueue(ctx context.Context, q Querier, j NewJob) (int64, error) {
	if j.Kind == "" {
		return 0, fmt.Errorf("job kind is required")
	}
	payload, err := json.Marshal(j.Payload)
	if err != nil {
		return 0, fmt.Errorf("marshal payload: %w", err)
	}
	if j.Payload == nil {
		payload = []byte(`{}`)
	}
	runAt := j.RunAt
	if runAt.IsZero() {
		runAt = time.Now()
	}
	maxAttempts := j.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 10
	}
	var key *string
	if j.IdempotencyKey != "" {
		key = &j.IdempotencyKey
	}

	var id int64
	err = q.QueryRow(ctx, `
		INSERT INTO jobs (kind, payload, workspace_id, run_at, max_attempts, idempotency_key)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (kind, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
		RETURNING id`,
		j.Kind, payload, j.WorkspaceID, runAt, maxAttempts, key,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = q.QueryRow(ctx, `SELECT id FROM jobs WHERE kind = $1 AND idempotency_key = $2`, j.Kind, key).Scan(&id)
		return id, err
	}
	if err != nil {
		return 0, err
	}
	// Delivered on commit when q is a transaction.
	if _, err := q.Exec(ctx, `SELECT pg_notify($1, '')`, notifyChannel); err != nil {
		return 0, err
	}
	return id, nil
}

// Queue runs registered handlers against the jobs table.
type Queue struct {
	pool     *pgxpool.Pool
	log      *slog.Logger
	handlers map[string]Handler
	workerID string

	// PollInterval is the longest an idle worker waits before checking for
	// due jobs (scheduled jobs and missed notifications). Default 1s.
	PollInterval time.Duration
	// LeaseTimeout is how long a running job may go without its worker
	// renewing it before it is assumed lost (crashed worker) and requeued.
	// Workers renew every third of it while a handler runs, so a handler
	// may take longer. Default 5m.
	LeaseTimeout time.Duration
	// Backoff returns the delay before retry number attempt (1-based).
	Backoff func(attempt int) time.Duration
	// Only, when set, limits this queue to these kinds; Skip leaves these
	// kinds to another queue. A second queue with its own workers keeps
	// urgent work (password resets) from waiting behind bulk work.
	Only []string
	Skip []string
}

func New(pool *pgxpool.Pool, log *slog.Logger) *Queue {
	host, _ := os.Hostname()
	return &Queue{
		pool:         pool,
		log:          log,
		handlers:     map[string]Handler{},
		workerID:     fmt.Sprintf("%s-%d", host, os.Getpid()),
		PollInterval: time.Second,
		LeaseTimeout: 5 * time.Minute,
		Backoff:      DefaultBackoff,
	}
}

// DefaultBackoff is exponential from 5s, capped at one hour.
func DefaultBackoff(attempt int) time.Duration {
	d := 5 * time.Second
	for i := 1; i < attempt && d < time.Hour; i++ {
		d *= 2
	}
	return min(d, time.Hour)
}

// Retired lists job kinds whose handler was removed. A leftover job of
// one, enqueued by the old version during a deploy, is closed quietly
// instead of dying as an error. Its removal migration deletes the rest.
var Retired = map[string]bool{
	"warmup.send":  true, // Outflare's own warm-up, removed in migration 0091
	"warmup.check": true,
}

// Handle registers the handler for a job kind. Call before Run.
func (q *Queue) Handle(kind string, h Handler) { q.handlers[kind] = h }

// Run processes jobs with up to concurrency handlers at once until ctx ends.
// It returns after in-flight jobs finish.
func (q *Queue) Run(ctx context.Context, concurrency int) error {
	if concurrency <= 0 {
		return nil
	}
	wake := make(chan struct{}, 1)
	go q.listen(ctx, wake)

	slots := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()

	lastReap := time.Time{}
	for {
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(lastReap) > q.LeaseTimeout/4 {
			q.reap(ctx)
			lastReap = time.Now()
		}

		free := concurrency - len(slots)
		var claimed []Job
		if free > 0 {
			var err error
			claimed, err = q.claim(ctx, free)
			if err != nil && ctx.Err() == nil {
				q.log.Error("claim jobs", "err", err)
			}
		}
		for _, job := range claimed {
			slots <- struct{}{}
			wg.Add(1)
			go func(job Job) {
				defer wg.Done()
				defer func() { <-slots; signal(wake) }()
				q.execute(ctx, job)
			}(job)
		}
		if len(claimed) == free && free > 0 {
			continue // likely more work waiting
		}

		timer := time.NewTimer(q.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-wake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// listen forwards Postgres notifications to wake. On connection loss it backs
// off and reconnects; polling covers the gap.
func (q *Queue) listen(ctx context.Context, wake chan struct{}) {
	for ctx.Err() == nil {
		conn, err := q.pool.Acquire(ctx)
		if err != nil {
			sleep(ctx, time.Second)
			continue
		}
		_, err = conn.Exec(ctx, "LISTEN "+notifyChannel)
		for err == nil {
			_, err = conn.Conn().WaitForNotification(ctx)
			if err == nil {
				signal(wake)
			}
		}
		// The connection may still be subscribed; drop it rather than return it to the pool.
		conn.Hijack().Close(context.Background()) //nolint:errcheck
		if ctx.Err() == nil {
			sleep(ctx, time.Second)
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (q *Queue) claim(ctx context.Context, limit int) ([]Job, error) {
	rows, err := q.pool.Query(ctx, `
		UPDATE jobs SET status = 'running', attempts = attempts + 1,
			locked_by = $2, locked_at = now(), updated_at = now()
		WHERE id IN (
			SELECT id FROM jobs
			WHERE status = 'queued' AND run_at <= now()
			  AND (cardinality($3::text[]) = 0 OR kind = ANY($3))
			  AND NOT (kind = ANY($4))
			  AND kind = ANY($5)
			ORDER BY run_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, kind, payload, workspace_id::text, attempts, max_attempts, extract(epoch FROM now() - run_at)::float8`,
		limit, q.workerID, nonNil(q.Only), nonNil(q.Skip), q.kinds())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Job, error) {
		var j Job
		var waited float64
		err := r.Scan(&j.ID, &j.Kind, &j.Payload, &j.WorkspaceID, &j.Attempt, &j.MaxAttempts, &waited)
		j.waited = time.Duration(waited * float64(time.Second))
		return j, err
	})
}

// kinds lists the job kinds this process can finish: those with a handler,
// plus retired ones it closes. A job of any other kind (a Cloud-only kind
// while an engine-only process runs during a deploy) stays queued for a
// process that has its handler, instead of dying here.
func (q *Queue) kinds() []string {
	kinds := make([]string, 0, len(q.handlers)+len(Retired))
	for k := range q.handlers {
		kinds = append(kinds, k)
	}
	for k := range Retired {
		if _, ok := q.handlers[k]; !ok {
			kinds = append(kinds, k)
		}
	}
	return kinds
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (q *Queue) execute(ctx context.Context, job Job) {
	log := q.log.With("job_id", job.ID, "kind", job.Kind, "attempt", job.Attempt)
	h, ok := q.handlers[job.Kind]
	if !ok && Retired[job.Kind] {
		fctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		log.Info("job kind retired, closing it")
		if _, err := q.pool.Exec(fctx, `UPDATE jobs SET status = 'dead', locked_by = NULL, locked_at = NULL, last_error = 'job kind retired', updated_at = now() WHERE id = $1`, job.ID); err != nil {
			log.Error("close retired job", "err", err)
		}
		return
	}
	var err error
	done := metrics.JobStarted(job.Kind, job.waited)
	// What the job records (audit actions) is sent only if it succeeds.
	hctx, release := track.Hold(ctx)
	hctx, stopRenew := q.keepLease(hctx, log, job)
	if !ok {
		err = fmt.Errorf("%w: no handler for kind %q", ErrPermanent, job.Kind)
	} else {
		err = runHandler(hctx, h, job)
	}
	if stopRenew() && err == nil {
		err = errLeaseLost
	}
	ev := track.Event{Surface: "job", ActorType: "system", Props: map[string]any{"kind": job.Kind, "job_id": job.ID, "attempt": job.Attempt}}
	if job.WorkspaceID != nil {
		ev.WorkspaceID = *job.WorkspaceID
	}
	release(err == nil, ev)

	// Finish with a fresh context so shutdown does not strand the row as running.
	fctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err == nil {
		done(metrics.JobSucceeded)
		if _, err := q.pool.Exec(fctx, `UPDATE jobs SET status = 'succeeded', locked_by = NULL, locked_at = NULL, last_error = NULL, updated_at = now() WHERE id = $1 AND `+ownLease, job.ID, job.Attempt); err != nil {
			log.Error("mark job succeeded", "err", err)
		}
		return
	}

	ev.Outcome, ev.ErrorCode = track.Error, jobErrorCode(err)
	if errors.Is(err, ErrPermanent) || job.Attempt >= job.MaxAttempts {
		ev.Name = "job.dead"
		done(metrics.JobDead)
		track.Emit(ctx, ev)
		log.Error("job dead", "err", err)
		_, uerr := q.pool.Exec(fctx, `UPDATE jobs SET status = 'dead', locked_by = NULL, locked_at = NULL, last_error = $3, updated_at = now() WHERE id = $1 AND `+ownLease, job.ID, job.Attempt, err.Error())
		if uerr != nil {
			log.Error("mark job dead", "err", uerr)
		}
		return
	}

	delay := q.Backoff(job.Attempt)
	ev.Name = "job.failed"
	done(metrics.JobRetried)
	track.Emit(ctx, ev)
	log.Warn("job failed, retrying", "err", err, "retry_in", delay)
	_, uerr := q.pool.Exec(fctx, `UPDATE jobs SET status = 'queued', locked_by = NULL, locked_at = NULL, last_error = $3, run_at = now() + $4::interval, updated_at = now() WHERE id = $1 AND `+ownLease,
		job.ID, job.Attempt, err.Error(), fmt.Sprintf("%d milliseconds", delay.Milliseconds()))
	if uerr != nil {
		log.Error("requeue job", "err", uerr)
	}
}

// ownLease limits a finishing update to the claim that ran it: a job
// reaped and claimed again has a higher attempt count. $2 is the attempt.
const ownLease = `status = 'running' AND attempts = $2`

// errLeaseLost is a job that finished after another worker took it over.
var errLeaseLost = errors.New("job lease lost to another worker")

// keepLease renews a running job's lease every third of LeaseTimeout, so
// a handler may run for as long as it needs without being reaped. If the
// lease was lost anyway (the database was unreachable for a whole lease
// and another worker took the job), it cancels ctx. stop ends renewal and
// says whether the lease was lost.
func (q *Queue) keepLease(ctx context.Context, log *slog.Logger, job Job) (context.Context, func() (lost bool)) {
	ctx, cancel := context.WithCancelCause(ctx)
	var lost atomic.Bool
	quit := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		every := max(q.LeaseTimeout/3, 10*time.Millisecond)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-quit:
				return
			case <-ctx.Done():
				return
			case <-t.C:
			}
			rctx, rcancel := context.WithTimeout(context.Background(), every)
			tag, err := q.pool.Exec(rctx, `UPDATE jobs SET locked_at = now() WHERE id = $1 AND `+ownLease, job.ID, job.Attempt)
			rcancel()
			switch {
			case err != nil:
				log.Warn("renew job lease", "err", err)
			case tag.RowsAffected() == 0:
				lost.Store(true)
				log.Error("job lease lost to another worker, stopping it")
				cancel(errLeaseLost)
				return
			}
		}
	}()
	return ctx, func() bool {
		close(quit)
		<-finished
		cancel(nil)
		return lost.Load()
	}
}

// jobErrorCode classifies a job's error for its event; the error itself,
// which can quote customer data, goes only to the log.
func jobErrorCode(err error) string {
	switch {
	case strings.HasPrefix(err.Error(), "panic: "):
		return "panic"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ErrPermanent):
		return "permanent"
	case errors.Is(err, errLeaseLost):
		return "lease_lost"
	}
	return "job_error"
}

func runHandler(ctx context.Context, h Handler, job Job) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		}
	}()
	return h(ctx, job)
}

// reap requeues jobs whose worker stopped renewing them (a crash mid-job).
func (q *Queue) reap(ctx context.Context) {
	rows, err := q.pool.Query(ctx, `
		UPDATE jobs SET status = 'queued', locked_by = NULL, locked_at = NULL,
			last_error = 'lease expired', updated_at = now()
		WHERE status = 'running' AND locked_at < now() - $1::interval
		RETURNING kind`,
		fmt.Sprintf("%d milliseconds", q.LeaseTimeout.Milliseconds()))
	var kinds []string
	if err == nil {
		kinds, err = pgx.CollectRows(rows, pgx.RowTo[string])
	}
	if err != nil {
		if ctx.Err() == nil {
			track.Fail(ctx, q.log, track.Event{Name: "job.reap_failed", Surface: "job", ActorType: "system"}, "unavailable", err)
		}
		return
	}
	for _, kind := range kinds {
		metrics.JobReaped(kind)
	}
	if n := len(kinds); n > 0 {
		q.log.Warn("requeued stale jobs", "count", n)
		track.Emit(ctx, track.Event{Name: "job.reaped", Surface: "job", ActorType: "system", Outcome: track.Error, ErrorCode: "internal", Props: map[string]any{"count": n}})
	}
}
