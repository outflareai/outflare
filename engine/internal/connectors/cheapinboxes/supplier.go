package cheapinboxes

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/infra"
)

// Supplier lets infrastructure plans buy domains and mailboxes on the
// customer's own Cheap Inboxes account. Register it at start-up with
// infra.RegisterSupplier(ID, Supplier{Open: ...}).
type Supplier struct {
	// Open loads a workspace's integration with its credentials.
	Open func(ctx context.Context, ws, integrationID string) (*c.Opened, error)
}

var _ infra.Supplier = Supplier{}

func (s Supplier) open(ctx context.Context, acct infra.Account) (*c.Opened, error) {
	if s.Open == nil {
		return nil, errors.New("cheapinboxes: supplier has no Open")
	}
	o, err := s.Open(ctx, acct.WorkspaceID, acct.IntegrationID)
	if err != nil {
		return nil, err
	}
	if o.Integration.Connector != ID {
		return nil, c.Errf(c.ErrInvalid, "this plan's account is %s, not Cheap Inboxes", o.Integration.Connector)
	}
	return o, nil
}

// splitDomain returns a domain's first label and the rest ("acme", "co.uk").
func splitDomain(d string) (label, tld string) {
	d = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
	label, tld, _ = strings.Cut(d, ".")
	return label, tld
}

// Quote checks each domain is free to register, then prices the free ones
// with their mailboxes. It changes nothing at Cheap Inboxes.
func (s Supplier) Quote(ctx context.Context, acct infra.Account, req infra.QuoteRequest) (infra.Quote, error) {
	o, err := s.open(ctx, acct)
	if err != nil {
		return infra.Quote{}, err
	}
	var ci Connector
	q := infra.Quote{Currency: "USD", Domains: []infra.DomainQuote{}}
	var cart []CartDomain
	for _, d := range req.Domains {
		label, tld := splitDomain(d)
		dq := infra.DomainQuote{Domain: strings.ToLower(d)}
		if label == "" || tld == "" {
			dq.Reason = "not a domain name"
			q.Domains = append(q.Domains, dq)
			continue
		}
		exact, _, err := ci.SearchDomains(ctx, o.Client, o.Creds, label, []string{tld})
		if err != nil {
			return infra.Quote{}, err
		}
		dq.Reason = "Cheap Inboxes does not offer it"
		for _, e := range exact {
			if !strings.EqualFold(e.Domain, dq.Domain) {
				continue
			}
			dq.Available, dq.Reason = e.Available, ""
			if !e.Available {
				dq.Reason = orDefault(e.Status, "registered")
			}
			if e.Price > 0 {
				cents := int64(math.Round(e.Price * 100))
				dq.PriceCents = &cents
			}
			if e.Currency != "" {
				q.Currency = strings.ToUpper(e.Currency)
			}
		}
		q.Domains = append(q.Domains, dq)
		if dq.Available {
			cart = append(cart, CartDomain{Domain: dq.Domain, TLD: tld,
				Mailboxes: CartMailboxes{Provider: req.Provider, Count: req.MailboxesPerDomain}})
		}
	}
	if len(cart) == 0 {
		return q, nil
	}
	var price struct {
		Quote struct {
			Totals struct {
				TotalTodayCents     *int64 `json:"total_today_cents"`
				MailboxMonthlyCents *int64 `json:"mailbox_monthly_cents"`
			} `json:"totals"`
		} `json:"quote"`
	}
	if err := call(ctx, o.Client, o.Creds, http.MethodPost, "/v1/orders/quote",
		map[string]any{"cart": map[string]any{"domains": cart}}, &price); err != nil {
		return infra.Quote{}, err
	}
	q.OnceCents, q.MonthlyCents = price.Quote.Totals.TotalTodayCents, price.Quote.Totals.MailboxMonthlyCents
	return q, nil
}

// Order checks out once, charging the card on file at Cheap Inboxes. The
// plan id goes along as the Idempotency-Key, so the planner retrying after
// a crash replays the first order instead of buying again.
func (s Supplier) Order(ctx context.Context, acct infra.Account, req infra.OrderRequest) (string, error) {
	o, err := s.open(ctx, acct)
	if err != nil {
		return "", err
	}
	cart := make([]CartDomain, 0, len(req.Domains))
	for _, d := range req.Domains {
		_, tld := splitDomain(d.Domain)
		cd := CartDomain{Domain: strings.ToLower(d.Domain), TLD: tld, Existing: d.Owned,
			Mailboxes: CartMailboxes{Provider: req.Provider, Accounts: []CartAccount{}}}
		for _, m := range d.Mailboxes {
			local, at, ok := strings.Cut(strings.ToLower(m.Email), "@")
			if !ok || local == "" || at != cd.Domain {
				return "", c.Errf(c.ErrInvalid, "%s is not an address on %s", m.Email, d.Domain)
			}
			cd.Mailboxes.Accounts = append(cd.Mailboxes.Accounts, CartAccount{FirstName: m.FirstName, LastName: m.LastName, EmailPrefix: local})
		}
		cart = append(cart, cd)
	}
	name := "Outflare"
	if req.IdempotencyKey != "" {
		name = "Outflare plan " + req.IdempotencyKey
	}
	ord, err := Connector{}.Checkout(ctx, o.Client, o.Creds, name, req.IdempotencyKey, cart)
	if err != nil {
		return "", err
	}
	if ord.ID == "" {
		return "", c.Errf(c.ErrUnavailable, "Cheap Inboxes took the order but did not say its id; check your Cheap Inboxes orders before trying again")
	}
	return ord.ID, nil
}

