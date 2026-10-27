package connectors

import (
	"context"
	"time"
)

// Social channels (Reddit, then X and LinkedIn) implement these, so the
// engine's watches and approval queue work the same on every network.
// Every action runs on the brand's own account, through the workspace's
// own connection.

// SocialPost is a post a search found.
type SocialPost struct {
	// The channel's own id, such as a Reddit fullname (t3_...).
	ExternalID string
	// Where it was posted, such as a subreddit.
	Community string
	Title     string
	Body      string
	Author    string
	URL       string
	Score     int
	Comments  int
	PostedAt  time.Time
	// The channel closed it to replies (archived or locked).
	Closed bool
}

// SocialResult is what an action made on the channel, such as a comment.
type SocialResult struct {
	ExternalID string
	URL        string
	Score      int
	// A moderator or the author took it down.
	Removed bool
}

// SocialSearcher finds recent posts on a channel.
type SocialSearcher interface {
	// SearchPosts finds recent posts matching query, in community when
	// one is given or else across the channel, newest first.
	SearchPosts(ctx context.Context, cl *Client, creds Creds, community, query string, limit int) ([]SocialPost, error)
}

// SocialReplier replies to posts as the connection's signed-in account.
type SocialReplier interface {
	// CanAct reports whether the connection is signed in to an account
	// it can act as.
	CanAct(creds Creds) bool
	// ActingAs is the account's handle, so its own posts are skipped.
	ActingAs(creds Creds) string
	ReplyTo(ctx context.Context, cl *Client, creds Creds, externalID, text string) (SocialResult, error)
	// Results reads what earlier actions made, by their external ids.
	Results(ctx context.Context, cl *Client, creds Creds, ids []string) (map[string]SocialResult, error)
}
