package connectors

import (
	"context"
	"time"
)

// CapReadFeeds marks a chat tool whose chosen channels Outflare reads as a
// feed of posts from other tools: the connector implements FeedReader.
const CapReadFeeds = "feeds.read"

// CapSearchMessages marks a chat tool whose messages can be searched live:
// the connector implements MessageSearcher.
const CapSearchMessages = "messages.search"

// FeedMessage is one message in a feed channel. Author is the tool's own id
// for a person or bot, never a name.
type FeedMessage struct {
	ID          string // Slack ts
	Author      string // Slack user or bot id, never a name
	Bot         bool
	Source      string // the name of the app that posted ("Fillout"); "" for a person
	Text        string // text, then attachments, then blocks, flattened
	At          time.Time
	EditedAt    *time.Time
	ReplyCount  int
	LatestReply string // ts of the newest thread reply, "" when none
}

// SearchHit is one message a live search found.
type SearchHit struct {
	Channel Channel // ID, Name; Private is true for a private channel, direct message or group message
	Author  string
	Text    string
	At      time.Time
	Link    string
	// Public is true only when Slack says the match is in a public channel
	// that is not shared: a channel object with an id, is_channel, and none of
	// the private, direct, group or shared flags. Anything unclear is false.
	Public bool
}

// FeedReader reads the channels a workspace chose as feeds, and nothing else.
type FeedReader interface {
	Channels(ctx context.Context, cl *Client, creds Creds) ([]Channel, error)
	// History returns the channel's top-level messages with oldest < ts < latest,
	// oldest first, following Slack's cursor to the end. latest "" means now.
	History(ctx context.Context, cl *Client, creds Creds, channel, oldest, latest string) ([]FeedMessage, error)
	// Latest returns the channel's newest limit top-level messages, newest
	// first, from one call (and one more page at most when filtering leaves
	// fewer). It never reads the channel's history.
	Latest(ctx context.Context, cl *Client, creds Creds, channel string, limit int) ([]FeedMessage, error)
	// Thread returns every reply under ts, oldest first, without the parent.
	Thread(ctx context.Context, cl *Client, creds Creds, channel, ts string) ([]FeedMessage, error)
	// BaseURL is the workspace's address, "https://acme.slack.com/", from auth.test.
	BaseURL(ctx context.Context, cl *Client, creds Creds) (string, error)
}

// CapPostFeedAlerts marks a chat tool Outflare can post a message to, change
// it and put emojis on it: the connector implements FeedPoster.
const CapPostFeedAlerts = "feeds.alert"

// FeedPoster posts one message per conversation to a channel the workspace
// picked and keeps it current. It needs more than reading does (in Slack,
// the bot scopes chat:write and reactions:write); without them the calls
// return ErrAuth.
type FeedPoster interface {
	// PostAlert posts text (the tool's own markup) and returns the
	// message's id.
	PostAlert(ctx context.Context, cl *Client, creds Creds, channel, text string) (string, error)
	UpdateAlert(ctx context.Context, cl *Client, creds Creds, channel, id, text string) error
	// React puts emoji (its short name, "eyes") on the message, or takes it
	// off. Doing either twice is not an error.
	React(ctx context.Context, cl *Client, creds Creds, channel, id, emoji string, on bool) error
}

// MessageSearcher searches messages live.
type MessageSearcher interface {
	// Search needs the connection's search token; without one it returns ErrInvalid.
	Search(ctx context.Context, cl *Client, creds Creds, query string, limit int) ([]SearchHit, error)
}

// SearchGate is implemented by a MessageSearcher whose connection may lack
// the credential search needs.
type SearchGate interface {
	CanSearch(creds Creds) bool
}

// CanSearchMessages says whether a connection can search messages live: its
// connector searches and, when it asks, the connection has what it needs.
func CanSearchMessages(conn Connector, creds Creds) bool {
	if _, ok := conn.(MessageSearcher); !ok {
		return false
	}
	if g, ok := conn.(SearchGate); ok {
		return g.CanSearch(creds)
	}
	return true
}
