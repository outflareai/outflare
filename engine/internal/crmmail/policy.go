// Package crmmail defines the privacy boundary for personal CRM email.
// No shared inbox, activity, search, agent or reporting path reads its content.
package crmmail

import (
	"bytes"
	"fmt"
	"net/mail"
	"net/textproto"
	"slices"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/classify"
	"github.com/outflareai/outflare/engine/internal/inbox"
)

type Policy struct {
	InternalDomains  []string `json:"internal_domains"`
	BlockedAddresses []string `json:"blocked_addresses"`
	BlockedDomains   []string `json:"blocked_domains"`
	Version          int      `json:"version"`
}
type Sharing struct {
	Visibility string   `json:"visibility"`
	Audience   string   `json:"audience"`
	ViewerIDs  []string `json:"viewer_ids"`
	Version    int      `json:"version"`
}

func Private() Sharing {
	return Sharing{Visibility: "private", Audience: "selected", ViewerIDs: []string{}}
}
func DefaultPolicy() Policy {
	return Policy{InternalDomains: []string{}, BlockedAddresses: []string{}, BlockedDomains: []string{}}
}

type Message struct {
	Subject             string    `json:"subject"`
	Body                string    `json:"body"`
	Participants        []string  `json:"participants"`
	VisibleParticipants []string  `json:"visible_participants"`
	OwnerEmail          string    `json:"owner_email"`
	OccurredAt          time.Time `json:"occurred_at"`
}

func Parse(raw string, owner string) (Message, error) {
	var out Message
	if len(raw) > 2<<20 {
		return out, fmt.Errorf("raw must be at most 2 MiB")
	}
	r, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		return out, fmt.Errorf("raw must be a valid RFC 5322 message")
	}
	// Malformed address headers fail closed, including cc, bcc and reply-to.
	for _, key := range []string{"From", "To", "Cc", "Bcc", "Sender", "Reply-To"} {
		for _, value := range textproto.MIMEHeader(r.Header).Values(key) {
			if strings.TrimSpace(value) == "" {
				continue
			}
			list, e := mail.ParseAddressList(value)
			if e != nil {
				return out, fmt.Errorf("raw contains an invalid address header")
			}
			for _, a := range list {
				v := strings.ToLower(strings.TrimSpace(a.Address))
				// Hidden recipients still participate in every exclusion check,
				// but must never become visible through a full-content share.
				if (key == "From" || key == "To" || key == "Cc") && !slices.Contains(out.VisibleParticipants, v) {
					out.VisibleParticipants = append(out.VisibleParticipants, v)
				}
				if !slices.Contains(out.Participants, v) {
					out.Participants = append(out.Participants, v)
				}
			}
		}
	}
	owner = strings.ToLower(owner)
	if !slices.Contains(out.Participants, owner) {
		return out, fmt.Errorf("raw must include your signed-in email address")
	}
	parsed, err := inbox.Parse(bytes.Clone([]byte(raw)))
	if err != nil {
		return out, fmt.Errorf("raw could not be read")
	}
	out.Subject, out.Body, out.OwnerEmail, out.OccurredAt = parsed.Subject, parsed.Body, owner, parsed.Date
	if out.OccurredAt.IsZero() {
		out.OccurredAt = time.Now().UTC()
	}
	return out, nil
}
func Domain(email string) string {
	_, domain, _ := strings.Cut(strings.ToLower(email), "@")
	return domain
}
func domainMatch(domain string, rules []string) bool {
	for _, r := range rules {
		if domain == r || strings.HasSuffix(domain, "."+r) {
			return true
		}
	}
	return false
}

// Eligible is evaluated both before storage and on every read, so tightening
// exclusions revokes access to old content immediately. Any internal participant
// other than the mailbox owner excludes the entire mixed conversation.
func Eligible(m Message, brand, personal Policy, memberEmails, recordEmails, recordDomains []string) bool {
	external := 0
	for _, p := range m.Participants {
		d := Domain(p)
		if slices.Contains(brand.BlockedAddresses, p) || slices.Contains(personal.BlockedAddresses, p) || domainMatch(d, brand.BlockedDomains) || domainMatch(d, personal.BlockedDomains) {
			return false
		}
		if p == m.OwnerEmail {
			continue
		}
		if slices.Contains(memberEmails, p) || domainMatch(d, brand.InternalDomains) {
			return false
		}
		// A message must belong entirely to the chosen customer/contact, not
		// merely contain one matching address alongside unrelated third parties.
		if !slices.Contains(recordEmails, p) && !captureDomainMatch(d, recordDomains) {
			return false
		}
		external++
	}
	return external > 0
}

// Visibility has no role parameter: being a manager, admin or HR member
// cannot override the owner's rule. Membership/scope checks happen before it.
func Visibility(owner, viewer string, s Sharing) string {
	if viewer == "" {
		return "private"
	}
	if owner == viewer {
		return "full"
	}
	if s.Visibility != "metadata" && s.Visibility != "full" {
		return "private"
	}
	if s.Audience == "brand" || (s.Audience == "selected" && slices.Contains(s.ViewerIDs, viewer)) {
		return s.Visibility
	}
	return "private"
}

// Public mailbox/alias/disposable services do not identify an employer.
// Reuse the same global, immutable classifier as company inference; a
// workspace override must never broaden this privacy boundary.
func CaptureDomainAllowed(domain string) bool {
	if _, err := classify.ParseDomain(domain); err != nil {
		return false
	}
	return !classify.Builtin().MailboxProvider(domain)
}
func captureDomainMatch(domain string, rules []string) bool {
	for _, rule := range rules {
		if CaptureDomainAllowed(rule) && domainMatch(domain, []string{rule}) {
			return true
		}
	}
	return false
}
