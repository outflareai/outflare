// Package partnerapps connects the partner platforms a workspace may move
// its referral or affiliate program from. They are only read.
package partnerapps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/partners"
)

// toltAPI is Tolt's v2 API. v1 (api.tolt.com/v1) has no customers list
// in this form and answers it with something that isn't JSON.
const toltAPI = "https://platform.tolt.com/v2"

// toltPage is how many objects a list call asks for (Tolt's maximum).
const toltPage = 100

// All returns the partner platform connectors.
func All() []c.Connector { return []c.Connector{Tolt{}} }

// Tolt reads a Tolt account with the workspace's own API key, to move its
// program into Outflare. Field names follow Tolt's v2 API; where Tolt
// has used more than one name, each is tried.
type Tolt struct{}

func (Tolt) Definition() c.Definition {
	return c.Definition{
		ID: "tolt", Name: "Tolt", Category: c.CategoryPartners,
		Summary: "Move your referral or affiliate program over from Tolt.",
		Description: "Reads your Tolt program: partners of every status with their link codes, promo codes, referred customers, " +
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
	Data    []map[string]any
	HasMore *bool
	Total   *int
	// Next is the cursor to the next page (cur_...), when Tolt sends one.
	Next string
}

// toltRetries is how often a rate-limited call is tried again, and
// toltWait the longest it waits before each (Tolt allows 25 calls a second
// per key and says how long to wait in Retry-After).
var (
	toltRetries = 3
	toltWait    = 10 * time.Second
)

// get reads one Tolt list. what names it in errors and the log ("customers").
func (t Tolt) get(ctx context.Context, cl *c.Client, creds c.Creds, what, path string, q url.Values) (toltList, error) {
	status, ctype, body, err := t.fetch(ctx, cl, creds, what, path, q)
	if err != nil {
		return toltList{}, err
	}
	return decodeToltList(what, status, ctype, body)
}

// fetch makes one GET, waiting and trying again while Tolt says it is rate
// limited. A status other than 2xx is an error naming what was read.
func (Tolt) fetch(ctx context.Context, cl *c.Client, creds c.Creds, what, path string, q url.Values) (int, string, []byte, error) {
	u := cl.Base("tolt", toltAPI) + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var resp *http.Response
	var body []byte
	for try := 0; ; try++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		req.Header.Set("Authorization", "Bearer "+c.Token(creds))
		req.Header.Set("Accept", "application/json")
		var err error
		if resp, err = cl.Do(req); err != nil {
			return 0, "", nil, c.Errf(c.ErrUnavailable, "reading %s from Tolt: could not reach Tolt: %v", what, err)
		}
		body, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			return 0, "", nil, c.Errf(c.ErrUnavailable, "reading %s from Tolt: %v", what, err)
		}
		if resp.StatusCode != http.StatusTooManyRequests || try >= toltRetries {
			break
		}
		wait := time.Duration(1<<try) * time.Second
		if s, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && s >= 0 {
			wait = time.Duration(s) * time.Second
		}
		select {
		case <-ctx.Done():
			return 0, "", nil, ctx.Err()
		case <-time.After(min(wait, toltWait)):
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		e := c.HTTPError("Tolt", resp.StatusCode, string(body))
		e.Message = "reading " + what + " from Tolt: " + e.Message
		return resp.StatusCode, "", nil, e
	}
	return resp.StatusCode, resp.Header.Get("Content-Type"), body, nil
}

