// Package domainconnect sends a person to their DNS provider's own "apply
// these records?" screen (Domain Connect, domainconnect.org), the
// synchronous flow. They approve the exact records once, at the provider,
// and Outflare keeps no access afterwards: no connection, no token.
//
// It only works where the domain's DNS provider supports Domain Connect
// and has onboarded Outflare's templates (templates/), and where Outflare's
// public key is published at syncPubKeyDomain, since every request is
// signed. Anywhere else the one-click button falls back to a DNS host
// connection (Cloudflare API token or sign-in).
package domainconnect

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"
)

// ProviderID is Outflare's id as a Domain Connect service provider: the
// providerId of every template in templates/.
const ProviderID = "outflare.ai"

// PubKeyDomain is the templates' syncPubKeyDomain: where the public half
// of the signing key is published, at <key id>.outflare.ai.
const PubKeyDomain = "outflare.ai"

// Reasons a domain can't use Domain Connect, for the API and events.
const (
	ReasonNotConfigured = "not_configured" // this server's key isn't published
	ReasonNoProvider    = "no_provider"    // the DNS provider doesn't support Domain Connect
	ReasonNotOnboarded  = "not_onboarded"  // it does, but hasn't taken Outflare's template yet
	ReasonNoTemplate    = "no_template"    // the records don't fit any template
	ReasonLookupFailed  = "lookup_failed"  // the provider's API didn't answer
	ReasonConflict      = "conflict"       // a record would land beside one already there
)

// Steps of a lookup that can fail, for the API's events and metrics:
// which call didn't answer, so a broken resolver, a provider that is down
// and one that answers oddly can be told apart without the logs.
const (
	StepDiscoverDNS    = "discover_dns"    // the _domainconnect TXT lookup
	StepSettingsHTTP   = "settings_http"   // the provider's settings call didn't answer
	StepSettingsStatus = "settings_status" // it answered with an error status
	StepTemplateHTTP   = "template_http"   // the template check didn't answer
	StepTemplateStatus = "template_status" // it answered with an error status
	StepConflictDNS    = "conflict_dns"    // looking up the records already in DNS
	StepSign           = "sign"            // signing the apply link with this server's key
)

// Unavailable says why Domain Connect can't be used, with one of the
// Reason codes, and for a failed lookup the Step that failed. Cached is
// set when the answer is a recent failure given again, not a new lookup.
type Unavailable struct {
	Reason string
	Step   string
	Err    error
	Cached bool
}

func (u *Unavailable) Error() string {
	if u.Err != nil {
		return u.Reason + ": " + u.Err.Error()
	}
	return u.Reason
}

func (u *Unavailable) Unwrap() error { return u.Err }

func unavailable(reason string, err error) error { return &Unavailable{Reason: reason, Err: err} }

func failedAt(step string, err error) error {
	return &Unavailable{Reason: ReasonLookupFailed, Step: step, Err: err}
}

// StepOf gives the step a failed lookup stopped at, or "".
func StepOf(err error) string {
	var u *Unavailable
	if errors.As(err, &u) {
		return u.Step
	}
	return ""
}

// Repeated reports whether err is a recent failure given again from the
// cache, already reported when it happened.
func Repeated(err error) bool {
	var u *Unavailable
	return errors.As(err, &u) && u.Cached
}

// ReasonOf gives an error's Reason code, or lookup_failed.
func ReasonOf(err error) string {
	var u *Unavailable
	if errors.As(err, &u) {
		return u.Reason
	}
	return ReasonLookupFailed
}

// Settings is a DNS provider's answer for a zone (the spec's settings
// data structure).
type Settings struct {
	ProviderID          string `json:"providerId"`
	ProviderName        string `json:"providerName"`
	ProviderDisplayName string `json:"providerDisplayName"`
	URLSyncUX           string `json:"urlSyncUX"`
	URLAPI              string `json:"urlAPI"`
}

// Name is how to call the provider to a person.
func (s Settings) Name() string {
	if s.ProviderDisplayName != "" {
		return s.ProviderDisplayName
	}
	return s.ProviderName
}

// Resolver looks up TXT and MX records.
type Resolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
}

