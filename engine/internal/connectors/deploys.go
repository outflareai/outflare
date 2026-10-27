package connectors

import (
	"context"
	"time"
)

// Code and deploys: connectors that say when a website changed. A watched
// target (a GitHub repository, a Vercel project) reports each production
// deploy, so features that wait for a change to go live (a fix's re-check,
// an experiment's start) act on the deploy instead of guessing. They only
// read: Outflare never pushes code or starts a deploy through them.

// CategoryCode groups code hosts and deploy platforms.
const CategoryCode = "code"

// CapDeploys marks a connector that implements DeployReader.
const CapDeploys = "deploys.read"

// DeployTarget is something a DeployReader can watch: a repository or a
// project.
type DeployTarget struct {
	// ID is what Deploys takes: "owner/name" on GitHub, the project id on
	// Vercel.
	ID   string `json:"id"`
	Name string `json:"name"`
	// URL is the target's page on the provider.
	URL string `json:"url,omitempty"`
	// Branch is the branch production deploys from, when known.
	Branch string `json:"branch,omitempty"`
	// Domains are the websites it serves, when the provider knows them,
	// so a target can be matched to a site.
	Domains []string `json:"domains,omitempty"`
}

// Deploy states.
const (
	DeployReady    = "ready"    // live
	DeployFailed   = "failed"   // errored or cancelled
	DeployBuilding = "building" // not finished yet
)

// Deploy is one production deploy of a target.
type Deploy struct {
	// ID is the provider's id for it, stable across reads.
	ID string
	// Kind is deployment (the provider's deploy record) or push (a commit
	// to the production branch of a repository that records no deploys).
	Kind  string
	State string
	// Commit is the commit it shipped, Ref its branch, Message the
	// commit's first line.
	Commit  string
	Ref     string
	Message string
	// URL is where to see it on the provider.
	URL string
	// At is when it went live (ready) or was created.
	At time.Time
}

// DeployReader lists what a connection can watch and the production
// deploys of one target. Deploys returns the newest first, at most 50,
// none older than since.
type DeployReader interface {
	Connector
	DeployTargets(ctx context.Context, c *Client, creds Creds) ([]DeployTarget, error)
	Deploys(ctx context.Context, c *Client, creds Creds, target string, since time.Time) ([]Deploy, error)
}
