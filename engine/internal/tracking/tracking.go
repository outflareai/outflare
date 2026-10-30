// Package tracking handles a workspace's own tracking domain and optional
// click tracking. Both are off unless a workspace turns them on: by default
// unsubscribe links use the instance's address and links are left as written.
package tracking

import (
	"context"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Querier is satisfied by a pool or a transaction.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// CNAMEResolver is the part of net.Resolver used to verify a domain.
type CNAMEResolver interface {
	LookupCNAME(ctx context.Context, host string) (string, error)
}

// Domain is a workspace's tracking domain.
type Domain struct {
	ID            string     `json:"id"`
	Domain        string     `json:"domain"`
	Status        string     `json:"status"` // pending, verified, failed
	Error         string     `json:"error"`
	Target        string     `json:"target"` // what the CNAME must point at
	LastCheckedAt *time.Time `json:"last_checked_at"`
	VerifiedAt    *time.Time `json:"verified_at"`
	CreatedAt     time.Time  `json:"created_at"`
	// HTTPSStatus is the certificate the server's provider is getting for
	// the domain (pending, active, failed), or nil when the server has no
	// provider and the operator's own proxy serves HTTPS.
	HTTPSStatus *string `json:"https_status"`
	HTTPSError  string  `json:"https_error"`
}

var hostname = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// ValidDomain normalises a domain a person typed (with or without a scheme
// or trailing dot) and reports whether it is a usable host name.
func ValidDomain(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		s = u.Host
	}
	s = strings.TrimSuffix(s, ".")
	return s, hostname.MatchString(s) && len(s) <= 253
}

// Target is the host a tracking domain's CNAME must point at: the
// instance's public host.
func Target(publicURL string) string {
	u, err := url.Parse(publicURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// Verify checks that domain is a CNAME to target. It returns a reason a
// person can act on when it isn't.
func Verify(ctx context.Context, r CNAMEResolver, domain, target string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cname, err := r.LookupCNAME(ctx, domain)
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return errors.New("no DNS record found for " + domain + " yet; add a CNAME record pointing to " + target + " (changes can take up to an hour)")
	case err != nil:
		return errors.New("couldn't look up " + domain + ": " + err.Error())
	}
	got := strings.ToLower(strings.TrimSuffix(cname, "."))
	if got == domain {
		// A record behind Cloudflare's proxy answers with addresses, not
		// its CNAME, which is the most common way to land here.
		return errors.New(domain + " has no CNAME record; add one pointing to " + target +
			" (if its DNS is on Cloudflare, set the record to DNS only, the grey cloud, then check again in 5 minutes)")
	}
	if got != strings.ToLower(target) {
		return errors.New(domain + " points to " + got + ", not " + target)
	}
	return nil
}

// BaseURL is where a workspace's unsubscribe and click links point: its
// verified tracking domain, or the instance's public URL. When the server
// gets certificates for tracking domains, a domain is used only once its
// certificate is active, so no link goes out that a browser would refuse.
func BaseURL(ctx context.Context, q Querier, ws, publicURL string) (string, error) {
	var domain string
	// A domain tied to a partner program serves its portal, and one tied
	// to a help centre the help centre, not the workspace's email links.
	err := q.QueryRow(ctx, `SELECT domain FROM tracking_domains d WHERE workspace_id = $1 AND status = 'verified'
		AND NOT EXISTS (SELECT 1 FROM partner_programs p WHERE p.portal_domain_id = d.id)
		AND NOT EXISTS (SELECT 1 FROM help_centres h WHERE h.domain_id = d.id)
		AND (https_status IS NULL OR https_status = 'active') ORDER BY verified_at LIMIT 1`, ws).Scan(&domain)
	if errors.Is(err, pgx.ErrNoRows) {
		return strings.TrimRight(publicURL, "/"), nil
	}
	if err != nil {
		return "", err
	}
	return "https://" + domain, nil
}

// links finds http(s) URLs in plain text. Trailing punctuation that usually
// ends a sentence is left out of the link.
var links = regexp.MustCompile(`https?://[^\s<>"']+[^\s<>"'.,;:!?)\]]`)

// ReplaceLinks replaces each http(s) link in plain text with what f returns.
func ReplaceLinks(body string, f func(link string) string) string {
	return links.ReplaceAllStringFunc(body, f)
}

// Rewrite replaces each link in body with a click link under base, except
// links in skip (the unsubscribe link). The same link sent again in the
// same step to the same lead reuses its row.
func Rewrite(ctx context.Context, q Querier, ws, enrollmentID, sequenceID string, step int, body, base string, skip ...string) (string, error) {
	var err error
	out := links.ReplaceAllStringFunc(body, func(link string) string {
		if err != nil || strings.HasPrefix(link, base+"/") {
			return link
		}
		for _, s := range skip {
			if link == s {
				return link
			}
		}
		var id string
		err = q.QueryRow(ctx, `
			INSERT INTO tracked_links (workspace_id, enrollment_id, sequence_id, step_position, url) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (enrollment_id, step_position, url) DO UPDATE SET url = EXCLUDED.url
			RETURNING id`, ws, enrollmentID, sequenceID, step, link).Scan(&id)
		return base + "/c/" + id
	})
	return out, err
}

// scanners are user agents of link scanners that mail filters run on
// every link before a person sees the email.
var scanners = regexp.MustCompile(`(?i)bot|crawl|spider|scan|preview|barracuda|proofpoint|mimecast|symantec|forcepoint|trendmicro|microsoft office|outlook-ios|python|curl|wget|go-http-client|java/|headless`)

// LikelyBot reports whether a click looks automated: a known scanner, no
// user agent, or within a minute of sending.
func LikelyBot(userAgent string, sentAt, now time.Time) bool {
	return userAgent == "" || scanners.MatchString(userAgent) || now.Sub(sentAt) < time.Minute
}

// Clicked is a recorded click: where to send the person, and the click's
// row, workspace and whether it looked automated.
type Clicked struct {
	Target    string
	ID        int64
	Workspace string
	LikelyBot bool
}

// Click records a click and returns where to send the person. ok is false
// for an unknown link.
func Click(ctx context.Context, q Querier, linkID, userAgent string, now time.Time) (c Clicked, ok bool, err error) {
	var created time.Time
	err = q.QueryRow(ctx, `SELECT workspace_id, url, created_at FROM tracked_links WHERE id = $1`, linkID).Scan(&c.Workspace, &c.Target, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	if len(userAgent) > 300 {
		userAgent = userAgent[:300]
	}
	c.LikelyBot = LikelyBot(userAgent, created, now)
	err = q.QueryRow(ctx, `INSERT INTO link_clicks (link_id, workspace_id, likely_bot, user_agent, clicked_at) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		linkID, c.Workspace, c.LikelyBot, userAgent, now).Scan(&c.ID)
	// A failed record still sends the person on.
	return c, true, err
}
