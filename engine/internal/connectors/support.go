package connectors

import (
	"context"
	"time"
)

// CategorySupport is where a business answers its customers today
// (Intercom, Zendesk): conversations are copied into the support inbox.
const CategorySupport = "support"

// CapImportSupport marks a support tool whose conversations Outflare copies
// in and keeps in step: the connector implements SupportImporter.
const CapImportSupport = "import.support"

// SupportImporter reads a support tool's conversations, read-only, so the
// support inbox holds the same history as the tool.
type SupportImporter interface {
	Connector
	// SupportAgents lists the tool's teammates and teams.
	SupportAgents(ctx context.Context, c *Client, creds Creds) ([]SupportAgent, error)
	// SupportChanged lists conversations the tool changed after since,
	// oldest change first, one page at a time: an empty cursor starts, and
	// an empty next means the last page.
	SupportChanged(ctx context.Context, c *Client, creds Creds, since time.Time, cursor string, limit int) (refs []SupportRef, next string, err error)
	// SupportConversation reads one conversation with its messages.
	SupportConversation(ctx context.Context, c *Client, creds Creds, id string) (SupportConversation, error)
	// SupportContact reads one customer. ok is false when the tool no
	// longer has them (deleted, or merged into another).
	SupportContact(ctx context.Context, c *Client, creds Creds, id string) (contact SupportContact, ok bool, err error)
}

// SupportRef is a conversation a sync should look at.
type SupportRef struct {
	ID        string
	UpdatedAt time.Time
}

// Support author kinds and message kinds, as the inbox stores them.
const (
	SupportFromCustomer = "customer"
	SupportFromAgent    = "agent"
	SupportFromAI       = "ai"
	SupportFromSystem   = "system"
)

// SupportAgent is a teammate (admin), a team, or a bot of the tool.
type SupportAgent struct {
	Kind  string // admin, team or bot
	ID    string
	Name  string
	Email string // to find the Outflare user; never stored
}

// SupportContact is a customer as the tool knows them.
type SupportContact struct {
	ID    string
	Email string
	Name  string
	Role  string // the tool's own word: user or lead in Intercom
}

// SupportConversation is one conversation, as the support inbox stores it.
type SupportConversation struct {
	ID string
	// Channel is email or chat; ExternalChannel the tool's own name for it.
	Channel         string
	ExternalChannel string
	Subject         string
	// Status is open, pending (snoozed) or closed.
	Status       string
	Priority     string // "", low, medium, high or urgent
	CreatedAt    time.Time
	UpdatedAt    time.Time
	FirstReplyAt *time.Time
	ClosedAt     *time.Time
	Reopens      int
	// ContactID is the customer it is with; empty when it has none.
	ContactID  string
	AssigneeID string // a teammate's id, or empty
	TeamID     string // a team's id, or empty
	Tags       []string
	Rating     *SupportRating
	AI         *SupportAI
	Messages   []SupportMessage
}

// SupportRating is the customer's rating of a conversation.
type SupportRating struct {
	Score  int
	Remark string
	At     time.Time
}

// SupportAI is what the tool's AI agent did in a conversation.
type SupportAI struct {
	Agent        string // fin
	Resolution   string
	AnswerType   string
	SourceType   string
	SourceTitle  string
	Rating       int // 0 when not rated
	RatingRemark string
	Sources      []SupportAISource
}

// SupportAISource is something the AI agent answered from.
type SupportAISource struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Locale string `json:"locale"`
}

// SupportMessage is one message: in (from the customer), out (to them) or
// note (for the team only).
type SupportMessage struct {
	ID         string
	Kind       string
	AuthorType string // customer, agent, ai or system
	// AuthorKind and AuthorID name the teammate, team or bot who wrote an
	// out message or note; AuthorName is their name in the tool.
	AuthorKind string
	AuthorID   string
	AuthorName string
	Body       string
	At         time.Time
}
