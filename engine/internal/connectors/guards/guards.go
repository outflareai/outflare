// Package guards connects the outside sources that say whether an address
// may be contacted (connectors.ContactGuard): a product's own account
// lookup, which knows who is already a customer, and a shared outreach
// blocklist.
//
// Both are services the workspace runs or subscribes to itself, at an
// address of its own, so the address is part of the connection beside the
// key. Both fail closed: a redirect, a timeout, an answer that is not the
// shape expected, is out of order or is about another address never clears
// anyone. Base URLs can be pointed at a fake in tests through
// connectors.Client.BaseURLs.
package guards

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// All returns every guard connector.
//
// Their ids are one word on purpose. The key check (internal/ownkeys)
// takes the first word of a connector's id for a vendor's name, and
// "account" or "outreach" would then make it flag, and the engine remove
// at start-up, any environment variable with such a word and a word like
// KEY in its name.
func All() []c.Connector {
	return []c.Connector{AccountLookup{}, OutreachBlocklist{}}
}

// emailShape is what an address must look like to be asked about. Plus
// tags and dots are kept as they are: an address that can't be read never
// clears anyone.
var emailShape = regexp.MustCompile(`^[^\s<>@,;]+@[a-z0-9.-]+\.[a-z]{2,}$`)

// address is an email in the form it is asked about (trimmed, lower
// case), or "" when it is not one address.
func address(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) > 254 || !emailShape.MatchString(s) {
		return ""
	}
	return s
}

// badKey matches a key that can't go in a header.
var badKey = regexp.MustCompile(`[\s\x00-\x1f\x7f]`)

// fail builds a GuardError. The kind follows the code, so the connection
// page says the right thing (a refused key, a rate limit, a service down).
func fail(name, code string, retryAfter time.Duration) *c.GuardError {
	e := &c.GuardError{Code: code, RetryAfter: retryAfter}
	switch code {
	case "credential":
		e.Err = c.Errf(c.ErrInvalid, "%s needs its address and its key. Check the connection.", name)
	case "base_url":
		e.Err = c.Errf(c.ErrInvalid, "%s needs an https address, without a sign-in, a query or a fragment in it, on a public host name.", name)
	case "http_401", "http_403":
		e.Err = c.Errf(c.ErrAuth, "%s refused the key (%s). Check the key, or create a new one.", name, strings.ToUpper(strings.Replace(code, "_", " ", 1)))
	case "http_402", "http_429":
		e.Err = c.Errf(c.ErrQuota, "%s is rate limiting this account. Nobody is cleared until it answers.", name)
	case "timeout":
		e.Err = c.TimedOutf("%s did not answer in time. Nobody is cleared until it answers.", name)
	default:
		e.Err = c.Errf(c.ErrUnavailable, "%s could not be checked (%s). Nobody is cleared until it answers.", name, code)
	}
	return e
}

// baseURL reads a connection's address: https, no sign-in in it, no query
// and no fragment, on a host name (never an IP address or localhost). A
// path is kept only when withPath is set.
func baseURL(raw string, withPath bool) (string, bool) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") || u.Opaque != "" {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || host == "localhost" || !strings.Contains(host, ".") || strings.HasSuffix(host, ".localhost") {
		return "", false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return "", false
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	if path != "" && !withPath {
		return "", false
	}
	return "https://" + u.Host + path, true
}

// answer is a service's reply, read whole.
type answer struct {
	status int
	header http.Header
	body   []byte
}

// send makes one call and reads the whole answer within wait. It never
// follows a redirect, so a key is never sent on to another address: the
// redirect itself comes back as the answer, and its status is not a 200.
func send(ctx context.Context, cl *c.Client, req *http.Request, wait time.Duration, maxBytes int64) (answer, string) {
	base := http.DefaultClient
	if cl != nil && cl.HTTP != nil {
		base = cl.HTTP
	}
	h := &http.Client{Timeout: wait, Transport: base.Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req.Header.Set("User-Agent", "outflare")
	req.Header.Set("Cache-Control", "no-store")
	resp, err := h.Do(req.WithContext(ctx))
	if err != nil {
		if timedOut(err) {
			return answer{}, "timeout"
		}
		return answer{}, "transport"
	}
	defer resp.Body.Close()
	a := answer{status: resp.StatusCode, header: resp.Header}
	if resp.StatusCode != http.StatusOK {
		// Nothing of a refusal's body is read or kept.
		return a, ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	switch {
	case err != nil && timedOut(err):
		return a, "timeout"
	case err != nil:
		return a, "transport"
	case int64(len(body)) > maxBytes:
		return a, "body_limit"
	}
	a.body = body
	return a, ""
}

func timedOut(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

// busyAfter is how long a service that had no room for a call asked to be
// left alone: always for a rate limit (HTTP 429, a minute when it did not
// say), and for HTTP 503 only when it named a time (Retry-After), which
// makes it a busy service rather than one that is down. Zero otherwise.
func busyAfter(status int, h http.Header, now time.Time) time.Duration {
	switch {
	case status == http.StatusTooManyRequests:
		return retryAfter(h.Get("Retry-After"), now)
	case status == http.StatusServiceUnavailable && strings.TrimSpace(h.Get("Retry-After")) != "":
		return retryAfter(h.Get("Retry-After"), now)
	}
	return 0
}

// retryAfter reads how long a service asked to be left alone: seconds, or
// a date. A minute when it did not say or the value can't be read; never
// under a second or over a day.
func retryAfter(value string, now time.Time) time.Duration {
	const fallback = time.Minute
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	var d time.Duration
	if secs, err := strconv.ParseFloat(value, 64); err == nil && secs >= 0 && !strings.ContainsAny(value, "eE+-") {
		d = time.Duration(secs * float64(time.Second))
	} else if at, err := http.ParseTime(value); err == nil {
		d = at.Sub(now)
	} else if at, err := time.Parse(time.RFC3339, value); err == nil {
		d = at.Sub(now)
	} else {
		return fallback
	}
	return min(max(d, time.Second), 24*time.Hour)
}

// sleep waits d, or less when ctx ends first (false).
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// isJSON says whether an answer calls itself JSON.
func isJSON(h http.Header) bool {
	return strings.Contains(strings.ToLower(h.Get("Content-Type")), "application/json")
}
