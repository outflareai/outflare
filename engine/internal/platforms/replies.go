package platforms

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/inbox"
	"github.com/outflareai/outflare/engine/internal/track"
)

const (
	// repliesFirstBack is how far back the first read of an account's
	// replies goes, so connecting it doesn't import its whole history.
	repliesFirstBack = 3 * 24 * time.Hour
	// repliesOverlap re-reads a little before the newest stored reply, in
	// case the platform shows a reply late; stored ones are skipped.
	repliesOverlap = time.Hour
)

// syncReplies brings the replies a sequencer received into the unified
// inbox. A failed read is an event and a log line; it never fails the
// mailbox sync around it.
func (s *Service) syncReplies(ctx context.Context, ws string, op *connectors.Opened) int {
	src, ok := op.Connector.(connectors.ReplySource)
	if !ok {
		return 0
	}
	fail := func(err error) int {
		track.Fail(ctx, s.Log, track.Event{Name: "inbox.replies_sync_failed", WorkspaceID: ws, ActorType: "system", Surface: "job",
			Props: map[string]any{"connector": op.Integration.Connector, "integration_id": op.Integration.ID, "kind": connectors.KindOf(err)}},
			"provider_"+connectors.KindOf(err), err)
		return 0
	}
	since := s.now().Add(-repliesFirstBack)
	var newest *time.Time
	if err := s.Pool.QueryRow(ctx, `SELECT max(received_at) FROM inbound_messages WHERE workspace_id = $1 AND integration_id = $2`,
		ws, op.Integration.ID).Scan(&newest); err != nil {
		return fail(err)
	}
	if newest != nil && newest.Add(-repliesOverlap).After(since) {
		since = newest.Add(-repliesOverlap)
	}
	replies, err := src.ListReplies(ctx, op.Client, op.Creds, since)
	if err != nil {
		return fail(err)
	}
	stored := 0
	for _, r := range replies {
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			var mailboxID string
			err := tx.QueryRow(ctx, `
				SELECT mailbox_id::text FROM mailbox_platforms WHERE integration_id = $1 AND external_id = $2 AND $2 <> ''
				UNION ALL
				SELECT id::text FROM mailboxes WHERE workspace_id = $3 AND lower(email) = $4 AND $4 <> ''
				LIMIT 1`, op.Integration.ID, r.MailboxExternalID, ws, r.MailboxEmail).Scan(&mailboxID)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil // a sender email this workspace doesn't hold
			}
			if err != nil {
				return err
			}
			ok, err := inbox.StoreExternal(ctx, tx, inbox.External{WorkspaceID: ws, IntegrationID: op.Integration.ID,
				Source: op.Integration.Connector, MailboxID: mailboxID, Reply: r})
			if ok {
				stored++
			}
			return err
		})
		if err != nil {
			return stored + fail(err)
		}
	}
	if stored > 0 {
		track.Emit(ctx, track.Event{Name: "inbox.replies_synced", WorkspaceID: ws, ActorType: "system", Surface: "job",
			Outcome: track.Success, Props: map[string]any{"connector": op.Integration.Connector, "integration_id": op.Integration.ID, "count": stored}})
	}
	return stored
}