// PartnerByID reads one partner, for one that other objects name but the
// partner list left out (Tolt lists onboarded partners only).
func (t Tolt) PartnerByID(ctx context.Context, cl *c.Client, creds c.Creds, program, id string) (c.ImportPartner, error) {
	status, ctype, body, err := t.fetch(ctx, cl, creds, "a partner", "/partners/"+url.PathEscape(id), url.Values{"program_id": {program}})
	if err != nil && c.KindOf(err) == c.ErrInvalid {
		// Gone, or refused for this one partner: the status only, never
		// the answer's text.
		return c.ImportPartner{}, &c.UnreadableError{Message: fmt.Sprintf("Tolt answered HTTP %d for this partner", status)}
	}
	if err != nil {
		return c.ImportPartner{}, err
	}
	var v map[string]any
	if json.Unmarshal(body, &v) != nil {
		return c.ImportPartner{}, &c.UnreadableError{Message: fmt.Sprintf("reading a partner from Tolt (HTTP %d, %s): the answer is not JSON", status, ctype)}
	}
	// {"success": true, "data": {...}}, or the partner itself.
	o := v
	if d, ok := v["data"].(map[string]any); ok {
		o = d
	}
	if str(o, "id") == "" {
		return c.ImportPartner{}, &c.UnreadableError{Message: fmt.Sprintf("reading a partner from Tolt (HTTP %d): no partner in the answer (%s)", status, toltShape(any(v)))}
	}
	return toltPartner(o), nil
}

// decodeToltList reads a list however Tolt wraps it. Tolt has answered
// {"data": [...], "has_more": true} and {"success": true, "data": {"data":
// [...]}, "has_more": "true", "total_count": 120}; a bare array works too.
// An answer that isn't JSON, or holds no list, is a *c.UnreadableError
// naming the kind, the status and the content type, with the answer's
// top-level keys and their types (never values). The log gets the same,
// and the start of an answer that isn't JSON, with addresses taken out.
func decodeToltList(what string, status int, ctype string, body []byte) (toltList, error) {
	var out toltList
	ctype = strings.TrimSpace(strings.SplitN(ctype, ";", 2)[0])
	if ctype == "" {
		ctype = "no content type"
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		slog.Warn("tolt answer is not JSON", "kind", what, "status", status, "content_type", ctype, "start", toltSnippet(body))
		return out, &c.UnreadableError{Message: fmt.Sprintf("reading %s from Tolt (HTTP %d, %s): the answer is not JSON", what, status, ctype)}
	}
	list, top, ok := toltItems(v)
	if !ok {
		shape := toltShape(v)
		slog.Warn("tolt answer holds no list", "kind", what, "status", status, "content_type", ctype, "shape", shape)
		msg := fmt.Sprintf("reading %s from Tolt (HTTP %d, %s): the answer holds no list (%s)", what, status, ctype, shape)
		if m, ok := v.(map[string]any); ok && m["success"] == false {
			if said := str(m, "message", "error"); said != "" {
				msg += ": Tolt says " + cutText(said, 200)
			}
		}
		return out, &c.UnreadableError{Message: msg}
	}
	for _, it := range list {
		if o, ok := it.(map[string]any); ok {
			out.Data = append(out.Data, o)
		}
	}
	for _, m := range top {
		if b, ok := flag(m["has_more"]); ok && out.HasMore == nil {
			out.HasMore = &b
		}
		if n, ok := count(m["total_count"]); ok && out.Total == nil {
			out.Total = &n
		}
		if s, ok := m["next_cursor"].(string); ok && out.Next == "" {
			out.Next = strings.TrimSpace(s)
		}
	}
	return out, nil
}

// toltItems finds the list in an answer, and the objects around it that
// may say whether more follow (outermost first).
func toltItems(v any) ([]any, []map[string]any, bool) {
	switch v := v.(type) {
	case []any:
		return v, nil, true
	case map[string]any:
		for _, k := range []string{"data", "results", "items"} {
			if l, ok := v[k].([]any); ok {
				return l, []map[string]any{v}, true
			}
		}
		// An empty list may come as "data": null.
		if d, ok := v["data"]; ok && d == nil {
			return nil, []map[string]any{v}, true
		}
		for _, k := range []string{"data", "results", "items"} {
			if inner, ok := v[k].(map[string]any); ok {
				if l, top, ok := toltItems(inner); ok {
					return l, append([]map[string]any{v}, top...), true
				}
			}
		}
		// A list under the kind's own name ({"customers": [...]}).
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if l, ok := v[k].([]any); ok {
				return l, []map[string]any{v}, true
			}
		}
	}
	return nil, nil, false
}

