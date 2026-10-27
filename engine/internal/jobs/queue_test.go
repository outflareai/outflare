package jobs_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/db"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// testPool connects to TEST_DATABASE_URL and resets the jobs table. Tests are
// skipped when it is not set.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE jobs`); err != nil {
		t.Fatal(err)
	}
	return pool
}

func newQueue(pool *pgxpool.Pool) *jobs.Queue {
	q := jobs.New(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 50 * time.Millisecond
	q.Backoff = func(int) time.Duration { return 10 * time.Millisecond }
	return q
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for condition")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func countStatus(t *testing.T, pool *pgxpool.Pool, status string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE status = $1`, status).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRunsEveryJobOnce(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var runs atomic.Int64
	count := func(ctx context.Context, j jobs.Job) error {
		runs.Add(1)
		return nil
	}
	q1, q2 := newQueue(pool), newQueue(pool)
	q1.Handle("count", count)
	q2.Handle("count", count)

	const n = 200
	for i := 0; i < n; i++ {
		if _, err := jobs.Enqueue(ctx, pool, jobs.NewJob{Kind: "count"}); err != nil {
			t.Fatal(err)
		}
	}
	// Two queues compete for the same rows; SKIP LOCKED must keep runs exact.
	go q1.Run(ctx, 8)
	go q2.Run(ctx, 8)

	waitFor(t, func() bool { return countStatus(t, pool, "succeeded") == n })
	if got := runs.Load(); got != n {
		t.Fatalf("handler ran %d times, want %d", got, n)
	}
}

func TestRetriesThenSucceeds(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q := newQueue(pool)
	q.Handle("flaky", func(ctx context.Context, j jobs.Job) error {
		if j.Attempt < 3 {
			return errors.New("not yet")
		}
		return nil
	})
	id, err := jobs.Enqueue(ctx, pool, jobs.NewJob{Kind: "flaky"})
	if err != nil {
		t.Fatal(err)
	}
	go q.Run(ctx, 2)

	waitFor(t, func() bool { return countStatus(t, pool, "succeeded") == 1 })
	var attempts int
	pool.QueryRow(ctx, `SELECT attempts FROM jobs WHERE id = $1`, id).Scan(&attempts)
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestDeadAfterMaxAttemptsAndPermanent(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q := newQueue(pool)
	q.Handle("always-fails", func(ctx context.Context, j jobs.Job) error { return errors.New("boom") })
	q.Handle("permanent", func(ctx context.Context, j jobs.Job) error {
		return errors.Join(jobs.ErrPermanent, errors.New("bad input"))
	})
	q.Handle("panics", func(ctx context.Context, j jobs.Job) error { panic("oops") })

	jobs.Enqueue(ctx, pool, jobs.NewJob{Kind: "always-fails", MaxAttempts: 3})
	jobs.Enqueue(ctx, pool, jobs.NewJob{Kind: "permanent"})
	jobs.Enqueue(ctx, pool, jobs.NewJob{Kind: "panics", MaxAttempts: 1})
	go q.Run(ctx, 4)

	waitFor(t, func() bool { return countStatus(t, pool, "dead") == 3 })
	var attempts int
	pool.QueryRow(ctx, `SELECT attempts FROM jobs WHERE kind = 'always-fails'`).Scan(&attempts)
	if attempts != 3 {
		t.Fatalf("always-fails attempts = %d, want 3", attempts)
	}
	pool.QueryRow(ctx, `SELECT attempts FROM jobs WHERE kind = 'permanent'`).Scan(&attempts)
	if attempts != 1 {
		t.Fatalf("permanent attempts = %d, want 1", attempts)
	}
}

// A process without a kind's handler (an engine-only worker during a Cloud
// deploy) leaves its jobs queued for a process that has it.
func TestLeavesKindsWithoutHandler(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	id, _ := jobs.Enqueue(ctx, pool, jobs.NewJob{Kind: "elsewhere"})
	jobs.Enqueue(ctx, pool, jobs.NewJob{Kind: "here"})
	q := newQueue(pool)
	q.Handle("here", func(ctx context.Context, j jobs.Job) error { return nil })
	go q.Run(ctx, 2)

	waitFor(t, func() bool { return countStatus(t, pool, "succeeded") == 1 })
	time.Sleep(200 * time.Millisecond) // a few more polls
	var status string
	var attempts int
	pool.QueryRow(ctx, `SELECT status, attempts FROM jobs WHERE id = $1`, id).Scan(&status, &attempts)
	if status != "queued" || attempts != 0 {
		t.Fatalf("unhandled job = %s after %d attempts, want queued after 0", status, attempts)
	}

	other := newQueue(pool)
	other.Handle("elsewhere", func(ctx context.Context, j jobs.Job) error { return nil })
	go other.Run(ctx, 1)
	waitFor(t, func() bool { return countStatus(t, pool, "succeeded") == 2 })
}

func TestRetiredKindClosesQuietly(t *testing.T) {
	pool := testPool(t)
	events := track.Capture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jobs.Enqueue(ctx, pool, jobs.NewJob{Kind: "warmup.send"})
	go newQueue(pool).Run(ctx, 1)

	waitFor(t, func() bool { return countStatus(t, pool, "dead") == 1 })
	var last string
	pool.QueryRow(ctx, `SELECT last_error FROM jobs WHERE kind = 'warmup.send'`).Scan(&last)
	if last != "job kind retired" {
		t.Fatalf("last_error = %q", last)
	}
	for _, e := range events() {
		if e.Name == "job.dead" || e.Name == "job.failed" {
			t.Fatalf("retired job emitted %s", e.Name)
		}
	}
}

func TestIdempotentEnqueue(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	a, err := jobs.Enqueue(ctx, pool, jobs.NewJob{Kind: "send", IdempotencyKey: "msg-1"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := jobs.Enqueue(ctx, pool, jobs.NewJob{Kind: "send", IdempotencyKey: "msg-1"})
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("second enqueue made a new job: %d vs %d", a, b)
	}
	c, _ := jobs.Enqueue(ctx, pool, jobs.NewJob{Kind: "send", IdempotencyKey: "msg-2"})
	if c == a {
		t.Fatal("different key reused the job")
	}
}

func TestScheduledJobWaits(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var ran atomic.Bool
	q := newQueue(pool)
	q.Handle("later", func(ctx context.Context, j jobs.Job) error { ran.Store(true); return nil })
	jobs.Enqueue(ctx, pool, jobs.NewJob{Kind: "later", RunAt: time.Now().Add(400 * time.Millisecond)})
	go q.Run(ctx, 1)

	time.Sleep(200 * time.Millisecond)
	if ran.Load() {
		t.Fatal("job ran before run_at")
	}
	waitFor(t, ran.Load)
}

func TestEnqueueInRolledBackTxLeavesNoJob(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: "x"}); err != nil {
		t.Fatal(err)
	}
	tx.Rollback(ctx)
	if n := countStatus(t, pool, "queued"); n != 0 {
		t.Fatalf("%d jobs survived rollback", n)
	}
}

func TestLongJobKeepsItsLease(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var runs atomic.Int64
	slow := func(ctx context.Context, j jobs.Job) error {
		runs.Add(1)
		select {
		case <-time.After(3 * time.Second):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	q1, q2 := newQueue(pool), newQueue(pool)
	for _, q := range []*jobs.Queue{q1, q2} {
		// Long enough that a renewal is never late under -race on a busy
		// CI runner, short enough that the job outlives three leases.
		q.LeaseTimeout = time.Second
		q.Handle("slow", slow)
	}
	if _, err := jobs.Enqueue(ctx, pool, jobs.NewJob{Kind: "slow"}); err != nil {
		t.Fatal(err)
	}
	// The job runs three leases long while both queues reap; renewal must
	// keep the second queue from taking it over.
	go q1.Run(ctx, 2)
	go q2.Run(ctx, 2)

	waitFor(t, func() bool { return countStatus(t, pool, "succeeded") == 1 })
	if got := runs.Load(); got != 1 {
		t.Fatalf("handler ran %d times, want 1", got)
	}
}

func TestLostLeaseStopsTheHandler(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stopped := make(chan error, 1)
	q := newQueue(pool)
	q.LeaseTimeout = 150 * time.Millisecond
	q.Handle("taken", func(ctx context.Context, j jobs.Job) error {
		// Another worker takes the job over, as if this one had stalled.
		if _, err := pool.Exec(context.Background(), `UPDATE jobs SET attempts = attempts + 1 WHERE id = $1`, j.ID); err != nil {
			return err
		}
		<-ctx.Done()
		stopped <- ctx.Err()
		return ctx.Err()
	})
	id, err := jobs.Enqueue(ctx, pool, jobs.NewJob{Kind: "taken"})
	if err != nil {
		t.Fatal(err)
	}
	go q.Run(ctx, 1)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler kept running after losing its lease")
	}
	// The takeover's row is left alone: still running, not marked by the loser.
	waitFor(t, func() bool {
		var status string
		pool.QueryRow(context.Background(), `SELECT status FROM jobs WHERE id = $1`, id).Scan(&status) //nolint:errcheck
		return status == "running"
	})
}

func TestReapCountsExpiredLeases(t *testing.T) {
	pool := testPool(t)
	events := track.Capture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	id, _ := jobs.Enqueue(ctx, pool, jobs.NewJob{Kind: "reap.test"})
	if _, err := pool.Exec(ctx, `UPDATE jobs SET status = 'running', locked_by = 'gone', locked_at = now() - interval '1 hour' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	before := reapedCount(t, "reap.test")

	var runs atomic.Int32
	q := newQueue(pool)
	q.Handle("reap.test", func(context.Context, jobs.Job) error { runs.Add(1); return nil })
	go q.Run(ctx, 1)

	waitFor(t, func() bool { return countStatus(t, pool, "succeeded") == 1 })
	if got := reapedCount(t, "reap.test") - before; got != 1 {
		t.Fatalf("outflare_jobs_reaped_total rose by %v, want 1", got)
	}
	var reaped bool
	for _, e := range events() {
		if e.Name == "job.reaped" && e.Props["count"] == 1 {
			reaped = true
		}
	}
	if !reaped {
		t.Fatal("no job.reaped event")
	}
}

// reapedCount reads outflare_jobs_reaped_total for one kind.
func reapedCount(t *testing.T, kind string) float64 {
	t.Helper()
	families, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != "outflare_jobs_reaped_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "kind" && l.GetValue() == kind {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}
