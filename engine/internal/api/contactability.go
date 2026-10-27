package api

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/policy"
)

// contactability is what may go out to a person, and why: their contact
// basis (customers.Basis), where they came from, what the opt-out list
// holds for their addresses, and the workspace's rule.
type contactability struct {
	Basis       string         `json:"basis"`
	AcquiredVia string         `json:"acquired_via"`
	Allowed     contactAllowed `json:"allowed"`
	// The opt-out list's reasons for any of their email addresses or
	// domains.
	Suppressed []string `json:"suppressed"`
	Why        string   `json:"why"`
}

type contactAllowed struct {
	Transactional bool `json:"transactional"`
	ColdOutreach  bool `json:"cold_outreach"`
	Marketing     bool `json:"marketing"`
}

// withContactability fills in what may go out to each person in list.
func withContactability(ctx context.Context, q pgx.Tx, list []recordView) error {
	var ids []string
	at := map[string]int{}
	for i, v := range list {
		if v.Type == "person" {
			ids, at[v.ID] = append(ids, v.ID), i
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := q.Query(ctx, `
		SELECT r.id::text, r.origin, r.acquired_via, w.customer_marketing,
			COALESCE((SELECT c.status FROM consents c WHERE c.workspace_id = r.workspace_id AND c.record_id = r.id
				AND c.channel = 'email' AND c.topic = '' AND c.purpose = 'marketing'
				ORDER BY c.occurred_at DESC, c.created_at DESC LIMIT 1), ''),
			COALESCE((SELECT array_agg(DISTINCT s.reason ORDER BY s.reason) FROM record_identities i
				JOIN suppressions s ON s.workspace_id = r.workspace_id AND (
					(s.kind = 'email' AND s.value = i.value) OR (s.kind = 'domain' AND s.value = split_part(i.value, '@', 2)))
				WHERE i.record_id = r.id AND i.namespace = 'email' AND i.valid_to IS NULL), '{}')
		FROM records r JOIN workspaces w ON w.id = r.workspace_id
		WHERE r.id = ANY($1::uuid[])`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, origin, via, rule, marketing string
		var suppressed []string
		if err := rows.Scan(&id, &origin, &via, &rule, &marketing, &suppressed); err != nil {
			return err
		}
		c := contactFor(origin, via, rule, marketing, suppressed)
		list[at[id]].Contactability = &c
	}
	return rows.Err()
}

// contactFor works out a person's contactability.
func contactFor(origin, via, rule, marketing string, suppressed []string) contactability {
	c := contactability{Basis: customers.Basis(origin, marketing), AcquiredVia: via, Suppressed: suppressed}
	lets := func(purpose string) bool {
		return !slices.ContainsFunc(suppressed, func(r string) bool { return !policy.Lets(purpose, r) })
	}
	mayMarket, whyNot := customers.MayMarket(c.Basis, rule, false)
	c.Allowed = contactAllowed{
		Transactional: lets(policy.PurposeTransactional),
		ColdOutreach:  len(suppressed) == 0 && c.Basis != customers.BasisOptedOut,
		Marketing:     lets(policy.PurposeLifecycle) && mayMarket,
	}
	switch {
	case len(suppressed) > 0:
		c.Why = "On the opt-out list (" + strings.Join(suppressed, ", ") + ")."
	case c.Basis == customers.BasisOptedIn:
		c.Why = "Agreed to marketing email."
	case c.Basis == customers.BasisThirdParty:
		c.Why = "A lead from a third party: cold outreach only, no newsletters or product updates until they agree."
	case !mayMarket:
		c.Why = strings.ToUpper(whyNot[:1]) + whyNot[1:] + "."
	case c.Basis == customers.BasisCustomer:
		c.Why = "A customer: gets marketing email unless they say no."
	default:
		c.Why = "Gets marketing email unless they say no; nothing says how the workspace got them."
	}
	return c
}

// contactWarning is something that looks wrong about who an email is going
// to, in plain words: what is happening and what to do instead. Outflare
// only connects the tools people send with, so it warns rather than
// refuses, except where a send would clearly break the rules.
type contactWarning struct {
	Code    string `json:"code"`
	Count   int    `json:"count"`
	Message string `json:"message"`
}

// audienceWarnings is what looks wrong about sending marketing email to an
// audience of these bases under the workspace's rule.
func audienceWarnings(bases map[string]int, rule string) []contactWarning {
	total := 0
	for _, n := range bases {
		total += n
	}
	out := []contactWarning{}
	if n := bases[customers.BasisThirdParty]; n > 0 {
		out = append(out, contactWarning{Code: "third_party_in_audience", Count: n, Message: fmt.Sprintf(
			"%s from lead lists, data providers or sequencers, and never agreed to hear from you. They won't get this email: sending newsletters or product updates to bought or scraped leads is spam and can get your domain blocked. Reach them with cold outreach, and send them this only once they opt in.",
			people(n, "is a lead", "are leads"))})
	}
	if n := bases[customers.BasisUnknown]; n > 0 && rule == customers.MarketingInferred && n*4 >= total {
		out = append(out, contactWarning{Code: "unknown_origin_in_audience", Count: n, Message: fmt.Sprintf(
			"%s no record of how you got them (a CRM sync, an import or added by hand). If any were bought or scraped, emailing them is likely to be reported as spam. Set their origin to prospect so they are kept out, or to first_party if they are your customers.",
			people(n, "has", "have"))})
	}
	return out
}

// people is "1 person is" or "3 people are", with the verb given.
func people(n int, one, many string) string {
	if n == 1 {
		return "1 person " + one
	}
	return fmt.Sprintf("%d people %s", n, many)
}
