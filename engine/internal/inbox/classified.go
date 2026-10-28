package inbox

import (
	"context"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// Classification is a label given to a received message, by the rules,
// the AI, a person or an agent.
type Classification struct {
	WorkspaceID string
	InboundID   string
	LeadID      *string
	SequenceID  *string
	MailboxID   string
	Source      string // mailbox, or the sequencer's connector id
	Category    *string
	Previous    *string
	// By is who set it: rules, ai, human, api_token, agent.
	By          string
	Confidence  float64
	NeedsReview bool
	// Actor for the audit row; empty for the system.
	ActorID   string
	ActorType string
	RequestID string
}

// Classified records inbox.classified in the transaction that set the
// label, so webhooks, the events feed and agents hear about it together,
// and settles a label that needs no review (rules and alerts).
// The data carries ids and enums only; the reply itself is one GET away.
func Classified(ctx context.Context, q jobs.Querier, c Classification) error {
	group := ""
	if c.Category != nil {
		group = GroupOf(*c.Category)
	}
	actorType := c.ActorType
	if actorType == "" {
		actorType = "system"
	}
	data := map[string]any{
		"inbound_id": c.InboundID, "lead_id": c.LeadID, "sequence_id": c.SequenceID, "mailbox_id": c.MailboxID,
		"source": c.Source, "category": c.Category, "group": nilIfEmpty(group), "previous_category": c.Previous,
		"category_source": c.By, "confidence": c.Confidence, "needs_review": c.NeedsReview,
	}
	props := map[string]any{"source": c.Source, "category_source": c.By, "needs_review": c.NeedsReview,
		"confidence_band": band(c.Confidence)}
	if c.Category != nil {
		props["category"], props["group"] = *c.Category, group
	}
	if err := audit.Record(ctx, q, audit.Event{WorkspaceID: c.WorkspaceID, ActorID: c.ActorID, ActorType: actorType,
		Action: "inbox.classified", TargetType: "inbound_message", TargetID: c.InboundID, RequestID: c.RequestID,
		Data: data, Props: props}); err != nil {
		return err
	}
	// A label someone or something is sure of runs the reply rules, and
	// alerts when it is hot.
	if c.Category == nil || c.NeedsReview {
		return nil
	}
	return Settled(ctx, q, c.WorkspaceID, c.InboundID, *c.Category, c.ActorID, c.RequestID)
}

func band(c float64) string {
	switch {
	case c >= 0.9:
		return "high"
	case c >= 0.8:
		return "medium"
	}
	return "low"
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
