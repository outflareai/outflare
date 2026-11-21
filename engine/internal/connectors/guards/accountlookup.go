package guards

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/classify"
	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// AccountLookupID is the connector id.
const AccountLookupID = "accountlookup"

// AccountLookup asks a product's own account lookup API whether an email
// address or a company's domain belongs to an existing account, so nobody
// who is already a customer is cold emailed.
//
// The API is the workspace's own (its product's admin API), at an address
// the connection holds:
//
//	GET <lookup URL>/check?q=<email or domain>&q=...   (1 to 50 q)
//	Authorization: Bearer <key>
//
//	{"success": true, "result": {"checkedAt": "<RFC 3339>", "results": [
//	  {"input": "<q>", "result": "match" | "no_match" | "invalid",
//	   "matchedBy": "email" | "domain" | null,
//	   "sources": ["account" | "deleted_account" | "reseller_sub_user"],
//	   "reason": "<why invalid>" | null}]}}
//
// One result for each q, in the same order. An answer is used only when
// it is whole: checkedAt within the last two minutes, every input echoed
// in its place, a match that says what matched and where, a no_match that
// says nothing else, an invalid that says why. Anything else clears
// nobody.
type AccountLookup struct{}

var _ c.ContactGuard = AccountLookup{}

func (AccountLookup) Definition() c.Definition {
	return c.Definition{
		ID: AccountLookupID, Name: "Product account lookup", Category: c.CategoryData,
		Summary:     "Never cold email a customer: ask your own product who already has an account.",
		Description: "Before a company is researched, and again before anyone is added to a campaign, Outflare asks your product's account lookup API whether the company's domain or the person's email address already has an account. A match stops them as a customer. When the lookup can't answer, they wait and are asked about again later; nobody is added on a guess. Every five minutes Outflare also reads the new accounts from its account feed into the opt-out list (each address, and its company's domain), so companies are checked without a call and people already in a campaign who become customers are stopped. It only reads, with a key that stays in this workspace.",
		Auth:        c.AuthAPIKey,
		Fields: []c.Field{
			{Name: "base_url", Label: "Lookup URL", Placeholder: "https://admin.yourproduct.example/api/admin/v1/account-lookup",
				Help: "The https address of your account lookup API, without /check. Outflare calls GET <this address>/check?q=<email or domain>, and reads the account feed at GET <this address>/accounts."},
			{Name: "api_key", Label: "API key", Secret: true, Help: "A read-only key for the lookup, sent as a Bearer token."},
		},
		Capabilities: []string{c.CapContactGuard},
		Pricing:      "Your own API: no charge from Outflare",
	}
}

// Limits of the lookup, as variables so tests don't wait.
var (
	// lookupBatch is the most q one call takes.
	lookupBatch = 50
	// lookupCallWait is how long one call may take, body included, and
	// lookupBudget how long checking one set of contacts may take in all,
	// waits for a rate limit included.
	lookupCallWait = 45 * time.Second
	lookupBudget   = 60 * time.Second
	// An answer older than lookupMaxAge, or dated further ahead than
	// lookupMaxAhead, is not about now.
	lookupMaxAge   = 2 * time.Minute
	lookupMaxAhead = 30 * time.Second
)

const lookupMaxBytes = 8 << 20

type lookupAPI struct {
	cl        *c.Client
	base, key string
}

func (AccountLookup) open(cl *c.Client, creds c.Creds) (lookupAPI, *c.GuardError) {
	const name = "The account lookup"
	key := strings.TrimSpace(creds["api_key"])
	if key == "" || badKey.MatchString(key) {
		return lookupAPI{}, fail(name, "credential", 0)
	}
	base, ok := baseURL(creds["base_url"], true)
	if !ok {
		return lookupAPI{}, fail(name, "base_url", 0)
	}
	return lookupAPI{cl: cl, base: cl.Base(AccountLookupID, base), key: key}, nil
}

