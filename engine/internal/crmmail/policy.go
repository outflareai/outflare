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
	// PrivateLabels are Gmail labels whose mail is never captured, such as
	// Personal or HR; the brand's and the person's own both apply.
	PrivateLabels []string `json:"private_labels"`
	// TeamSharing (brand policy only) makes the brand's members able to
	// read each other's captured work mail, unless its owner made that
	// contact private. It starts at TeamSharingFrom, a notice period after
	// an owner switches it on, so everyone is told first.
	TeamSharing     bool       `json:"team_sharing"`
	TeamSharingFrom *time.Time `json:"team_sharing_from"`
	// CaptureAllContacts (brand policy only) captures each enabled
	// person's mail with any of the brand's CRM contacts and companies,
	// not only the ones they picked, under the same filters.
	CaptureAllContacts bool `json:"capture_all_contacts"`
	Version            int  `json:"version"`
}

// TeamSharingNotice is how long after an owner switches team sharing on
// it starts, so every member is told before their mail is shared.
const TeamSharingNotice = 48 * time.Hour

// DefaultPrivateLabels are the Gmail labels kept out of capture unless a
// brand changes them.
var DefaultPrivateLabels = []string{"Personal", "HR", "Legal", "Finance", "Medical"}

// PrivateSenderDomains are payroll, HR, benefits, banking and expense
// services: their mail is about a person's job, pay or money, never a
// business relationship, so it is never captured while team sharing is
// on. A brand adds its own (an accountant, a law firm) as blocked domains.
var PrivateSenderDomains = []string{
	"gusto.com", "deel.com", "rippling.com", "adp.com", "paychex.com", "justworks.com", "trinet.com", "remote.com", "oysterhr.com",
	"bamboohr.com", "hibob.com", "personio.com", "workday.com", "namely.com",
	"wise.com", "revolut.com", "mercury.com", "brex.com", "ramp.com", "expensify.com", "paypal.com",
}

// Labels are the Gmail labels a policy keeps out of capture: its own list,
// or the defaults when it has never set one (an empty list means none).
func (p Policy) Labels() []string {
	if p.PrivateLabels == nil {
		return DefaultPrivateLabels
	}
	return p.PrivateLabels
}

// ExcludeLabels is the Gmail search terms that leave out mail with any of
// the policies' private labels. Gmail matches a label by its name in
// lower case with spaces and slashes as hyphens.
func ExcludeLabels(policies ...Policy) string {
	seen := map[string]bool{}
	var out []string
	for _, p := range policies {
		for _, l := range p.Labels() {
			name := strings.NewReplacer(" ", "-", "/", "-", `"`, "").Replace(strings.ToLower(strings.TrimSpace(l)))
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, "-label:"+name)
		}
	}
	return strings.Join(out, " ")
}

// TeamSharingOn reports whether a brand's team sharing has started at now.
func (p Policy) TeamSharingOn(now time.Time) bool {
	return p.TeamSharing && p.TeamSharingFrom != nil && !now.Before(*p.TeamSharingFrom)
}

// TeamShared is how a contact's mail is shared when its owner made no
// choice for it and the brand's team sharing is on: in full, with the
// brand's members.
func TeamShared() Sharing {
	return Sharing{Visibility: "full", Audience: "brand", ViewerIDs: []string{}}
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
	return Policy{InternalDomains: []string{}, BlockedAddresses: []string{}, BlockedDomains: []string{}, PrivateLabels: slices.Clone(DefaultPrivateLabels)}
}

type Message struct {
	Subject             string    `json:"subject"`
	Body                string    `json:"body"`
	Participants        []string  `json:"participants"`
	VisibleParticipants []string  `json:"visible_participants"`
	OwnerEmail          string    `json:"owner_email"`
	OccurredAt          time.Time `json:"occurred_at"`
	// MessageID and References (RFC 5322, without angle brackets) thread a
	// reply sent as the owner; mail captured before they were kept has none.
	MessageID  string   `json:"message_id,omitempty"`
	References []string `json:"references,omitempty"`
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
	out.MessageID, out.References = strings.Trim(parsed.MessageID, "<>"), parsed.References
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
		// With team sharing, payroll, HR, banking and expense services stay out.
		if brand.TeamSharing && domainMatch(d, PrivateSenderDomains) {
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

// External is a message's participants outside the company: not its owner,
// a member or the brand's internal domains. Every one of them must belong
// to the CRM contact or company the message is captured on.
func External(m Message, brand Policy, memberEmails []string) []string {
	var out []string
	for _, p := range m.Participants {
		if p == m.OwnerEmail || slices.Contains(memberEmails, p) || domainMatch(Domain(p), brand.InternalDomains) || slices.Contains(out, p) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// CaptureDomains is the company domains an address could belong to: its
// own domain and each parent (mail.acme.example, then acme.example), never
// a public mailbox service or a bare suffix.
func CaptureDomains(address string) []string {
	var out []string
	for d := Domain(address); strings.Contains(d, "."); {
		if CaptureDomainAllowed(d) {
			out = append(out, d)
		}
		_, rest, _ := strings.Cut(d, ".")
		d = rest
	}
	return out
}
