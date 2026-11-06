package connectors

import (
	"context"
	"errors"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Some search results come back as Google's own redirect links
// (google.com/url?q=..., google.com/goto?url=<token>, /aclk) instead of the
// page they lead to. A result is only useful as the page itself, so readers
// unwrap them: from the link's own parameters when they carry the address,
// else by asking Google where the link goes (one request, redirects not
// followed, no page read).

// IsGoogleRedirect reports whether raw is a Google redirect link.
func IsGoogleRedirect(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return googleRedirect(u)
}

func googleRedirect(u *url.URL) bool {
	h := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if h != "google.com" && !strings.HasPrefix(h, "google.") && !strings.HasPrefix(h, "google.co") {
		return false
	}
	switch u.Path {
	case "/url", "/goto", "/aclk", "/imgres", "/link":
		return true
	}
	return false
}

// UnwrapGoogleRedirect returns the page a Google redirect link names in its
// own parameters, or "" when the link carries only a token.
func UnwrapGoogleRedirect(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !googleRedirect(u) {
		return ""
	}
	for _, k := range []string{"q", "url", "u", "adurl", "imgrefurl"} {
		if v := u.Query().Get(k); pageURL(v) {
			return v
		}
	}
	return ""
}

// pageURL reports whether v is an http(s) address of a page that is not
// Google itself.
func pageURL(v string) bool {
	u, err := url.Parse(strings.TrimSpace(v))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return false
	}
	return !googleRedirect(u) && !strings.HasSuffix(strings.ToLower(u.Hostname()), "google.com")
}

var metaRefresh = regexp.MustCompile(`(?i)(?:url\s*=\s*['"]?|href\s*=\s*['"]|location(?:\.href)?\s*=\s*['"]|location\.replace\(\s*['"])(https?://[^'"\s<>)]+)`)

// ErrUnresolved is returned when a Google redirect link can't be followed
// to a page.
var ErrUnresolved = errors.New("google redirect could not be resolved")

// ResolveGoogleRedirect returns the page a Google redirect link leads to.
// A link that isn't one comes back as it is.
func ResolveGoogleRedirect(ctx context.Context, c *Client, raw string) (string, error) {
	if !IsGoogleRedirect(raw) {
		return raw, nil
	}
	if v := UnwrapGoogleRedirect(raw); v != "" {
		return v, nil
	}
	h := http.DefaultClient
	if c != nil && c.HTTP != nil {
		h = c.HTTP
	}
	cl := &http.Client{Timeout: 10 * time.Second, Transport: h.Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "outflare")
	resp, err := cl.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "" {
		if u, e := resp.Request.URL.Parse(loc); e == nil {
			if pageURL(u.String()) {
				return u.String(), nil
			}
			if v := UnwrapGoogleRedirect(u.String()); v != "" {
				return v, nil
			}
		}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	for _, m := range metaRefresh.FindAllStringSubmatch(string(body), -1) {
		if v := html.UnescapeString(m[1]); pageURL(v) {
			return v, nil
		}
	}
	return "", ErrUnresolved
}

// ResolveGoogleRedirects resolves every Google redirect link in urls, a
// few at a time, and returns each one's page, "" where it couldn't be
// followed. Other links come back as they are.
func ResolveGoogleRedirects(ctx context.Context, c *Client, urls []string) []string {
	out := make([]string, len(urls))
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for i, raw := range urls {
		if !IsGoogleRedirect(raw) {
			out[i] = raw
			continue
		}
		wg.Add(1)
		go func(i int, raw string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if v, err := ResolveGoogleRedirect(ctx, c, raw); err == nil {
				out[i] = v
			}
		}(i, raw)
	}
	wg.Wait()
	return out
}
