package connectors

import "context"

// Publishing: the brand's own original posts (not replies), written or
// approved by a person and posted on the brand's own account through the
// workspace's own connection. X posts through the workspace's X app,
// LinkedIn through the person's sign-in. Nothing here decides when to
// post; the engine's publish job calls Publish once per approved post.

// CapSocialPublish marks a connector that implements SocialPublisher.
const CapSocialPublish = "social.publish"

// SocialPostInput is one post to publish.
type SocialPostInput struct {
	// The text as it will appear, links included.
	Text string
	// The page or profile that posts, from SocialAccounts.
	Account SocialAccount
	// A key the channel can use to refuse a second post with the same key,
	// where it supports one. The publish job also never retries a post
	// whose outcome is unknown, so a post is never made twice.
	IdempotencyKey string
}

// SocialPublisher posts as the connection's signed-in account.
type SocialPublisher interface {
	// CanPublish reports whether the connection is signed in with the
	// rights to post.
	CanPublish(creds Creds) bool
	// MaxLength is the longest text the channel takes, in its own count
	// (X counts a link as 23 characters).
	MaxLength() int
	// Publish makes the post and returns its id and address.
	Publish(ctx context.Context, cl *Client, creds Creds, in SocialPostInput) (SocialResult, error)
}
