package inbox

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

// External is a reply a sequencer received for one of the workspace's
// mailboxes, read through its connection.
type External struct {
	WorkspaceID   string
	IntegrationID string
	// Source is the connector id, such as emailbison.
	Source    string
	MailboxID string
	Reply     connectors.Reply
}

// StoreExternal saves a sequencer's reply in the unified inbox and acts on
// it the way a reply read from the mailbox itself is acted on: the rules
// label it, an opt-out is suppressed and a reply stops the lead's
// sequences. It reports false if the reply was stored before, or is
// warm-up mail.
func StoreExternal(ctx context.Context, tx pgx.Tx, e External) (bool, error) {
	ws, r := e.WorkspaceID, e.Reply
	from := strings.ToLower(strings.TrimSpace(r.FromEmail))
	if from == "" || r.ExternalID == "" {
		return false, nil
	}
	body := r.Body
	if strings.TrimSpace(body) == "" {
		body = strings.TrimSpace(stripHTML(r.HTML))
	}
	if len(body) > maxBody {
		body = body[:maxBody]
	}
	// Mail between the workspace's own mailboxes is warm-up or a test.
	var own bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mailboxes WHERE workspace_id = $1 AND lower(email) = $2)`, ws, from).Scan(&own); err != nil {
		return false, err
	}
	if own {
		return false, nil
	}
	if tagged, err := warmupTagged(ctx, tx, ws, r.Subject, body); err != nil || tagged {
		return false, err
	}

	var leadID, enrollmentID, sequenceID *string
	err := tx.QueryRow(ctx, `
		SELECT l.id::text, e.id::text, e.sequence_id::text FROM leads l
		LEFT JOIN LATERAL (
			SELECT id, sequence_id FROM enrollments WHERE lead_id = l.id ORDER BY created_at DESC LIMIT 1
		) e ON true
		WHERE l.workspace_id = $1 AND lower(l.email) = $2
		LIMIT 1`, ws, from).Scan(&leadID, &enrollmentID, &sequenceID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}

	// Everything in a sequencer's replies folder answers a campaign, so
	// it is a reply even when the lead lives only in the sequencer.
	kind := "reply"
	if r.Automated || autoSubject.MatchString(r.Subject) {
		kind = "auto_reply"
	}
	complained := kind == "reply" && Complaint(body)
	optOut := kind == "reply" && (complained || OptOut(body))
	var category *string
	if c := Categorize(kind, optOut, body); c != "" {
		category = &c
	}
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO inbound_messages (workspace_id, mailbox_id, uidvalidity, uid, message_id, from_email, from_name, to_email,
			subject, body, kind, opt_out, lead_id, enrollment_id, sequence_id, received_at, source, integration_id, external_id,
			category, category_source, done, category_confidence, classified_at)
		VALUES ($1, $2, 0, nextval('inbound_external_uid_seq'), $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17,
			$18, CASE WHEN $18::text IS NULL THEN NULL ELSE 'rules' END,
			COALESCE($18::text IN ('unsubscribe', 'out_of_office', 'auto_reply'), false),
			CASE WHEN $18::text IS NULL THEN NULL ELSE 1 END,
			CASE WHEN $18::text IS NULL THEN NULL ELSE now() END)
		ON CONFLICT (integration_id, external_id) WHERE external_id IS NOT NULL DO NOTHING
		RETURNING id`,
		ws, e.MailboxID, r.MessageID, from, r.FromName, r.MailboxEmail, r.Subject, body, kind, optOut,
		leadID, enrollmentID, sequenceID, r.ReceivedAt, e.Source, e.IntegrationID, r.ExternalID, category).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if category != nil {
		if err := Classified(ctx, tx, Classification{WorkspaceID: ws, InboundID: id, LeadID: leadID,
			SequenceID: sequenceID, MailboxID: e.MailboxID, Source: e.Source, Category: category, By: "rules",
			Confidence: 1}); err != nil {
			return false, err
		}
	}
	if category == nil && kind == "reply" {
		if err := QueueLabel(ctx, tx, ws, id); err != nil {
			return false, err
		}
	}
	lead := ""
	if leadID != nil {
		lead = *leadID
	}
	switch {
	case optOut:
		return true, OptedOut(ctx, tx, ws, id, lead, from, complained)
	case kind == "reply" && leadID != nil:
		s := &Syncer{}
		return true, s.replied(ctx, tx, ws, id, lead, map[string]any{
			"category": category, "sequence_id": sequenceID, "mailbox_id": e.MailboxID, "from_email": from,
			"subject": r.Subject, "source": e.Source})
	}
	return true, nil
}
