package connectors

import "context"

// CapPushLeads: the sequencer can add a person to one of the account's
// campaigns (LeadPusher), for journeys' push_to_sequencer steps.
const CapPushLeads = "push.leads"

// PushedLead is the person a push adds: their address, name and company,
// and the campaign's own variables for its templates.
type PushedLead struct {
	Email     string
	FirstName string
	LastName  string
	Company   string
	Website   string
	// Variables fill the campaign's custom fields, by their name there.
	Variables map[string]string
	// AllowElsewhere adds them even when they are in another of the
	// account's campaigns. Off, the platform leaves them out.
	AllowElsewhere bool
}

// What a push did.
const (
	PushAdded     = "added"      // they are in the campaign now
	PushAlreadyIn = "already_in" // they already were
	// PushRefused: the platform left them out (in another campaign,
	// unsubscribed or blocked there, an address it won't take); Reason
	// says which.
	PushRefused = "refused"
)

// LeadPushed is what a push did, with the platform's id for the lead when
// it says.
type LeadPushed struct {
	Outcome    string
	Reason     string
	ExternalID string
}

// LeadPusher adds one person to a platform campaign. PushLead must succeed
// (PushAlreadyIn) when they are already in it, so a retry never adds them
// twice; an error is the platform failing, never the person being left out.
type LeadPusher interface {
	Connector
	PushLead(ctx context.Context, c *Client, creds Creds, campaignID string, lead PushedLead) (LeadPushed, error)
}
