package classify

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"time"
)

// Resolver looks up mail servers. *net.Resolver satisfies it.
type Resolver interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// mailPath is what DNS says about how a domain receives mail.
type mailPath struct {
	checked    bool // false when DNS could not answer
	hasMX      bool
	receives   bool   // has MX, or no MX but an address to fall back to
	nullMX     bool   // RFC 7505: the domain says it takes no mail
	mxProvider string // who runs the mail servers, when we recognise them
	at         time.Time
}

// mxProviders maps the end of an MX host name to who runs it. Only the
// mailbox host, never proof of what the domain is: lots of unrelated
// domains share these.
var mxProviders = []struct{ suffix, id string }{
	{".google.com", "google"}, {".googlemail.com", "google"},
	{".mail.protection.outlook.com", "microsoft"}, {".outlook.com", "microsoft"},
	{".zoho.com", "zoho"}, {".zoho.eu", "zoho"}, {".zoho.in", "zoho"}, {".zoho.com.au", "zoho"},
	{".protonmail.ch", "proton"},
	{".messagingengine.com", "fastmail"},
	{".yahoodns.net", "yahoo"},
	{".mail.icloud.com", "icloud"},
	{".mx.cloudflare.net", "cloudflare_email_routing"},
	{".improvmx.com", "improvmx"},
	{".forwardemail.net", "forward_email"},
	{".mimecast.com", "mimecast"},
	{".pphosted.com", "proofpoint"},
	{".barracudanetworks.com", "barracuda"},
	{".secureserver.net", "godaddy"},
	{".privateemail.com", "namecheap"},
	{".mx.yandex.net", "yandex"}, {".yandex.ru", "yandex"},
	{".mail.ru", "mailru"},
	{".hostinger.com", "hostinger"},
	{".ovh.net", "ovh"},
	{".gandi.net", "gandi"},
	{".migadu.com", "migadu"},
	{".tutanota.de", "tuta"},
	{".mailbox.org", "mailboxorg"},
}

func mxProvider(host string) string {
	host = "." + strings.ToLower(strings.TrimSuffix(host, "."))
	for _, p := range mxProviders {
		if strings.HasSuffix(host, p.suffix) {
			return p.id
		}
	}
	return ""
}

// dnsCache keeps answers for a while, so a burst of sign-ups from one
// domain costs one lookup.
type dnsCache struct {
	mu sync.Mutex
	m  map[string]mailPath
}

const (
	dnsTTL      = 10 * time.Minute
	dnsCacheMax = 10_000
)

func (c *dnsCache) get(domain string, now time.Time) (mailPath, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.m[domain]
	if !ok || now.Sub(p.at) > dnsTTL {
		return mailPath{}, false
	}
	return p, true
}

func (c *dnsCache) put(domain string, p mailPath) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) >= dnsCacheMax {
		c.m = map[string]mailPath{}
	}
	c.m[domain] = p
}

// mail asks DNS how a domain receives mail. A timeout or server failure is
// not proof of anything: it comes back unchecked.
func (c *Classifier) mail(ctx context.Context, domain string) mailPath {
	now := time.Now()
	if p, ok := c.cache.get(domain, now); ok {
		return p
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	p := mailPath{at: now}
	mxs, err := c.Resolver.LookupMX(ctx, domain)
	var dnsErr *net.DNSError
	switch {
	case err == nil && len(mxs) == 1 && (mxs[0].Host == "." || mxs[0].Host == ""):
		p.checked, p.nullMX = true, true
	case err == nil && len(mxs) > 0:
		p.checked, p.hasMX, p.receives = true, true, true
		p.mxProvider = mxProvider(mxs[0].Host)
	case err == nil, errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		// No MX: mail falls back to the domain's own address, if it has one.
		hosts, herr := c.Resolver.LookupHost(ctx, domain)
		if herr != nil && !(errors.As(herr, &dnsErr) && dnsErr.IsNotFound) {
			return p
		}
		p.checked, p.receives = true, len(hosts) > 0
	default:
		return p
	}
	c.cache.put(domain, p)
	return p
}
