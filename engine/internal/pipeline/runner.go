package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Runner works on every registered stage. Run it in each worker process
// (OUTFLARE_ROLE all or worker); processes share the work through the
// database.
type Runner struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	// PollInterval is the longest an idle stage waits before looking for
	// due items (retries, waits, missed notifications). Default 1s.
	PollInterval time.Duration

	mu      sync.Mutex
	stages  []*Stage
	settled map[string]func(ctx context.Context, workspaceID, runID string) error
	wakes   []chan struct{}
}

// NewRunner makes a runner. Register stages before Run.
func NewRunner(pool *pgxpool.Pool, log *slog.Logger) *Runner {
	return &Runner{Pool: pool, Log: log, PollInterval: time.Second, settled: map[string]func(context.Context, string, string) error{}}
}

// Register adds a stage. It panics on a bad or repeated stage, at start-up.
func (r *Runner) Register(s Stage) {
	if err := s.defaults(); err != nil {
		panic(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, o := range r.stages {
		if o.Pipeline == s.Pipeline && o.Name == s.Name {
			panic(fmt.Sprintf("pipeline: stage %s/%s registered twice", s.Pipeline, s.Name))
		}
	}
	r.stages = append(r.stages, &s)
}

// OnSettled sets what runs when a run of pipeline has nothing waiting or
// being worked on any more: every item is done, skipped, held or failed.
// It is where a feature finishes its run or asks for a review. It can be
// called more than once for a run (two batches settling at once, a held
// item released and settled again), so it must be idempotent.
func (r *Runner) OnSettled(pipeline string, fn func(ctx context.Context, workspaceID, runID string) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.settled[pipeline] = fn
}

// Run works until ctx ends, then waits for batches in flight.
func (r *Runner) Run(ctx context.Context) {
	r.mu.Lock()
	stages := append([]*Stage(nil), r.stages...)
	r.wakes = make([]chan struct{}, len(stages))
	for i := range r.wakes {
		r.wakes[i] = make(chan struct{}, 1)
	}
	wakes := r.wakes
	r.mu.Unlock()
	if len(stages) == 0 {
		return
	}
	go r.listen(ctx, wakes)
	go r.prune(ctx)
	var wg sync.WaitGroup
	for i, s := range stages {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.loop(ctx, s, wakes[i])
		}()
	}
	wg.Wait()
}

// Keep is how long a run's items are kept after it ends.
const Keep = 30 * 24 * time.Hour

// prune deletes old runs' items every hour. Every worker does it; the
// deletes are idempotent and cheap when there is nothing to do.
func (r *Runner) prune(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n, err := Prune(ctx, r.Pool, Keep); err != nil && ctx.Err() == nil {
			r.Log.Warn("pipeline prune", "err", err)
		} else if n > 0 {
			r.Log.Info("pipeline pruned items of old runs", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (r *Runner) poll() time.Duration {
	if r.PollInterval <= 0 {
		return time.Second
	}
	return r.PollInterval
}

// listen wakes every stage on a notification. Polling covers any gap.
func (r *Runner) listen(ctx context.Context, wakes []chan struct{}) {
	for ctx.Err() == nil {
		conn, err := r.Pool.Acquire(ctx)
		if err != nil {
			sleep(ctx, time.Second)
			continue
		}
		_, err = conn.Exec(ctx, "LISTEN "+notifyChannel)
		for err == nil {
			_, err = conn.Conn().WaitForNotification(ctx)
			if err == nil {
				for _, w := range wakes {
					signal(w)
				}
			}
		}
		conn.Hijack().Close(context.Background()) //nolint:errcheck
		if ctx.Err() == nil {
			sleep(ctx, time.Second)
		}
	}
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
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

func (r *Runner) loop(ctx context.Context, s *Stage, wake chan struct{}) {
	slots := make(chan struct{}, s.Concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()
	lastReap := time.Time{}
	for ctx.Err() == nil {
		if time.Since(lastReap) > s.Lease/4 {
			r.reap(ctx, s)
			lastReap = time.Now()
		}
		claimed := false
		if len(slots) < s.Concurrency {
			items, err := r.claim(ctx, s)
			if err != nil && ctx.Err() == nil {
				r.Log.Error("pipeline claim", "pipeline", s.Pipeline, "stage", s.Name, "err", err)
			}
			if len(items) > 0 {
				claimed = true
				slots <- struct{}{}
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer func() { <-slots; signal(wake) }()
					r.work(ctx, s, items)
				}()
			}
		}
		if claimed && len(slots) < s.Concurrency {
			continue // likely more waiting
		}
		t := time.NewTimer(r.poll())
		select {
		case <-ctx.Done():
		case <-wake:
		case <-t.C:
		}
		t.Stop()
	}
}

// claim takes up to Batch due items of a stage from running runs, round
// robin across workspaces: each workspace's oldest items first, and never
// more than PerWorkspace in progress for one workspace.
func (r *Runner) claim(ctx context.Context, s *Stage) ([]Item, error) {
	rows, err := r.Pool.Query(ctx, `
		WITH busy AS (
			SELECT workspace_id, count(*) AS n FROM pipeline_items
			WHERE pipeline = $1 AND stage = $2 AND state = 'working' GROUP BY workspace_id
		), due AS (
			SELECT d.id, r.workspace_id, d.next_at,
				row_number() OVER (PARTITION BY r.workspace_id ORDER BY d.next_at) AS rn
			FROM pipeline_runs r
			CROSS JOIN LATERAL (
				SELECT id, next_at FROM pipeline_items
				WHERE run_id = r.id AND stage = $2 AND state = 'waiting' AND next_at <= now()
				ORDER BY next_at LIMIT $3
			) d
			WHERE r.pipeline = $1 AND r.state = 'running'
		), picked AS (
			SELECT d.id FROM due d LEFT JOIN busy b USING (workspace_id)
			WHERE d.rn + coalesce(b.n, 0) <= $4
			ORDER BY d.rn, d.next_at LIMIT $3
		)
		UPDATE pipeline_items i SET state = 'working', attempts = attempts + 1,
			lease_until = now() + $5::interval, updated_at = now()
		WHERE i.id IN (SELECT id FROM pipeline_items WHERE id IN (SELECT id FROM picked) AND state = 'waiting' FOR UPDATE SKIP LOCKED)
		RETURNING i.id, i.workspace_id, i.run_id, i.ref, i.stage, i.attempts, i.stage_entered_at,
			extract(epoch FROM now() - i.next_at)::float8`,
		s.Pipeline, s.Name, s.Batch, s.PerWorkspace, interval(s.Lease))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Item, error) {
		var it Item
		var waited float64
		err := row.Scan(&it.ID, &it.WorkspaceID, &it.RunID, &it.Ref, &it.Stage, &it.Attempt, &it.Entered, &waited)
		metrics.PipelineClaimed(s.Pipeline, s.Name, time.Duration(waited*float64(time.Second)))
		return it, err
	})
}

func interval(d time.Duration) string { return fmt.Sprintf("%d milliseconds", d.Milliseconds()) }

// reap puts back items whose worker stopped renewing them (a crash), or
// fails them once they used up their attempts.
func (r *Runner) reap(ctx context.Context, s *Stage) {
	tag, err := r.Pool.Exec(ctx, `
		UPDATE pipeline_items SET
			state = CASE WHEN attempts >= $3 THEN 'failed' ELSE 'waiting' END,
			next_at = now(), lease_until = NULL, error_code = 'lease_expired', updated_at = now()
		WHERE pipeline = $1 AND stage = $2 AND state = 'working' AND lease_until < now()`,
		s.Pipeline, s.Name, s.MaxAttempts)
	if err != nil {
		if ctx.Err() == nil {
			r.Log.Error("pipeline reap", "pipeline", s.Pipeline, "stage", s.Name, "err", err)
		}
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		r.Log.Warn("pipeline requeued stale items", "pipeline", s.Pipeline, "stage", s.Name, "count", n)
		metrics.PipelineCode(s.Pipeline, s.Name, "lease_expired", int(n))
	}
}

// work runs a stage on a batch and writes what it decided.
func (r *Runner) work(ctx context.Context, s *Stage, items []Item) {
	start := time.Now()
	wctx, stop := r.keepLease(ctx, s, items)
	results, err := runWork(wctx, s, items)
	stop()
	took := time.Since(start)
	if err != nil {
		r.Log.Error("pipeline stage failed", "pipeline", s.Pipeline, "stage", s.Name, "items", len(items), "err", err)
		results = nil
		for _, it := range items {
			results = append(results, Result{ItemID: it.ID, Outcome: Retry, Code: "stage_error"})
		}
	}
	// Write with a fresh context so shutdown doesn't strand claimed items.
	fctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var settled []runKey
	// Two batches can touch the same run's summary rows; a deadlock or
	// serialization failure rolls back cleanly, so try again.
	for try := 1; ; try++ {
		settled, err = r.apply(fctx, s, items, results, took)
		if err == nil || try == 3 || !retryable(err) {
			break
		}
		time.Sleep(time.Duration(try*50) * time.Millisecond)
	}
	if err != nil {
		track.Fail(fctx, r.Log, track.Event{Name: "pipeline.write_failed", Surface: "job", ActorType: "system",
			Props: map[string]any{"pipeline": s.Pipeline, "stage": s.Name, "items": len(items)}}, "internal", err)
		return
	}
	r.mu.Lock()
	fn := r.settled[s.Pipeline]
	r.mu.Unlock()
	if fn == nil {
		return
	}
	for _, run := range settled {
		if err := fn(fctx, run.workspaceID, run.id); err != nil {
			track.Fail(fctx, r.Log, track.Event{Name: "pipeline.settle_failed", WorkspaceID: run.workspaceID, Surface: "job", ActorType: "system",
				Props: map[string]any{"pipeline": s.Pipeline, "run_id": run.id}}, "internal", err)
		}
	}
}

func runWork(ctx context.Context, s *Stage, items []Item) (res []Result, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v\n%s", p, debug.Stack())
		}
	}()
	return s.Work(ctx, items)
}

// keepLease renews the batch's leases every third of Lease while Work runs.
func (r *Runner) keepLease(ctx context.Context, s *Stage, items []Item) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(max(s.Lease/3, 10*time.Millisecond))
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if _, err := r.Pool.Exec(ctx, `UPDATE pipeline_items SET lease_until = now() + $2::interval
				WHERE id = ANY($1) AND state = 'working'`, ids, interval(s.Lease)); err != nil && ctx.Err() == nil {
				r.Log.Warn("pipeline renew lease", "pipeline", s.Pipeline, "stage", s.Name, "err", err)
			}
		}
	}()
	return ctx, func() {
		close(done)
		<-finished
		cancel()
	}
}