// OrderStatus reports each domain and mailbox. A mailbox is done once it
// is active at Cheap Inboxes; it then reaches the workspace through the
// connector's own import (which, for Google, waits on an app password).
func (s Supplier) OrderStatus(ctx context.Context, acct infra.Account, ref string) (infra.OrderStatus, error) {
	o, err := s.open(ctx, acct)
	if err != nil {
		return infra.OrderStatus{}, err
	}
	ord, err := Connector{}.GetOrder(ctx, o.Client, o.Creds, ref)
	if err != nil {
		return infra.OrderStatus{}, err
	}
	out := infra.OrderStatus{Domains: []infra.ItemStatus{}, Mailboxes: []infra.ItemStatus{}}
	pending, failed := 0, 0
	count := func(st string) {
		switch st {
		case infra.OrderPending:
			pending++
		case infra.OrderFailed:
			failed++
		}
	}
	for _, d := range ord.Domains {
		st, detail := domainProgress(d.Status)
		count(st)
		out.Domains = append(out.Domains, infra.ItemStatus{Name: strings.ToLower(d.Domain), Status: st, Detail: detail})
	}
	for _, m := range ord.Mailboxes {
		st, detail := mailboxProgress(m.Status)
		if st == infra.OrderFailed && m.ProvisioningError != nil && *m.ProvisioningError != "" {
			detail = "Cheap Inboxes could not set it up: " + *m.ProvisioningError
		}
		count(st)
		out.Mailboxes = append(out.Mailboxes, infra.ItemStatus{Name: strings.ToLower(m.FullEmail), Status: st, Detail: detail})
	}
	switch st := strings.ToLower(ord.Status); {
	case st == "failed" || st == "cancelled" || st == "canceled" || st == "refunded" || st == "payment_failed":
		out.Status, out.Detail = infra.OrderFailed, "Cheap Inboxes marked the order "+humanize(st)+"."
	case pending > 0 || (len(out.Domains) == 0 && len(out.Mailboxes) == 0):
		out.Status, out.Detail = infra.OrderPending, "Cheap Inboxes is setting it up ("+humanize(orDefault(st, "pending"))+")."
	case failed > 0:
		out.Status, out.Detail = infra.OrderFailed, fmt.Sprintf("%d item(s) could not be set up at Cheap Inboxes.", failed)
	default:
		out.Status = infra.OrderDone
	}
	return out, nil
}

func humanize(s string) string { return strings.ReplaceAll(strings.ToLower(s), "_", " ") }

// domainProgress maps a Cheap Inboxes domain status.
func domainProgress(st string) (string, string) {
	switch s := strings.ToLower(st); s {
	case "active":
		return infra.OrderDone, ""
	case "error", "failed", "cancelled", "canceled", "expired":
		return infra.OrderFailed, "Cheap Inboxes reports the domain as " + humanize(s) + "."
	case "pending_ns_verification":
		return infra.OrderPending, "Waiting for the domain's nameservers to point at Cheap Inboxes."
	case "pending_manual_setup":
		return infra.OrderPending, "Waiting for DNS set-up at Cheap Inboxes (see the domain there)."
	default:
		return infra.OrderPending, "Cheap Inboxes is setting it up (" + humanize(orDefault(s, "pending")) + ")."
	}
}

// mailboxProgress maps a Cheap Inboxes mailbox status.
func mailboxProgress(st string) (string, string) {
	switch s := strings.ToLower(st); s {
	case "active":
		return infra.OrderDone, ""
	case "error", "failed", "cancelled", "canceled", "suspended":
		return infra.OrderFailed, "Cheap Inboxes reports the mailbox as " + humanize(s) + "."
	default:
		return infra.OrderPending, "Cheap Inboxes is creating it (" + humanize(orDefault(s, "pending")) + ")."
	}
}

// Cancel schedules the mailbox at this address to end with its billing
// period. A mailbox already cancelled, or no longer there, is not an error.
func (s Supplier) Cancel(ctx context.Context, acct infra.Account, email string) error {
	o, err := s.open(ctx, acct)
	if err != nil {
		return err
	}
	var ci Connector
	email = strings.ToLower(strings.TrimSpace(email))
	page := c.VendorPage{}
	for {
		list, next, err := ci.ListMailboxes(ctx, o.Client, o.Creds, page)
		if err != nil {
			return err
		}
		for _, m := range list {
			if m.Email != email {
				continue
			}
			if m.Status == c.VendorCancelling || m.Status == c.VendorCancelled {
				return nil
			}
			return ci.CancelMailbox(ctx, o.Client, o.Creds, m.ExternalID, "Retired by an Outflare infrastructure plan")
		}
		if next == "" {
			return nil
		}
		page.Cursor = next
	}
}
