package crawl

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
)

// Ownership proofs. Any one of them proves a site.
const (
	// VerifyName is the meta tag's name and the DNS record's prefix.
	VerifyName = "outflare-site-verification"
	// VerifyFile is where the file proof lives on the site.
	VerifyFile = "/.well-known/outflare-site-verification.txt"
)

// NewVerifyToken returns a fresh token for a site.
func NewVerifyToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// TXTResolver looks up DNS TXT records.
type TXTResolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// Proofs are the three ways to prove a site, for showing to its owner.
type Proofs struct {
	DNSName  string `json:"dns_name"`
	DNSValue string `json:"dns_value"`
	MetaTag  string `json:"meta_tag"`
	FileURL  string `json:"file_url"`
	FileBody string `json:"file_body"`
}

// ProofsFor lists how to prove host with token. The DNS record goes on the
// site's domain without "www.".
func ProofsFor(startURL, host, token string) Proofs {
	u, _ := url.Parse(startURL)
	scheme := "https"
	if u != nil && u.Scheme != "" {
		scheme = u.Scheme
	}
	return Proofs{
		DNSName:  dnsHost(host),
		DNSValue: VerifyName + "=" + token,
		MetaTag:  `<meta name="` + VerifyName + `" content="` + token + `">`,
		FileURL:  scheme + "://" + host + VerifyFile,
		FileBody: token,
	}
}

func dnsHost(host string) string {
	h := host
	if i := strings.LastIndexByte(h, ':'); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	return strings.TrimPrefix(h, "www.")
}

// ErrNotVerified says none of the proofs was found.
var ErrNotVerified = errors.New("no proof of ownership found")

// Verify looks for any proof that the workspace owns host, and returns the
// method that matched: dns, meta or file.
func Verify(ctx context.Context, f Fetcher, dns TXTResolver, startURL, host, token string) (string, error) {
	want := VerifyName + "=" + token
	if dns != nil {
		for _, name := range []string{dnsHost(host), host} {
			recs, _ := dns.LookupTXT(ctx, name)
			for _, r := range recs {
				if strings.TrimSpace(r) == want {
					return "dns", nil
				}
			}
		}
	}
	p := ProofsFor(startURL, host, token)
	if resp, err := f.Fetch(ctx, p.FileURL); err == nil && resp.Status == 200 && strings.TrimSpace(string(resp.Body)) == token {
		return "file", nil
	}
	// The home page may redirect (to https, to www, to a language), so
	// follow a few hops that stay on the same site.
	next := startURL
	for range 5 {
		resp, err := f.Fetch(ctx, next)
		if err != nil {
			break
		}
		if resp.Status >= 300 && resp.Status < 400 {
			base, _ := url.Parse(next)
			loc, ok := Normalize(base, resp.Header.Get("Location"))
			if !ok || !SameSite(hostOfURL(loc), host) {
				break
			}
			next = loc
			continue
		}
		if resp.Status == 200 {
			base, _ := url.Parse(next)
			if strings.TrimSpace(ParsePage(base, resp.Body).Meta[VerifyName]) == token {
				return "meta", nil
			}
		}
		break
	}
	return "", ErrNotVerified
}

// SameSite reports whether two hosts are the same site, allowing for a
// leading "www.".
func SameSite(a, b string) bool {
	return strings.TrimPrefix(strings.ToLower(a), "www.") == strings.TrimPrefix(strings.ToLower(b), "www.")
}

func hostOfURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}
