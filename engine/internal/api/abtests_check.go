package api

// The background check of running A/B tests: every few hours each one's
// results are counted, and an alert is raised once when it becomes safe to
// stop, when its split breaks, and when a version clearly hurts a
// guardrail. It only tells people; ending a test or shipping a version is
// theirs to do.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/abtests"
	"github.com/outflareai/outflare/engine/internal/alerts"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

const jobABTestCheck = "ab_tests.check"

// abCheckEvery is how often a running test is checked.
const abCheckEvery = 6 * time.Hour

// RunABTests queues the checks of running tests that are due, every
// interval, until ctx ends.
func (s *Server) RunABTests(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := s.QueueDueABTestChecks(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.log, track.Event{Name: "ab_test.scheduler_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// QueueDueABTestChecks moves each due running test's next check on and
// queues it, in one transaction, and reports how many it queued.
func (s *Server) QueueDueABTestChecks(ctx context.Context) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE ab_tests t SET next_check_at = now() + $1::interval
			WHERE t.id IN (SELECT id FROM ab_tests WHERE status = 'running' AND next_check_at <= now()
			               ORDER BY next_check_at LIMIT 100 FOR UPDATE SKIP LOCKED)
			RETURNING t.workspace_id::text, t.id::text, t.next_check_at`, fmt.Sprintf("%d seconds", int(abCheckEvery.Seconds())))
		if err != nil {
			return err
		}
		type due struct {
			ws, id string
			at     time.Time
		}
		var list []due
		var d due
		if _, err := pgx.ForEachRow(rows, []any{&d.ws, &d.id, &d.at}, func() error { list = append(list, d); return nil }); err != nil {
			return err
		}
		for _, d := range list {
			if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobABTestCheck, WorkspaceID: &d.ws, Payload: map[string]string{"test_id": d.id},
				IdempotencyKey: "ab_tests.check:" + d.id + ":" + strconv.FormatInt(d.at.Unix(), 10), MaxAttempts: 3}); err != nil {
				return err
			}
		}
		n = len(list)
		return nil
	})
	return n, err
}

// abTestCheckJob counts one running test's results and raises the alerts
// they call for. Each alert is raised once (alerts dedupe on their key).
func (s *Server) abTestCheckJob(ctx context.Context, job jobs.Job) error {
	var p map[string]string
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || !validUUID(p["test_id"]) {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	list, err := loadABTests(ctx, s.pool, ws, ` AND t.id = $2`, p["test_id"])
	if err != nil {
		return err
	}
	if len(list) != 1 || list[0].Status != "running" {
		return nil
	}
	t := list[0]
	f, err := s.abFlag(ctx, nil, ws, t.FlagID)
	if errors.Is(err, errNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	res, err := s.abResults(ctx, s.pool, ws, t, f, time.Now())
	if err != nil {
		return err
	}
	type raise struct{ kind, key, variant string }
	var due []raise
	if res.SampleRatio {
		due = append(due, raise{abtests.AlertSplit, "split", ""})
	} else if w := abtests.Winner(res, t.Control); w != "" {
		due = append(due, raise{abtests.AlertReady, "ready", w})
	}
	for i, g := range res.Guardrails {
		for _, v := range g.Harmed {
			due = append(due, raise{abtests.AlertGuardrail, "guardrail:" + strconv.Itoa(i) + ":" + v, v})
		}
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, r := range due {
			if _, err := alerts.Raise(ctx, tx, alerts.Alert{WorkspaceID: ws, BrandID: t.BrandID, Kind: r.kind,
				DedupeKey: "ab_test:" + t.ID + ":" + r.key, TargetType: "ab_test", TargetID: t.ID,
				Data: map[string]any{"test_id": t.ID, "flag_id": t.FlagID, "variant": r.variant}}); err != nil {
				return err
			}
		}
		return nil
	})
}
