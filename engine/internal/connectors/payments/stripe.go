// Package payments connects the workspace's own billing accounts so
// Outflare knows who pays, how much and for what. It only reads.
package payments

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const stripeAPI = "https://api.stripe.com"

// stripeVersion pins the API version, so a change to the account's default
// never changes what Outflare reads.
const stripeVersion = "2024-06-20"

// stripePage is how many objects Stripe returns per list call (its maximum).
const stripePage = 100

// stripeTimeWindow is how many pages of changes a time-based cursor reads
// in one go, before a first change has been seen.
const stripeTimeWindow = 20

// All returns the payment connectors.
func All() []c.Connector { return []c.Connector{Stripe{}} }

// Stripe reads a Stripe account, signed in through the server's Stripe
// App (OAuth, read-only permissions set in the app's manifest) or with a
// restricted, read-only key.
type Stripe struct{}

func (Stripe) Definition() c.Definition {
	return c.Definition{
		ID: "stripe", Name: "Stripe", Category: c.CategoryPayments,
		Summary: "See who pays you: customers, subscriptions, payments and refunds on each person's record.",
		Description: "Reads your Stripe customers, subscriptions, payments and refunds, every few minutes. Each customer becomes a person " +
			"with their plan, monthly revenue, lifetime revenue and billing status, and each payment is an event on their timeline, " +
			"so the CRM, lifecycle email, missions and partners all use the same numbers. Read-only: Outflare never charges, refunds or changes anything in Stripe.",
		Website: "https://stripe.com", DocsURL: "https://docs.stripe.com/api",
		KeyURL: "https://dashboard.stripe.com/apikeys",
		Auth:   c.AuthOAuth2, AuthMethods: c.Both,
		// A Stripe App's install link is its consent page; set
		// CONNECTOR_STRIPE_AUTH_URL to the app's own link when Stripe gives
		// one. The token call takes the app's secret key and nothing else.
		OAuth: &c.OAuth{
			AuthURL: "https://marketplace.stripe.com/oauth/v2/authorize", TokenURL: "https://api.stripe.com/v1/oauth/token",
			Scopes: []string{}, SecretOnly: true, Lifetime: time.Hour,
		},
		Fields: []c.Field{{Name: "api_key", Label: "Restricted key", Secret: true, Placeholder: "rk_live_...",
			Help: "In Stripe: Developers, API keys, Create restricted key. Give it Read access to Customers, Charges, Events, Products, " +
				"Prices and Subscriptions (under Billing), and nothing else. A test-mode key reads test data."}},
		Capabilities: []string{c.CapPayments},
		Pricing:      "Free: Stripe does not charge for API calls",
	}
}

// get calls Stripe and decodes the answer into out.
func get(ctx context.Context, cl *c.Client, creds c.Creds, path string, q url.Values, out any) error {
	u := cl.Base("stripe", stripeAPI) + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+c.Token(creds))
	req.Header.Set("Stripe-Version", stripeVersion)
	status, err := c.DoJSON(ctx, cl, "Stripe", req, out)
	if err != nil && (status == 400 || status == 404) && strings.Contains(err.Error(), "resource_missing") {
		return errMissing
	}
	return err
}

var errMissing = errors.New("stripe: no such object")

func (Stripe) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var list struct {
		Data []struct {
			Livemode bool `json:"livemode"`
		} `json:"data"`
	}
	if err := get(ctx, cl, creds, "/v1/customers", url.Values{"limit": {"1"}}, &list); err != nil {
		return c.Account{}, err
	}
	key := c.Token(creds)
	mode := "live"
	// A signed-in token doesn't always say its mode; the data does.
	if strings.Contains(key, "_test_") || (len(list.Data) > 0 && !list.Data[0].Livemode) {
		mode = "test"
	}
	acct := c.Account{Name: "Stripe", Details: map[string]string{"mode": mode}}
	// A restricted key may not read the account itself; the name is a
	// nicety, so a refusal is fine.
	var a struct {
		ID       string `json:"id"`
		Settings struct {
			Dashboard struct {
				DisplayName string `json:"display_name"`
			} `json:"dashboard"`
		} `json:"settings"`
		BusinessProfile struct {
			Name string `json:"name"`
		} `json:"business_profile"`
	}
	if get(ctx, cl, creds, "/v1/account", nil, &a) == nil {
		acct.ID = a.ID
		for _, n := range []string{a.Settings.Dashboard.DisplayName, a.BusinessProfile.Name} {
			if n != "" {
				acct.Name = n
				break
			}
		}
	}
	if mode == "test" {
		acct.Name += " (test mode)"
	}
	return acct, nil
}

// ---- Stripe objects ----

