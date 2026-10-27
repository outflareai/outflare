package infra

import (
	"context"
	"errors"
	"sort"
	"sync"
)

// SupplierManual is the supplier for plans where the person buys the
// domains and mailboxes themselves; the plan waits for them and checks
// their work.
const SupplierManual = "manual"

// Account is the workspace's connected account at a supplier: an
// integration on the connectors framework, holding the customer's own
// credentials. Customers pay suppliers directly.
type Account struct {
	WorkspaceID   string
	IntegrationID string
}

// QuoteRequest asks a supplier to price a plan.
type QuoteRequest struct {
	Domains            []string
	Provider           string // google or microsoft
	MailboxesPerDomain int
}

// DomainQuote is one domain's availability and price.
type DomainQuote struct {
	Domain    string `json:"domain"`
	Available bool   `json:"available"`
	// Price of the domain for its first year, in the quote's currency.
	PriceCents *int64 `json:"price_cents,omitempty"`
	// Why it is unavailable, e.g. "registered", "premium".
	Reason string `json:"reason,omitempty"`
}

// Quote is a supplier's price for a plan. Totals are nil when the supplier
// does not say.
type Quote struct {
	Currency string        `json:"currency"`
	Domains  []DomainQuote `json:"domains"`
	// Paid once now (domains, set-up) and every month (mailboxes).
	OnceCents    *int64 `json:"once_cents,omitempty"`
	MonthlyCents *int64 `json:"monthly_cents,omitempty"`
}

// OrderMailbox is one mailbox to create.
type OrderMailbox struct {
	Email     string
	FirstName string
	LastName  string
}

// OrderDomain is one domain to buy and the mailboxes to create on it.
type OrderDomain struct {
	Domain string
	// Owned is true for a domain the customer already has: only its
	// mailboxes are ordered.
	Owned     bool
	Mailboxes []OrderMailbox
}

// OrderRequest buys domains with their mailboxes. It spends the customer's
// money, so it is only ever sent for a plan a person (or an agent with the
// approve scope) approved, with the total they saw.
type OrderRequest struct {
	Provider string
	Domains  []OrderDomain
	// The plan id. A supplier that supports idempotent orders passes it on,
	// so a retry after a crash cannot buy twice.
	IdempotencyKey string
}

// Progress of an order, or of one domain or mailbox in it.
const (
	OrderPending = "pending"
	OrderDone    = "done"
	OrderFailed  = "failed"
)

// ItemStatus is one domain or mailbox in an order. A domain is done when
// it is registered; DNS is checked by the engine itself.
type ItemStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// OrderStatus is where an order is.
type OrderStatus struct {
	Status    string       `json:"status"`
	Domains   []ItemStatus `json:"domains"`
	Mailboxes []ItemStatus `json:"mailboxes"`
	Detail    string       `json:"detail,omitempty"`
}

// Supplier buys domains and mailboxes on the customer's own account. Each
// one is a connector on the connectors framework (Cheap Inboxes first);
// the engine never resells. Created mailboxes reach the workspace through
// the connector's own import, and the plan picks them up by address.
type Supplier interface {
	// Quote prices the plan. It changes nothing at the supplier.
	Quote(ctx context.Context, acct Account, req QuoteRequest) (Quote, error)
	// Order places the order and returns the supplier's reference.
	Order(ctx context.Context, acct Account, req OrderRequest) (ref string, err error)
	// OrderStatus reports an order's progress.
	OrderStatus(ctx context.Context, acct Account, ref string) (OrderStatus, error)
	// Cancel stops paying for a mailbox, by address, at the end of the
	// period, where the supplier allows it.
	Cancel(ctx context.Context, acct Account, email string) error
}

// ErrUnknownSupplier is returned for a supplier no connector registered.
var ErrUnknownSupplier = errors.New("unknown supplier")

var (
	suppliersMu sync.RWMutex
	suppliers   = map[string]Supplier{}
)

// RegisterSupplier makes a supplier available to plans under name, the
// connector's id (e.g. "cheapinboxes"). Call at start-up.
func RegisterSupplier(name string, s Supplier) {
	suppliersMu.Lock()
	defer suppliersMu.Unlock()
	suppliers[name] = s
}

// LookupSupplier returns a registered supplier.
func LookupSupplier(name string) (Supplier, error) {
	suppliersMu.RLock()
	defer suppliersMu.RUnlock()
	s, ok := suppliers[name]
	if !ok {
		return nil, ErrUnknownSupplier
	}
	return s, nil
}

// Suppliers lists the supplier names plans can use, "manual" first.
func Suppliers() []string {
	suppliersMu.RLock()
	defer suppliersMu.RUnlock()
	out := []string{}
	for name := range suppliers {
		out = append(out, name)
	}
	sort.Strings(out)
	return append([]string{SupplierManual}, out...)
}
