package stories

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/jobs"
)

// Pitching a story: a journalist enrolled in an Outreach sequence with an
// opener a person approved. The pitches themselves live in package api;
// this is what the inbox calls when a pitched journalist replies.

// JobReadReply reads one reply to a story pitch.
const JobReadReply = "story_pitches.read_reply"

// ReplyKinds are what a journalist's reply to a pitch says.
var ReplyKinds = []string{"covering", "wants_data", "wants_interview", "not_my_beat", "declined", "other"}

// Replied marks the journalist's latest enrolled pitch replied and queues
// reading the reply, in tx. The inbox calls it for every reply to a
// sequence, so it does nothing for leads without a pitch. The sequence's
// stop on reply has already ended the enrollment.
func Replied(ctx context.Context, tx pgx.Tx, ws, leadID, inboundID string) error {
	var pitchID string
	err := tx.QueryRow(ctx, `UPDATE story_pitches SET replied_at = now(), updated_at = now()
		WHERE id = (SELECT id FROM story_pitches WHERE workspace_id = $1 AND lead_id = $2 AND status = 'enrolled' ORDER BY approved_at DESC LIMIT 1)
		RETURNING id::text`, ws, leadID).Scan(&pitchID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobReadReply, Payload: map[string]string{"pitch_id": pitchID, "inbound_id": inboundID},
		WorkspaceID: &ws, MaxAttempts: 3, IdempotencyKey: "story_reply:" + inboundID})
	return err
}
