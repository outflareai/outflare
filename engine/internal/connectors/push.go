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
	// Note is written on a contact the push creates, where the platform
	// keeps notes. It also marks the contact as this push's own: a
	// platform that otherwise leaves out a contact it already holds may
	// take one back that carries the same note and was never used (no
	// campaign, nothing sent, no reply), which is an earlier try of the
	// same push that failed before the contact went into the campaign.
	Note string
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

// LeadsPusher adds several people to one platform campaign in as few calls
// as the platform allows. It follows LeadPusher's rules for each person,
// and answers one PushOutcome per lead, in their order: what the push did,
// or the error that kept it from that person (a call for the whole batch
// that failed is every person's error). A connector that has it also has
// PushLead.
type LeadsPusher interface {
	LeadPusher
	PushLeads(ctx context.Context, c *Client, creds Creds, campaignID string, leads []PushedLead) []PushOutcome
}

// PushOutcome is what a push did for one person of a batch, or Err.
type PushOutcome struct {
	LeadPushed
	Err error
}

// CapCampaignReadiness: the sequencer can say whether a campaign is ready
// to take new people (CampaignReadiness), read before people are added.
const CapCampaignReadiness = "observe.campaign_readiness"

// Readiness is what a sequencer says about one campaign before people are
// added to it.
type Readiness struct {
	// Reasons say why the campaign can't take people now (it is paused,
	// its sequence has no steps, it has no sending days, no connected
	// sender). None means it is ready.
	Reasons []string
	// DailyCapacity is how many new people a day the campaign can start:
	// its connected senders' daily limits, within the campaign's own
	// limits. Zero when Reasons say it can't send.
	DailyCapacity int
	// Senders is how many connected senders it has.
	Senders int
}

// Ready says whether the campaign can take people.
func (r Readiness) Ready() bool { return len(r.Reasons) == 0 }

// CampaignReadiness reads whether a campaign is ready to take people. It
// only reads. An error is the platform not answering, never the campaign
// not being ready.
type CampaignReadiness interface {
	CampaignReady(ctx context.Context, c *Client, creds Creds, campaignID string) (Readiness, error)
}

// CapPushContacts: the CRM can create or update a person by their email
// (ContactPusher), for journeys' push_to_crm steps.
const CapPushContacts = "push.contacts"

// PushedContact is the person a CRM push creates or updates: their
// address, name and company, and the CRM's own properties by their
// internal names there.
type PushedContact struct {
	Email     string
	FirstName string
	LastName  string
	Company   string
	Website   string
	// Properties are set as given, after the fields above, so they can
	// also replace them.
	Properties map[string]string
}

// ContactPushed is what a CRM push did: whether it made a new contact,
// and the CRM's id for it.
type ContactPushed struct {
	Created    bool
	ExternalID string
}

// ContactPusher creates the person in a CRM, or updates the contact that
// has their email. Pushing the same person again updates the same contact,
// so a retry never makes a second one. An empty field leaves what the CRM
// holds.
type ContactPusher interface {
	Connector
	PushContact(ctx context.Context, c *Client, creds Creds, p PushedContact) (ContactPushed, error)
}

// CapContactHistory: the sequencer can say whether it has already emailed
// an address (ContactHistory), so a feature doesn't contact someone the
// account is already writing to.
const CapContactHistory = "observe.contact_history"

// SentBefore is what a sequencer knows about one address.
type SentBefore struct {
	// Found is false when the account has no lead with the address.
	Found bool
	// EmailsSent is how many emails the account has sent them, across its
	// campaigns.
	EmailsSent int
}

// ContactHistory reads whether the account has emailed an address. It
// only reads.
type ContactHistory interface {
	ContactSent(ctx context.Context, c *Client, creds Creds, email string) (SentBefore, error)
}
