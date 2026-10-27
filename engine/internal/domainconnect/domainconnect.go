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
)

// Unavailable says why Domain Connect can't be used, with one of the
// Reason codes.
type Unavailable struct {
	Reason string
	Err    error
}

func (u *Unavailable) Error() string {
	if u.Err != nil {
		return u.Reason + ": " + u.Err.Error()
	}
	return u.Reason
}

func (u *Unavailable) Unwrap() error { return u.Err }

func unavailable(reason string, err error) error { return &Unavailable{Reason: reason, Err: err} }

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

// Resolver looks up TXT records.
type Resolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
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
	val any
	err error
}

const cacheFor = 10 * time.Minute

func (c *Client) remember(key string, fn func() (any, error)) (any, error) {
	c.mu.Lock()
	if c.cache == nil {
		c.cache = map[string]cached{}
	}
	if v, ok := c.cache[key]; ok && time.Since(v.at) < cacheFor {
		c.mu.Unlock()
		return v.val, v.err
	}
	c.mu.Unlock()
	val, err := fn()
	var u *Unavailable
	if err == nil || errors.As(err, &u) && u.Reason != ReasonLookupFailed {
		c.mu.Lock()
		c.cache[key] = cached{at: time.Now(), val: val, err: err}
		c.mu.Unlock()
	}
	return val, err
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
	for cand := name; ; {
		txts, err := c.Resolver.LookupTXT(ctx, "_domainconnect."+cand)
		var dnsErr *net.DNSError
		if err != nil && !(errors.As(err, &dnsErr) && dnsErr.IsNotFound) {
			return Zone{}, unavailable(ReasonLookupFailed, err)
		}
		if len(txts) > 0 {
			s, err := c.settings(ctx, strings.TrimSpace(txts[0]), cand)
			if err != nil {
				return Zone{}, err
			}
			return Zone{Domain: cand, Host: strings.TrimSuffix(strings.TrimSuffix(name, cand), "."), Settings: s}, nil
		}
		if cand == top {
			return Zone{}, unavailable(ReasonNoProvider, nil)
		}
		cand = cand[strings.IndexByte(cand, '.')+1:]
	}
}

func (c *Client) settings(ctx context.Context, api, domain string) (Settings, error) {
	var s Settings
	status, err := c.get(ctx, "https://"+strings.TrimSuffix(api, "/")+"/v2/"+url.PathEscape(domain)+"/settings", &s)
	switch {
	case err != nil:
		return s, unavailable(ReasonLookupFailed, err)
	case status == http.StatusNotFound:
		// A _domainconnect record left behind by a previous DNS host.
		return s, unavailable(ReasonNoProvider, nil)
	case status/100 != 2:
		return s, unavailable(ReasonLookupFailed, fmt.Errorf("settings: status %d", status))
	case s.URLSyncUX == "" || !strings.HasPrefix(s.URLSyncUX, "https://") || !strings.HasPrefix(s.URLAPI, "https://"):
		return s, unavailable(ReasonNoProvider, nil)
	}
	return s, nil
}

// Supports reports whether the provider has taken a template of Outflare's.
func (c *Client) Supports(ctx context.Context, s Settings, service string) error {
	_, err := c.remember("tpl "+s.URLAPI+" "+service, func() (any, error) {
		status, err := c.get(ctx, strings.TrimSuffix(s.URLAPI, "/")+"/v2/domainTemplates/providers/"+ProviderID+"/services/"+service, nil)
		switch {
		case err != nil:
			return nil, unavailable(ReasonLookupFailed, err)
		case status == http.StatusNotFound:
			return nil, unavailable(ReasonNotOnboarded, nil)
		case status/100 != 2:
			return nil, unavailable(ReasonLookupFailed, fmt.Errorf("template: status %d", status))
		}
		return nil, nil
	})
	return err
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

// ApplyURL is the signed link to the provider's consent screen for a plan.
// The provider sends the browser back to redirect with state, and error
// when the person cancelled or it failed.
func (c *Client) ApplyURL(z Zone, p Plan, redirect, state string) (string, error) {
	if c.Key == nil {
		return "", unavailable(ReasonNotConfigured, nil)
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
		return "", err
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
