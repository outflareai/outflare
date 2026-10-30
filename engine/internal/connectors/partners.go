package connectors

import (
	"context"
	"errors"
	"time"

	"github.com/outflareai/outflare/engine/internal/partners"
)

// Partner platforms are where a workspace ran its referral or affiliate
// program before Outflare (Tolt). Outflare only reads them, to move the
// program over: partners with their link codes, promo codes, referred
// customers, transactions and commissions, paid or not.

const (
	CategoryPartners  = "partners"
	CapPartnersImport = "partners.import" // implements PartnerImporter
)

// Kinds of objects a PartnerImporter lists, in the order an import reads
// them, so each finds what it points at already stored. Groups come first,
// then links: a partner joins their group with the code of their link.
// Flows come after partners, whose own rates they can hold.
const (
	PartnerKindGroups       = "groups"
	PartnerKindFlows        = "flows"
	PartnerKindPartners     = "partners"
	PartnerKindLinks        = "links"
	PartnerKindPromoCodes   = "promo_codes"
	PartnerKindCustomers    = "customers"
	PartnerKindTransactions = "transactions"
	PartnerKindCommissions  = "commissions"
)

// PartnerImportKinds is every kind, in import order.
var PartnerImportKinds = []string{PartnerKindGroups, PartnerKindLinks, PartnerKindPartners, PartnerKindFlows, PartnerKindPromoCodes,
	PartnerKindCustomers, PartnerKindTransactions, PartnerKindCommissions}

// LegacyPartnerImportKinds is the order before groups and flows, for an
// import whose saved progress counted in it.
var LegacyPartnerImportKinds = []string{PartnerKindLinks, PartnerKindPartners, PartnerKindPromoCodes, PartnerKindCustomers,
	PartnerKindTransactions, PartnerKindCommissions}

// ErrPartnerKindUnavailable is a kind the platform doesn't list (it has no
// such endpoint, or not on the account's plan). The import notes it and
// goes on with the next kind.
var ErrPartnerKindUnavailable = errors.New("the partner platform doesn't list these")

// PartnerImporter reads a partner platform's program, one page at a time.
type PartnerImporter interface {
	// PartnerPrograms lists the account's programs.
	PartnerPrograms(ctx context.Context, cl *Client, creds Creds) ([]PartnerProgram, error)
	// PartnerPage reads one page of kind in program, after the cursor a
	// previous page returned ("" for the first). next is "" on the last page.
	PartnerPage(ctx context.Context, cl *Client, creds Creds, program, kind, after string) (page PartnerObjects, next string, err error)
}

// PartnerFetcher is a PartnerImporter that can read one partner by id.
// An import uses it for a partner that a promo code, customer or
// commission names but the partner list didn't give, such as one the
// platform leaves out of its list (not onboarded, invited).
type PartnerFetcher interface {
	PartnerByID(ctx context.Context, cl *Client, creds Creds, program, id string) (ImportPartner, error)
}

// PartnerIDs are the partners a page's promo codes, customers and
// commissions name, each once.
func (p PartnerObjects) PartnerIDs() []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, c := range p.PromoCodes {
		add(c.PartnerID)
	}
	for _, c := range p.Customers {
		add(c.PartnerID)
	}
	for _, c := range p.Commissions {
		add(c.PartnerID)
	}
	return out
}

// UnreadableError is a page the platform answered that couldn't be read:
// not JSON, or no list in it. Asking again gets the same answer, so an
// import notes it and reads the next kind.
type UnreadableError struct{ Message string }

func (e *UnreadableError) Error() string { return e.Message }

type PartnerProgram struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Currency string `json:"currency"` // "" when the platform doesn't say
	// Status is active, inactive, or "" when the platform doesn't say.
	Status string `json:"status"`
}