// toltShape describes an answer by its top-level keys and their types.
func toltShape(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return "a " + jsonType(v)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, cutText(k, 40)+": "+jsonType(m[k]))
	}
	if len(parts) == 0 {
		return "an empty object"
	}
	if len(parts) > 12 {
		parts = append(parts[:12], "…")
	}
	return "keys " + strings.Join(parts, ", ")
}

func jsonType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	}
	return "object"
}

var emailLike = regexp.MustCompile(`[^\s"'<>@]+@[^\s"'<>@]+`)

// toltSnippet is the start of an answer that isn't JSON, for the log, with
// anything that looks like an address taken out.
func toltSnippet(body []byte) string {
	return emailLike.ReplaceAllString(cutText(string(body), 200), "[address]")
}

func cutText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// flag reads a boolean sent as true, "true" or 1.
func flag(v any) (bool, bool) {
	switch v := v.(type) {
	case bool:
		return v, true
	case string:
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		return b, err == nil
	case float64:
		return v != 0, true
	}
	return false, false
}

func count(v any) (int, bool) {
	switch v := v.(type) {
	case float64:
		return int(v), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		return n, err == nil
	}
	return 0, false
}

func (t Tolt) PartnerPrograms(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.PartnerProgram, error) {
	l, err := t.get(ctx, cl, creds, "programs", "/programs", nil)
	if err != nil {
		return nil, err
	}
	out := make([]c.PartnerProgram, 0, len(l.Data))
	if len(l.Data) > 0 {
		// Which fields Tolt sends for a program (never their values), to
		// see what tells a live program from an old one.
		slog.Info("tolt programs", "count", len(l.Data), "shape", toltShape(any(l.Data[0])))
	}
	for _, p := range l.Data {
		out = append(out, c.PartnerProgram{ID: str(p, "id"), Name: str(p, "name"), Currency: strings.ToUpper(str(p, "currency_code", "currency")),
			Status: toltProgramStatus(p)})
	}
	return out, nil
}

// toltPartnerStatuses are the statuses Tolt's partner list filters by;
// each is read in turn so none is left out. New ones go at the end, since
// an import's saved cursor holds a status's index. From toltOptionalStatus
// on, an account whose list refuses the status just has none of them.
var toltPartnerStatuses = []string{"active", "pending", "rejected", "suspended", "invited"}

const toltOptionalStatus = 4

