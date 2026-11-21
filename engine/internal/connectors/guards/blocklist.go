package guards

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// OutreachBlocklistID is the connector id.
const OutreachBlocklistID = "outreachblocklist"

// OutreachBlocklist asks a shared outreach blocklist service whether an
// address may be written to: a list a company keeps outside any one tool,
// of people and domains nobody in the company may cold email.
//
// The service is the workspace's own, at an address the connection holds:
//
//	POST <base URL>/api/v1/check
//	Authorization: Bearer <key>
//	{"emails": ["<address>", ...]}                     (1 to 100)
//
//	{"success": true, "result": {"results": [
//	  {"input": "<address>", "status": "blocked" | "not_blocked" | "unknown"}]}}
//
// One result for each address, in the same order, each echoing its input.
// Only not_blocked clears an address. The base URL is an https origin: no
// path, query or sign-in in it.
type OutreachBlocklist struct{}

var _ c.ContactGuard = OutreachBlocklist{}

func (OutreachBlocklist) Definition() c.Definition {
	return c.Definition{
		ID: OutreachBlocklistID, Name: "Outreach blocklist", Category: c.CategoryData,
		Summary:     "Never cold email an address your company's shared outreach blocklist has.",
		Description: "Before a company is researched, Outflare checks its domain against your blocklist's domains export, and before anyone is added to a campaign it asks your outreach blocklist service about their address. A blocked domain or address is stopped. When the service can't answer or doesn't know the address, the person waits and is asked about again later; nobody is added on a guess. Every 30 minutes Outflare also copies the emails and domains exports into the opt-out list, so people already in a campaign who are blocked later are stopped. It only reads, with a key that stays in this workspace.",
		Auth:        c.AuthAPIKey,
		Fields: []c.Field{
			{Name: "base_url", Label: "Base URL", Placeholder: "https://blocklist.yourcompany.example",
				Help: "The https address of your blocklist service, with no path. Outflare calls POST <this address>/api/v1/check."},
			{Name: "api_key", Label: "API key", Secret: true, Help: "A key for the blocklist service, sent as a Bearer token."},
		},
		Capabilities: []string{c.CapContactGuard},
		Pricing:      "Your own service: no charge from Outflare",
	}
}

// Limits of the check, as variables so tests don't wait.
var (
	// blocklistBatch is the most addresses one call takes.
	blocklistBatch = 100
	// blocklistWait is how long one call may take, reading the answer
	// included.
	blocklistWait = 12 * time.Second
)

const blocklistMaxBytes = 8 << 20

type blocklistAPI struct {
	cl        *c.Client
	base, key string
}

func (OutreachBlocklist) open(cl *c.Client, creds c.Creds) (blocklistAPI, *c.GuardError) {
	const name = "The outreach blocklist"
	key := strings.TrimSpace(creds["api_key"])
	if key == "" || badKey.MatchString(key) {
		return blocklistAPI{}, fail(name, "credential", 0)
	}
	base, ok := baseURL(creds["base_url"], false)
	if !ok {
		return blocklistAPI{}, fail(name, "base_url", 0)
	}
	return blocklistAPI{cl: cl, base: cl.Base(OutreachBlocklistID, base), key: key}, nil
}

// Test asks about a reserved address: any whole answer proves the address
// and the key. It changes nothing.
func (g OutreachBlocklist) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	api, gerr := g.open(cl, creds)
	if gerr != nil {
		return c.Account{}, gerr
	}
	if _, gerr := api.check(ctx, []string{"connection-test@example.com"}); gerr != nil {
		return c.Account{}, gerr
	}
	return c.Account{Name: "Outreach blocklist"}, nil
}

