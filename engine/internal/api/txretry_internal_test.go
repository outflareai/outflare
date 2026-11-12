package api

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/db"
	"github.com/outflareai/outflare/engine/internal/track"
)

// TestRetryTxDeadlock: two requests that lock the same two things in
// opposite order deadlock; Postgres rolls one back after deadlock_timeout.
// Both still succeed, and the actions the rolled-back try recorded are
// sent once, not twice.
func TestRetryTxDeadlock(t *testing.T) {
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
	s := &Server{pool: pool, log: slog.New(slog.DiscardHandler)}
	got := track.Capture(t)

	// Both hold their first lock before either asks for its second, so
	// the first tries always deadlock.
	var first sync.WaitGroup
	first.Add(2)
	var mu sync.Mutex
	tries := map[int64]int{}
	run := func(a, b int64) error {
		hctx, release := track.Hold(ctx)
		err := s.retryTx(hctx, "test", func(tx pgx.Tx) error {
			mu.Lock()
			tries[a]++
			try := tries[a]
			mu.Unlock()
			track.Emit(hctx, track.Event{Name: "test.locked"})
			if _, err := tx.Exec(hctx, `SELECT pg_advisory_xact_lock(9150000 + $1)`, a); err != nil {
				return err
			}
			if try == 1 {
				first.Done()
				first.Wait()
			}
			_, err := tx.Exec(hctx, `SELECT pg_advisory_xact_lock(9150000 + $1)`, b)
			return err
		})
		release(err == nil, track.Event{})
		return err
	}
	errs := make(chan error, 2)
	go func() { errs <- run(1, 2) }()
	go func() { errs <- run(2, 1) }()
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("a write that lost a deadlock wasn't retried: %v", err)
		}
	}
	if tries[1]+tries[2] < 3 {
		t.Fatalf("expected a deadlock and a retry, got tries %v", tries)
	}
	n := 0
	for _, e := range got() {
		if e.Name == "test.locked" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("sent %d test.locked events, want 2: a rolled-back try's events must be dropped", n)
	}
}
