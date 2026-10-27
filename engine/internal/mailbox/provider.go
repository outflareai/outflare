package mailbox

import (
	"context"
	"errors"
	"net"
	"strings"
)

const (
	ProviderGoogle    = "google"
	ProviderMicrosoft = "microsoft"
	ProviderOther     = "other"
	// Simulated mailboxes belong to test workspaces. They accept every
	// send and check without connecting anywhere, so nothing reaches a
	// real inbox.
	ProviderSimulated = "simulated"
)

// SimulatedHost is the mail server of every simulated mailbox. .invalid
// never resolves (RFC 6761), so a simulated mailbox cannot reach a real
// server even by mistake.
const SimulatedHost = "simulated.outflare.invalid"

// Simulated reports whether an endpoint belongs to a simulated mailbox.
func Simulated(ep Endpoint) bool { return ep.Host == SimulatedHost }

// ErrSimulated is returned for an IMAP session with a simulated mailbox,
// which has no real inbox to read: replies to it are made with the test
// inbound API instead.
var ErrSimulated = errors.New("simulated mailbox: no real inbox to read")

// Endpoint is a mail server address and how to secure the connection.
type Endpoint struct {
	Host string
	Port int
	// ImplicitTLS is true for ports that speak TLS from the first byte (465,
	// 993). Otherwise the client upgrades with STARTTLS, which is required:
	// credentials are never sent in plain text.
	ImplicitTLS bool
}

// EndpointFor builds an endpoint, choosing TLS mode from the port.
func EndpointFor(host string, port int) Endpoint {
	return Endpoint{Host: host, Port: port, ImplicitTLS: port == 465 || port == 993}
}

type defaults struct {
	smtpHost, imapHost string
	smtpPort, imapPort int
}

var providerDefaults = map[string]defaults{
	ProviderGoogle:    {"smtp.gmail.com", "imap.gmail.com", 465, 993},
	ProviderMicrosoft: {"smtp.office365.com", "outlook.office365.com", 587, 993},
	ProviderSimulated: {SimulatedHost, SimulatedHost, 465, 993},
}

// Simulate makes a row a simulated mailbox, for a test workspace: its
// servers and sign-in are replaced, whatever the row said.
func Simulate(r *Row) {
	r.Provider = ProviderSimulated
	r.SMTPHost, r.SMTPPort, r.IMAPHost, r.IMAPPort = SimulatedHost, 465, SimulatedHost, 993
	r.SMTPUsername, r.IMAPUsername = r.Email, r.Email
	r.SMTPPassword, r.IMAPPassword = "", ""
	r.ConnectionID, r.RefreshToken = nil, ""
}

// Resolver is the subset of net.Resolver used here, so tests can fake DNS.
type Resolver interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// ProviderFromMX classifies a domain's mail host.
func ProviderFromMX(mxs []*net.MX) string {
	for _, mx := range mxs {
		h := strings.ToLower(strings.TrimSuffix(mx.Host, "."))
		switch {
		case strings.HasSuffix(h, "google.com") || strings.HasSuffix(h, "googlemail.com"):
			return ProviderGoogle
		case strings.HasSuffix(h, "protection.outlook.com") || strings.HasSuffix(h, "outlook.com"):
			return ProviderMicrosoft
		}
	}
	if len(mxs) == 0 {
		return ""
	}
	return ProviderOther
}

// Complete fills a row's provider, hosts and ports. When the provider is not
// given it is detected from the domain's MX records. It returns an error when
// the row lacks hosts and the provider has no defaults.
func Complete(ctx context.Context, res Resolver, r *Row) error {
	if r.Provider == "" && (r.SMTPHost == "" || r.IMAPHost == "") {
		domain := r.Email[strings.LastIndexByte(r.Email, '@')+1:]
		mxs, _ := res.LookupMX(ctx, domain)
		r.Provider = ProviderFromMX(mxs)
	}
	if r.Provider == "" {
		r.Provider = ProviderOther
	}
	d, known := providerDefaults[r.Provider]
	if r.SMTPHost == "" {
		r.SMTPHost = d.smtpHost
	}
	if r.IMAPHost == "" {
		r.IMAPHost = d.imapHost
	}
	if r.SMTPPort == 0 {
		r.SMTPPort = d.smtpPort
	}
	if r.IMAPPort == 0 {
		r.IMAPPort = d.imapPort
	}
	if !known && (r.SMTPHost == "" || r.IMAPHost == "" || r.SMTPPort == 0 || r.IMAPPort == 0) {
		return errMissingHosts
	}
	return nil
}

var errMissingHosts = errors.New("could not detect the provider; add smtp_host, smtp_port, imap_host and imap_port columns")
