// Package browser runs browser tasks for channels that have no API
// (plans/brand-agent, "Browser automation"; docs/browser-workers.md). A
// task is a capability: the brand, the account it may sign in as, the
// hosts it may reach, what it may do, the content it may type, and
// ceilings on steps, time and AI spend. The worker enforces the boundary
// itself, on every request the browser makes, never through the model's
// instructions. Secrets come from the account vault for one authorised
// use and never reach the model: not in context, page text, logs, errors
// or reports. The browser runs on the operator's own compute (Chrome on
// the worker's host); no hosted browser vendor and no third-party key.
package browser

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"

	"github.com/outflareai/outflare/engine/internal/netguard"
)

// Boundary is where a task's browser may go: the hosts it was given (a
// host matches itself and its subdomains) and the identity providers it
// signs in through, on the public internet only. Every request the
// browser makes (navigations, redirects, scripts, images, XHR) is checked
// against it; the rest is refused at the network, so a page can't send
// the browser anywhere else.
type Boundary struct {
	Hosts []string
	// AllowPrivate lets the browser reach private and loopback addresses
	// and plain http, for tests and local development only.
	AllowPrivate bool
	// Resolver, when set, resolves hosts to check their addresses; nil
	// uses the system resolver.
	Resolver interface {
		LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
	}
}

// ErrOutside means a URL is outside the task's boundary.
var ErrOutside = errors.New("outside the task's boundary")

// NormalizeHost lowercases a host and strips a port, scheme and path, so
// "https://Example.com:443/x" and "example.com" are the same host.
func NormalizeHost(h string) string {
	h = strings.TrimSpace(strings.ToLower(h))
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.TrimSuffix(h, ".")
}

// HostIn reports whether host is one of hosts or under one of them.
func HostIn(host string, hosts []string) bool {
	host = NormalizeHost(host)
	for _, h := range hosts {
		h = NormalizeHost(h)
		if h == "" {
			continue
		}
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// Check reports whether the browser may fetch rawURL: a public https
// address on one of the boundary's hosts. It resolves the host, so a name
// that points at a private address is refused, and so is every redirect
// (each one is a request checked on its own).
func (b Boundary) Check(ctx context.Context, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrOutside, err)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !b.AllowPrivate {
			return fmt.Errorf("%w: only https", ErrOutside)
		}
	case "about", "data", "blob":
		// The browser's own pages (about:blank) and data the page made.
		return nil
	default:
		return fmt.Errorf("%w: the %s scheme", ErrOutside, u.Scheme)
	}
	host := NormalizeHost(u.Hostname())
	if host == "" || !HostIn(host, b.Hosts) {
		return fmt.Errorf("%w: %s is not one of the task's hosts", ErrOutside, host)
	}
	if b.AllowPrivate {
		return nil
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if !netguard.PublicAddr(ip) {
			return fmt.Errorf("%w: %s is not a public address", ErrOutside, host)
		}
		return nil
	}
	r := b.Resolver
	if r == nil {
		r = net.DefaultResolver
	}
	ips, err := r.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("%w: %s does not resolve", ErrOutside, host)
	}
	for _, ip := range ips {
		if !netguard.PublicAddr(ip) {
			return fmt.Errorf("%w: %s resolves to a non-public address", ErrOutside, host)
		}
	}
	return nil
}
