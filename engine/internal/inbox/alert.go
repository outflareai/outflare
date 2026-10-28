package inbox

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/alerts"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/outcomes"
)

// AlertHotReply is the alert for a reply labelled hot: interested, a
// meeting booked or a question.
const AlertHotReply = "reply.hot"

func init() {
	alerts.Register(alerts.Kind{ID: AlertHotReply, Title: "Hot replies",
		Description: "A lead replied interested, booked a meeting or asked a question. It goes to the person the reply is assigned to, else who made the sequence.",
		Scope:       auth.ScopeInboxRead, Describe: describeHotReply})
}

// categoryWords says a label in a sentence: "is interested".
var categoryWords = map[string]string{
	"interested":     "is interested",
	"meeting_booked": "booked a meeting",
	"question":       "asked a question",
}

// Settled runs what follows a label someone or something is sure of: the
// reply rules, writing it into connected CRMs that asked for its label,
// and an alert when the reply is hot. actorID is who set it,
// so they aren't told what they already know.
func Settled(ctx context.Context, q jobs.Querier, ws, inboundID, category, actorID, requestID string) error {
	if err := QueueRules(ctx, q, ws, inboundID, category); err != nil {
		return err
	}
	if err := outcomes.QueueReplyPush(ctx, q, ws, inboundID, category, expandLabels); err != nil {
		return err
	}
	if GroupOf(category) != "hot" {
		return nil
	}
	var brand, record, owner, sequence *string
	err := q.QueryRow(ctx, `
		SELECT mb.brand_id::text, i.record_id::text, COALESCE(i.assigned_to, u.id)::text, i.sequence_id::text
		FROM inbound_messages i
		JOIN mailboxes mb ON mb.id = i.mailbox_id
		LEFT JOIN sequences s ON s.id = i.sequence_id
		LEFT JOIN users u ON u.actor_id = s.created_by
		WHERE i.id = $1 AND i.workspace_id = $2`, inboundID, ws).Scan(&brand, &record, &owner, &sequence)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // the reply is gone
	}
	if err != nil {
		return err
	}
	_, err = alerts.Raise(ctx, q, alerts.Alert{WorkspaceID: ws, BrandID: brand, Kind: AlertHotReply, DedupeKey: inboundID,
		TargetType: "inbound_message", TargetID: inboundID, RecordID: record, OwnerID: owner, SkipActorID: actorID, RequestID: requestID,
		Data: map[string]any{"inbound_id": inboundID, "category": category, "sequence_id": sequence}})
	return err
}

// expandLabels turns labels and groups into labels, dropping unknown names.
func expandLabels(names []string) []string {
	var out []string
	for _, n := range names {
		if l, bad := Expand([]string{n}); bad == "" {
			out = append(out, l...)
		}
	}
	return out
}

func describeHotReply(ctx context.Context, q jobs.Querier, a alerts.Row) (alerts.View, error) {
	var first, last, company, email, category, sequence string
	err := q.QueryRow(ctx, `
		SELECT COALESCE(l.first_name, ''), COALESCE(l.last_name, ''), COALESCE(l.company, ''), i.from_email,
			COALESCE(i.category, ''), COALESCE(s.name, '')
		FROM inbound_messages i
		LEFT JOIN leads l ON l.id = i.lead_id
		LEFT JOIN sequences s ON s.id = i.sequence_id
		WHERE i.id = $1 AND i.workspace_id = $2`, a.TargetID, a.WorkspaceID).Scan(&first, &last, &company, &email, &category, &sequence)
	if err != nil {
		return alerts.View{}, err
	}
	who := strings.TrimSpace(first + " " + last)
	if who == "" {
		who = email
	}
	if company != "" {
		who += " at " + company
	}
	words, ok := categoryWords[category]
	if !ok {
		words = "replied"
	}
	text := "Reply soon while it's warm."
	if sequence != "" {
		text = "They replied to " + sequence + ". Reply soon while it's warm."
	}
	return alerts.View{Title: who + " " + words, Text: text, Path: "inbox/" + a.TargetID}, nil
}
