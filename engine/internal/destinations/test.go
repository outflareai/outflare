package destinations

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// TestName is the name of the made-up event a test sends.
const TestName = "outflare_test"

// EnqueueTest asks for one test of a destination. Call it in the
// transaction that sets test_requested_at.
func EnqueueTest(ctx context.Context, q Querier, ws, id string) (int64, error) {
	return jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobTest, WorkspaceID: &ws, Payload: payload{Destination: id}, MaxAttempts: 1})
}

func (s *Sender) testJob(ctx context.Context, job jobs.Job) error {
	var p payload
	if err := json.Unmarshal(job.Payload, &p); err != nil || p.Destination == "" || job.WorkspaceID == nil {
		return nil
	}
	kind, ok, err := s.Test(ctx, *job.WorkspaceID, p.Destination)
	ev := track.Event{Name: "destination.tested", WorkspaceID: *job.WorkspaceID, ActorType: "system", Surface: "job",
		Props: map[string]any{"destination_id": p.Destination, "kind": kind}}
	if err != nil {
		track.Fail(ctx, s.Log, ev, "internal", err)
		return err
	}
	if kind == "" {
		return nil
	}
	metrics.DestinationTest(kind, ok)
	if !ok {
		ev.Outcome = track.Failure
		ev.ErrorCode = "connector_invalid"
	}
	track.Emit(ctx, ev)
	return nil
}

// Test sends one made-up event to a destination, on or off, and keeps
// how it went on the destination. It reports the kind ("" when the
// destination is gone) and whether the tool took the event.
func (s *Sender) Test(ctx context.Context, ws, id string) (kind string, ok bool, err error) {
	d, err := s.load(ctx, ws, id, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	reason := ""
	if err != nil {
		// A secret that no longer opens is the person's to fix.
		d.kind, reason = s.kindOf(ctx, ws, id), "its saved key could not be opened; save it again"
		if d.kind == "" {
			return "", false, err
		}
	} else {
		ok, reason = s.tryOnce(ctx, d)
	}
	_, err = s.Pool.Exec(ctx, `UPDATE event_destinations SET last_test_at = now(), last_test_ok = $3, last_test_reason = $4
		WHERE workspace_id = $1 AND id = $2`, ws, id, ok, trim(reason))
	return d.kind, ok, err
}

func (s *Sender) kindOf(ctx context.Context, ws, id string) string {
	var kind string
	_ = s.Pool.QueryRow(ctx, `SELECT kind FROM event_destinations WHERE workspace_id = $1 AND id = $2`, ws, id).Scan(&kind)
	return kind
}

// tryOnce delivers the test event and says in words what happened.
func (s *Sender) tryOnce(ctx context.Context, d destination) (bool, string) {
	deliver, why := s.deliverer(ctx, d)
	if deliver.one == nil && deliver.many == nil {
		return false, why
	}
	if d.kind == "hubspot" {
		// A test must not make or change a contact: opening the
		// connection is the check.
		return true, "The connection works. No contact was made or changed."
	}
	now := time.Now().UTC()
	anon := "outflare-test"
	e := Event{ID: newID(), Type: "event", Name: TestName, RawName: "Outflare test", Kind: "track", BrandID: d.brand, OccurredAt: now, ReceivedAt: now,
		AnonymousID: &anon, Properties: map[string]any{"test": true}, sendID: "test_" + newID()}
	var o outcome
	var reason string
	if deliver.many != nil {
		r := deliver.many(ctx, []Event{e})
		o, reason = r[0].o, r[0].reason
	} else {
		o, reason = deliver.one(ctx, e)
	}
	switch o {
	case sent:
		return true, fmt.Sprintf("A test event named %s was sent.", TestName)
	case skipped:
		return false, "The test event was skipped: " + reason
	}
	return false, reason
}

// newID is a random UUID (version 4).
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