// Client discovers DNS providers and builds signed apply links. Answers
// are cached for a few minutes, so a page that asks again doesn't call
// the provider again.
type Client struct {
	HTTP     *http.Client
	Resolver Resolver
	Key      *Key

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	at  time.Time
	ttl time.Duration
	val any
	err error
}

// cacheFor is how long an answer is kept; failedFor is how long a failed
// lookup is, so a provider that is down isn't asked again on every page
// load, but one that comes back is tried again soon.
const (
	cacheFor  = 10 * time.Minute
	failedFor = time.Minute
)

// retryAfter is the pause before a failed DNS lookup or provider call is
// tried a second time.
var retryAfter = 300 * time.Millisecond

func (c *Client) remember(key string, fn func() (any, error)) (any, error) {
	c.mu.Lock()
	if c.cache == nil {
		c.cache = map[string]cached{}
	}
	if v, ok := c.cache[key]; ok && time.Since(v.at) < v.ttl {
		c.mu.Unlock()
		var u *Unavailable
		if errors.As(v.err, &u) && u.Reason == ReasonLookupFailed {
			again := *u
			again.Cached = true
			return v.val, &again
		}
		return v.val, v.err
	}
	c.mu.Unlock()
	val, err := fn()
	ttl := cacheFor
	var u *Unavailable
	switch {
	case err == nil:
	case errors.As(err, &u) && u.Reason == ReasonLookupFailed:
		ttl = failedFor
	case errors.As(err, &u):
	default:
		return val, err // not one of ours (the key check): ask again next time
	}
	c.mu.Lock()
	c.cache[key] = cached{at: time.Now(), ttl: ttl, val: val, err: err}
	c.mu.Unlock()
	return val, err
}

