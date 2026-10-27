package pipeline

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/db"
	"github.com/outflareai/outflare/engine/internal/track"
)

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
	if _, err := pool.Exec(ctx, `TRUNCATE pipeline_runs, pipeline_items, pipeline_stage_stats, pipeline_limits`); err != nil {
		t.Fatal(err)
	}
	return pool
}

func workspace(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var ws string
	if err := pool.QueryRow(context.Background(), `INSERT INTO workspaces (name, slug) VALUES ('Acme', 'acme-' || substr(gen_random_uuid()::text, 1, 8)) RETURNING id::text`).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	return ws
}

func newRun(t *testing.T, pool *pgxpool.Pool, ws, pipeline string, n int, stage string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := Start(ctx, pool, ws, pipeline, id); err != nil {
		t.Fatal(err)
	}
	items := make([]NewItem, n)
	for i := range items {
		items[i] = NewItem{Ref: fmt.Sprintf("acme-%03d.example", i), Stage: stage}
	}
	added, err := Add(ctx, pool, ws, id, items)
	if err != nil || added != n {
		t.Fatalf("added %d of %d: %v", added, n, err)
	}
	if again, err := Add(ctx, pool, ws, id, items[:min(3, n)]); err != nil || again != 0 {
		t.Fatalf("a ref already in the run was added again: %d %v", again, err)
	}
	return id
}

