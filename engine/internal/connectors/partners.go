package connectors

import (
	"context"
	"time"
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
// them, so each finds what it points at already stored. Links come first:
// a partner joins with the code of their link.
const (
	PartnerKindPartners     = "partners"
	PartnerKindLinks        = "links"
	PartnerKindPromoCodes   = "promo_codes"
	PartnerKindCustomers    = "customers"
	PartnerKindTransactions = "transactions"
	PartnerKindCommissions  = "commissions"
)

// PartnerImportKinds is every kind, in import order.
var PartnerImportKinds = []string{PartnerKindLinks, PartnerKindPartners, PartnerKindPromoCodes, PartnerKindCustomers,
	PartnerKindTransactions, PartnerKindCommissions}

// PartnerImporter reads a partner platform's program, one page at a time.
type PartnerImporter interface {
	// PartnerPrograms lists the account's programs.
	PartnerPrograms(ctx context.Context, cl *Client, creds Creds) ([]PartnerProgram, error)
	// PartnerPage reads one page of kind in program, after the cursor a
	// previous page returned ("" for the first). next is "" on the last page.
	PartnerPage(ctx context.Context, cl *Client, creds Creds, program, kind, after string) (page PartnerObjects, next string, err error)
}

type PartnerProgram struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Currency string `json:"currency"` // "" when the platform doesn't say
}

// PartnerObjects is one page; only the slice for the kind read is set.
type PartnerObjects struct {
	Partners     []ImportPartner
	Links        []ImportLink
	PromoCodes   []ImportPromoCode
	Customers    []ImportCustomer
	Transactions []ImportTransaction
	Commissions  []ImportCommission
}

// Len is how many objects the page holds.
func (p PartnerObjects) Len() int {
	return len(p.Partners) + len(p.Links) + len(p.PromoCodes) + len(p.Customers) + len(p.Transactions) + len(p.Commissions)
}

type ImportPartner struct {
	ID    string
	Email string
	Name  string
	// Status is pending, active, suspended or rejected.
	Status string
	// PayoutMethod is the payout details the platform holds, as text.
	PayoutMethod string
	Created      time.Time
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
}

// ImportCustomer is someone a partner referred.
type ImportCustomer struct {
	ID        string
	PartnerID string
	Email     string
	// CustomerID is the workspace's own id for them, as its backend sent it.
	CustomerID string
	Created    time.Time
}

type ImportTransaction struct {
	ID          string
	CustomerID  string // the platform's customer id
	AmountCents int64
	Currency    string // "" for the program's
	Recurring   bool
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