// check asks about emails (1 to blocklistBatch) and returns each one's
// status, in order, or why the answer can't be used.
func (a blocklistAPI) check(ctx context.Context, emails []string) ([]string, *c.GuardError) {
	const name = "The outreach blocklist"
	if len(emails) < 1 || len(emails) > blocklistBatch {
		return nil, fail(name, "query", 0)
	}
	body, _ := json.Marshal(map[string]any{"emails": emails})
	req, err := http.NewRequest(http.MethodPost, a.base+"/api/v1/check", bytes.NewReader(body))
	if err != nil {
		return nil, fail(name, "base_url", 0)
	}
	req.Header.Set("Authorization", "Bearer "+a.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	ans, code := send(ctx, a.cl, req, blocklistWait, blocklistMaxBytes)
	if code != "" {
		return nil, fail(name, code, 0)
	}
	if ans.status != http.StatusOK {
		after := busyAfter(ans.status, ans.header, time.Now())
		return nil, fail(name, "http_"+strconv.Itoa(ans.status), after)
	}
	if !isJSON(ans.header) {
		return nil, fail(name, "content_type", 0)
	}
	var env struct {
		Success json.RawMessage `json:"success"`
		Result  *struct {
			Results []map[string]json.RawMessage `json:"results"`
		} `json:"result"`
	}
	if json.Unmarshal(ans.body, &env) != nil || string(bytes.TrimSpace(env.Success)) != "true" || env.Result == nil || env.Result.Results == nil {
		return nil, fail(name, "schema", 0)
	}
	rows := env.Result.Results
	if len(rows) != len(emails) {
		return nil, fail(name, "incomplete_check", 0)
	}
	out := make([]string, len(rows))
	for i, m := range rows {
		input, ok := text(m["input"])
		status, sok := text(m["status"])
		if !ok || !sok || input != emails[i] || (status != "blocked" && status != "not_blocked" && status != "unknown") {
			return nil, fail(name, "incomplete_check", 0)
		}
		out[i] = status
	}
	return out, nil
}

// CheckContacts asks about each contact's address. A blocked address is
// blocked as blocklisted; only not_blocked clears one. A call that fails
// clears nobody, and nothing more is asked after it.
//
// A contact with no address and a domain is a company asked about alone:
// its domain is checked against the domains export (blockedDomains), and
// is blocked as blocklisted when it or a domain it is under is listed,
// clear when not, and unknown while there is no export under a day old.
func (g OutreachBlocklist) CheckContacts(ctx context.Context, cl *c.Client, creds c.Creds, contacts []c.GuardedContact) ([]c.ContactVerdict, error) {
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
	var emails []string
	at := map[string][]int{}
	companies := map[int]string{}
	for i, ct := range contacts {
		if strings.TrimSpace(ct.Email) == "" && strings.TrimSpace(ct.Domain) != "" {
			// A company asked about alone: its domain is checked against
			// the domains export.
			if d, _, ok := companyDomain(ct.Domain); ok {
				companies[i] = d
			} else {
				out[i].Detail = "invalid_domain"
			}
			continue
		}
		email := address(ct.Email)
		if email == "" {
			out[i].Detail = "invalid_email"
			continue
		}
		if at[email] == nil {
			emails = append(emails, email)
		}
		at[email] = append(at[email], i)
	}
	var first error
	if len(companies) > 0 {
		list, gerr := api.blockedDomains(ctx)
		for i, d := range companies {
			switch {
			case gerr != nil:
				out[i].Detail = gerr.Code
			case domainBlocked(list, d):
				out[i].Status, out[i].Reason = c.GuardBlocked, c.GuardBlocklisted
			default:
				out[i].Status = c.GuardClear
			}
		}
		if gerr != nil {
			first = gerr
		}
	}
	for from := 0; from < len(emails); from += blocklistBatch {
		batch := emails[from:min(from+blocklistBatch, len(emails))]
		statuses, gerr := api.check(ctx, batch)
		if gerr != nil {
			for _, email := range emails[from:] {
				for _, i := range at[email] {
					out[i].Detail = gerr.Code
				}
			}
			return out, gerr
		}
		for n, email := range batch {
			for _, i := range at[email] {
				switch statuses[n] {
				case "blocked":
					out[i].Status, out[i].Reason = c.GuardBlocked, c.GuardBlocklisted
				case "not_blocked":
					out[i].Status = c.GuardClear
				default:
					out[i].Detail = "unknown"
				}
			}
		}
	}
	return out, first
}
