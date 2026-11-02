package connectors

import (
	"context"
	"time"
)

// CategoryKnowledge is where a team already writes answers down, read so
// support answers can learn from it.
const CategoryKnowledge = "knowledge"

// CapReadMessages marks a chat tool Outflare may read chosen channels of:
// the connector implements MessageReader.
const CapReadMessages = "messages.read"

// Message is one message in a channel, with its thread's replies. Author
// is the tool's own id for who wrote it, never their name.
type Message struct {
	ID      string    `json:"id"`
	Channel string    `json:"channel"`
	Author  string    `json:"author"`
	Bot     bool      `json:"bot"`
	Text    string    `json:"text"`
	At      time.Time `json:"at"`
	Link    string    `json:"link"`
	Replies []Message `json:"replies,omitempty"`
}

// MessageReader reads the channels a workspace chose, and nothing else.
type MessageReader interface {
	Channels(ctx context.Context, cl *Client, creds Creds) ([]Channel, error)
	// Join makes the app a member of a public channel, so it can read it.
	// Private channels need the app invited by a member.
	Join(ctx context.Context, cl *Client, creds Creds, channel string) error
	// Recent returns the channel's messages since since, newest first, at
	// most limit, each with its thread's replies when it has any.
	Recent(ctx context.Context, cl *Client, creds Creds, channel string, since time.Time, limit int) ([]Message, error)
}