type runKey struct{ id, workspaceID string }

// stat is what one batch did in one run's stage.
type stat struct {
	entered, advanced, done, skipped, held, failed, retried, waited int
	busy                                                            time.Duration
	codes                                                           map[string]int
}

// apply writes every result in one transaction and returns the runs that
// have settled. An update only lands on an item still claimed by this
// batch (state working, same attempt), so a reaped and reclaimed item, or
// a cancelled run, is left alone.
func (r *Runner) apply(ctx context.Context, s *Stage, items []Item, results []Result, took time.Duration) ([]runKey, error) {
	byID := make(map[string]Item, len(items))
	for _, it := range items {
		byID[it.ID] = it
	}
	got := map[string]Result{}
	for _, res := range results {
		if _, ok := byID[res.ItemID]; ok {
			got[res.ItemID] = res
		}
	}
	for _, it := range items {
		if _, ok := got[it.ID]; !ok {
			got[it.ID] = Result{ItemID: it.ID, Outcome: Retry, Code: "no_result"}
		}
	}

	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	stats := map[runKey]map[string]*stat{} // run -> stage -> stat
	statFor := func(k runKey, stage string) *stat {
		if stats[k] == nil {
			stats[k] = map[string]*stat{}
		}
		if stats[k][stage] == nil {
			stats[k][stage] = &stat{codes: map[string]int{}}
		}
		return stats[k][stage]
	}
	perRun := map[runKey]int{}
	for _, it := range items {
		perRun[runKey{it.RunID, it.WorkspaceID}]++
	}
	pauses := map[runKey]Result{}
	left := map[runKey]bool{} // runs where an item stopped moving
	counts := map[Outcome]int{}
	codes := map[string]int{}
	byWorkspace := map[string]map[string]int{} // workspace -> outcome -> n, for events

	b := &pgx.Batch{}
	const own = ` AND state = 'working' AND attempts = $2`
	for _, it := range items {
		res := got[it.ID]
		k := runKey{it.RunID, it.WorkspaceID}
		st := statFor(k, s.Name)
		out := res.Outcome
		if out == Retry && it.Attempt >= s.MaxAttempts {
			out = Fail
			if res.Code == "" {
				res.Code = "attempts_exhausted"
			}
		}
		reason := truncate(res.Reason, 500)
		code := nilIfEmpty(res.Code)
		switch out {
		case Advance:
			if !nameRe.MatchString(res.Next) {
				return nil, fmt.Errorf("pipeline: stage %s gave item %s a bad next stage %q", s.Name, it.ID, res.Next)
			}
			b.Queue(`UPDATE pipeline_items SET stage = $3, state = 'waiting', next_at = now(), attempts = 0, lease_until = NULL,
				error_code = NULL, reason = NULL, stage_entered_at = now(), updated_at = now() WHERE id = $1`+own, it.ID, it.Attempt, res.Next)
			st.advanced++
			statFor(k, res.Next).entered++
		case Done, Skip, Hold, Fail:
			state := map[Outcome]string{Done: StateDone, Skip: StateSkipped, Hold: StateHeld, Fail: StateFailed}[out]
			b.Queue(`UPDATE pipeline_items SET state = $3, lease_until = NULL, error_code = $4, reason = $5, updated_at = now() WHERE id = $1`+own,
				it.ID, it.Attempt, state, code, reason)
			left[k] = true
			switch out {
			case Done:
				st.done++
			case Skip:
				st.skipped++
			case Hold:
				st.held++
			case Fail:
				st.failed++
			}
		case Retry:
			after := res.RetryAfter
			if after <= 0 {
				after = s.Backoff(it.Attempt)
			}
			b.Queue(`UPDATE pipeline_items SET state = 'waiting', next_at = now() + $3::interval, lease_until = NULL, error_code = $4, reason = $5, updated_at = now() WHERE id = $1`+own,
				it.ID, it.Attempt, interval(after), code, reason)
			st.retried++
		case Wait, Pause:
			after := res.RetryAfter
			if out == Pause {
				after = 0
				pauses[k] = res
			} else if after <= 0 {
				after = 30 * time.Second
			}
			b.Queue(`UPDATE pipeline_items SET state = 'waiting', next_at = now() + $3::interval, attempts = attempts - 1, lease_until = NULL, error_code = $4, reason = $5, updated_at = now() WHERE id = $1`+own,
				it.ID, it.Attempt, interval(after), code, reason)
			st.waited++
		default:
			return nil, fmt.Errorf("pipeline: unknown outcome %d", out)
		}
		if res.Code != "" && out != Advance && out != Done {
			st.codes[res.Code]++
			codes[res.Code]++
		}
		counts[out]++
		if byWorkspace[it.WorkspaceID] == nil {
			byWorkspace[it.WorkspaceID] = map[string]int{}
		}
		byWorkspace[it.WorkspaceID][out.String()]++
		if out == Retry || out == Fail || out == Pause {
			r.Log.Warn("pipeline item "+out.String(), "pipeline", s.Pipeline, "stage", s.Name, "workspace_id", it.WorkspaceID,
				"run_id", it.RunID, "item_id", it.ID, "attempt", it.Attempt, "error_code", res.Code, "reason", reason)
		}
	}
	// Summary rows are written in one order (run, then stage) so that two
	// batches touching the same rows never wait on each other in a cycle.
	runs := make([]runKey, 0, len(stats))
	for k := range stats {
		runs = append(runs, k)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].id < runs[j].id })
	for _, k := range runs {
		share := took * time.Duration(perRun[k]) / time.Duration(len(items))
		names := make([]string, 0, len(stats[k]))
		for stage := range stats[k] {
			names = append(names, stage)
		}
		sort.Strings(names)
		for _, stage := range names {
			st := stats[k][stage]
			batches, busy := 0, int64(0)
			if stage == s.Name {
				batches, busy = 1, share.Milliseconds()
			}
			codesJSON, _ := json.Marshal(st.codes)
			b.Queue(`
				INSERT INTO pipeline_stage_stats AS p (run_id, stage, workspace_id, entered, advanced, done, skipped, held, failed, retried, waited, batches, busy_ms, errors)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
				ON CONFLICT (run_id, stage) DO UPDATE SET
					entered = p.entered + excluded.entered, advanced = p.advanced + excluded.advanced, done = p.done + excluded.done,
					skipped = p.skipped + excluded.skipped, held = p.held + excluded.held, failed = p.failed + excluded.failed,
					retried = p.retried + excluded.retried, waited = p.waited + excluded.waited, batches = p.batches + excluded.batches,
					busy_ms = p.busy_ms + excluded.busy_ms,
					errors = p.errors || (SELECT coalesce(jsonb_object_agg(e.k, coalesce((p.errors ->> e.k)::int, 0) + e.v::int), '{}')
						FROM jsonb_each_text(excluded.errors) AS e (k, v)),
					updated_at = now()`,
				k.id, stage, k.workspaceID, st.entered, st.advanced, st.done, st.skipped, st.held, st.failed, st.retried, st.waited, batches, busy, codesJSON)
		}
	}
	paused := make([]runKey, 0, len(pauses))
	for k := range pauses {
		paused = append(paused, k)
	}
	sort.Slice(paused, func(i, j int) bool { return paused[i].id < paused[j].id })
	for _, k := range paused {
		res := pauses[k]
		b.Queue(`UPDATE pipeline_runs SET state = 'paused', pause_code = $3, pause_reason = left($4, 500), updated_at = now()
			WHERE id = $1 AND workspace_id = $2 AND state = 'running'`, k.id, k.workspaceID, nilIfEmpty(res.Code), res.Reason)
	}
	if counts[Advance] > 0 {
		b.Queue(`SELECT pg_notify($1, '')`, notifyChannel)
	}
	if err := tx.SendBatch(ctx, b).Close(); err != nil {
		return nil, err
	}
	var settled []runKey
	keys := make([]runKey, 0, len(left))
	for k := range left {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].id < keys[j].id })
	for _, k := range keys {
		var open bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pipeline_items WHERE run_id = $1 AND state IN ('waiting', 'working'))`, k.id).Scan(&open); err != nil {
			return nil, err
		}
		if !open {
			settled = append(settled, k)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	// After the commit: metrics, one log line per batch, and events.
	metrics.PipelineBatch(s.Pipeline, s.Name, len(items), took)
	for out, n := range counts {
		metrics.PipelineOutcome(s.Pipeline, s.Name, out.String(), n)
	}
	for code, n := range codes {
		metrics.PipelineCode(s.Pipeline, s.Name, code, n)
	}
	level := slog.LevelDebug
	if counts[Retry]+counts[Fail]+counts[Pause] > 0 {
		level = slog.LevelWarn
	}
	r.Log.Log(ctx, level, "pipeline batch", "pipeline", s.Pipeline, "stage", s.Name, "items", len(items), "duration_ms", took.Milliseconds(),
		"advanced", counts[Advance], "done", counts[Done], "skipped", counts[Skip], "held", counts[Hold],
		"retried", counts[Retry], "waited", counts[Wait], "paused", counts[Pause], "failed", counts[Fail])
	for ws, n := range byWorkspace {
		if n[Retry.String()]+n[Fail.String()] == 0 {
			continue
		}
		props := map[string]any{"pipeline": s.Pipeline, "stage": s.Name, "items": len(items)}
		for out, c := range n {
			props[out] = c
		}
		track.Emit(ctx, track.Event{Name: "pipeline.stage_failed", WorkspaceID: ws, Surface: "job", ActorType: "system",
			Outcome: track.Error, ErrorCode: topCode(codes), Props: props})
	}
	for k, res := range pauses {
		track.Emit(ctx, track.Event{Name: "pipeline.run_paused", WorkspaceID: k.workspaceID, Surface: "job", ActorType: "system",
			Outcome: track.Failure, ErrorCode: res.Code, Props: map[string]any{"pipeline": s.Pipeline, "stage": s.Name, "run_id": k.id}})
	}
	return settled, nil
}

func topCode(codes map[string]int) string {
	best, n := "", 0
	for c, k := range codes {
		if k > n || (k == n && c < best) {
			best, n = c, k
		}
	}
	return best
}

func truncate(s string, n int) *string {
	if s == "" {
		return nil
	}
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	out := string(r)
	return &out
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// retryable reports a deadlock or serialization failure, which a fresh
// transaction can get past.
func retryable(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && (pg.Code == "40P01" || pg.Code == "40001")
}