var toltPaths = map[string]string{
	c.PartnerKindGroups: "/groups", c.PartnerKindFlows: "/flows",
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
	// Partners are read one status at a time, since Tolt's list filters by
	// status: the cursor is "<status index>:<Tolt's cursor>". A cursor with
	// "!" in place of the ":" (or before it, for other kinds) is one asked
	// past where Tolt said the list ended (see below).
	st, probed := 0, false
	if kind == c.PartnerKindPartners {
		if i := strings.IndexAny(after, ":!"); i > 0 {
			if n, err := strconv.Atoi(after[:i]); err == nil && n >= 0 && n < len(toltPartnerStatuses) {
				st, probed, after = n, after[i] == '!', after[i+1:]
			}
		}
		q.Set("status", toltPartnerStatuses[st])
	} else if rest, ok := strings.CutPrefix(after, "!"); ok {
		probed, after = true, rest
	}
	if after != "" {
		q.Set("starting_after", after)
	}
	what := strings.ReplaceAll(kind, "_", " ")
	status, ctype, body, err := t.fetch(ctx, cl, creds, what, path, q)
	if status == http.StatusNotFound && !probed && (kind == c.PartnerKindGroups || kind == c.PartnerKindFlows) {
		// Not every Tolt account or plan lists these.
		return page, "", c.ErrPartnerKindUnavailable
	}
	var l toltList
	if err == nil {
		l, err = decodeToltList(what, status, ctype, body)
	}
	refused := err != nil && (c.KindOf(err) == c.ErrInvalid || errors.As(err, new(*c.UnreadableError)))
	if refused && kind == c.PartnerKindPartners && st >= toltOptionalStatus && after == "" {
		// Tolt doesn't filter by this status here: there are none to read.
		slog.Info("tolt list refused a status", "status", toltPartnerStatuses[st])
		if st+1 < len(toltPartnerStatuses) {
			return page, strconv.Itoa(st+1) + ":", nil
		}
		return page, "", nil
	}
	if refused && probed {
		// Tolt refused the extra ask: the list did end there.
		if kind == c.PartnerKindPartners && st+1 < len(toltPartnerStatuses) {
			return page, strconv.Itoa(st+1) + ":", nil
		}
		return page, "", nil
	}
	if err != nil {
		return page, "", err
	}
	if after == "" && l.Total != nil {
		n := *l.Total
		page.Reported, page.ReportedFor = &n, q.Get("status")
		// How many Tolt says there are, to compare with what an import read.
		slog.Info("tolt list", "kind", kind, "status", q.Get("status"), "total_count", *l.Total, "first_page", len(l.Data), "has_more", l.HasMore != nil && *l.HasMore)
	}
	for _, o := range l.Data {
		switch kind {
		case c.PartnerKindGroups:
			page.Groups = append(page.Groups, c.ImportGroup{ID: str(o, "id"), Name: str(o, "name"), Default: truthy(o["is_default"]) || truthy(o["default"])})
		case c.PartnerKindFlows:
			page.Flows = append(page.Flows, toltFlow(o))
		case c.PartnerKindPartners:
			page.Partners = append(page.Partners, toltPartner(o))
		case c.PartnerKindLinks:
			page.Links = append(page.Links, c.ImportLink{ID: str(o, "id"), PartnerID: ref(o, "partner_id", "partner"),
				Param: str(o, "param"), Value: str(o, "value"), Created: when(o, "created_at", "created")})
		case c.PartnerKindPromoCodes:
			pc := c.ImportPromoCode{ID: str(o, "id"), PartnerID: ref(o, "partner_id", "partner"), Code: str(o, "code", "name")}
			if pc.PartnerID == "" {
				pc.Keys = toltShape(any(o))
			}
			page.PromoCodes = append(page.PromoCodes, pc)
		case c.PartnerKindCustomers:
			page.Customers = append(page.Customers, c.ImportCustomer{ID: str(o, "id"), PartnerID: ref(o, "partner_id", "partner"),
				Email: str(o, "email"), CustomerID: str(o, "customer_id", "external_id"), Created: when(o, "lead_at", "created_at", "created"),
				// Tolt's own id (cus_...) is what tolt.signup() returns, so
				// backends often store it and report renewals with it.
				Aliases: nonEmpty(str(o, "id"))})
		case c.PartnerKindTransactions:
			page.Transactions = append(page.Transactions, toltTransaction(o))
		case c.PartnerKindCommissions:
			page.Commissions = append(page.Commissions, toltCommission(o))
		}
	}
	// v2 pages with next_cursor (cur_...) until has_more is false; an
	// answer without one falls back to the last object's id.
	next := ""
	more := len(l.Data) == toltPage
	if l.HasMore != nil {
		more = *l.HasMore
	}
	if more && len(l.Data) > 0 {
		next = l.Next
		if next == "" {
			next = str(l.Data[len(l.Data)-1], "id")
		}
	}
	// Tolt said there are no more right after a full page. In case its
	// list stops at a cap rather than at the end, ask once more, after the
	// last object's id; a list that really ended answers with nothing.
	if next == "" && !probed && len(l.Data) == toltPage {
		next, probed = str(l.Data[len(l.Data)-1], "id"), true
	}
	sep := ":"
	if probed {
		sep = "!"
	}
	switch {
	case kind != c.PartnerKindPartners:
		if next != "" && probed {
			next = "!" + next
		}
	case next != "":
		next = strconv.Itoa(st) + sep + next
	case st+1 < len(toltPartnerStatuses):
		next = strconv.Itoa(st+1) + ":"
	}
	return page, next, nil
}

