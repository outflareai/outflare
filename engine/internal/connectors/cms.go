package connectors

import "context"

// Publishing content: posts and pages written in Outflare (by a person, or
// drafted by an agent and approved) go to the brand's own website through
// the workspace's own CMS connection. Nothing here decides what to
// publish or when; the engine's publish job (cmspublish) calls Publish
// once per queued publication and reads the post back to see that the
// CMS shows it.

// CapCMSPublish marks a connector that implements CMSPublisher.
const CapCMSPublish = "cms.publish"

// CMS targets: what kind of thing a post is published as.
const (
	CMSPosts = "posts"
	CMSPages = "pages"
)

// CMSPostInput is one post or page to create or update.
type CMSPostInput struct {
	// posts or pages (Targets lists what the CMS takes).
	Target string
	// The post on the CMS to update; empty creates one.
	ExternalID string
	Title      string
	// The address slug; empty lets the CMS choose.
	Slug string
	// The body as HTML, already rendered from the workspace's Markdown
	// with raw HTML left out.
	HTML    string
	Excerpt string
	// The canonical address, when the content first appeared elsewhere.
	CanonicalURL string
	Tags         []string
	// Live publishes it; otherwise it is a draft on the CMS.
	Live bool
	// A key the CMS can use to refuse a second copy, where it supports
	// one. The publish job also never retries a create whose outcome is
	// unknown, so a post is never made twice.
	IdempotencyKey string
}

// CMSResult is a post as the CMS shows it.
type CMSResult struct {
	ExternalID string
	URL        string
	// draft or published, as the CMS reports it.
	Status string
	Slug   string
}

// CMSPublisher creates and updates posts on the connection's site.
type CMSPublisher interface {
	// Targets lists what the CMS publishes: posts, pages.
	Targets() []string
	// Publish creates the post (no ExternalID) or updates it, and returns
	// it as the CMS shows it.
	Publish(ctx context.Context, cl *Client, creds Creds, in CMSPostInput) (CMSResult, error)
	// ReadPost reads one post back.
	ReadPost(ctx context.Context, cl *Client, creds Creds, target, externalID string) (CMSResult, error)
}
