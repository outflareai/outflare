package inbox

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/jobs"
)

// JobSalesReply opens or updates the Sales conversation of a hot reply a
// connected sequencer received from a person Outflare put in its campaign.
// The API package handles it: it reads the thread from the sequencer.
const JobSalesReply = "sales.reply_import"

// SalesSources are the sequencers (inbound_messages.source) whose replies
// can open a Sales conversation. Add one here when its connector can read
// a reply's thread (connectors.ReplyThreadSource).
var SalesSources = []string{"emailbison"}

// SalesReply is a reply that may open a Sales conversation.
type SalesReply struct {
	InboundID, BrandID, RecordID, IntegrationID, Source string
	// ExternalID is the sequencer's id for the reply.
	ExternalID string
	// SenderExternalID is the sequencer's id for the mailbox it arrived
	// at, or "" when the connection hasn't reported the mailbox yet.
	SenderExternalID string
	FromEmail        string
	MailboxEmail     string
	Subject, Body    string
	MessageID        string
	ReceivedAt       time.Time
	// Campaigns are the sequencer campaigns Outflare added the person to
	// on this connection that are linked to a sequence of their brand.
	Campaigns []string
}

// SalesReplyOf reads the reply when every condition for a Sales
// conversation holds, and nil otherwise:
//
//   - it was read from a sequencer in SalesSources through a connection,
//   - a person wrote it (kind reply) and its label is sure and hot
//     (interested, meeting_booked or question),
//   - it is linked to a person in a brand, and
//   - Outflare added that person to a campaign on that connection
//     (a sequence.lead_pushed action names them and the connection) that
//     is linked to a sequence of the person's brand.
//
// So a reply from a stranger, or from a person some other tool put in the
// campaign, stays an Inbox message.
func SalesReplyOf(ctx context.Context, q jobs.Querier, ws, inboundID string) (*SalesReply, error) {
	r := SalesReply{InboundID: inboundID}
	err := q.QueryRow(ctx, `
		SELECT r.brand_id::text, r.id::text, i.integration_id::text, i.source, i.external_id,
			COALESCE((SELECT mp.external_id FROM mailbox_platforms mp WHERE mp.integration_id = i.integration_id AND mp.mailbox_id = i.mailbox_id LIMIT 1), ''),
			i.from_email, COALESCE(i.to_email, ''), COALESCE(i.subject, ''), COALESCE(i.body, ''), COALESCE(i.message_id, ''), i.received_at,
			ARRAY(
				SELECT DISTINCT a.data->>'campaign_id' FROM audit_events a
				JOIN campaign_links l ON l.workspace_id = a.workspace_id AND l.integration_id = i.integration_id
					AND l.external_id = a.data->>'campaign_id'
				JOIN sequences s ON s.id = l.sequence_id AND s.workspace_id = l.workspace_id AND s.brand_id = r.brand_id
				WHERE a.workspace_id = i.workspace_id AND a.action = 'sequence.lead_pushed'
					AND a.data->>'integration_id' = i.integration_id::text
					-- The person, or a duplicate of them merged into this record since.
					AND a.data->>'record_id' IN (
						SELECT r.id::text
						UNION ALL
						SELECT m.id::text FROM records m WHERE m.workspace_id = r.workspace_id AND m.merged_into = r.id))
		FROM inbound_messages i
		JOIN records r ON r.id = i.record_id AND r.workspace_id = i.workspace_id
		WHERE i.id = $1 AND i.workspace_id = $2 AND i.kind = 'reply' AND i.source = ANY($3)
			AND i.integration_id IS NOT NULL AND i.external_id IS NOT NULL
			AND i.category = ANY($4) AND NOT i.needs_review
			AND r.record_type = 'person' AND r.brand_id IS NOT NULL AND r.deleted_at IS NULL AND r.merged_into IS NULL`,
		inboundID, ws, SalesSources, Groups["hot"]).
		Scan(&r.BrandID, &r.RecordID, &r.IntegrationID, &r.Source, &r.ExternalID, &r.SenderExternalID,
			&r.FromEmail, &r.MailboxEmail, &r.Subject, &r.Body, &r.MessageID, &r.ReceivedAt, &r.Campaigns)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(r.Campaigns) == 0 {
		return nil, nil
	}
	return &r, nil
}

// QueueSalesReply queues the Sales conversation of a reply whose label
// just settled, when SalesReplyOf says it gets one. A reply is queued
// once however often its label settles: the key is its connection and
// the sequencer's id for it.
func QueueSalesReply(ctx context.Context, q jobs.Querier, ws, inboundID string) error {
	r, err := SalesReplyOf(ctx, q, ws, inboundID)
	if err != nil || r == nil {
		return err
	}
	_, err = jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobSalesReply, WorkspaceID: &ws,
		Payload: map[string]string{"inbound_id": inboundID}, MaxAttempts: 5,
		IdempotencyKey: r.IntegrationID + ":" + r.ExternalID})
	return err
}