// toltProgramStatus is active or inactive, from whichever field Tolt sent.
func toltProgramStatus(p map[string]any) string {
	switch strings.ToLower(str(p, "status", "state")) {
	case "active", "live", "published", "enabled":
		return "active"
	case "":
		if b, ok := flag(p["is_active"]); ok {
			if b {
				return "active"
			}
			return "inactive"
		}
		return ""
	}
	return "inactive"
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
	// Tolt's v2 API has no payout details: partners give theirs on the
	// portal.
	return c.ImportPartner{ID: str(o, "id"), Email: str(o, "email"), Name: name, Status: status,
		GroupID: ref(o, "group_id", "group"), Created: when(o, "created_at", "created")}
}

func toltTransaction(o map[string]any) c.ImportTransaction {
	t := c.ImportTransaction{ID: str(o, "id"), CustomerID: ref(o, "customer_id", "customer"), AmountCents: cents(o, "amount", "revenue"),
		Currency: strings.ToUpper(str(o, "currency", "currency_code")), Created: when(o, "created_at", "created")}
	bt := strings.ToLower(str(o, "billing_type", "type"))
	t.Recurring = bt == "subscription" || bt == "recurring" || bt == "renewal"
	// source is the payment provider in capitals (STRIPE); interval how a
	// subscription bills (month).
	if src := strings.ToLower(str(o, "source")); paymentSourceRE.MatchString(src) {
		t.PaymentSource = src
	}
	switch iv := strings.ToLower(str(o, "interval")); iv {
	case "day", "week", "month", "year":
		t.Interval = iv
	default:
		if bt == "one_time" {
			t.Interval = "one_time"
		}
	}
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
	// A commission in a payout is paid, whatever its status says.
	if status != "rejected" && ref(o, "payout_id", "payout") != "" {
		status = "paid"
	}
	return c.ImportCommission{ID: str(o, "id"), PartnerID: ref(o, "partner_id", "partner"), CustomerID: ref(o, "customer_id", "customer"),
		TransactionID: ref(o, "transaction_id", "transaction"), AmountCents: cents(o, "amount"), Currency: strings.ToUpper(str(o, "currency", "currency_code")),
		Status: status, Created: when(o, "created_at", "created"), PaidAt: when(o, "paid_at"), PayoutID: ref(o, "payout_id", "payout")}
}

