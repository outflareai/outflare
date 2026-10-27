package connectors

import (
	"context"
	"net/http"
	"time"
)

// Email providers send lifecycle and transactional email (receipts,
// password resets, onboarding) to people who asked for it, from the
// workspace's own provider account. Cold outreach never goes through them:
// their terms forbid it, and it stays on mailboxes.

const (
	CategoryEmailSending = "email_sending"
	CapSendEmail         = "email.send" // implements EmailSender
)

// OutboundEmail is one message to hand to a provider.
type OutboundEmail struct {
	// ID is Outflare's id for the email. Providers that take an
	// idempotency key get it, so a retried send is never sent twice, and
	// it is attached as a tag where they allow one.
	ID      string
	From    string
	To      []string
	Cc      []string
	Bcc     []string
	ReplyTo []string
	Subject string
	HTML    string
	Text    string
	Headers map[string]string
	// Tags are short labels for the provider's own reports.
	Tags []string
	// Config is the provider's settings saved when its events were set up,
	// such as the SES configuration set.
	Config map[string]string
}

// DomainRecord is one DNS record a provider needs for a sending domain.
type DomainRecord struct {
	// Purpose: dkim, spf, return_path (custom MAIL FROM), dmarc or
	// verification.
	Purpose  string `json:"purpose"`
	Type     string `json:"type"` // TXT, CNAME or MX
	Name     string `json:"name"` // the full name, e.g. selector._domainkey.example.com
	Value    string `json:"value"`
	Priority int    `json:"priority,omitempty"` // MX only
	// Required records must be in place before the domain sends. DMARC is
	// recommended, not required by providers.
	Required bool `json:"required"`
	// Status from the last check: pending (not seen yet), verified,
	// missing or mismatch (a different value is there).
	Status string `json:"status"`
	// Found is what the public DNS answered, when it differs, and the
	// DMARC policy in place, which may differ from the one suggested.
	Found string `json:"found,omitempty"`
	// Hint says in words what looks wrong, when the check can tell.
	Hint string `json:"hint,omitempty"`
}

// DomainSetup is a provider's view of a sending domain.
type DomainSetup struct {
	Ref      string         // the provider's id for the domain, if it has one
	Records  []DomainRecord // what to add
	Verified bool           // the provider will sign and send for it
	Reason   string         // why not, when the provider says
}

// Delivery event types a provider reports back, normalised.
const (
	EventSent      = "sent"      // the provider accepted it
	EventDelivered = "delivered" // the receiving server accepted it
	EventDelayed   = "delayed"   // a temporary failure; the provider keeps trying
	EventBounced   = "bounced"   // BounceType says hard or soft
	EventComplaint = "complained"
	EventFailed    = "failed" // the provider gave up or refused it
	EventOpened    = "opened"
	EventClicked   = "clicked"
)

// ProviderEvent is one delivery event from a provider's callback.
type ProviderEvent struct {
	MessageID  string // the provider's message id, as Send returned it
	Type       string
	BounceType string // hard or soft, for bounces
	Recipient  string
	Reason     string // the provider's explanation, such as an SMTP answer
	EventID    string // the provider's id for the event, to drop repeats
	At         time.Time
}

// InactiveRecipient means the provider refused the recipient because of
// an earlier hard bounce or complaint: the email is suppressed, not failed.
type InactiveRecipient struct{ Message string }

func (e *InactiveRecipient) Error() string { return e.Message }

// ErrEventsManual means the provider's events must be connected by hand:
// its API cannot register the callback.
var ErrEventsManual = Errf(ErrInvalid, "events must be connected by hand")

// EmailSender is implemented by email provider connectors.
type EmailSender interface {
	// Send hands the email over and returns the provider's message id.
	// Errors are *Error: ErrQuota and ErrUnavailable are worth retrying,
	// the rest are not.
	Send(ctx context.Context, c *Client, creds Creds, m OutboundEmail) (string, error)
	// AddDomain registers a sending domain (or finds it, when it is
	// already there) and returns the records it needs.
	AddDomain(ctx context.Context, c *Client, creds Creds, domain string) (DomainSetup, error)
	// CheckDomain asks the provider whether the domain is verified yet.
	CheckDomain(ctx context.Context, c *Client, creds Creds, domain, ref string) (DomainSetup, error)
	// SetUpEvents points the provider's delivery events at url. It returns
	// settings to keep (a signing secret, a configuration set name), or
	// ErrEventsManual when a person has to add the URL themselves.
	SetUpEvents(ctx context.Context, c *Client, creds Creds, url string) (map[string]string, error)
	// ParseEvents reads a callback the provider sent to that url, checking
	// its signature with the kept settings. A callback that only confirms
	// the subscription returns no events.
	ParseEvents(ctx context.Context, c *Client, r *http.Request, body []byte, config map[string]string) ([]ProviderEvent, error)
}
