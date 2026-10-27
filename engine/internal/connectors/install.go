package connectors

import (
	"context"
	"time"
)

// Installing Outflare on a workspace's own site and DNS, through the
// workspace's own accounts: the brand setup checklist's one-click paths.
// Other features that need DNS records (sending domains, tracking domains)
// use the same DNSHost.

const (
	// CapDNSWrite: implements DNSHost.
	CapDNSWrite = "dns.write"
	// CapTagInstall: implements TagInstaller.
	CapTagInstall = "tags.install"
)

// DNSRecord is one record a DNS host is asked to hold. Name is the full
// host name (example.com, _dmarc.example.com).
type DNSRecord struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	// Priority is an MX record's preference.
	Priority int `json:"priority,omitempty"`
}

// DNSResult says what a DNS host did.
type DNSResult struct {
	// The zone the records went in.
	Zone string `json:"zone"`
	// Records added, and records that were already there.
	Added    int `json:"added"`
	Existing int `json:"existing"`
}

// DNSInstall is the last one-click run that added a feature's records
// (a sending domain's, a site's proof, a tracking domain's CNAME).
type DNSInstall struct {
	Status        string `json:"status"` // queued, done or failed
	IntegrationID string `json:"integration_id,omitempty"`
	Added         int    `json:"added"`
	Existing      int    `json:"existing"`
	Zone          string `json:"zone,omitempty"`
	Error         string `json:"error,omitempty"`
	// Provider is set when the person approved the records on their DNS
	// provider's own screen (Domain Connect) instead of through a
	// connection: there are no counts then, only the checks that follow.
	Provider string    `json:"provider,omitempty"`
	At       time.Time `json:"at"`
}

// DNSHost adds records to the zones a connection can edit. It only adds:
// a record is never changed or removed, so other TXT records on the same
// name (Google's, SPF) stay as they are.
type DNSHost interface {
	EnsureRecords(ctx context.Context, cl *Client, creds Creds, records []DNSRecord) (DNSResult, error)
}

// TagContainer is a tag manager container a connection can edit.
type TagContainer struct {
	// The container's id in the tag manager's API (accounts/1/containers/2).
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	PublicID string   `json:"public_id"` // GTM-XXXXXXX
	Account  string   `json:"account"`
	Domains  []string `json:"domains"`
}

// TagResult says what a tag install did.
type TagResult struct {
	// Published: the change is live; otherwise it waits in the workspace
	// (Link) for someone to publish it.
	Published bool   `json:"published"`
	Version   string `json:"version,omitempty"`
	// Where to see the change in the tag manager.
	Link string `json:"link"`
	// Updated: a tag of the same name was already there and now holds the
	// new HTML.
	Updated bool `json:"updated"`
}

// TagInstaller puts a tag that fires on every page into a tag manager
// container, in a workspace of its own so nobody else's unpublished
// changes are published with it.
type TagInstaller interface {
	Containers(ctx context.Context, cl *Client, creds Creds) ([]TagContainer, error)
	InstallTag(ctx context.Context, cl *Client, creds Creds, container, name, html string, publish bool) (TagResult, error)
}
