package connectors

import "context"

// CapNotify marks a chat tool alerts can post to: the connector implements
// Notifier.
const CapNotify = "notify.post"

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
