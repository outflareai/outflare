package emails

import (
	"context"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// Delivery states on each recipient's customer timeline, beside their
// product events and consents: email.sent, email.delivered, email.bounced,
// email.rejected, email.complained, and the first email.opened and
// email.clicked. Only people who already have a record get them; sending
// to an address never makes one, and test sends stay off.

// onTimeline records an email's state for its recipients, or for the given
// ones. Each state is stored once per email and person.
func onTimeline(ctx context.Context, q jobs.Querier, ws, emailID, state string, recipients []string, extra map[string]any, at time.Time) error {
	var purpose string
	var test bool
	var to []string
	var template, topic *string
	err := q.QueryRow(ctx, `SELECT e.purpose, e.test, e.to_addresses, t.handle, tp.key FROM emails e
		LEFT JOIN email_templates t ON t.id = e.template_id
		LEFT JOIN email_topics tp ON tp.id = e.topic_id
		WHERE e.workspace_id = $1 AND e.id = $2`, ws, emailID).Scan(&purpose, &test, &to, &template, &topic)
	if err != nil || test {
		return err
	}
	if recipients == nil {
		recipients = to
	}
	props := map[string]any{"email_id": emailID, "purpose": purpose}
	if template != nil {
		props["template"] = *template
	}
	if topic != nil {
		props["topic"] = *topic
	}
	for k, v := range extra {
		props[k] = v
	}
	for _, addr := range recipients {
		addr = strings.ToLower(addr)
		rec, ok, err := customers.OwnerOf(ctx, q, ws, "email", addr)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if _, err := customers.Record(ctx, q, ws, customers.Event{RecordID: rec, Name: "email." + state, Properties: props,
			Source: "lifecycle", OccurredAt: at, MessageID: "email:" + emailID + ":" + state + ":" + addr}); err != nil {
			return err
		}
	}
	return nil
}

// timelineState is the customer timeline state a provider's delivery event
// stands for, or "". A soft bounce is a delay while the provider retries.
func timelineState(ev connectors.ProviderEvent) string {
	switch {
	case ev.Type == connectors.EventDelivered:
		return "delivered"
	case ev.Type == connectors.EventBounced && ev.BounceType != "soft":
		return "bounced"
	case ev.Type == connectors.EventComplaint:
		return "complained"
	case ev.Type == connectors.EventFailed:
		return "rejected"
	}
	return ""
}
