package connectors

import (
	"context"
	"errors"
	"time"
)

// Payment providers say who pays, how much and for what, from the
// workspace's own billing account. Outflare only reads: customers,
// subscriptions, payments and refunds become customer records and events,
// so revenue is stored once and every module (the CRM, lifecycle email,
// missions, partners) reads the same numbers. Nothing is charged, changed
// or paid out through them.

const (
	CategoryPayments = "payments"
	CapPayments      = "payments.read" // implements PaymentReader
)

// Kinds of objects a PaymentReader lists, in the order a first sync reads
// them, so a subscription or payment finds its customer already stored.
const (
	PayCustomers     = "customers"
	PaySubscriptions = "subscriptions"
	PayCharges       = "charges"
	PayRefunds       = "refunds"
)

// PaymentKinds is every kind, in sync order.
var PaymentKinds = []string{PayCustomers, PaySubscriptions, PayCharges, PayRefunds}

// ErrPaymentCursor means the provider no longer knows where the last sync
// stopped (its change log is kept for a limited time), so everything is
// read again.
var ErrPaymentCursor = errors.New("the payment provider no longer has the changes since the last sync")

// PaymentCustomer is someone who pays.
type PaymentCustomer struct {
	ID    string
	Email string
	Name  string
	// UserID is the workspace's own id for the person, when its product
	// stores one on the customer (metadata user_id).
	UserID  string
	Created time.Time
	Deleted bool
}

// PaymentSubscription is a recurring plan a customer is on. MonthlyMinor
// is what it brings in a month (all items, before discounts and tax), in
// the currency's smallest unit.
type PaymentSubscription struct {
	ID           string
	CustomerID   string
	Status       string // trialing, active, past_due, canceled or other
	Plan         string // the product's name
	ProductID    string
	PriceID      string
	Interval     string // day, week, month or year
	MonthlyMinor int64
	Currency     string // ISO 4217, upper case
	Created      time.Time
	CanceledAt   *time.Time
}

// PaymentCharge is money received.
type PaymentCharge struct {
	ID          string
	CustomerID  string
	InvoiceID   string
	AmountMinor int64
	Currency    string
	Created     time.Time
}

// PaymentRefund is money given back.
type PaymentRefund struct {
	ID          string
	ChargeID    string
	CustomerID  string
	AmountMinor int64
	Currency    string
	Created     time.Time
}

// PaymentPage is one page of a list, or one batch of changes.
type PaymentPage struct {
	Customers     []PaymentCustomer
	Subscriptions []PaymentSubscription
	Charges       []PaymentCharge
	Refunds       []PaymentRefund
	// Cursor is where the next page starts. More is false on the last page.
	Cursor string
	More   bool
}

// PaymentReader reads a billing account.
type PaymentReader interface {
	// PaymentList reads one page of a kind, starting after cursor ("" for
	// the first page).
	PaymentList(ctx context.Context, cl *Client, creds Creds, kind, cursor string) (PaymentPage, error)
	// PaymentChanges reads what changed after cursor, oldest first, with
	// the cursor to read from next. An empty cursor returns no changes,
	// only the position to start from, which a first sync takes before it
	// lists everything. A cursor the provider has forgotten is
	// ErrPaymentCursor.
	PaymentChanges(ctx context.Context, cl *Client, creds Creds, cursor string) (PaymentPage, error)
	// PaymentCustomer reads one customer.
	PaymentCustomer(ctx context.Context, cl *Client, creds Creds, id string) (PaymentCustomer, error)
}
