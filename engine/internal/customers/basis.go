package customers

import "context"

// The contact basis says, for every person, why the workspace holds them
// and so what may go out to them: product updates and newsletters only to
// people who agreed, never to a lead someone found on the web or bought
// from a data provider.
const (
	// BasisOptedIn: they agreed to marketing email (a sign-up form, a
	// preference page, the consents API).
	BasisOptedIn = "opted_in"
	// BasisOptedOut: they took back their consent to all marketing email.
	BasisOptedOut = "opted_out"
	// BasisCustomer: they signed up, bought or used the product (origin
	// first_party), and never said yes or no to marketing email.
	BasisCustomer = "customer"
	// BasisThirdParty: a lead from a list, a data provider, a scrape or a
	// sequencer (origin prospect). Cold outreach only.
	BasisThirdParty = "third_party"
	// BasisUnknown: from a CRM, an import or added by hand, with nothing
	// saying how the workspace got them.
	BasisUnknown = "unknown"
)

// The workspace's rule for marketing email to people who never agreed to
// it (workspaces.customer_marketing).
const (
	// MarketingOptIn: only people who agreed get marketing email.
	MarketingOptIn = "opt_in"
	// MarketingInferred (the default): everyone but third-party leads does,
	// unless they said no.
	MarketingInferred = "inferred"
)

// Basis is the contact basis of a person of an origin whose latest choice
// about all marketing email is marketing (granted, withdrawn or "").
func Basis(origin, marketing string) string {
	switch {
	case marketing == Granted:
		return BasisOptedIn
	case marketing == Withdrawn:
		return BasisOptedOut
	case origin == "first_party":
		return BasisCustomer
	case origin == "prospect":
		return BasisThirdParty
	}
	return BasisUnknown
}

// MayMarket reports whether marketing email may go to someone of a basis
// under the workspace's rule, and why not when it may not. consented says
// they agreed to the email's own topic, which is enough on its own.
func MayMarket(basis, rule string, consented bool) (bool, string) {
	switch {
	case consented || basis == BasisOptedIn:
		return true, ""
	case basis == BasisOptedOut:
		return false, "unsubscribed from marketing email"
	case basis == BasisThirdParty:
		return false, "a third-party lead who never agreed to marketing email"
	case rule == MarketingInferred:
		return true, ""
	case basis == BasisCustomer:
		return false, "a customer who never agreed to marketing email (the workspace sends it only to people who opted in)"
	}
	return false, "never agreed to marketing email, and nothing says how the workspace got them"
}

// MarketingRule is the workspace's rule for marketing email to people who
// never agreed to it.
func MarketingRule(ctx context.Context, q Querier, workspaceID string) (string, error) {
	var rule string
	err := q.QueryRow(ctx, `SELECT customer_marketing FROM workspaces WHERE id = $1`, workspaceID).Scan(&rule)
	return rule, err
}