type stripeCustomer struct {
	ID       string            `json:"id"`
	Email    string            `json:"email"`
	Name     string            `json:"name"`
	Metadata map[string]string `json:"metadata"`
	Created  int64             `json:"created"`
	Deleted  bool              `json:"deleted"`
}

func (s stripeCustomer) out() c.PaymentCustomer {
	uid := ""
	for _, k := range []string{"user_id", "userId", "userid"} {
		if v := strings.TrimSpace(s.Metadata[k]); v != "" {
			uid = v
			break
		}
	}
	return c.PaymentCustomer{ID: s.ID, Email: strings.TrimSpace(s.Email), Name: strings.TrimSpace(s.Name), UserID: uid,
		Created: unix(s.Created), Deleted: s.Deleted}
}

// ref is a field Stripe sends as an id, or as the object when expanded.
type ref string

func (r *ref) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		err := json.Unmarshal(b, &s)
		*r = ref(s)
		return err
	}
	var o struct {
		ID string `json:"id"`
	}
	err := json.Unmarshal(b, &o)
	*r = ref(o.ID)
	return err
}

type stripePrice struct {
	ID         string `json:"id"`
	Product    ref    `json:"product"`
	Currency   string `json:"currency"`
	UnitAmount *int64 `json:"unit_amount"`
	Recurring  *struct {
		Interval      string `json:"interval"`
		IntervalCount int64  `json:"interval_count"`
	} `json:"recurring"`
}

type stripeSubscription struct {
	ID         string `json:"id"`
	Customer   ref    `json:"customer"`
	Status     string `json:"status"`
	Created    int64  `json:"created"`
	CanceledAt *int64 `json:"canceled_at"`
	EndedAt    *int64 `json:"ended_at"`
	Items      struct {
		Data []struct {
			Price    stripePrice `json:"price"`
			Quantity *int64      `json:"quantity"`
		} `json:"data"`
	} `json:"items"`
}

// monthly is what an item brings in a month, in minor units.
func monthly(amount int64, interval string, count int64) int64 {
	if count < 1 {
		count = 1
	}
	a := float64(amount) / float64(count)
	switch interval {
	case "day":
		a *= 365.0 / 12
	case "week":
		a *= 52.0 / 12
	case "year":
		a /= 12
	}
	return int64(math.Round(a))
}

var subStatus = map[string]string{
	"trialing": "trialing", "active": "active", "past_due": "past_due", "unpaid": "past_due",
	"canceled": "canceled", "incomplete_expired": "canceled",
}

func (s stripeSubscription) out(names func(string) string) c.PaymentSubscription {
	o := c.PaymentSubscription{ID: s.ID, CustomerID: string(s.Customer), Status: subStatus[s.Status], Created: unix(s.Created)}
	if o.Status == "" {
		o.Status = "other"
	}
	var top int64 = -1
	for _, it := range s.Items.Data {
		p := it.Price
		if p.Recurring == nil {
			continue
		}
		q := int64(1)
		if it.Quantity != nil {
			q = *it.Quantity
		}
		var amount int64
		if p.UnitAmount != nil {
			amount = *p.UnitAmount * q
		}
		m := monthly(amount, p.Recurring.Interval, p.Recurring.IntervalCount)
		cur := strings.ToUpper(p.Currency)
		if o.Currency == "" {
			o.Currency = cur
		}
		if cur == o.Currency {
			o.MonthlyMinor += m
		}
		// The plan is the item that brings in the most.
		if m > top {
			top = m
			o.ProductID, o.PriceID, o.Interval = string(p.Product), p.ID, p.Recurring.Interval
		}
	}
	if o.ProductID != "" && names != nil {
		o.Plan = names(o.ProductID)
	}
	if t := s.CanceledAt; t != nil {
		at := unix(*t)
		o.CanceledAt = &at
	} else if t := s.EndedAt; t != nil && o.Status == "canceled" {
		at := unix(*t)
		o.CanceledAt = &at
	}
	return o
}