func quietRunner(pool *pgxpool.Pool) *Runner {
	r := NewRunner(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.PollInterval = 20 * time.Millisecond
	return r
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for condition")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func stageOf(v RunView, name string) StageCount {
	for _, s := range v.Stages {
		if s.Stage == name {
			return s
		}
	}
	return StageCount{}
}

func TestRunMovesThroughStages(t *testing.T) {
	pool := testPool(t)
	ws := workspace(t, pool)
	events := track.Capture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var seen sync.Map
	r := quietRunner(pool)
	r.Register(Stage{Pipeline: "test", Name: "read", Batch: 7, Work: func(ctx context.Context, items []Item) ([]Result, error) {
		var out []Result
		for _, it := range items {
			if prev, dup := seen.LoadOrStore("read:"+it.Ref, true); dup && prev.(bool) {
				t.Errorf("%s read twice", it.Ref)
			}
			if it.Ref[len(it.Ref)-9] == '7' { // refs ending in 7: skip
				out = append(out, Result{ItemID: it.ID, Outcome: Skip, Code: "no_website", Reason: "no site"})
				continue
			}
			out = append(out, Result{ItemID: it.ID, Outcome: Advance, Next: "qualify"})
		}
		return out, nil
	}})
	r.Register(Stage{Pipeline: "test", Name: "qualify", Work: func(ctx context.Context, items []Item) ([]Result, error) {
		var out []Result
		for _, it := range items {
			out = append(out, Result{ItemID: it.ID, Outcome: Done})
		}
		return out, nil
	}})
	var settled atomic.Int64
	r.OnSettled("test", func(ctx context.Context, workspaceID, runID string) error {
		if workspaceID != ws {
			t.Errorf("settled in %s", workspaceID)
		}
		settled.Add(1)
		return nil
	})
	run := newRun(t, pool, ws, "test", 60, "read")
	go r.Run(ctx)

	waitFor(t, func() bool { return settled.Load() > 0 })
	v, err := View(ctx, pool, ws, run)
	if err != nil {
		t.Fatal(err)
	}
	read, qualify := stageOf(v, "read"), stageOf(v, "qualify")
	if read.Entered != 60 || read.Skipped != 6 || read.Errors["no_website"] != 6 || read.Waiting+read.Working != 0 {
		t.Fatalf("read: %+v", read)
	}
	if qualify.Entered != 54 || qualify.Done != 54 {
		t.Fatalf("qualify: %+v", qualify)
	}
	if _, err := View(ctx, pool, workspace(t, pool), run); err == nil {
		t.Fatal("another workspace read the run")
	}
	for _, e := range events() {
		if e.Name == "pipeline.stage_failed" {
			t.Fatalf("no failures, but got %+v", e)
		}
	}
}

func TestClaimSharesBetweenWorkspaces(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	big, small := workspace(t, pool), workspace(t, pool)
	newRun(t, pool, big, "test", 200, "read")
	time.Sleep(10 * time.Millisecond) // the small run's items are newer
	newRun(t, pool, small, "test", 5, "read")

	r := quietRunner(pool)
	s := &Stage{Pipeline: "test", Name: "read", Batch: 10, PerWorkspace: 8, Work: func(context.Context, []Item) ([]Result, error) { return nil, nil }}
	if err := s.defaults(); err != nil {
		t.Fatal(err)
	}
	items, err := r.claim(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	per := map[string]int{}
	for _, it := range items {
		per[it.WorkspaceID]++
	}
	if per[small] != 5 || per[big] != 5 {
		t.Fatalf("first batch should split between workspaces: %v", per)
	}
	// The big workspace has 5 in progress and a cap of 8.
	items, err = r.claim(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("the per-workspace cap let %d more in", len(items))
	}
}

func TestOutcomes(t *testing.T) {
	pool := testPool(t)
	ws := workspace(t, pool)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := track.Capture(t)

	var calls sync.Map
	r := quietRunner(pool)
	r.Register(Stage{Pipeline: "test", Name: "check", MaxAttempts: 2, Backoff: func(int) time.Duration { return 10 * time.Millisecond },
		Work: func(ctx context.Context, items []Item) ([]Result, error) {
			var out []Result
			for _, it := range items {
				n, _ := calls.LoadOrStore(it.Ref, new(atomic.Int64))
				c := n.(*atomic.Int64).Add(1)
				switch it.Ref {
				case "acme-000.example": // fails twice, then gives up
					out = append(out, Result{ItemID: it.ID, Outcome: Retry, Code: "connector_unavailable"})
				case "acme-001.example": // waits three times, which never counts
					if c <= 3 {
						out = append(out, Result{ItemID: it.ID, Outcome: Wait, Code: "rate_limited", RetryAfter: 10 * time.Millisecond})
					} else {
						out = append(out, Result{ItemID: it.ID, Outcome: Done})
					}
				case "acme-002.example":
					out = append(out, Result{ItemID: it.ID, Outcome: Hold, Code: "needs_review"})
				case "acme-003.example": // left out of the results: retried
					if c > 1 {
						out = append(out, Result{ItemID: it.ID, Outcome: Done})
					}
				}
			}
			return out, nil
		}})
	run := newRun(t, pool, ws, "test", 4, "check")
	go r.Run(ctx)

	waitFor(t, func() bool {
		v, _ := View(ctx, pool, ws, run)
		c := stageOf(v, "check")
		return c.Failed == 1 && c.Done == 2 && c.Held == 1
	})
	v, _ := View(ctx, pool, ws, run)
	c := stageOf(v, "check")
	if c.Waited != 3 || c.Errors["connector_unavailable"] != 2 || c.Errors["no_result"] != 1 {
		t.Fatalf("stats: %+v", c)
	}
	var failed bool
	for _, e := range events() {
		failed = failed || (e.Name == "pipeline.stage_failed" && e.WorkspaceID == ws)
	}
	if !failed {
		t.Fatal("no pipeline.stage_failed event")
	}

	// A person sends the held item on.
	if n, err := Release(ctx, pool, ws, run, []string{"acme-002.example"}, "check", ""); err != nil || n != 1 {
		t.Fatalf("release: %d %v", n, err)
	}
	waitFor(t, func() bool {
		var calls2 int64
		if n, ok := calls.Load("acme-002.example"); ok {
			calls2 = n.(*atomic.Int64).Load()
		}
		return calls2 == 2
	})
}

func TestPauseAndCancel(t *testing.T) {
	pool := testPool(t)
	ws := workspace(t, pool)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var paused atomic.Bool
	paused.Store(true)
	var worked atomic.Int64
	r := quietRunner(pool)
	r.Register(Stage{Pipeline: "test", Name: "ask", Batch: 1, Concurrency: 1, Work: func(ctx context.Context, items []Item) ([]Result, error) {
		worked.Add(1)
		if paused.Load() {
			return []Result{{ItemID: items[0].ID, Outcome: Pause, Code: "ai_budget_reached", Reason: "monthly AI limit"}}, nil
		}
		return []Result{{ItemID: items[0].ID, Outcome: Done}}, nil
	}})
	run := newRun(t, pool, ws, "test", 5, "ask")
	go r.Run(ctx)

	waitFor(t, func() bool {
		v, _ := View(ctx, pool, ws, run)
		return v.State == RunPaused && v.PauseCode != nil && *v.PauseCode == "ai_budget_reached"
	})
	time.Sleep(200 * time.Millisecond)
	if worked.Load() != 1 {
		t.Fatalf("a paused run kept working: %d batches", worked.Load())
	}
	paused.Store(false)
	if err := ResumeRun(ctx, pool, ws, run); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		v, _ := View(ctx, pool, ws, run)
		return stageOf(v, "ask").Done == 5
	})

	other := newRun(t, pool, ws, "test", 3, "never")
	if err := CancelRun(ctx, pool, ws, other); err != nil {
		t.Fatal(err)
	}
	v, _ := View(ctx, pool, ws, other)
	if v.State != RunCancelled || stageOf(v, "never").Skipped != 3 {
		t.Fatalf("cancel: %+v", v)
	}
	if _, err := Add(ctx, pool, ws, other, []NewItem{{Ref: "late.example", Stage: "never"}}); err != ErrNotRunning {
		t.Fatalf("added to a cancelled run: %v", err)
	}
}

