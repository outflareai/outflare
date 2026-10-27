package emails

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/netguard"
)

// Authoritative asks the domain's own nameservers, so a record shows as
// soon as the DNS host has it. A caching resolver can keep answering "not
// found" for the zone's negative cache time (Cloudflare's is 30 minutes)
// after a check that ran before the record was added. When the
// nameservers can't be reached it falls back to the system resolver.
type Authoritative struct {
	// Fallback finds the nameservers and answers when they can't. nil
	// uses the system resolver.
	Fallback *net.Resolver
}

func (a Authoritative) fallback() *net.Resolver {
	if a.Fallback != nil {
		return a.Fallback
	}
	return net.DefaultResolver
}

// nameservers are the NS hosts of the closest zone above name.
func (a Authoritative) nameservers(ctx context.Context, name string) []string {
	labels := strings.Split(strings.TrimSuffix(strings.ToLower(name), "."), ".")
	for i := 0; i+1 < len(labels); i++ {
		ns, err := a.fallback().LookupNS(ctx, strings.Join(labels[i:], "."))
		if err != nil || len(ns) == 0 {
			continue
		}
		hosts := make([]string, 0, len(ns))
		for _, n := range ns {
			hosts = append(hosts, strings.TrimSuffix(n.Host, "."))
		}
		return hosts
	}
	return nil
}

// direct is a resolver that sends every question to one of name's
// nameservers, or nil when they aren't known.
func (a Authoritative) direct(ctx context.Context, name string) *net.Resolver {
	ns := a.nameservers(ctx, name)
	if len(ns) == 0 {
		return nil
	}
	// A domain's nameservers are whatever its owner says, so they stay
	// on the public internet like other addresses a workspace can aim.
	d := net.Dialer{Control: netguard.Control}
	next := 0
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		host := ns[next%len(ns)]
		next++
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		return d.DialContext(ctx, network, net.JoinHostPort(host, "53"))
	}}
}

// ask runs q on the nameservers, then on the fallback when they gave no
// usable answer (unreachable, refused): "no such record" from them is
// the answer.
func ask[T any](ctx context.Context, a Authoritative, name string, q func(*net.Resolver) (T, error)) (T, error) {
	if r := a.direct(ctx, name); r != nil {
		v, err := q(r)
		var dnsErr *net.DNSError
		if err == nil || (errors.As(err, &dnsErr) && dnsErr.IsNotFound) {
			return v, err
		}
	}
	return q(a.fallback())
}

func (a Authoritative) LookupTXT(ctx context.Context, name string) ([]string, error) {
	return ask(ctx, a, name, func(r *net.Resolver) ([]string, error) { return r.LookupTXT(ctx, name) })
}

func (a Authoritative) LookupCNAME(ctx context.Context, host string) (string, error) {
	return ask(ctx, a, host, func(r *net.Resolver) (string, error) { return r.LookupCNAME(ctx, host) })
}

func (a Authoritative) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	return ask(ctx, a, name, func(r *net.Resolver) ([]*net.MX, error) { return r.LookupMX(ctx, name) })
}

func (a Authoritative) LookupHost(ctx context.Context, host string) ([]string, error) {
	return ask(ctx, a, host, func(r *net.Resolver) ([]string, error) { return r.LookupHost(ctx, host) })
}
