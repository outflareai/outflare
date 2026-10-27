package connectors

import (
	"context"
	"net/http"
	"time"
)

// CapVendorMailboxes marks a mailbox supplier whose mailboxes can be
// listed and imported: the connector implements MailboxVendor.
const CapVendorMailboxes = "vendor.mailboxes"

// Supplier-neutral mailbox statuses.
const (
	VendorProvisioning = "provisioning" // ordered, not usable yet
	VendorActive       = "active"       // usable; credentials can be read
	VendorCancelling   = "cancelling"   // still works, ends at the period end
	VendorCancelled    = "cancelled"    // gone, or about to be
	VendorFailed       = "failed"       // provisioning failed on the supplier's side
)

// VendorMailbox is one mailbox at a supplier, in neutral terms.
type VendorMailbox struct {
	ExternalID string `json:"external_id"`
	Email      string `json:"email"`
	FirstName  string `json:"first_name"`
	LastName   string `json:"last_name"`
	// google, microsoft or other.
	Provider string `json:"provider"`
	// One of the Vendor* statuses above.
	Status string `json:"status"`
	// The supplier's own status word, kept for people who know it.
	VendorStatus string `json:"vendor_status"`
	// The supplier's per-mailbox send cap, when it sets one.
	DailyLimit int      `json:"daily_limit,omitempty"`
	Tags       []string `json:"tags"`
	// The mailbox is also connected to these outside sending platforms
	// (e.g. emailbison, instantly), as the supplier reports it.
	ConnectedTo []string `json:"connected_to"`
	// Google app password state at the supplier: none, pending, ready, failed.
	AppPassword string     `json:"app_password"`
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	ActivatedAt *time.Time `json:"activated_at,omitempty"`
}

// VendorCredentials sign a mailbox in. Empty hosts mean the provider's
// defaults.
type VendorCredentials struct {
	Email       string
	Username    string
	Password    string
	AppPassword string
	SMTPHost    string
	SMTPPort    int
	IMAPHost    string
	IMAPPort    int
}

// VendorPage asks for one page of mailboxes. An empty cursor is the start.
type VendorPage struct {
	Cursor string
	Limit  int
}

// MailboxVendor is a mailbox supplier: Cheap Inboxes today, others the
// same way. Listing and reading credentials must not change anything at
// the supplier or cost money.
type MailboxVendor interface {
	Connector
	// ListMailboxes returns one page and the cursor for the next, empty at
	// the end.
	ListMailboxes(ctx context.Context, c *Client, creds Creds, page VendorPage) ([]VendorMailbox, string, error)
	GetMailbox(ctx context.Context, c *Client, creds Creds, externalID string) (VendorMailbox, error)
	// MailboxCredentials fails with ErrInvalid while the mailbox is not
	// active yet.
	MailboxCredentials(ctx context.Context, c *Client, creds Creds, externalID string) (VendorCredentials, error)
}

// App password states.
const (
	AppPasswordNone    = "none"
	AppPasswordPending = "pending"
	AppPasswordReady   = "ready"
	AppPasswordFailed  = "failed"
)

// AppPasswordVendor can create Google app passwords for its mailboxes,
// which Google requires for SMTP and IMAP sign-in with a password.
type AppPasswordVendor interface {
	MailboxVendor
	// RequestAppPassword starts generation. It is idempotent while a
	// request is pending.
	RequestAppPassword(ctx context.Context, c *Client, creds Creds, externalID string) error
	// AppPasswordStatus returns the state, the password once ready, and the
	// supplier's reason when it failed.
	AppPasswordStatus(ctx context.Context, c *Client, creds Creds, externalID string) (state, password, reason string, err error)
	// AppPasswordRate is how many requests the supplier accepts per hour
	// per account.
	AppPasswordRate() int
}

// Neutral webhook event types.
const (
	EventMailboxActive      = "mailbox.active"
	EventMailboxCredentials = "mailbox.credentials_ready"
	EventMailboxChanged     = "mailbox.changed" // anything else about a mailbox
	EventOther              = "other"
)

// VendorEvent is a verified webhook delivery.
type VendorEvent struct {
	// The supplier's event id; deliveries are at least once, so dedupe on it.
	ID   string
	Type string
	// The mailbox it is about, when it is about one.
	MailboxExternalID string
	// Set when the event may affect many mailboxes (an order completed,
	// a domain was provisioned): resync the account.
	Resync bool
}

// WebhookVendor pushes changes instead of waiting to be polled.
type WebhookVendor interface {
	MailboxVendor
	// RegisterWebhook subscribes url and returns the subscription id and
	// the signing secret.
	RegisterWebhook(ctx context.Context, c *Client, creds Creds, url string) (id, secret string, err error)
	DeleteWebhook(ctx context.Context, c *Client, creds Creds, id string) error
	// ParseWebhook verifies the signature over the raw body and decodes it.
	ParseWebhook(secret string, h http.Header, body []byte) (VendorEvent, error)
}

// PushVendor can push mailboxes into Outflare itself, by treating Outflare
// as one of the sequencers it supports (Cheap Inboxes sees a self-hosted
// EmailBison). Importing reads mailboxes; pushing lets the supplier send
// them, the way it does for sequencers.
type PushVendor interface {
	MailboxVendor
	// ConnectPush adds Outflare as a sequencer at the supplier and returns
	// the supplier's id for that connection.
	ConnectPush(ctx context.Context, c *Client, creds Creds, t PushTarget) (string, error)
	// PushMailboxes asks the supplier to push these mailboxes (by their
	// supplier ids) through the connection.
	PushMailboxes(ctx context.Context, c *Client, creds Creds, remoteID string, externalIDs []string) (PushResult, error)
	// DisconnectPush removes the connection at the supplier.
	DisconnectPush(ctx context.Context, c *Client, creds Creds, remoteID string) error
}

// PushTarget is where and how a supplier reaches Outflare.
type PushTarget struct {
	Name    string
	BaseURL string
	APIKey  string
	// LoginEmail and LoginPassword fill the sequencer UI login a supplier
	// may require. Outflare has no sequencer UI for it to sign in to, so the
	// password is random and never kept.
	LoginEmail    string
	LoginPassword string
}

// PushResult counts what one push did.
type PushResult struct {
	Synced  int `json:"synced"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}
