// Package catalog lists every connector the engine ships. Add a new
// connector by appending its implementation to Available (and removing it
// from planned if it was listed there), or as a manifest in the
// integrations/ folder at the root of the repository, which Available
// loads too.
package catalog

import (
	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/ads"
	"github.com/outflareai/outflare/engine/internal/connectors/analytics"
	"github.com/outflareai/outflare/engine/internal/connectors/attio"
	"github.com/outflareai/outflare/engine/internal/connectors/calendars"
	"github.com/outflareai/outflare/engine/internal/connectors/cheapinboxes"
	"github.com/outflareai/outflare/engine/internal/connectors/data"
	"github.com/outflareai/outflare/engine/internal/connectors/deploys"
	"github.com/outflareai/outflare/engine/internal/connectors/dnshosts"
	"github.com/outflareai/outflare/engine/internal/connectors/emailbison"
	"github.com/outflareai/outflare/engine/internal/connectors/emailsend"
	"github.com/outflareai/outflare/engine/internal/connectors/hubspot"
	"github.com/outflareai/outflare/engine/internal/connectors/instantly"
	"github.com/outflareai/outflare/engine/internal/connectors/linkedin"
	"github.com/outflareai/outflare/engine/internal/connectors/manifest"
	"github.com/outflareai/outflare/engine/internal/connectors/models"
	"github.com/outflareai/outflare/engine/internal/connectors/partnerapps"
	"github.com/outflareai/outflare/engine/internal/connectors/payments"
	"github.com/outflareai/outflare/engine/internal/connectors/reddit"
	"github.com/outflareai/outflare/engine/internal/connectors/seo"
	"github.com/outflareai/outflare/engine/internal/connectors/slack"
	"github.com/outflareai/outflare/engine/internal/connectors/smartlead"
	"github.com/outflareai/outflare/engine/internal/connectors/tagmanager"
	"github.com/outflareai/outflare/engine/internal/connectors/verifiers"
	"github.com/outflareai/outflare/engine/internal/connectors/x"
	"github.com/outflareai/outflare/integrations"
)

// Available returns every connector that can be connected.
func Available() []c.Connector {
	var out []c.Connector
	out = append(out, verifiers.All()...)
	out = append(out, data.All()...)
	out = append(out, emailbison.EmailBison{}, instantly.Instantly{}, smartlead.Smartlead{}, cheapinboxes.Connector{})
	out = append(out, calendars.All()...)
	out = append(out, hubspot.All()...)
	out = append(out, attio.All()...)
	out = append(out, models.All()...)
	out = append(out, analytics.All()...)
	out = append(out, seo.All()...)
	out = append(out, emailsend.All()...)
	out = append(out, payments.All()...)
	out = append(out, partnerapps.All()...)
	out = append(out, seo.Fetchers()...)
	out = append(out, deploys.All()...)
	out = append(out, ads.All()...)
	out = append(out, reddit.All()...)
	out = append(out, x.All()...)
	out = append(out, linkedin.All()...)
	out = append(out, dnshosts.All()...)
	out = append(out, tagmanager.All()...)
	out = append(out, slack.All()...)
	out = append(out, manifest.All(integrations.FS)...)
	return out
}

// New builds the registry.
func New() *c.Registry {
	return c.NewRegistry(Available(), planned())
}

func planned() []c.Definition {
	p := func(id, name, category, auth, site, summary string) c.Definition {
		return c.Definition{ID: id, Name: name, Category: category, Auth: auth, Website: site, Summary: summary, Capabilities: []string{}}
	}
	// both: the vendor has OAuth and a personal API key.
	both := func(d c.Definition) c.Definition { d.AuthMethods = c.Both; return d }
	out := verifiers.Planned()
	out = append(out, data.Planned()...)
	out = append(out, calendars.Planned()...)
	out = append(out, analytics.Planned()...)
	out = append(out,
		// Sequencers: import campaigns, leads and mailboxes when switching.
		p("plusvibe", "PlusVibe", c.CategorySequencer, c.AuthAPIKey, "https://plusvibe.ai", "Move campaigns, leads and mailboxes over from PlusVibe."),
		// Mailbox suppliers.
		p("mailreef", "Mailreef", c.CategoryMailbox, c.AuthAPIKey, "https://mailreef.com", "Unconfirmed: no public API checked yet. Upload its mailboxes by SMTP and IMAP; they are watched as a sending server."),
		p("infraforge", "Infraforge", c.CategoryMailbox, c.AuthAPIKey, "https://www.infraforge.ai", "Unconfirmed: no public API checked yet. Upload its mailboxes by SMTP and IMAP; they are watched as a sending server."),
		// DNS hosts: write SPF, DKIM and DMARC records for sending domains.
		p("namecheap", "Namecheap", c.CategoryDNS, c.AuthAPIKey, "https://www.namecheap.com", "Set SPF, DKIM, DMARC and tracking records automatically."),
		p("godaddy", "GoDaddy", c.CategoryDNS, c.AuthAPIKey, "https://www.godaddy.com", "Set SPF, DKIM, DMARC and tracking records automatically."),
		p("route53", "Amazon Route 53", c.CategoryDNS, c.AuthAPIKey, "https://aws.amazon.com/route53/", "Set SPF, DKIM, DMARC and tracking records automatically."),
		// Email providers for lifecycle and transactional email.
		p("sendgrid", "SendGrid", c.CategoryEmailSending, c.AuthAPIKey, "https://sendgrid.com", "Send transactional and lifecycle email through Twilio SendGrid."),
		p("mailgun", "Mailgun", c.CategoryEmailSending, c.AuthAPIKey, "https://www.mailgun.com", "Send transactional and lifecycle email through Mailgun."),
		// CRMs.
		p("salesforce", "Salesforce", c.CategoryCRM, c.AuthOAuth2, "https://www.salesforce.com", "Sync replies, interested leads and meetings to Salesforce."),
		both(p("pipedrive", "Pipedrive", c.CategoryCRM, c.AuthOAuth2, "https://www.pipedrive.com", "Create deals in Pipedrive from interested replies.")),
	)
	return out
}
