package api

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// txAttempts is how many times a write that keeps losing deadlocks runs
// before the request gives up.
const txAttempts = 3

// retryTx runs fn in a transaction, and runs it again in a fresh one when
// Postgres picks it as a deadlock victim or can't serialize it. Writes
// that lock several rows in an order set by their input (a batch of
// events, a payment and its person) can deadlock with another request
// doing the same for the same people; Postgres rolls one of them back
// after deadlock_timeout, and the second try gets through. Without this
// the sender saw 500 internal about a second after sending.
//
// fn must start from scratch on each call: reset anything it fills in
// outside the transaction. Events recorded by a try that rolled back are
// dropped, so a retried action is sent once. operation is a fixed label
// for the metric.
func (s *Server) retryTx(ctx context.Context, operation string, fn func(pgx.Tx) error) error {
	for attempt := 1; ; attempt++ {
		rewind := track.Checkpoint(ctx)
		err := pgx.BeginFunc(ctx, s.pool, fn)
		if err == nil || !retryableTx(err) || ctx.Err() != nil {
			return err
		}
		rewind()
		if attempt == txAttempts {
			metrics.TxRetry(operation, "gave_up")
			return err
		}
		metrics.TxRetry(operation, "retried")
		s.log.Warn("transaction retried", "operation", operation, "attempt", attempt, "request_id", RequestID(ctx), "err", err)
		// A short random pause, so the two requests that deadlocked don't
		// meet again in the same order.
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Duration(10+rand.IntN(40*attempt)) * time.Millisecond):
		}
	}
}

// retryableTx reports a deadlock (40P01) or serialization failure (40001):
// the transaction was rolled back whole and can run again.
func retryableTx(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && (pg.Code == "40P01" || pg.Code == "40001")
}