// str returns the first of keys that holds a non-empty string or number.
var paymentSourceRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,39}$`)

func nonEmpty(vals ...string) []string {
	out := []string{}
	for _, v := range vals {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

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

// Tolt's flows: a trigger (new payment or new signup), an action (generate
// a commission or switch group), AND/OR conditions on partner, customer
// and transaction attributes, approval, and custom rates per partner or
// plan. Tolt's docs couldn't be read from where this was built, so every
// name here is read under the names Tolt is known or likely to use
// (_check_ in docs/partners-tolt-migration.md), and anything else makes
// the flow Unsupported rather than guessed.

// toltFields maps Tolt condition attributes to Outflare's facts.
var toltFields = map[string]string{
	"partner_id": "partner.id", "partner.id": "partner.id",
	"partner_country": "partner.country", "country": "partner.country", "partner.country": "partner.country",
	"partner_transactions": "partner.transactions", "partner_transactions_count": "partner.transactions", "transactions_count": "partner.transactions",
	"partner_customers": "partner.customers", "partner_customers_count": "partner.customers", "customers_count": "partner.customers",
	"partner_leads": "partner.leads", "partner_leads_count": "partner.leads", "leads_count": "partner.leads",
	"partner_revenue": "partner.revenue_cents", "revenue": "partner.revenue_cents",
	"partner_earnings": "partner.earnings_cents", "earnings": "partner.earnings_cents",
	"customer_transactions": "customer.transactions", "customer_transactions_count": "customer.transactions",
	"customer_total_paid": "customer.total_paid_cents", "customer_revenue": "customer.total_paid_cents",
	"customer_conversion_date": "customer.signed_up_at", "conversion_date": "customer.signed_up_at",
	"customer_first_transaction_date": "customer.first_sale_at", "first_transaction_date": "customer.first_sale_at",
	"transaction_amount": "transaction.amount_cents", "amount": "transaction.amount_cents",
	"product_id": "transaction.product_id", "plan_id": "transaction.product_id", "price_id": "transaction.product_id", "plan": "transaction.product_id",
	"recurring": "transaction.recurring", "is_recurring": "transaction.recurring",
}

var toltOps = map[string]string{
	"equals": "eq", "eq": "eq", "is": "eq", "=": "eq",
	"not_equals": "neq", "neq": "neq", "is_not": "neq", "!=": "neq",
	"greater_than": "gt", "gt": "gt", ">": "gt",
	"greater_than_or_equal": "gte", "greater_than_or_equal_to": "gte", "gte": "gte", ">=": "gte",
	"less_than": "lt", "lt": "lt", "<": "lt",
	"less_than_or_equal": "lte", "less_than_or_equal_to": "lte", "lte": "lte", "<=": "lte",
	"in": "in", "is_one_of": "in", "not_in": "not_in", "is_not_one_of": "not_in",
	"within": "within", "within_last": "within", "in_the_last": "within", "not_within": "not_within",
}

func toltFlow(o map[string]any) c.ImportFlow {
	f := c.ImportFlow{ID: str(o, "id"), GroupID: ref(o, "group_id", "group"), Name: str(o, "name", "title"), Recurrence: "every", Approval: "program"}
	switch strings.ToLower(str(o, "trigger", "trigger_type", "event")) {
	case "new_payment", "payment", "transaction", "new_transaction", "sale", "":
		f.Trigger = "sale"
	case "new_signup", "signup", "customer", "new_customer", "lead":
		f.Trigger = "signup"
	default:
		f.Unsupported = append(f.Unsupported, "trigger")
	}
	action, _ := o["action"].(map[string]any)
	if action == nil {
		action = o
	}
	switch strings.ToLower(str(action, "type", "action_type", "action")) {
	case "switch_group", "change_group", "move_to_group", "group":
		f.Kind, f.TargetGroupID = "group", ref(action, "group_id", "target_group_id", "group")
		if f.TargetGroupID == "" {
			f.Unsupported = append(f.Unsupported, "action")
		}
	default:
		ct := strings.ToLower(str(action, "commission_type", "reward_type", "value_type", "type_of_commission"))
		v := cents(action, "value", "amount", "commission", "rate")
		switch {
		case ct == "percentage" || ct == "percent" || ct == "%":
			bp := int(math.Round(num(action, "value", "amount", "commission", "rate") * 100))
			f.Kind, f.PercentBP = "percent", &bp
		case ct == "fixed" || ct == "flat" || ct == "amount":
			f.Kind, f.AmountCents = "fixed", &v
		default:
			f.Unsupported = append(f.Unsupported, "action")
		}
	}
	switch strings.ToLower(str(o, "duration", "recurrence", "commission_duration")) {
	case "", "lifetime", "every", "forever", "recurring":
	case "first", "one_time", "first_payment", "once":
		f.Recurrence = "first"
	case "months", "limited", "first_months":
		n := int(cents(o, "duration_months", "months", "duration_value"))
		if n > 0 {
			f.Recurrence, f.Months = "months", &n
		} else {
			f.Unsupported = append(f.Unsupported, "duration")
		}
	default:
		f.Unsupported = append(f.Unsupported, "duration")
	}
	approval, _ := o["approval"].(map[string]any)
	if approval == nil {
		approval = map[string]any{"type": o["approval_type"], "days": o["approval_days"]}
	}
	switch strings.ToLower(str(approval, "type")) {
	case "", "default":
	case "auto", "automatic", "instant":
		f.Approval = "auto"
	case "auto_after", "automatic_after", "after_days", "delayed":
		d := int(cents(approval, "days", "delay_days"))
		f.Approval, f.ApprovalDays = "after_days", &d
	case "manual":
		f.Approval = "manual"
	default:
		f.Unsupported = append(f.Unsupported, "approval")
	}
	f.Conditions = toltConditions(o["conditions"], &f.Unsupported)
	for _, x := range list(o["custom_rates"], o["overrides"]) {
		r, _ := x.(map[string]any)
		ov := partners.Override{}
		if id := ref(r, "partner_id", "partner"); id != "" {
			ov.PartnerID = &id
		} else if id := ref(r, "plan_id", "product_id", "price_id", "plan"); id != "" {
			ov.ProductID = &id
		} else {
			f.Unsupported = append(f.Unsupported, "custom_rate")
			continue
		}
		v := cents(r, "value", "amount", "rate", "commission")
		if f.Kind == "percent" {
			bp := int(math.Round(num(r, "value", "amount", "rate", "commission") * 100))
			ov.PercentBP = &bp
		} else {
			ov.AmountCents = &v
		}
		f.Overrides = append(f.Overrides, ov)
	}
	return f
}

// num reads a number, given as a number or as text.
func num(o map[string]any, keys ...string) float64 {
	for _, k := range keys {
		switch v := o[k].(type) {
		case float64:
			return v
		case string:
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				return f
			}
		}
	}
	return 0
}

func list(vs ...any) []any {
	for _, v := range vs {
		if l, ok := v.([]any); ok {
			return l
		}
	}
	return nil
}

func toltMatch(v string) string {
	if strings.EqualFold(v, "or") || strings.EqualFold(v, "any") {
		return "any"
	}
	return "all"
}

// toltConditions reads {operator, groups: [{operator, conditions: [...]}]},
// or a flat list of conditions.
func toltConditions(v any, unsupported *[]string) partners.Conditions {
	var out partners.Conditions
	root, _ := v.(map[string]any)
	groups := list(v)
	if root != nil {
		out.Match = toltMatch(str(root, "operator", "match", "logic"))
		if g := list(root["groups"]); g != nil {
			groups = g
		} else if cs := list(root["conditions"], root["rules"]); cs != nil {
			groups = []any{map[string]any{"operator": str(root, "operator", "match", "logic"), "conditions": cs}}
			out.Match = "all"
		}
	} else if groups != nil {
		groups = []any{map[string]any{"operator": "and", "conditions": groups}}
	}
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		group := partners.ConditionGroup{Match: toltMatch(str(gm, "operator", "match", "logic"))}
		for _, x := range list(gm["conditions"], gm["rules"]) {
			cm, _ := x.(map[string]any)
			field, ok := toltFields[strings.ToLower(str(cm, "attribute", "field", "property"))]
			op, okOp := toltOps[strings.ToLower(str(cm, "operator", "op", "comparison"))]
			if !ok || !okOp {
				*unsupported = append(*unsupported, "condition")
				continue
			}
			cond := partners.Condition{Field: field, Op: op, Value: cm["value"]}
			if op == "within" || op == "not_within" {
				cond.Value = map[string]any{"amount": float64(cents(cm, "value", "amount")), "unit": strings.ToLower(str(cm, "unit", "period"))}
			}
			if s, isText := cond.Value.(string); isText && partners.Fields[field] == partners.FieldNumber {
				if f, err := strconv.ParseFloat(s, 64); err == nil {
					cond.Value = f
				}
			}
			group.Conditions = append(group.Conditions, cond)
		}
		out.Groups = append(out.Groups, group)
	}
	if err := out.Check(); err != nil {
		*unsupported = append(*unsupported, "condition")
	}
	return out
}
