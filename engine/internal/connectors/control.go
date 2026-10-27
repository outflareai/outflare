package connectors

import "context"

// Capabilities for mailbox allocation on outside sequencers.
const (
	// CapObserveMailboxes: the connector reports the account's mailboxes,
	// their state and which campaigns they are attached to (Observer).
	CapObserveMailboxes = "observe.mailboxes"
	// CapControlMailboxes: it can also attach mailboxes to a campaign,
	// detach them and set their daily limits (Controller).
	CapControlMailboxes = "control.mailboxes"
	// CapControlWarmup: it can turn the platform's own warm-up on and off
	// for a mailbox (WarmupController).
	CapControlWarmup = "control.warmup"
)

// Platform mailbox statuses, the same for every platform.
const (
	MailboxOK           = "ok"           // connected and allowed to send
	MailboxPaused       = "paused"       // paused by a person on the platform
	MailboxError        = "error"        // the platform cannot send from it, e.g. a failed login
	MailboxDisconnected = "disconnected" // removed or never finished connecting
)

// MailboxState is one mailbox as a platform account reports it.
type MailboxState struct {
	ExternalID    string
	Email         string
	Name          string
	Status        string
	StatusReason  string
	DailyLimit    *int
	WarmupEnabled bool
	// WarmupScore is the platform's warm-up health, 0-100, when it has one.
	WarmupScore *int
	// CampaignIDs are the platform campaigns it is attached to.
	CampaignIDs []string
}

// CampaignState is one campaign as a platform account reports it.
type CampaignState struct {
	ExternalID string
	Name       string
	// Status is the platform's word: active, paused, completed or draft.
	Status string
	// DailyLimit is the campaign's own daily cap, when it has one.
	DailyLimit *int
	// SequenceDays is the days from a lead's first email to its last step.
	SequenceDays *int
}

// Snapshot is everything allocation needs from a platform account.
type Snapshot struct {
	Mailboxes []MailboxState
	Campaigns []CampaignState
}

// Observer reads a platform account's mailboxes and campaigns. Observe
// must not change anything on the platform.
type Observer interface {
	Connector
	Observe(ctx context.Context, c *Client, creds Creds) (Snapshot, error)
}

// Controller changes which mailboxes a platform campaign sends from. Each
// call takes the mailboxes' ExternalIDs and Emails from the last Observe.
// Attach and Detach must leave the campaign's other mailboxes alone, and
// must succeed when the mailbox is already attached or detached.
type Controller interface {
	Observer
	Attach(ctx context.Context, c *Client, creds Creds, campaignID string, mailboxes []MailboxState) error
	Detach(ctx context.Context, c *Client, creds Creds, campaignID string, mailboxes []MailboxState) error
	SetDailyLimit(ctx context.Context, c *Client, creds Creds, mailbox MailboxState, limit int) error
}

// WarmupController turns a platform's own warm-up on or off. Each call
// takes the mailboxes' ExternalIDs and Emails from the last Observe, and
// must succeed when warm-up is already as asked.
type WarmupController interface {
	Observer
	SetWarmup(ctx context.Context, c *Client, creds Creds, mailboxes []MailboxState, on bool) error
}
