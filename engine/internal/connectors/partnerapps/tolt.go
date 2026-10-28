// Package partnerapps connects the partner platforms a workspace may move
// its referral or affiliate program from. They are only read.
package partnerapps

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const toltAPI = "https://api.tolt.com/v1"

// toltPage is how many objects a list call asks for (Tolt's maximum).
const toltPage = 100

// All returns the partner platform connectors.
func All() []c.Connector { return []c.Connector{Tolt{}} }

// Tolt reads a Tolt account with the workspace's own API key, to move its
// program into Outflare. Field names follow Tolt's v1 API; where Tolt
// has used more than one name, each is tried.
type Tolt struct{}

func (Tolt) Definition() c.Definition {
	return c.Definition{
		ID: "tolt", Name: "Tolt", Category: c.CategoryPartners,
		Summary: "Move your referral or affiliate program over from Tolt.",
		Description: "Reads your Tolt program: partners with their link codes and payout details, promo codes, referred customers, " +
			"transactions and commissions, paid or not. Run the import as often as you like until the day you switch: each run " +
			"brings only what changed. Outflare never changes anything in Tolt.",
		Website: "https://tolt.com", DocsURL: "https://docs.tolt.com",
		Auth: c.AuthAPIKey,
		Fields: []c.Field{{Name: "api_key", Label: "API key", Secret: true, Placeholder: "tlt_live_...",
			Help: "In Tolt: Settings, Integrations, API key."}},
		Capabilities: []string{c.CapPartnersImport},
		Pricing:      "Free: Tolt does not charge for API calls",
	}
}

func (Tolt) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	progs, err := Tolt{}.PartnerPrograms(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	a := c.Account{Details: map[string]string{"programs": strconv.Itoa(len(progs))}}
	if len(progs) == 1 {
		a.ID, a.Name = progs[0].ID, progs[0].Name
	}
	return a, nil
}

type toltList struct {
	Data    []map[string]any `json:"data"`
	HasMore *bool            `json:"has_more"`
}

func (Tolt) get(ctx context.Context, cl *c.Client, creds c.Creds, path string, q url.Values) (toltList, error) {
	u := cl.Base("tolt", toltAPI) + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+c.Token(creds))
	var out toltList
	_, err := c.DoJSON(ctx, cl, "Tolt", req, &out)
	return out, err
}

func (t Tolt) PartnerPrograms(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.PartnerProgram, error) {
	l, err := t.get(ctx, cl, creds, "/programs", nil)
	if err != nil {
		return nil, err
	}
	out := make([]c.PartnerProgram, 0, len(l.Data))
	for _, p := range l.Data {
		out = append(out, c.PartnerProgram{ID: str(p, "id"), Name: str(p, "name"), Currency: strings.ToUpper(str(p, "currency"))})
	}
	return out, nil
}

var toltPaths = map[string]string{
	c.PartnerKindPartners: "/partners", c.PartnerKindLinks: "/links", c.PartnerKindPromoCodes: "/promotion-codes",
	c.PartnerKindCustomers: "/customers", c.PartnerKindTransactions: "/transactions", c.PartnerKindCommissions: "/commissions",
}

func (t Tolt) PartnerPage(ctx context.Context, cl *c.Client, creds c.Creds, program, kind, after string) (c.PartnerObjects, string, error) {
	var page c.PartnerObjects
	path, ok := toltPaths[kind]
	if !ok {
		return page, "", c.Errf(c.ErrInvalid, "Tolt has no %s", kind)
	}
	q := url.Values{"program_id": {program}, "limit": {strconv.Itoa(toltPage)}}
	if after != "" {
		q.Set("starting_after", after)
	}
	l, err := t.get(ctx, cl, creds, path, q)
	if err != nil {
		return page, "", err
	}
	for _, o := range l.Data {
		switch kind {
		case c.PartnerKindPartners:
			page.Partners = append(page.Partners, toltPartner(o))
		case c.PartnerKindLinks:
			page.Links = append(page.Links, c.ImportLink{ID: str(o, "id"), PartnerID: ref(o, "partner_id", "partner"),
				Param: str(o, "param"), Value: str(o, "value"), Created: when(o, "created_at", "created")})
		case c.PartnerKindPromoCodes:
			page.PromoCodes = append(page.PromoCodes, c.ImportPromoCode{ID: str(o, "id"), PartnerID: ref(o, "partner_id", "partner"),
				Code: str(o, "code", "name")})
		case c.PartnerKindCustomers:
			page.Customers = append(page.Customers, c.ImportCustomer{ID: str(o, "id"), PartnerID: ref(o, "partner_id", "partner"),
				Email: str(o, "email"), CustomerID: str(o, "customer_id", "external_id"), Created: when(o, "created_at", "created")})
		case c.PartnerKindTransactions:
			page.Transactions = append(page.Transactions, toltTransaction(o))
		case c.PartnerKindCommissions:
			page.Commissions = append(page.Commissions, toltCommission(o))
		}
	}
	next := ""
	more := len(l.Data) == toltPage
	if l.HasMore != nil {
		more = *l.HasMore
	}
	if more && len(l.Data) > 0 {
		next = str(l.Data[len(l.Data)-1], "id")
	}
	return page, next, nil
}

