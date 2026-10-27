package connectors

import (
	"context"
	"time"
)

// Social accounts: the brand's own pages and profiles on a channel (X,
// LinkedIn), whose followers and posts Outflare tracks every day. A
// connection can hold more than one: a LinkedIn sign-in reaches the
// person's own profile and every company page they administer.

// CapSocialMetrics marks a connector that implements SocialMetrics.
const CapSocialMetrics = "social.metrics"

// Kinds of social account.
const (
	SocialProfile = "profile" // a person's own profile, or an X account
	SocialPage    = "page"    // a company page
)

// SocialAccount is a page or profile a connection can read and act as.
type SocialAccount struct {
	// The channel's own id, such as an X user id or a LinkedIn URN.
	ExternalID string
	Kind       string
	// The @handle or vanity name, without the @.
	Handle    string
	Name      string
	URL       string
	AvatarURL string
}

// SocialStats is what an account looks like today. A count the channel
// does not report is nil, never zero.
type SocialStats struct {
	Followers *int64
	Following *int64
	Posts     *int64
	// The account's name and picture, when the read shows them (a public
	// profile page does); empty keeps what is stored.
	Name      string
	AvatarURL string
}

// SocialPostMetrics is how one of the account's own posts did so far. A
// count the channel does not report is nil.
type SocialPostMetrics struct {
	Impressions *int64
	Likes       *int64
	Replies     *int64
	Reposts     *int64
	Quotes      *int64
	Bookmarks   *int64
	Clicks      *int64
}

// SocialOwnPost is one of the account's own posts.
type SocialOwnPost struct {
	ExternalID string
	URL        string
	Body       string
	PostedAt   time.Time
	Metrics    SocialPostMetrics
}

// SocialMetrics reads the accounts a connection reaches, their followers
// and their own recent posts.
type SocialMetrics interface {
	// SocialAccounts lists the pages and profiles the connection reaches.
	// An empty list means it is not signed in to an account yet.
	SocialAccounts(ctx context.Context, cl *Client, creds Creds) ([]SocialAccount, error)
	// SocialStats reads an account's counts today.
	SocialStats(ctx context.Context, cl *Client, creds Creds, a SocialAccount) (SocialStats, error)
	// SocialPosts reads the account's own posts since a time, newest
	// first, at most limit.
	SocialPosts(ctx context.Context, cl *Client, creds Creds, a SocialAccount, since time.Time, limit int) ([]SocialOwnPost, error)
}

// AccountSignInPKCE is an AccountSignIn whose provider needs PKCE, such as
// X: the sign-in link carries the challenge of a verifier the server keeps
// in its sealed state, and the code is traded with that verifier.
type AccountSignInPKCE interface {
	SignInURLPKCE(cl *Client, creds Creds, redirectURI, state, verifier string) string
	SignInPKCE(ctx context.Context, cl *Client, creds Creds, code, redirectURI, verifier string) (Creds, Account, error)
}

// CredsRefresher renews credentials the server's own OAuth apps do not
// cover, such as an account signed in through the workspace's own app.
// Open calls it, one at a time per connection, and stores what changed,
// because some providers (X) replace the refresh token on every use.
type CredsRefresher interface {
	// RefreshDue reports whether the credentials need renewing now.
	RefreshDue(creds Creds) bool
	RefreshCreds(ctx context.Context, cl *Client, creds Creds) (Creds, error)
}

// Int64 returns a pointer to n, for counts in stats.
func Int64(n int64) *int64 { return &n }
