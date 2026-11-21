package connectors

import "context"

// CapNotify marks a chat tool alerts can post to: the connector implements
// Notifier. A journey's Slack step posts through the first connection that
// has it.
const CapNotify = "notify.post"

// CapNotifyTelegram marks a Telegram bot alerts can post to: a Notifier
// too, with a capability of its own so nothing that wants a Slack channel
// (a journey's Slack step) picks it.
const CapNotifyTelegram = "notify.telegram"

// Channel is somewhere a Notifier can post.
type Channel struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Private bool   `json:"private"`
}

// Notice is one message to post: a title, a line of detail and a link.
type Notice struct {
	Title  string
	Text   string
	URL    string
	Button string
}

// Notifier posts notices to the workspace's own chat tool.
type Notifier interface {
	Channels(ctx context.Context, cl *Client, creds Creds) ([]Channel, error)
	Post(ctx context.Context, cl *Client, creds Creds, channel string, n Notice) error
}

// ChannelFinder is a Notifier that can look up one place to post by its
// id, for a chat tool that can't list them all (a Telegram bot only hears
// from the chats that wrote lately).
type ChannelFinder interface {
	Channel(ctx context.Context, cl *Client, creds Creds, id string) (Channel, error)
}
