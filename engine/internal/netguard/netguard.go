// Package netguard keeps outbound connections a workspace can aim (its
// connectors' self-hosted addresses, its mailboxes' SMTP and IMAP servers)
// on the public internet, so nobody can use them to reach the server's own
// network: the engine on loopback, the database or other services on a
// private network, or a cloud metadata address.
package netguard

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

var blocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("fc00::/7"),
	// IPv6 forms that carry an IPv4 address inside.
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("2002::/16"),
}

// PublicAddr reports whether an address is on the public internet.
func PublicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, p := range blocked {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// Control is a net.Dialer Control that refuses any address that is not
// public. It sees the address actually dialled, so a name that resolves (or
// later changes) to an internal address is refused too, and so is every
// redirect.
func Control(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !PublicAddr(ip) {
		return fmt.Errorf("refusing a non-public address %s: only public internet addresses are allowed (an operator can allow private ones with OUTFLARE_CONNECTORS_ALLOW_PRIVATE=true)", host)
	}
	return nil
}

// Client is an HTTP client that only reaches public addresses, never
// through a proxy (the check must see the real destination).
func Client(timeout time.Duration) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second, Control: Control}
	return &http.Client{Timeout: timeout, Transport: &http.Transport{
		Proxy:                 nil,
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   4,
	}}
}