type stripeCharge struct {
	ID       string `json:"id"`
	Customer ref    `json:"customer"`
	Invoice  ref    `json:"invoice"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
	Created  int64  `json:"created"`
	Status   string `json:"status"`
	Paid     bool   `json:"paid"`
}

func (s stripeCharge) ok() bool { return s.Status == "succeeded" && s.Paid }

func (s stripeCharge) out() c.PaymentCharge {
	return c.PaymentCharge{ID: s.ID, CustomerID: string(s.Customer), InvoiceID: string(s.Invoice), AmountMinor: s.Amount,
		Currency: strings.ToUpper(s.Currency), Created: unix(s.Created)}
}

type stripeRefund struct {
	ID       string          `json:"id"`
	Charge   json.RawMessage `json:"charge"`
	Amount   int64           `json:"amount"`
	Currency string          `json:"currency"`
	Created  int64           `json:"created"`
	Status   string          `json:"status"`
}

func (s stripeRefund) ok() bool { return s.Status == "succeeded" || s.Status == "pending" }

func (s stripeRefund) out(customer string) c.PaymentRefund {
	var id ref
	_ = json.Unmarshal(s.Charge, &id)
	if customer == "" {
		var ch struct {
			Customer ref `json:"customer"`
		}
		_ = json.Unmarshal(s.Charge, &ch)
		customer = string(ch.Customer)
	}
	return c.PaymentRefund{ID: s.ID, ChargeID: string(id), CustomerID: customer, AmountMinor: s.Amount,
		Currency: strings.ToUpper(s.Currency), Created: unix(s.Created)}
}

func unix(s int64) time.Time { return time.Unix(s, 0).UTC() }

// products looks up product names once per call.
type products struct {
	ctx   context.Context
	cl    *c.Client
	creds c.Creds
	names map[string]string
}

func (p *products) name(id string) string {
	if n, ok := p.names[id]; ok {
		return n
	}
	var prod struct {
		Name string `json:"name"`
	}
	if err := get(p.ctx, p.cl, p.creds, "/v1/products/"+url.PathEscape(id), nil, &prod); err != nil {
		prod.Name = ""
	}
	p.names[id] = prod.Name
	return prod.Name
}

// ---- Lists ----

func (Stripe) PaymentList(ctx context.Context, cl *c.Client, creds c.Creds, kind, cursor string) (c.PaymentPage, error) {
	q := url.Values{"limit": {strconv.Itoa(stripePage)}}
	if cursor != "" {
		q.Set("starting_after", cursor)
	}
	var page c.PaymentPage
	var resp struct {
		Data    json.RawMessage `json:"data"`
		HasMore bool            `json:"has_more"`
	}
	var path string
	switch kind {
	case c.PayCustomers:
		path = "/v1/customers"
	case c.PaySubscriptions:
		path = "/v1/subscriptions"
		q.Set("status", "all")
	case c.PayCharges:
		path = "/v1/charges"
	case c.PayRefunds:
		path = "/v1/refunds"
		q.Add("expand[]", "data.charge")
	default:
		return page, c.Errf(c.ErrInvalid, "Stripe has no list of %s", kind)
	}
	if err := get(ctx, cl, creds, path, q, &resp); err != nil {
		return page, err
	}
	page.More = resp.HasMore
	var ids []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(resp.Data, &ids); err != nil {
		return page, c.Errf(c.ErrUnavailable, "Stripe returned a list Outflare could not read")
	}
	if len(ids) > 0 {
		page.Cursor = ids[len(ids)-1].ID
	}
	p := &products{ctx: ctx, cl: cl, creds: creds, names: map[string]string{}}
	switch kind {
	case c.PayCustomers:
		var rows []stripeCustomer
		_ = json.Unmarshal(resp.Data, &rows)
		for _, r := range rows {
			page.Customers = append(page.Customers, r.out())
		}
	case c.PaySubscriptions:
		var rows []stripeSubscription
		_ = json.Unmarshal(resp.Data, &rows)
		for _, r := range rows {
			page.Subscriptions = append(page.Subscriptions, r.out(p.name))
		}
	case c.PayCharges:
		var rows []stripeCharge
		_ = json.Unmarshal(resp.Data, &rows)
		for _, r := range rows {
			if r.ok() {
				page.Charges = append(page.Charges, r.out())
			}
		}
	case c.PayRefunds:
		var rows []stripeRefund
		_ = json.Unmarshal(resp.Data, &rows)
		for _, r := range rows {
			if r.ok() {
				page.Refunds = append(page.Refunds, r.out(""))
			}
		}
	}
	return page, nil
}

func (Stripe) PaymentCustomer(ctx context.Context, cl *c.Client, creds c.Creds, id string) (c.PaymentCustomer, error) {
	var s stripeCustomer
	if err := get(ctx, cl, creds, "/v1/customers/"+url.PathEscape(id), nil, &s); err != nil {
		if errors.Is(err, errMissing) {
			return c.PaymentCustomer{ID: id, Deleted: true}, nil
		}
		return c.PaymentCustomer{}, err
	}
	return s.out(), nil
}

// ---- Changes ----

// The event types a sync reads.
var stripeEvents = []string{
	"customer.created", "customer.updated", "customer.deleted",
	"customer.subscription.created", "customer.subscription.updated", "customer.subscription.deleted",
	"charge.succeeded", "charge.refunded",
}

type stripeEvent struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Created int64  `json:"created"`
	Data    struct {
		Object json.RawMessage `json:"object"`
	} `json:"data"`
}

// The cursor is the last event read ("evt_..."), or, before the account has
// any event, "t:" and the time to read from.
func (s Stripe) PaymentChanges(ctx context.Context, cl *c.Client, creds c.Creds, cursor string) (c.PaymentPage, error) {
	q := url.Values{"limit": {strconv.Itoa(stripePage)}}
	for _, t := range stripeEvents {
		q.Add("types[]", t)
	}
	var resp struct {
		Data    []stripeEvent `json:"data"`
		HasMore bool          `json:"has_more"`
	}
	switch {
	case cursor == "":
		// Where to start: the newest event there is, or now.
		if err := get(ctx, cl, creds, "/v1/events", url.Values{"limit": {"1"}}, &resp); err != nil {
			return c.PaymentPage{}, err
		}
		if len(resp.Data) > 0 {
			return c.PaymentPage{Cursor: resp.Data[0].ID}, nil
		}
		return c.PaymentPage{Cursor: "t:" + strconv.FormatInt(time.Now().Unix(), 10)}, nil
	case strings.HasPrefix(cursor, "t:"):
		// Everything since the time, newest first, then turned around.
		q.Set("created[gte]", strings.TrimPrefix(cursor, "t:"))
		var events []stripeEvent
		for i := 0; i < stripeTimeWindow; i++ {
			if err := get(ctx, cl, creds, "/v1/events", q, &resp); err != nil {
				return c.PaymentPage{}, err
			}
			events = append(events, resp.Data...)
			if !resp.HasMore || len(resp.Data) == 0 {
				break
			}
			q.Set("starting_after", resp.Data[len(resp.Data)-1].ID)
		}
		if len(events) == 0 {
			return c.PaymentPage{Cursor: cursor}, nil
		}
		slices.Reverse(events)
		return s.apply(ctx, cl, creds, events, events[len(events)-1].ID, false)
	default:
		// The events just after the cursor: Stripe lists them newest first,
		// with has_more when there are newer ones still.
		q.Set("ending_before", cursor)
		if err := get(ctx, cl, creds, "/v1/events", q, &resp); err != nil {
			if errors.Is(err, errMissing) {
				return c.PaymentPage{}, c.ErrPaymentCursor
			}
			return c.PaymentPage{}, err
		}
		if len(resp.Data) == 0 {
			return c.PaymentPage{Cursor: cursor}, nil
		}
		next := resp.Data[0].ID
		slices.Reverse(resp.Data)
		return s.apply(ctx, cl, creds, resp.Data, next, resp.HasMore)
	}
}

// apply turns events, oldest first, into a page of what they changed. The
// latest copy of each object wins.
func (Stripe) apply(ctx context.Context, cl *c.Client, creds c.Creds, events []stripeEvent, cursor string, more bool) (c.PaymentPage, error) {
	page := c.PaymentPage{Cursor: cursor, More: more}
	p := &products{ctx: ctx, cl: cl, creds: creds, names: map[string]string{}}
	customers, subs := map[string]int{}, map[string]int{}
	for _, ev := range events {
		obj := ev.Data.Object
		switch {
		case strings.HasPrefix(ev.Type, "customer.subscription."):
			var s stripeSubscription
			if json.Unmarshal(obj, &s) != nil {
				continue
			}
			o := s.out(p.name)
			if i, ok := subs[o.ID]; ok {
				page.Subscriptions[i] = o
			} else {
				subs[o.ID] = len(page.Subscriptions)
				page.Subscriptions = append(page.Subscriptions, o)
			}
		case strings.HasPrefix(ev.Type, "customer."):
			var s stripeCustomer
			if json.Unmarshal(obj, &s) != nil {
				continue
			}
			o := s.out()
			o.Deleted = o.Deleted || ev.Type == "customer.deleted"
			if i, ok := customers[o.ID]; ok {
				page.Customers[i] = o
			} else {
				customers[o.ID] = len(page.Customers)
				page.Customers = append(page.Customers, o)
			}
		case ev.Type == "charge.succeeded":
			var s stripeCharge
			if json.Unmarshal(obj, &s) == nil && s.ok() {
				page.Charges = append(page.Charges, s.out())
			}
		case ev.Type == "charge.refunded":
			var s stripeCharge
			if json.Unmarshal(obj, &s) != nil {
				continue
			}
			var resp struct {
				Data []stripeRefund `json:"data"`
			}
			if err := get(ctx, cl, creds, "/v1/refunds", url.Values{"charge": {s.ID}, "limit": {strconv.Itoa(stripePage)}}, &resp); err != nil {
				return c.PaymentPage{}, err
			}
			for _, r := range resp.Data {
				if r.ok() {
					page.Refunds = append(page.Refunds, r.out(string(s.Customer)))
				}
			}
		}
	}
	return page, nil
}