// PartnerObjects is one page; only the slice for the kind read is set.
type PartnerObjects struct {
	Groups []ImportGroup
	Flows  []ImportFlow
	// Reported is how many objects the platform says the list holds, from
	// the first page of a list (nil when it doesn't say), to reconcile with
	// how many an import read.
	Reported *int
	// ReportedFor names the part of the list Reported counts, when the
	// platform lists a kind in parts (partners by status).
	ReportedFor  string
	Partners     []ImportPartner
	Links        []ImportLink
	PromoCodes   []ImportPromoCode
	Customers    []ImportCustomer
	Transactions []ImportTransaction
	Commissions  []ImportCommission
}

// Len is how many objects the page holds.
func (p PartnerObjects) Len() int {
	return len(p.Groups) + len(p.Flows) + len(p.Partners) + len(p.Links) + len(p.PromoCodes) + len(p.Customers) + len(p.Transactions) + len(p.Commissions)
}

type ImportPartner struct {
	ID    string
	Email string
	Name  string
	// Status is pending, active, suspended or rejected.
	Status string
	// PayoutMethod is the payout details the platform holds, as text.
	PayoutMethod string
	// GroupID is the platform's group they are in, if any.
	GroupID string
	Created time.Time
}

// ImportGroup is a group of partners with its own flows.
type ImportGroup struct {
	ID   string
	Name string
	// Default is the group new partners join.
	Default bool
}

// ImportFlow is a reward flow, already in Outflare's terms. Unsupported
// lists what the platform's flow has that Outflare couldn't map (a
// condition on an unknown attribute, say); such a flow isn't imported, so
// nothing pays that shouldn't.
type ImportFlow struct {
	ID string
	// GroupID is the platform group the flow applies to; "" is everyone.
	GroupID string
	Name    string
	// Trigger is sale or signup.
	Trigger string
	// Kind is percent, fixed, or group (move to TargetGroupID).
	Kind          string
	PercentBP     *int
	AmountCents   *int64
	TargetGroupID string
	// Recurrence is every, first or months (Months).
	Recurrence string
	Months     *int
	// Approval is program, auto, after_days (ApprovalDays) or manual.
	Approval     string
	ApprovalDays *int
	Conditions   partners.Conditions
	// Overrides name the platform's partner ids (PartnerID) or products.
	Overrides   []partners.Override
	Unsupported []string
}

// ImportLink is a partner's tracking link: ?param=value on the site.
type ImportLink struct {
	ID        string
	PartnerID string
	Param     string
	Value     string
	Created   time.Time
}

type ImportPromoCode struct {
	ID        string
	PartnerID string
	Code      string
	// Keys, when PartnerID is empty, are the object's top-level keys and
	// their types (never values), so the field naming its partner can be
	// found.
	Keys string
}

// ImportCustomer is someone a partner referred.
type ImportCustomer struct {
	ID        string
	PartnerID string
	Email     string
	// CustomerID is the workspace's own id for them, as its backend sent it.
	CustomerID string
	// Aliases are other ids the workspace's backend may report them by
	// after switching over, such as the platform's own customer id that
	// its sign-up call returned and the backend stored.
	Aliases []string
	Created time.Time
}

type ImportTransaction struct {
	ID          string
	CustomerID  string // the platform's customer id
	AmountCents int64
	Currency    string // "" for the program's
	Recurring   bool
	// PaymentSource is the payment provider in Outflare's form (stripe),
	// and Interval day, week, month, year or one_time; "" when unknown.
	PaymentSource string
	Interval      string
	// RefundedCents is how much was refunded, 0 for none.
	RefundedCents int64
	Created       time.Time
	RefundedAt    time.Time
}

type ImportCommission struct {
	ID            string
	PartnerID     string
	CustomerID    string
	TransactionID string
	AmountCents   int64 // negative for a clawback
	Currency      string
	// Status is pending, approved, paid or rejected.
	Status  string
	Created time.Time
	// PaidAt and PayoutID say when, and in which payout, a paid one was paid.
	PaidAt   time.Time
	PayoutID string
}
