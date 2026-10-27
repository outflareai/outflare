package connectors

import "context"

// CapBlocklist marks a sequencer whose blocklist Outflare keeps in step
// with the workspace's opt-out list: the connector implements Blocklist.
const CapBlocklist = "blocklist.sync"

// Blocked is one entry on a platform's blocklist: an email address, or a
// whole domain when Domain is set.
type Blocked struct {
	Value  string
	Domain bool
}

// Blocklist adds to a platform account's blocklist, so a send from the
// platform honours the workspace's opt-out list. Block adds entries and
// skips ones already there; nothing removes any.
type Blocklist interface {
	Connector
	// Block adds at most BlockBatch entries.
	Block(ctx context.Context, c *Client, creds Creds, entries []Blocked) error
}

// BlocklistReader is a Blocklist that can also be read, so what is
// blocked there joins the workspace's opt-out list.
type BlocklistReader interface {
	Blocklist
	ListBlocked(ctx context.Context, c *Client, creds Creds) ([]Blocked, error)
}

// BlockBatch is the most entries one Block call takes.
const BlockBatch = 500
