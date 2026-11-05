package platforms

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/leads"
	"github.com/outflareai/outflare/engine/internal/track"
)

const (
	// pullChunk is how many entries read from a platform one transaction
	// adds to the opt-out list.
	pullChunk = 5000
	// pushBatches is the most Block calls one sync makes; the rest wait
	// for the next sync.
	pushBatches = 20
	// pullEvery is how often a platform's whole blocklist is read. A
	// sequencer's list can run to many pages, and the mailbox sync runs
	// every few minutes, so reading it each time only wears the platform
	// out. What the list holds still goes out on every sync.
	pullEvery = 6 * time.Hour
	// retryAfter is how long a sync that failed waits before the next
	// try, so a platform that is down is not asked again every few
	// minutes.
	retryAfter = 30 * time.Minute
)

// syncBlocklist keeps the workspace's opt-out list and a sequencer's
// blocklist in step: what is blocked there joins the list (source
// <connector>_sync), and what the list holds is written there, so a send
// from either side honours both. Entries read from this platform are not
// written back to it. Their list is read at most every pullEvery; a pull
// that fails doesn't stop the opt-outs going out. A failure is an event, a
// log line and the sync's last_error, with provider_<kind> for the
// platform's answer and internal for our own database; it never fails the
// mailbox sync around it, and the next try waits retryAfter.
func (s *Service) syncBlocklist(ctx context.Context, ws string, op *connectors.Opened) (pulled, pushed int) {
	bl, ok := op.Connector.(connectors.Blocklist)
	if !ok {
		return 0, 0
	}
	integrationID := op.Integration.ID
	now := s.now()
	var pull, push bool
	var until, pulledAt *time.Time
	var after, lastCode *string
	var updated time.Time
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO suppression_syncs (integration_id, workspace_id) VALUES ($1, $2)
		ON CONFLICT (integration_id) DO UPDATE SET updated_at = suppression_syncs.updated_at
		RETURNING pull, push, pushed_until, pushed_id::text, pulled_at, error_code, updated_at`, integrationID, ws).
		Scan(&pull, &push, &until, &after, &pulledAt, &lastCode, &updated)
	var failErr error
	var failCode string
	fail := func(code string, err error) {
		failErr, failCode = err, code
		track.Fail(ctx, s.Log, track.Event{Name: "suppression.sync_failed", WorkspaceID: ws, ActorType: "system", Surface: "job",
			Props: map[string]any{"connector": op.Integration.Connector, "integration_id": integrationID, "pulled": pulled, "pushed": pushed}},
			code, err)
	}
	// record writes how the sync went: counts, and the error or none.
	record := func() (int, int) {
		var msg, code *string
		if failErr != nil {
			m := failErr.Error()
			msg, code = &m, &failCode
		}
		if _, err := s.Pool.Exec(ctx, `UPDATE suppression_syncs SET last_error = $2, error_code = $3, pulled_count = pulled_count + $4,
			pushed_count = pushed_count + $5, updated_at = $6 WHERE integration_id = $1`, integrationID, msg, code, pulled, pushed, now); err != nil && failErr == nil {
			fail("internal", err)
		}
		return pulled, pushed
	}
	provider := func(err error) string { return "provider_" + connectors.KindOf(err) }
	if err != nil {
		fail("internal", err)
		return pulled, pushed
	}
	if lastCode != nil && now.Sub(updated) < retryAfter {
		return 0, 0 // the last try failed a moment ago
	}
	source := op.Integration.Connector + "_sync"
	if r, ok := bl.(connectors.BlocklistReader); ok && pull && (pulledAt == nil || now.Sub(*pulledAt) >= pullEvery) {
		s.pullBlocklist(ctx, ws, op, r, source, &pulled, fail, provider)
		if failErr == nil {
			if _, err := s.Pool.Exec(ctx, `UPDATE suppression_syncs SET pulled_at = $2 WHERE integration_id = $1`, integrationID, now); err != nil {
				fail("internal", err)
			}
		}
	}
	if push {
		for range pushBatches {
			rows, err := s.Pool.Query(ctx, `
				SELECT id::text, kind, value, created_at FROM suppressions
				WHERE workspace_id = $1 AND integration_id IS DISTINCT FROM $2::uuid
				  AND ($3::timestamptz IS NULL OR (created_at, id) > ($3, $4::uuid))
				ORDER BY created_at, id LIMIT $5`, ws, integrationID, until, after, connectors.BlockBatch)
			if err != nil {
				fail("internal", err)
				return record()
			}
			var batch []connectors.Blocked
			var lastAt time.Time
			var lastID string
			for rows.Next() {
				var id, kind, value string
				if err := rows.Scan(&id, &kind, &value, &lastAt); err != nil {
					rows.Close()
					fail("internal", err)
					return record()
				}
				lastID = id
				batch = append(batch, connectors.Blocked{Value: value, Domain: kind == "domain"})
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				fail("internal", err)
				return record()
			}
			if len(batch) == 0 {
				break
			}
			if err := bl.Block(ctx, op.Client, op.Creds, batch); err != nil {
				fail(provider(err), err)
				return record()
			}
			pushed += len(batch)
			until, after = &lastAt, &lastID
			if _, err := s.Pool.Exec(ctx, `UPDATE suppression_syncs SET pushed_until = $2, pushed_id = $3, pushed_at = now() WHERE integration_id = $1`,
				integrationID, lastAt, lastID); err != nil {
				fail("internal", err)
				return record()
			}
			if len(batch) < connectors.BlockBatch {
				break
			}
		}
	}
	record()
	if failErr == nil && (pulled > 0 || pushed > 0) {
		track.Emit(ctx, track.Event{Name: "suppression.synced", WorkspaceID: ws, ActorType: "system", Surface: "job", Outcome: track.Success,
			Props: map[string]any{"connector": op.Integration.Connector, "integration_id": integrationID, "pulled": pulled, "pushed": pushed}})
	}
	return pulled, pushed
}

// pullBlocklist reads the platform's blocklist into the opt-out list, a
// chunk per transaction, adding what it took to pulled.
func (s *Service) pullBlocklist(ctx context.Context, ws string, op *connectors.Opened, r connectors.BlocklistReader, source string,
	pulled *int, fail func(string, error), provider func(error) string) {
	list, err := r.ListBlocked(ctx, op.Client, op.Creds)
	if err != nil {
		fail(provider(err), err)
		return
	}
	var entries []leads.Suppression
	for _, b := range list {
		kind, value, err := leads.ParseSuppression(b.Value)
		if err != nil {
			continue // something the platform holds that isn't an address or a domain
		}
		entries = append(entries, leads.Suppression{Kind: kind, Value: value})
	}
	// Their blocklist says only "don't cold email them", for an address
	// or a whole domain: not an unsubscribe, so receipts and marketing
	// email still follow the person's own consent.
	for start := 0; start < len(entries); start += pullChunk {
		chunk := entries[start:min(start+pullChunk, len(entries))]
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			n, err := leads.Suppress(ctx, tx, ws, leads.Added{Reason: "blocklist", Source: source, IntegrationID: op.Integration.ID,
				Note: "On the " + op.Integration.Name + " blocklist"}, chunk)
			if err != nil || n == 0 {
				return err
			}
			*pulled += n
			return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "suppressions.added",
				TargetType: "suppression", Data: map[string]any{"added": n, "reason": "blocklist", "method": leads.MethodAutomated,
					"source": source, "integration_id": op.Integration.ID}})
		})
		if err != nil {
			fail("internal", err)
			return
		}
	}
}
