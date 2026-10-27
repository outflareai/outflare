package connectors

import (
	"context"
	"time"
)

// CapReadReplies marks a sequencer whose replies Outflare brings into the
// inbox and answers through the platform: the connector implements
// ReplySource.
const CapReadReplies = "replies.read"

// Reply is one received reply as a platform reports it.
type Reply struct {
	// ExternalID is the platform's id for the reply; answering uses it.
	ExternalID string
	// MailboxExternalID and MailboxEmail are the sender email it arrived
	// at, as Observe reports it.
	MailboxExternalID string
	MailboxEmail      string
	FromEmail         string
	FromName          string
	Subject           string
	// Body is plain text; HTML is used only when there is none.
	Body       string
	HTML       string
	MessageID  string
	ReceivedAt time.Time
	// Automated is the platform's own word that this is an auto-reply.
	Automated bool
}

// ReplyOut answers a reply through the platform, from the sender email it
// arrived at and in the same thread.
type ReplyOut struct {
	ReplyExternalID   string
	MailboxExternalID string
	ToEmail           string
	ToName            string
	Body              string
}

// ReplySource reads a platform account's received replies and answers
// them. ListReplies must not change anything on the platform (it does not
// mark replies read).
type ReplySource interface {
	Connector
	// ListReplies returns the replies received at or after since, newest
	// first or in any order.
	ListReplies(ctx context.Context, c *Client, creds Creds, since time.Time) ([]Reply, error)
	SendReply(ctx context.Context, c *Client, creds Creds, r ReplyOut) error
}