func toltPartner(o map[string]any) c.ImportPartner {
	name := strings.TrimSpace(str(o, "first_name") + " " + str(o, "last_name"))
	if name == "" {
		name = str(o, "name")
	}
	status := "suspended"
	switch strings.ToLower(str(o, "status")) {
	case "active", "approved", "":
		status = "active"
	case "pending", "applied", "invited":
		status = "pending"
	case "rejected", "declined", "denied":
		status = "rejected"
	}
	payout := str(o, "payout_method")
	if d := str(o, "payout_details", "paypal_email", "payout_email", "wise_email"); d != "" {
		if payout != "" {
			payout += ": "
		}
		payout += d
	}
	return c.ImportPartner{ID: str(o, "id"), Email: str(o, "email"), Name: name, Status: status, PayoutMethod: payout,
		Created: when(o, "created_at", "created")}
}

func toltTransaction(o map[string]any) c.ImportTransaction {
	t := c.ImportTransaction{ID: str(o, "id"), CustomerID: ref(o, "customer_id", "customer"), AmountCents: cents(o, "amount", "revenue"),
		Currency: strings.ToUpper(str(o, "currency")), Created: when(o, "created_at", "created")}
	bt := strings.ToLower(str(o, "billing_type", "type"))
	t.Recurring = bt == "subscription" || bt == "recurring" || bt == "renewal"
	t.RefundedCents = cents(o, "refunded_amount", "amount_refunded")
	if t.RefundedCents == 0 && (strings.EqualFold(str(o, "status"), "refunded") || truthy(o["refunded"])) {
		t.RefundedCents = t.AmountCents
	}
	if t.RefundedCents > 0 {
		t.RefundedAt = when(o, "refunded_at", "updated_at")
	}
	return t
}

func toltCommission(o map[string]any) c.ImportCommission {
	status := "pending"
	switch strings.ToLower(str(o, "status")) {
	case "approved":
		status = "approved"
	case "paid":
		status = "paid"
	case "rejected", "declined", "refunded", "canceled", "cancelled", "void":
		status = "rejected"
	}
	return c.ImportCommission{ID: str(o, "id"), PartnerID: ref(o, "partner_id", "partner"), CustomerID: ref(o, "customer_id", "customer"),
		TransactionID: ref(o, "transaction_id", "transaction"), AmountCents: cents(o, "amount"), Currency: strings.ToUpper(str(o, "currency")),
		Status: status, Created: when(o, "created_at", "created"), PaidAt: when(o, "paid_at"), PayoutID: ref(o, "payout_id", "payout")}
}

// str returns the first of keys that holds a non-empty string or number.
func str(o map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := o[k].(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		}
	}
	return ""
}

// ref is an id that may come as a string or, expanded, as an object.
func ref(o map[string]any, keys ...string) string {
	for _, k := range keys {
		if m, ok := o[k].(map[string]any); ok {
			if id := str(m, "id"); id != "" {
				return id
			}
		}
		if s := str(o, k); s != "" {
			return s
		}
	}
	return ""
}

// cents reads an amount in cents, as a number or a numeric string.
func cents(o map[string]any, keys ...string) int64 {
	for _, k := range keys {
		switch v := o[k].(type) {
		case float64:
			return int64(math.Round(v))
		case string:
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				return int64(math.Round(f))
			}
		case json.Number:
			if f, err := v.Float64(); err == nil {
				return int64(math.Round(f))
			}
		}
	}
	return 0
}

// when reads a time given as RFC 3339 or as Unix seconds or milliseconds.
func when(o map[string]any, keys ...string) time.Time {
	for _, k := range keys {
		switch v := o[k].(type) {
		case string:
			if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
				return t.UTC()
			}
			if t, err := time.Parse("2006-01-02 15:04:05", v); err == nil {
				return t.UTC()
			}
		case float64:
			if v > 1e12 {
				return time.UnixMilli(int64(v)).UTC()
			}
			if v > 0 {
				return time.Unix(int64(v), 0).UTC()
			}
		}
	}
	return time.Time{}
}

func truthy(v any) bool {
	b, ok := v.(bool)
	return ok && b
}