// Test asks about a reserved domain: any whole answer proves the address
// and the key. It changes nothing.
func (g AccountLookup) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	api, gerr := g.open(cl, creds)
	if gerr != nil {
		return c.Account{}, gerr
	}
	if _, gerr := api.check(ctx, []string{"example.com"}, time.Now().Add(lookupCallWait)); gerr != nil {
		return c.Account{}, gerr
	}
	return c.Account{Name: "Product account lookup"}, nil
}

// lookupRow is one checked result.
type lookupRow struct {
	result    string // match, no_match or invalid
	matchedBy string // email or domain, for a match
}

// check asks about values (1 to lookupBatch of them) and returns one row
// for each, in order, or why the answer can't be used. A rate limit is
// waited out once when the wait fits before deadline.
func (a lookupAPI) check(ctx context.Context, values []string, deadline time.Time) ([]lookupRow, *c.GuardError) {
	const name = "The account lookup"
	if len(values) < 1 || len(values) > lookupBatch {
		return nil, fail(name, "query", 0)
	}
	q := url.Values{}
	for _, v := range values {
		if v == "" || len(v) > 254 || strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return nil, fail(name, "query", 0)
		}
		q.Add("q", v)
	}
	for limited := 0; ; {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fail(name, "deadline", 0)
		}
		req, err := http.NewRequest(http.MethodGet, a.base+"/check?"+q.Encode(), nil)
		if err != nil {
			return nil, fail(name, "base_url", 0)
		}
		req.Header.Set("Authorization", "Bearer "+a.key)
		req.Header.Set("Accept", "application/json")
		ans, code := send(ctx, a.cl, req, min(lookupCallWait, remaining), lookupMaxBytes)
		if code != "" {
			return nil, fail(name, code, 0)
		}
		if ans.status == http.StatusTooManyRequests {
			after := ans.header.Get("Retry-After")
			if after == "" {
				after = ans.header.Get("RateLimit-Reset")
			}
			wait := retryAfter(after, time.Now())
			if limited++; limited > 1 || !time.Now().Add(wait).Before(deadline) {
				return nil, fail(name, "http_429", wait)
			}
			if !sleep(ctx, wait) {
				return nil, fail(name, "timeout", 0)
			}
			continue
		}
		if ans.status != http.StatusOK {
			return nil, fail(name, "http_"+strconv.Itoa(ans.status), busyAfter(ans.status, ans.header, time.Now()))
		}
		if !isJSON(ans.header) {
			return nil, fail(name, "content_type", 0)
		}
		rows, code := readLookup(ans.body, values, time.Now())
		if code != "" {
			return nil, fail(name, code, 0)
		}
		return rows, nil
	}
}

// null says whether a JSON value is null.
func null(raw json.RawMessage) bool { return string(bytes.TrimSpace(raw)) == "null" }

