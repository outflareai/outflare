package cheapinboxes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Ordering. These calls are for the infrastructure planner, which owns the
// approval step: Checkout and AddMailboxes charge the card on file at Cheap
// Inboxes, and CancelMailbox ends a paid mailbox. Nothing else in the
// engine calls them.

// DomainOption is one searched domain.
type DomainOption struct {
	Domain    string  `json:"domain"`
	Available bool    `json:"available"`
	Status    string  `json:"status"`
	Price     float64 `json:"price"`
	Currency  string  `json:"currency"`
}

// SearchDomains checks a keyword across TLDs (without dots, e.g. "com").
// Free, changes nothing.
func (Connector) SearchDomains(ctx context.Context, cl *c.Client, cr c.Creds, keyword string, tlds []string) (exact, suggestions []DomainOption, err error) {
	var r struct {
		Exact       []DomainOption `json:"exact"`
		Suggestions []DomainOption `json:"suggestions"`
	}
	body := map[string]any{"keyword": keyword}
	if len(tlds) > 0 {
		body["tlds"] = tlds
	}
	err = call(ctx, cl, cr, http.MethodPost, "/v1/discovery/domains/search", body, &r)
	return r.Exact, r.Suggestions, err
}

// CartDomain is one domain in a quote or an order.
type CartDomain struct {
	Domain string `json:"domain"`
	TLD    string `json:"tld,omitempty"`
	// Existing: a domain the account already owns (top-up, priced at $0).
	Existing  bool          `json:"isExisting,omitempty"`
	Mailboxes CartMailboxes `json:"mailboxes"`
}

// CartMailboxes is how many mailboxes of which provider to create: a
// count for a quote, the accounts themselves for a checkout.
type CartMailboxes struct {
	Provider string        `json:"provider"` // google or microsoft
	Count    int           `json:"count,omitempty"`
	Accounts []CartAccount `json:"accounts,omitempty"`
}

// CartAccount is one mailbox to create at checkout.
type CartAccount struct {
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name"`
	EmailPrefix string `json:"email_prefix"`
}

// Quote prices a cart. Free, changes nothing. The answer is passed through
// as Cheap Inboxes returns it (items and totals in cents).
func (Connector) Quote(ctx context.Context, cl *c.Client, cr c.Creds, domains []CartDomain) (json.RawMessage, error) {
	var r struct {
		Quote json.RawMessage `json:"quote"`
	}
	err := call(ctx, cl, cr, http.MethodPost, "/v1/orders/quote", map[string]any{"cart": map[string]any{"domains": domains}}, &r)
	return r.Quote, err
}

// Order is an order's state.
type Order struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Status    string         `json:"status"`
	Domains   []OrderDomain  `json:"domains"`
	Mailboxes []OrderMailbox `json:"mailboxes"`
}

// OrderDomain is one domain in an order.
type OrderDomain struct {
	ID     string `json:"id"`
	Domain string `json:"domain"`
	Status string `json:"status"`
}

// OrderMailbox is one mailbox in an order.
type OrderMailbox struct {
	ID                string  `json:"id"`
	FullEmail         string  `json:"full_email"`
	Status            string  `json:"status"`
	ProvisioningError *string `json:"provisioning_error"`
}

// Checkout places an order and charges the account's default card. It is
// sent exactly once, never retried here: the caller owns retries, and
// passes the same idempotencyKey (if any) so Cheap Inboxes cannot charge
// twice for one order.
func (Connector) Checkout(ctx context.Context, cl *c.Client, cr c.Creds, orderName, idempotencyKey string, domains []CartDomain) (Order, error) {
	var r struct {
		OrderID string `json:"order_id"`
		Order
	}
	body := map[string]any{"domains": domains}
	if orderName != "" {
		body["name"] = orderName
	}
	var hdr http.Header
	if idempotencyKey != "" {
		hdr = http.Header{"Idempotency-Key": {idempotencyKey}}
	}
	err := send(ctx, cl, cr, http.MethodPost, "/v1/orders/checkout", body, &r, hdr, 0)
	r.Order.ID = orDefault(r.Order.ID, r.OrderID)
	return r.Order, err
}

// GetOrder reads an order, for polling after checkout.
func (Connector) GetOrder(ctx context.Context, cl *c.Client, cr c.Creds, id string) (Order, error) {
	var r struct {
		Order     Order          `json:"order"`
		Domains   []OrderDomain  `json:"domains"`
		Mailboxes []OrderMailbox `json:"mailboxes"`
	}
	if err := call(ctx, cl, cr, http.MethodGet, "/v1/orders/"+url.PathEscape(id), nil, &r); err != nil {
		return Order{}, err
	}
	o := r.Order
	o.Domains, o.Mailboxes = r.Domains, r.Mailboxes
	return o, nil
}

// AddMailboxes creates 1 to 100 more mailboxes on an active domain the
// account owns, and charges for them.
func (Connector) AddMailboxes(ctx context.Context, cl *c.Client, cr c.Creds, domainID string, count int, firstNames, lastNames string) ([]c.VendorMailbox, error) {
	var r struct {
		Mailboxes []mailbox `json:"mailboxes"`
	}
	body := map[string]any{"count": count}
	if firstNames != "" {
		body["first_name_pattern"] = firstNames
	}
	if lastNames != "" {
		body["last_name_pattern"] = lastNames
	}
	if err := call(ctx, cl, cr, http.MethodPost, "/v1/domains/"+url.PathEscape(domainID)+"/mailboxes", body, &r); err != nil {
		return nil, err
	}
	out := make([]c.VendorMailbox, 0, len(r.Mailboxes))
	for _, m := range r.Mailboxes {
		out = append(out, m.neutral())
	}
	return out, nil
}

// CancelMailbox schedules a mailbox to end at the close of its billing
// period. It can be undone until then.
func (Connector) CancelMailbox(ctx context.Context, cl *c.Client, cr c.Creds, id, detail string) error {
	body := map[string]any{"reason": "user_request"}
	if detail != "" {
		body["reason_detail"] = detail
	}
	return call(ctx, cl, cr, http.MethodPost, "/v1/mailboxes/"+url.PathEscape(id)+"/cancel", body, nil)
}
