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
// each is read in turn so none is left out.
var toltPartnerStatuses = []string{"active", "pending", "rejected", "suspended"}

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
	l, err := t.get(ctx, cl, creds, strings.ReplaceAll(kind, "_", " "), path, q)
	if err != nil && probed && (c.KindOf(err) == c.ErrInvalid || errors.As(err, new(*c.UnreadableError))) {
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
		Created: when(o, "created_at", "created")}
}

func toltTransaction(o map[string]any) c.ImportTransaction {
	t := c.ImportTransaction{ID: str(o, "id"), CustomerID: ref(o, "customer_id", "customer"), AmountCents: cents(o, "amount", "revenue"),
		Currency: strings.ToUpper(str(o, "currency", "currency_code")), Created: when(o, "created_at", "created")}
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
	// A commission in a payout is paid, whatever its status says.
	if status != "rejected" && ref(o, "payout_id", "payout") != "" {
		status = "paid"
	}
	return c.ImportCommission{ID: str(o, "id"), PartnerID: ref(o, "partner_id", "partner"), CustomerID: ref(o, "customer_id", "customer"),
		TransactionID: ref(o, "transaction_id", "transaction"), AmountCents: cents(o, "amount"), Currency: strings.ToUpper(str(o, "currency", "currency_code")),
		Status: status, Created: when(o, "created_at", "created"), PaidAt: when(o, "paid_at"), PayoutID: ref(o, "payout_id", "payout")}
}

// str returns the first of keys that holds a non-empty string or number.
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