// text reads a JSON string. Null and anything else is not one.
func text(raw json.RawMessage) (string, bool) {
	var s string
	if len(raw) == 0 || null(raw) || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// readLookup checks an answer against what was asked: "schema" when it is
// not the shape documented on AccountLookup, "incomplete_check" when it is
// that shape but stale, short, out of order or inconsistent.
func readLookup(body []byte, values []string, now time.Time) ([]lookupRow, string) {
	var env struct {
		Success json.RawMessage `json:"success"`
		Result  json.RawMessage `json:"result"`
	}
	if json.Unmarshal(body, &env) != nil || string(bytes.TrimSpace(env.Success)) != "true" || !bytes.HasPrefix(bytes.TrimSpace(env.Result), []byte("{")) {
		return nil, "schema"
	}
	var res struct {
		CheckedAt json.RawMessage              `json:"checkedAt"`
		Results   []map[string]json.RawMessage `json:"results"`
	}
	if json.Unmarshal(env.Result, &res) != nil || res.Results == nil {
		return nil, "schema"
	}
	stamp, ok := text(res.CheckedAt)
	if !ok {
		return nil, "schema"
	}
	checked, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return nil, "schema"
	}
	type parsed struct {
		input, result, matchedBy string
		sources                  int
		reason                   *string
	}
	rows := make([]parsed, 0, len(res.Results))
	for _, m := range res.Results {
		var p parsed
		var ok bool
		if m == nil {
			return nil, "schema"
		}
		if p.input, ok = text(m["input"]); !ok {
			return nil, "schema"
		}
		if p.result, ok = text(m["result"]); !ok || (p.result != "match" && p.result != "no_match" && p.result != "invalid") {
			return nil, "schema"
		}
		// matchedBy and reason must be there, as null or a value.
		switch raw := m["matchedBy"]; {
		case len(raw) == 0:
			return nil, "schema"
		case !null(raw):
			if p.matchedBy, ok = text(raw); !ok || (p.matchedBy != "email" && p.matchedBy != "domain") {
				return nil, "schema"
			}
		}
		var sources []json.RawMessage
		if raw := m["sources"]; len(raw) == 0 || null(raw) || json.Unmarshal(raw, &sources) != nil {
			return nil, "schema"
		}
		for _, s := range sources {
			if v, ok := text(s); !ok || (v != "account" && v != "deleted_account" && v != "reseller_sub_user") {
				return nil, "schema"
			}
		}
		p.sources = len(sources)
		switch raw := m["reason"]; {
		case len(raw) == 0:
			return nil, "schema"
		case !null(raw):
			r, ok := text(raw)
			if !ok {
				return nil, "schema"
			}
			p.reason = &r
		}
		rows = append(rows, p)
	}
	if age := now.Sub(checked); age > lookupMaxAge || age < -lookupMaxAhead || len(rows) != len(values) {
		return nil, "incomplete_check"
	}
	out := make([]lookupRow, len(rows))
	for i, p := range rows {
		match := p.result == "match"
		switch {
		case p.input != values[i],
			match && (p.matchedBy == "" || p.sources == 0 || p.reason != nil),
			!match && (p.matchedBy != "" || p.sources != 0),
			p.result == "no_match" && p.reason != nil,
			p.result == "invalid" && (p.reason == nil || *p.reason == ""):
			return nil, "incomplete_check"
		}
		out[i] = lookupRow{result: p.result, matchedBy: p.matchedBy}
	}
	return out, ""
}

