package crawl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// Token is the crawler's name in robots.txt and its user agent.
const Token = "OutflareBot"

// UserAgent is sent with every request; the page explains the bot and how
// to block it.
const UserAgent = "Mozilla/5.0 (compatible; OutflareBot/1.0; +https://outflare.ai/bot/)"

const (
	fetchTimeout = 15 * time.Second
	// maxBodyBytes is how much of a page is read; the rest is cut off and
	// the page marked so.
	maxBodyBytes = 5 << 20
)

// Response is one HTTP answer. Redirects are not followed: the crawler
// follows them itself, so each hop is checked and recorded.
type Response struct {
	URL       string
	Status    int
	Header    http.Header
	Body      []byte
	Truncated bool
	Duration  time.Duration
}

// ContentType is the response's media type, lowercase, without parameters.
func (r *Response) ContentType() string {
	ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	return strings.ToLower(strings.TrimSpace(ct))
}

// Fetcher gets one URL.
type Fetcher interface {
	Fetch(ctx context.Context, url string) (*Response, error)
}

// HTTPFetcher fetches from the internet as OutflareBot. Unless
// AllowPrivate is set (development and tests), it refuses every address
// that is not public, checked on the address actually dialled, so a name
// that resolves to an internal address is refused too.
type HTTPFetcher struct {
	AllowPrivate bool
	client       *http.Client
}

// NewHTTPFetcher builds a fetcher.
func NewHTTPFetcher(allowPrivate bool) *HTTPFetcher {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	if !allowPrivate {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil || !PublicAddr(ip) {
				return fmt.Errorf("%w: %s", ErrPrivate, host)
			}
			return nil
		}
	}
	return &HTTPFetcher{AllowPrivate: allowPrivate, client: &http.Client{
		Timeout: fetchTimeout,
		Transport: &http.Transport{
			// Never through a proxy: the address check must see the site.
			Proxy:                 nil,
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
			MaxIdleConnsPerHost:   1,
			IdleConnTimeout:       30 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// ErrPrivate refuses an address that is not on the public internet.
var ErrPrivate = errors.New("refusing a non-public address")

// readable are the media types whose bodies the crawler reads; others are
// recorded by status and type only.
func readable(ct string) bool {
	return ct == "" || strings.HasPrefix(ct, "text/") || strings.Contains(ct, "html") || strings.Contains(ct, "xml") ||
		ct == "application/gzip" || ct == "application/x-gzip"
}

func (f *HTTPFetcher) Fetch(ctx context.Context, raw string) (*Response, error) {
	if err := CheckURL(raw, f.AllowPrivate); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,text/plain;q=0.8,*/*;q=0.5")
	start := time.Now()
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out := &Response{URL: raw, Status: resp.StatusCode, Header: resp.Header}
	if readable(out.ContentType()) {
		out.Body, err = io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
		if err != nil {
			return nil, err
		}
		if len(out.Body) > maxBodyBytes {
			out.Body, out.Truncated = out.Body[:maxBodyBytes], true
		}
	}
	out.Duration = time.Since(start)
	return out, nil
}

// PublicAddr reports whether an address is on the public internet.
func PublicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() &&
		!netip.MustParsePrefix("100.64.0.0/10").Contains(ip) && !netip.MustParsePrefix("fc00::/7").Contains(ip)
}

// CheckURL reports why a URL cannot be crawled, or nil: it must be http(s)
// without a user name, and (unless allowPrivate) name a public host on the
// default port.
func CheckURL(raw string, allowPrivate bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("a site's address is a web address like https://example.com")
	}
	if u.User != nil {
		return errors.New("a site's address must not contain a user name or password")
	}
	if allowPrivate {
		return nil
	}
	if u.Port() != "" {
		return errors.New("a site's address must use the default port")
	}
	host := u.Hostname()
	if !strings.Contains(host, ".") || host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".arpa") {
		return fmt.Errorf("%w: %s", ErrPrivate, host)
	}
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil && !PublicAddr(ip) {
		return fmt.Errorf("%w: %s", ErrPrivate, host)
	}
	return nil
}
