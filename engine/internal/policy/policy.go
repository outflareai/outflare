// Package policy decides whether an email may be sent. Every outbound email
// goes through Check; nothing else in the engine may decide that on its own.
//
// It checks recipients against the suppression list and withdrawn marketing
// consents. Sending adds mailbox limits, sending windows and per-domain
// throttles here.
package policy

import (
	"context"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
)

// Reason codes for a blocked recipient.
const (
	InvalidEmail     = "invalid_email"
	SuppressedEmail  = "suppressed_email"
	SuppressedDomain = "suppressed_domain"
	// MarketingWithdrawn: the person took back their consent to all
	// marketing email (the preference page's "unsubscribe from all", or the
	// consents API), on a customer record with this address.
	MarketingWithdrawn = "marketing_withdrawn"
	// OtherBrand: another brand emailed the person within the workspace's
	// cross-brand window. The reason reads other_brand:<handle>.
	OtherBrand = "other_brand"
)

// Decision is the verdict for one recipient.
type Decision struct {
	Email   string   `json:"email"`
	Allowed bool     `json:"allowed"`
	Reasons []string `json:"reasons"`
}

// CheckRecipients returns a decision for each email, in order.
func CheckRecipients(ctx context.Context, q Querier, workspaceID string, emails []string) ([]Decision, error) {
	out := make([]Decision, len(emails))
	var check []string
	for i, e := range emails {
		e = strings.TrimSpace(e)
		out[i] = Decision{Email: e, Allowed: true, Reasons: []string{}}
		if a, err := mail.ParseAddress(e); err != nil || a.Address != e {
			out[i].Allowed, out[i].Reasons = false, []string{InvalidEmail}
			continue
		}
		check = append(check, strings.ToLower(e))
	}
	if len(check) == 0 {
		return out, nil
	}
	rows, err := queryRows(ctx, q, `
		SELECT e, s.kind, s.reason
		FROM unnest($2::text[]) AS e
		JOIN suppressions s ON s.workspace_id = $1 AND (
			(s.kind = 'email' AND s.value = e) OR
			(s.kind = 'domain' AND s.value = split_part(e, '@', 2)))`, workspaceID, check)
	if err != nil {
		return nil, err
	}
	hits := map[string][]string{}
	for _, r := range rows {
		code := SuppressedEmail
		if r[1] == "domain" {
			code = SuppressedDomain
		}
		hits[r[0]] = append(hits[r[0]], code+":"+r[2])
	}
	// A withdrawn consent to all marketing email stops cold and lifecycle
	// email as a suppression does: the latest decision for the channel as a
	// whole, on any record that has the address now.
	rows, err = queryRows(ctx, q, `
		SELECT DISTINCT e, '', ''
		FROM unnest($2::text[]) AS e
		JOIN record_identities i ON i.workspace_id = $1 AND i.namespace = 'email' AND i.value = e AND i.valid_to IS NULL
		JOIN LATERAL (
			SELECT c.status FROM consents c
			WHERE c.workspace_id = $1 AND c.record_id = i.record_id AND c.channel = 'email' AND c.topic = '' AND c.purpose = 'marketing'
			ORDER BY c.occurred_at DESC, c.created_at DESC LIMIT 1) c ON true
		WHERE c.status = 'withdrawn'`, workspaceID, check)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		hits[r[0]] = append(hits[r[0]], MarketingWithdrawn)
	}
	for i := range out {
		if h, ok := hits[strings.ToLower(out[i].Email)]; ok && out[i].Allowed {
			out[i].Allowed, out[i].Reasons = false, h
		}
	}
	return out, nil
}

// OtherBrands applies the cross-brand contact rule: for each of emails, the
// handle of another brand than brandID that emailed the person in the days
// before now. People with no such email are left out. Emails from shared
// sequences (no brand) count for no brand.
func OtherBrands(ctx context.Context, q Querier, workspaceID string, emails []string, brandID string, days int, now time.Time) (map[string]string, error) {
	lower := make([]string, len(emails))
	for i, e := range emails {
		lower[i] = strings.ToLower(strings.TrimSpace(e))
	}
	rows, err := queryRows(ctx, q, `
		SELECT DISTINCT ON (lower(l.email)) lower(l.email), b.handle, ''
		FROM messages m
		JOIN leads l ON l.id = m.lead_id
		JOIN sequences s ON s.id = m.sequence_id
		JOIN brands b ON b.id = s.brand_id
		WHERE m.workspace_id = $1 AND l.workspace_id = $1 AND lower(l.email) = ANY($2)
		  AND s.brand_id <> $3::uuid AND m.status IN ('sending', 'sent', 'unknown')
		  AND m.created_at > $4::timestamptz - make_interval(days => $5)
		ORDER BY lower(l.email), m.created_at DESC`, workspaceID, lower, brandID, now, days)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range rows {
		out[r[0]] = r[1]
	}
	return out, nil
}

// Querier is satisfied by a pool or a transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func queryRows(ctx context.Context, q Querier, sql string, args ...any) ([][3]string, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) ([3]string, error) {
		var v [3]string
		err := r.Scan(&v[0], &v[1], &v[2])
		return v, err
	})
}

// Purposes of a send. Cold email to prospects goes through mailboxes;
// lifecycle (onboarding, newsletters) and transactional (receipts, password
// resets) email goes to people who asked for it, through an email
// provider.
const (
	PurposeCold          = "cold"
	PurposeLifecycle     = "lifecycle"
	PurposeTransactional = "transactional"
)