func TestStaleLeaseIsReaped(t *testing.T) {
	pool := testPool(t)
	ws := workspace(t, pool)
	ctx := context.Background()
	run := newRun(t, pool, ws, "test", 1, "slow")
	r := quietRunner(pool)
	s := &Stage{Pipeline: "test", Name: "slow", Lease: time.Second, Work: func(context.Context, []Item) ([]Result, error) { return nil, nil }}
	if err := s.defaults(); err != nil {
		t.Fatal(err)
	}
	items, err := r.claim(ctx, s)
	if err != nil || len(items) != 1 {
		t.Fatalf("claim: %v %v", items, err)
	}
	// The worker crashed: its lease runs out.
	if _, err := pool.Exec(ctx, `UPDATE pipeline_items SET lease_until = now() - interval '1 second' WHERE run_id = $1`, run); err != nil {
		t.Fatal(err)
	}
	r.reap(ctx, s)
	again, err := r.claim(ctx, s)
	if err != nil || len(again) != 1 || again[0].Attempt != 2 {
		t.Fatalf("reclaim: %+v %v", again, err)
	}
	// The first worker's late results land nowhere.
	if _, err := r.apply(ctx, s, items, []Result{{ItemID: items[0].ID, Outcome: Done}}, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	v, _ := View(ctx, pool, ws, run)
	if c := stageOf(v, "slow"); c.Working != 1 || c.Done != 0 {
		t.Fatalf("a stale worker overwrote the new claim: %+v", c)
	}
}

func TestLimits(t *testing.T) {
	pool := testPool(t)
	ws := workspace(t, pool)
	ctx := context.Background()
	l := Limits{Q: pool}
	key := Key("integration", "geonode-test")
	rate := Rate{PerMinute: 60, Burst: 2}

	for i := 0; i < 2; i++ {
		if wait, err := l.Take(ctx, ws, key, rate); err != nil || wait != 0 {
			t.Fatalf("call %d: wait %v, %v", i, wait, err)
		}
	}
	wait, err := l.Take(ctx, ws, key, rate)
	if err != nil || wait <= 0 || wait > 1100*time.Millisecond {
		t.Fatalf("third call should wait about a second: %v %v", wait, err)
	}

	// A 429 halves the rate.
	if err := l.Report(ctx, key, RateLimited); err != nil {
		t.Fatal(err)
	}
	wait, _ = l.Take(ctx, ws, key, rate)
	if wait < 1500*time.Millisecond {
		t.Fatalf("after a rate limit answer the wait should double: %v", wait)
	}

	// Five failures open the breaker.
	if _, err := pool.Exec(ctx, `UPDATE pipeline_limits SET tokens = 2, slow_until = NULL WHERE key = $1`, key); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < breakerFailures; i++ {
		if err := l.Report(ctx, key, Failed); err != nil {
			t.Fatal(err)
		}
	}
	wait, _ = l.Take(ctx, ws, key, rate)
	if wait < 50*time.Second {
		t.Fatalf("an open breaker should wait about a minute: %v", wait)
	}
	// The pause ends: one probe goes, the next caller waits for it.
	if _, err := pool.Exec(ctx, `UPDATE pipeline_limits SET reopen_at = now() - interval '1 second', tokens = 2 WHERE key = $1`, key); err != nil {
		t.Fatal(err)
	}
	if wait, _ = l.Take(ctx, ws, key, rate); wait != 0 {
		t.Fatalf("the probe should go: %v", wait)
	}
	if wait, _ = l.Take(ctx, ws, key, rate); wait == 0 {
		t.Fatal("a second call went while the probe was out")
	}
	// The probe fails: open again, for twice as long.
	if err := l.Report(ctx, key, Failed); err != nil {
		t.Fatal(err)
	}
	var state string
	var secs int
	if err := pool.QueryRow(ctx, `SELECT breaker, open_seconds FROM pipeline_limits WHERE key = $1`, key).Scan(&state, &secs); err != nil || state != "open" || secs != 120 {
		t.Fatalf("after a failed probe: %s %d %v", state, secs, err)
	}
	if err := l.Report(ctx, key, Succeeded); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT breaker, open_seconds FROM pipeline_limits WHERE key = $1`, key).Scan(&state, &secs); err != nil || state != "closed" || secs != 60 {
		t.Fatalf("after a success: %s %d %v", state, secs, err)
	}
}

func TestFinishAndPrune(t *testing.T) {
	pool := testPool(t)
	ws := workspace(t, pool)
	ctx := context.Background()
	run := newRun(t, pool, ws, "test", 4, "read")
	live := newRun(t, pool, ws, "test", 2, "read")
	if err := FinishRun(ctx, pool, ws, run); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE pipeline_runs SET finished_at = now() - interval '40 days' WHERE id = $1`, run); err != nil {
		t.Fatal(err)
	}
	if n, err := Prune(ctx, pool, Keep); err != nil || n != 4 {
		t.Fatalf("pruned %d: %v", n, err)
	}
	v, err := View(ctx, pool, ws, run)
	if err != nil || v.State != RunFinished || stageOf(v, "read").Entered != 4 {
		t.Fatalf("the summary should outlive the items: %+v %v", v, err)
	}
	if v, _ := View(ctx, pool, ws, live); stageOf(v, "read").Waiting != 2 {
		t.Fatal("a live run lost its items")
	}
}