// companyDomain is a website or host as the company domain to ask about
// (its registrable domain, so app.acme.example is acme.example), whether
// it is a mailbox provider's rather than a company's (gmail.com, a relay,
// a disposable domain), and whether it could be read at all.
func companyDomain(s string) (domain string, mailbox, ok bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, p := range []string{"https://", "http://"} {
		s = strings.TrimPrefix(s, p)
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimPrefix(s, "www.")
	d, err := classify.ParseDomain(s)
	if err != nil || d.Registrable == "" {
		return "", false, false
	}
	intel := classify.Builtin()
	return d.Registrable, intel.MailboxProvider(d.Name) || intel.MailboxProvider(d.Registrable), true
}

// CheckContacts asks about each contact's address, the domain of that
// address and their company's domain. Any match blocks the contact as a
// customer; a contact is clear only when everything asked about them came
// back no_match.
//
// A mailbox provider's domain (gmail.com, a private mailbox service, a
// relay) is never asked about and never decides anything: one customer
// with a Gmail address must not make every Gmail user a customer, and no
// answer about gmail.com says anything about the person. Their address
// alone is asked about.
//
// A contact with no address and a domain is a company asked about alone
// (before anyone there is known): its registrable domain is asked, a
// match blocks it as a customer and no_match clears it. A mailbox
// provider's domain is not a company's: it stays unknown
// (mailbox_domain), never asked.
func (g AccountLookup) CheckContacts(ctx context.Context, cl *c.Client, creds c.Creds, contacts []c.GuardedContact) ([]c.ContactVerdict, error) {
	out := make([]c.ContactVerdict, len(contacts))
	for i, ct := range contacts {
		out[i] = c.ContactVerdict{Email: ct.Email, Status: c.GuardUnknown}
	}
	api, gerr := g.open(cl, creds)
	if gerr != nil {
		for i := range out {
			out[i].Detail = gerr.Code
		}
		return out, gerr
	}
	// What is asked about each contact, and every value asked once.
	type asked struct {
		email   string
		mailbox bool // the address is at a mailbox provider
		domains []string
	}
	plan := make([]*asked, len(contacts))
	var values []string
	seen := map[string]bool{}
	add := func(v string) {
		if !seen[v] {
			seen[v] = true
			values = append(values, v)
		}
	}
	for i, ct := range contacts {
		if strings.TrimSpace(ct.Email) == "" && strings.TrimSpace(ct.Domain) != "" {
			// Only the company is asked about. A mailbox provider's domain
			// is not a company's: it is never asked, and nothing is said
			// about it.
			company, mailbox, ok := companyDomain(ct.Domain)
			switch {
			case !ok:
				out[i].Detail = "invalid_domain"
			case mailbox:
				out[i].Detail = "mailbox_domain"
			default:
				plan[i] = &asked{domains: []string{company}}
				add(company)
			}
			continue
		}
		email := address(ct.Email)
		if email == "" {
			out[i].Detail = "invalid_email"
			continue
		}
		host, mailbox, ok := companyDomain(email[strings.LastIndexByte(email, '@')+1:])
		if !ok {
			out[i].Detail = "invalid_email"
			continue
		}
		p := &asked{email: email, mailbox: mailbox}
		if !mailbox {
			p.domains = append(p.domains, host)
		}
		if strings.TrimSpace(ct.Domain) != "" {
			company, companyMailbox, ok := companyDomain(ct.Domain)
			if !ok {
				out[i].Detail = "invalid_domain"
				continue
			}
			if !companyMailbox && company != host {
				p.domains = append(p.domains, company)
			}
		}
		plan[i] = p
		add(email)
		for _, d := range p.domains {
			add(d)
		}
	}
	// Fresh answers every time: nothing is remembered between checks.
	deadline := time.Now().Add(lookupBudget)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		// A caller with less time than that gets it: no wait for a rate
		// limit runs past it.
		deadline = d
	}
	rows := map[string]lookupRow{}
	failed := map[string]string{}
	var first *c.GuardError
	for from := 0; from < len(values); from += lookupBatch {
		batch := values[from:min(from+lookupBatch, len(values))]
		got, gerr := api.check(ctx, batch, deadline)
		if gerr != nil {
			if first == nil {
				first = gerr
			}
			for _, v := range batch {
				failed[v] = gerr.Code
			}
			continue
		}
		for i, v := range batch {
			rows[v] = got[i]
		}
	}
	for i, p := range plan {
		if p == nil {
			continue
		}
		customer, detail := false, ""
		unsure := func(code string) {
			if detail == "" {
				detail = code
			}
		}
		for n, v := range append([]string{p.email}, p.domains...) {
			if v == "" {
				// A company asked about alone: there is no address.
				continue
			}
			row, ok := rows[v]
			switch {
			case !ok && failed[v] != "":
				unsure(failed[v])
			case !ok:
				unsure("unanswered")
			case row.result == "invalid":
				unsure("invalid")
			case row.result == "match" && n == 0 && p.mailbox && row.matchedBy == "domain":
				// A mailbox provider's domain matched, not the person: it
				// neither blocks nor clears them.
				unsure("mailbox_domain_match")
			case row.result == "match":
				customer = true
			}
		}
		switch {
		case customer:
			out[i].Status, out[i].Reason = c.GuardBlocked, c.GuardCustomer
		case detail != "":
			out[i].Detail = detail
		default:
			out[i].Status = c.GuardClear
		}
	}
	if first != nil {
		return out, first
	}
	return out, nil
}