// pause waits before a second try, or until ctx is done.
func pause(ctx context.Context) error {
	t := time.NewTimer(retryAfter)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// lookupTXT looks a name up, once more after a pause when the resolver
// fails with anything but "no such name" (a timeout or SERVFAIL is often
// gone a moment later).
func (c *Client) lookupTXT(ctx context.Context, name string) ([]string, error) {
	txts, err := c.Resolver.LookupTXT(ctx, name)
	if err != nil && !notFound(err) && pause(ctx) == nil {
		txts, err = c.Resolver.LookupTXT(ctx, name)
	}
	return txts, err
}

func notFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

// Ready reports whether this server's public key is the one published at
// PubKeyDomain, so providers can check its signatures. A self-hosted
// install never is, and offers the connection routes only.
func (c *Client) Ready(ctx context.Context) bool {
	if c == nil || c.Key == nil {
		return false
	}
	v, _ := c.remember("key", func() (any, error) {
		txts, err := c.Resolver.LookupTXT(ctx, c.Key.ID+"."+PubKeyDomain)
		if err != nil {
			return false, err
		}
		return PublicKeyFrom(txts) == c.Key.PublicKey(), nil
	})
	ok, _ := v.(bool)
	return ok
}

// Zone is where a name's records go at its DNS provider: the zone (the
// spec's domain) and the name's host within it, empty at the apex.
type Zone struct {
	Domain   string
	Host     string
	Settings Settings
}

// Discover finds the DNS provider for name: the closest zone above it, up
// to the registered domain, that answers a _domainconnect TXT record, and
// that provider's settings for it.
func (c *Client) Discover(ctx context.Context, name string) (Zone, error) {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	v, err := c.remember("zone "+name, func() (any, error) { return c.discover(ctx, name) })
	z, _ := v.(Zone)
	return z, err
}

func (c *Client) discover(ctx context.Context, name string) (Zone, error) {
	top, err := publicsuffix.EffectiveTLDPlusOne(name)
	if err != nil {
		return Zone{}, unavailable(ReasonNoProvider, err)
	}
	// Some hosting resolvers answer "server misbehaving" instead of "no
	// such name" for a level below the zone that has no record, every
	// time, so the walk goes on past a level that fails: a record higher
	// up still names the provider. The registered domain is where the
	// zone almost always is, so its answer decides: when it says plainly
	// that there is no record, the provider doesn't support Domain
	// Connect, whatever a level below said. Only when the registered
	// domain's own lookup fails is the answer "lookup failed", so an
	// outage isn't taken for no provider.
	for cand := name; ; {
		txts, err := c.lookupTXT(ctx, "_domainconnect."+cand)
		if len(txts) > 0 {
			s, err := c.settings(ctx, strings.TrimSpace(txts[0]), cand)
			if err != nil {
				return Zone{}, err
			}
			return Zone{Domain: cand, Host: strings.TrimSuffix(strings.TrimSuffix(name, cand), "."), Settings: s}, nil
		}
		if cand == top {
			if err != nil && !notFound(err) {
				return Zone{}, failedAt(StepDiscoverDNS, err)
			}
			return Zone{}, unavailable(ReasonNoProvider, nil)
		}
		cand = cand[strings.IndexByte(cand, '.')+1:]
	}
}

func (c *Client) settings(ctx context.Context, api, domain string) (Settings, error) {
	var s Settings
	status, err := c.getTwice(ctx, "https://"+strings.TrimSuffix(api, "/")+"/v2/"+url.PathEscape(domain)+"/settings", &s)
	switch {
	case err != nil:
		return s, failedAt(StepSettingsHTTP, err)
	case status == http.StatusNotFound:
		// A _domainconnect record left behind by a previous DNS host.
		return s, unavailable(ReasonNoProvider, nil)
	case status/100 != 2:
		return s, failedAt(StepSettingsStatus, fmt.Errorf("settings: status %d", status))
	case s.URLSyncUX == "" || !strings.HasPrefix(s.URLSyncUX, "https://") || !strings.HasPrefix(s.URLAPI, "https://"):
		return s, unavailable(ReasonNoProvider, nil)
	}
	return s, nil
}

// Supports reports whether the provider has taken a template of Outflare's.
func (c *Client) Supports(ctx context.Context, s Settings, service string) error {
	_, err := c.remember("tpl "+s.URLAPI+" "+service, func() (any, error) {
		status, err := c.getTwice(ctx, strings.TrimSuffix(s.URLAPI, "/")+"/v2/domainTemplates/providers/"+ProviderID+"/services/"+service, nil)
		switch {
		case err != nil:
			return nil, failedAt(StepTemplateHTTP, err)
		case status == http.StatusNotFound:
			return nil, unavailable(ReasonNotOnboarded, nil)
		case status/100 != 2:
			return nil, failedAt(StepTemplateStatus, fmt.Errorf("template: status %d", status))
		}
		return nil, nil
	})
	return err
}

// getTwice is get, tried once more after a pause when the provider
// didn't answer or answered with a server error.
func (c *Client) getTwice(ctx context.Context, u string, into any) (int, error) {
	status, err := c.get(ctx, u, into)
	if (err != nil || status/100 == 5) && ctx.Err() == nil && pause(ctx) == nil {
		status, err = c.get(ctx, u, into)
	}
	return status, err
}

func (c *Client) get(ctx context.Context, u string, into any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil {
		return 0, err
	}
	if into != nil && res.StatusCode/100 == 2 {
		if err := json.Unmarshal(body, into); err != nil {
			return 0, fmt.Errorf("decode: %w", err)
		}
	}
	return res.StatusCode, nil
}

// Conflicts reports a record in want that the provider would add beside
// one already in DNS instead of replacing it. Cloudflare ignores the
// templates' TXT conflict settings and replaces an apex MX, so a second
// key at a DKIM selector, a second DMARC policy, or an MX in place of the
// name's own would break mail. Those go through a DNS host connection or
// by hand instead, where the person sees what is already there.
func (c *Client) Conflicts(ctx context.Context, want []Rec) error {
	for _, r := range want {
		name := strings.TrimSuffix(strings.ToLower(r.Name), ".")
		var found bool
		var err error
		switch {
		case r.Type == "MX":
			var mxs []*net.MX
			mxs, err = c.Resolver.LookupMX(ctx, name)
			found = len(mxs) > 0
		case r.Type == "TXT" && strings.Contains(name, "._domainkey."):
			var txts []string
			txts, err = c.lookupTXT(ctx, name)
			found = len(txts) > 0
		case r.Type == "TXT" && strings.HasPrefix(name, "_dmarc."):
			var txts []string
			txts, err = c.lookupTXT(ctx, name)
			found = slices.ContainsFunc(txts, func(t string) bool {
				return strings.HasPrefix(strings.ToLower(strings.TrimSpace(t)), "v=dmarc1")
			})
		default:
			continue
		}
		if err != nil && !notFound(err) {
			return failedAt(StepConflictDNS, err)
		}
		if found {
			return unavailable(ReasonConflict, nil)
		}
	}
	return nil
}

// ApplyURL is the signed link to the provider's consent screen for a plan.
// The provider sends the browser back to redirect with state, and error
// when the person cancelled or it failed.
func (c *Client) ApplyURL(z Zone, p Plan, redirect, state string) (string, error) {
	if c.Key == nil {
		return "", unavailable(ReasonNotConfigured, nil)
	}
	if p.Template.HostRequired && z.Host == "" {
		// Cloudflare doesn't enforce hostRequired: without host= the CNAME
		// goes on the apex, flattened over the site's own A records.
		return "", unavailable(ReasonNoTemplate, nil)
	}
	q := url.Values{}
	for k, v := range p.Vars {
		q.Set(k, v)
	}
	q.Set("domain", z.Domain)
	if z.Host != "" {
		q.Set("host", z.Host)
	}
	if len(p.Groups) > 0 {
		q.Set("groupId", strings.Join(p.Groups, ","))
	}
	q.Set("redirect_uri", redirect)
	q.Set("state", state)
	query := q.Encode()
	sig, err := c.Key.Sign(query)
	if err != nil {
		// This server's key is broken, not the provider: report it as such.
		return "", &Unavailable{Reason: ReasonNotConfigured, Step: StepSign, Err: err}
	}
	return strings.TrimSuffix(z.Settings.URLSyncUX, "/") + "/v2/domainTemplates/providers/" + ProviderID + "/services/" + p.Template.ServiceID +
		"/apply?" + query + "&sig=" + url.QueryEscape(sig) + "&key=" + url.QueryEscape(c.Key.ID), nil
}

// Key is Outflare's signing key for apply links. Providers check each
// signature against the public key in TXT records at ID.PubKeyDomain.
type Key struct {
	ID   string
	priv *rsa.PrivateKey
}

// NewKey wraps a private key.
func NewKey(id string, priv *rsa.PrivateKey) *Key { return &Key{ID: id, priv: priv} }

// Sign signs a query string as the spec asks: RSA with SHA-256 over the
// URL-encoded query, base64.
func (k *Key) Sign(query string) (string, error) {
	sum := sha256.Sum256([]byte(query))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k.priv, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// PublicKey is the public half, base64 DER (SubjectPublicKeyInfo).
func (k *Key) PublicKey() string {
	der, _ := marshalPublic(&k.priv.PublicKey)
	return base64.StdEncoding.EncodeToString(der)
}

// TXT gives the records to publish at ID.PubKeyDomain, the key split into
// parts that fit a TXT string.
func (k *Key) TXT() []string {
	d := k.PublicKey()
	var out []string
	for i := 1; len(d) > 0; i++ {
		n := min(len(d), 200)
		out = append(out, fmt.Sprintf("p=%d,a=RS256,d=%s", i, d[:n]))
		d = d[n:]
	}
	return out
}

// PublicKeyFrom puts a key published as TXT parts back together.
func PublicKeyFrom(txts []string) string {
	parts := map[int]string{}
	for _, t := range txts {
		var n int
		var d string
		for _, f := range strings.Split(t, ",") {
			k, v, _ := strings.Cut(strings.TrimSpace(f), "=")
			switch k {
			case "p":
				fmt.Sscanf(v, "%d", &n)
			case "d":
				d = strings.TrimSpace(t[strings.Index(t, "d=")+2:])
			}
		}
		if n > 0 {
			parts[n] = d
		}
	}
	var b strings.Builder
	for i := 1; parts[i] != ""; i++ {
		b.WriteString(parts[i])
	}
	return b.String()
}