// CheckPurpose is CheckRecipients for a purpose. Transactional email is
// mail a person needs (a receipt, a reset link), so an unsubscribe from
// marketing (or a withdrawn marketing consent) does not stop it, nor does
// a competitor, customer or partner entry; bounces, complaints, manual
// blocks, legal and do-not-contact entries still do. Lifecycle email is
// stopped by everything but customer and partner entries, which only keep
// people out of cold email. Cold email honours every suppression.
func CheckPurpose(ctx context.Context, q Querier, workspaceID, purpose string, emails []string) ([]Decision, error) {
	out, err := CheckRecipients(ctx, q, workspaceID, emails)
	if err != nil || purpose == PurposeCold {
		return out, err
	}
	for i, d := range out {
		if d.Allowed {
			continue
		}
		var keep []string
		for _, r := range d.Reasons {
			if !Lets(purpose, r) {
				keep = append(keep, r)
			}
		}
		if len(keep) == 0 {
			out[i].Allowed, out[i].Reasons = true, []string{}
		} else {
			out[i].Reasons = keep
		}
	}
	return out, nil
}

// Lets reports whether a block reason (suppressed_email:<reason>,
// suppressed_domain:<reason> or marketing_withdrawn) still lets email of
// a purpose through.
func Lets(purpose, reason string) bool {
	_, why, ok := strings.Cut(reason, ":")
	if !ok {
		why = reason
	}
	switch purpose {
	case PurposeTransactional:
		return why == "unsubscribed" || why == MarketingWithdrawn || why == "competitor" || why == "customer" || why == "partner" || why == "blocklist"
	case PurposeLifecycle:
		return why == "customer" || why == "partner" || why == "blocklist"
	}
	return false
}

// Reason codes from a person's contact basis, for marketing (lifecycle)
// email only.
const (
	// ThirdPartyNoConsent: a third-party lead (origin prospect) who never
	// agreed to marketing email.
	ThirdPartyNoConsent = "third_party_no_consent"
	// NoConsent: never agreed to marketing email, and the workspace's
	// contact rule is opt_in.
	NoConsent = "no_consent"
)

// CheckContacts is CheckPurpose plus each person's contact basis
// (customers.MayMarket) for lifecycle email, for up to a few thousand
// addresses at once. The person is whoever holds the address now; one
// with no customer record has an unknown basis. Consent is to all
// marketing email: a send of a topic someone agreed to on its own may
// still reach them.
func CheckContacts(ctx context.Context, q Querier, workspaceID, purpose string, emails []string) ([]Decision, error) {
	out, err := CheckPurpose(ctx, q, workspaceID, purpose, emails)
	if err != nil || purpose != PurposeLifecycle {
		return out, err
	}
	var check []string
	for _, d := range out {
		if d.Allowed {
			check = append(check, strings.ToLower(d.Email))
		}
	}
	if len(check) == 0 {
		return out, nil
	}
	rows, err := queryRows(ctx, q, `
		SELECT e, COALESCE(r.origin, ''), COALESCE((SELECT c.status FROM consents c WHERE c.workspace_id = $1 AND c.record_id = r.id
				AND c.channel = 'email' AND c.topic = '' AND c.purpose = 'marketing' ORDER BY c.occurred_at DESC, c.created_at DESC LIMIT 1), '')
				|| ':' || (SELECT customer_marketing FROM workspaces WHERE id = $1)
		FROM unnest($2::text[]) AS e
		LEFT JOIN records r ON r.id = outflare_person_with_email($1, e)`, workspaceID, check)
	if err != nil {
		return nil, err
	}
	why := map[string]string{}
	for _, r := range rows {
		marketing, rule, _ := strings.Cut(r[2], ":")
		basis := customers.Basis(r[1], marketing)
		if ok, _ := customers.MayMarket(basis, rule, false); !ok {
			code := NoConsent
			switch basis {
			case customers.BasisThirdParty:
				code = ThirdPartyNoConsent
			case customers.BasisOptedOut:
				code = MarketingWithdrawn
			}
			why[r[0]] = code
		}
	}
	for i, d := range out {
		if code, ok := why[strings.ToLower(d.Email)]; ok && d.Allowed {
			out[i].Allowed, out[i].Reasons = false, []string{code}
		}
	}
	return out, nil
}

// DomainDecision is the verdict for a whole company's domain.
type DomainDecision struct {
	Domain  string   `json:"domain"`
	Allowed bool     `json:"allowed"`
	Reasons []string `json:"reasons"`
}

// CheckDomains says, for each domain in order, whether email of a purpose
// may go to people there, by the domain entries on the opt-out list
// (suppressed_domain:<reason>): so a lead search can skip a competitor,
// customer or partner company before it finds anyone's address.
func CheckDomains(ctx context.Context, q Querier, workspaceID, purpose string, domains []string) ([]DomainDecision, error) {
	out := make([]DomainDecision, len(domains))
	lower := make([]string, len(domains))
	for i, d := range domains {
		lower[i] = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
		out[i] = DomainDecision{Domain: lower[i], Allowed: true, Reasons: []string{}}
	}
	rows, err := queryRows(ctx, q, `SELECT s.value, s.reason, '' FROM suppressions s
		WHERE s.workspace_id = $1 AND s.kind = 'domain' AND s.value = ANY($2::text[])`, workspaceID, lower)
	if err != nil {
		return nil, err
	}
	hits := map[string][]string{}
	for _, r := range rows {
		if code := SuppressedDomain + ":" + r[1]; !Lets(purpose, code) {
			hits[r[0]] = append(hits[r[0]], code)
		}
	}
	for i := range out {
		if h, ok := hits[out[i].Domain]; ok {
			out[i].Allowed, out[i].Reasons = false, h
		}
	}
	return out, nil
}
