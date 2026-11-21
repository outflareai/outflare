package guards

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Reading a guard's whole list (connectors.GuardLister), so the workspace
// keeps a copy in its opt-out list and companies are checked against it
// without a call.
//
// The account lookup's list is its feed of accounts, oldest first, a page
// at a time; new accounts are added at the end, so each sync reads only
// what came after the last cursor:
//
//	GET <lookup URL>/accounts?limit=500&after=<cursor>   (no after: the first page)
//	Authorization: Bearer <key>
//
//	{"success": true, "result": {"accounts": [{"email": "<address>" | null,
//	  "freeEmailProvider": true | false, ...}, ...],
//	  "nextCursor": "<cursor>" | null, "hasMore": true | false}}
//
// Only email and freeEmailProvider are read; nothing else about an
// account is kept. Each account's address joins the list, and so does its
// company's domain (the address's registrable domain), unless that is a
// mailbox provider's (gmail.com): one customer with a Gmail address must
// not stop every Gmail user.
//
// The blocklist's list is its two exports, read whole each time:
//
//	GET <base URL>/api/v1/exports?format=csv&view=emails    (one column, email)
//	GET <base URL>/api/v1/exports?format=csv&view=domains   (one column, domain)
//
// as the domains export is described in blocklist_domains.go. Each is read
// a row at a time, so a long list never sits in memory whole.
var (
	// feedPage is how many accounts one feed call asks for.
	feedPage = 500
	// feedWait is how long one feed call may take, the body included.
	feedWait = 45 * time.Second
	// listExportWait is how long one export download may take, the body
	// included, and listExportMaxBytes the largest read.
	listExportWait           = 5 * time.Minute
	listExportMaxBytes int64 = 1 << 30
	// listBatch is how many entries of an export go to emit at once.
	listBatch = 5000
)

const feedMaxBytes = 16 << 20

var (
	_ c.GuardLister = AccountLookup{}
	_ c.GuardLister = OutreachBlocklist{}
)

// GuardList: the account feed, read every five minutes.
func (AccountLookup) GuardList() c.GuardListInfo {
	return c.GuardListInfo{Mode: c.GuardListFeed, Reason: c.GuardCustomer, Every: 5 * time.Minute}
}

// GuardList: the exports, read whole every thirty minutes.
func (OutreachBlocklist) GuardList() c.GuardListInfo {
	return c.GuardListInfo{Mode: c.GuardListSnapshot, Reason: c.GuardBlocklisted, Every: 30 * time.Minute}
}

// ReadGuardList reads the account feed from cursor, up to pages pages.
func (g AccountLookup) ReadGuardList(ctx context.Context, cl *c.Client, creds c.Creds, cursor string, pages int,
	emit func([]c.GuardListEntry, string) error) (bool, error) {
	api, gerr := g.open(cl, creds)
	if gerr != nil {
		return false, gerr
	}
	for range max(pages, 1) {
		accounts, next, more, gerr := api.feed(ctx, cursor)
		if gerr != nil {
			return true, gerr
		}
		var entries []c.GuardListEntry
		seen := map[c.GuardListEntry]bool{}
		add := func(e c.GuardListEntry) {
			if !seen[e] {
				seen[e] = true
				entries = append(entries, e)
			}
		}
		for _, a := range accounts {
			email := address(a.email)
			if email == "" {
				continue // an account with no address, or one that can't be read
			}
			add(c.GuardListEntry{Value: email})
			if host, mailbox, ok := companyDomain(email[strings.LastIndexByte(email, '@')+1:]); ok && !mailbox && !a.freeMail {
				add(c.GuardListEntry{Value: host, Domain: true})
			}
		}
		if err := emit(entries, next); err != nil {
			return true, err
		}
		cursor = next
		if !more {
			return false, nil
		}
	}
	return true, nil
}

// feedAccount is what is read of one account.
type feedAccount struct {
	email    string
	freeMail bool
}

// feed reads one page after cursor: its accounts, the cursor after it and
// whether more follow. The cursor never goes back: a page with accounts
// names a new cursor, and an empty last page keeps the one it was given.
func (a lookupAPI) feed(ctx context.Context, cursor string) ([]feedAccount, string, bool, *c.GuardError) {
	const name = "The account lookup's feed"
	q := url.Values{"limit": {strconv.Itoa(feedPage)}}
	if cursor != "" {
		q.Set("after", cursor)
	}
	req, err := http.NewRequest(http.MethodGet, a.base+"/accounts?"+q.Encode(), nil)
	if err != nil {
		return nil, "", false, fail(name, "base_url", 0)
	}
	req.Header.Set("Authorization", "Bearer "+a.key)
	req.Header.Set("Accept", "application/json")
	ans, code := send(ctx, a.cl, req, feedWait, feedMaxBytes)
	if code != "" {
		return nil, "", false, fail(name, code, 0)
	}
	if ans.status != http.StatusOK {
		var after time.Duration
		if ans.status == http.StatusTooManyRequests {
			after = retryAfter(ans.header.Get("Retry-After"), time.Now())
		}
		return nil, "", false, fail(name, "http_"+strconv.Itoa(ans.status), after)
	}
	if !isJSON(ans.header) {
		return nil, "", false, fail(name, "content_type", 0)
	}
	var env struct {
		Success json.RawMessage `json:"success"`
		Result  *struct {
			Accounts []map[string]json.RawMessage `json:"accounts"`
			Next     json.RawMessage              `json:"nextCursor"`
			HasMore  *bool                        `json:"hasMore"`
		} `json:"result"`
	}
	if json.Unmarshal(ans.body, &env) != nil || string(env.Success) != "true" || env.Result == nil || env.Result.Accounts == nil || env.Result.HasMore == nil {
		return nil, "", false, fail(name, "schema", 0)
	}
	r := env.Result
	next := ""
	if len(r.Next) > 0 && !null(r.Next) {
		var ok bool
		if next, ok = text(r.Next); !ok || next == "" || len(next) > 512 {
			return nil, "", false, fail(name, "schema", 0)
		}
	}
	switch n := len(r.Accounts); {
	case n > feedPage,
		*r.HasMore && (n == 0 || next == ""),
		n > 0 && (next == "" || next == cursor),
		n == 0 && next != "" && next != cursor:
		return nil, "", false, fail(name, "feed_cursor", 0)
	}
	if next == "" {
		next = cursor
	}
	out := make([]feedAccount, 0, len(r.Accounts))
	for _, m := range r.Accounts {
		if m == nil {
			return nil, "", false, fail(name, "schema", 0)
		}
		var acc feedAccount
		if raw := m["email"]; len(raw) > 0 && !null(raw) {
			s, ok := text(raw)
			if !ok {
				return nil, "", false, fail(name, "schema", 0)
			}
			acc.email = s
		}
		if raw := m["freeEmailProvider"]; len(raw) > 0 && !null(raw) {
			acc.freeMail = string(raw) == "true"
		}
		out = append(out, acc)
	}
	return out, next, *r.HasMore, nil
}

// ReadGuardList reads both exports whole, emails then domains.
func (g OutreachBlocklist) ReadGuardList(ctx context.Context, cl *c.Client, creds c.Creds, _ string, _ int,
	emit func([]c.GuardListEntry, string) error) (bool, error) {
	api, gerr := g.open(cl, creds)
	if gerr != nil {
		return false, gerr
	}
	for _, view := range []string{"emails", "domains"} {
		if err := api.streamExport(ctx, view, emit); err != nil {
			return false, err
		}
	}
	return false, nil
}

// streamExport downloads one export and hands its rows to emit in
// batches. An export that is not the shape described, or is more than a
// day old, fails whole: the copy is then left as it was.
func (a blocklistAPI) streamExport(ctx context.Context, view string, emit func([]c.GuardListEntry, string) error) error {
	const name = "The outreach blocklist's export"
	column := map[string]string{"emails": "email", "domains": "domain"}[view]
	req, err := http.NewRequest(http.MethodGet, a.base+"/api/v1/exports?format=csv&view="+view, nil)
	if err != nil {
		return fail(name, "base_url", 0)
	}
	req.Header.Set("Authorization", "Bearer "+a.key)
	req.Header.Set("Accept", "text/csv")
	req.Header.Set("User-Agent", "outflare")
	req.Header.Set("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(ctx, listExportWait)
	defer cancel()
	base := http.DefaultClient
	if a.cl != nil && a.cl.HTTP != nil {
		base = a.cl.HTTP
	}
	h := &http.Client{Transport: base.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := h.Do(req.WithContext(ctx))
	if err != nil {
		if timedOut(err) {
			return fail(name, "timeout", 0)
		}
		return fail(name, "transport", 0)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		// A key that may check addresses but not export: the live checks
		// still work, so the connection is not marked as refused.
		return &c.GuardError{Code: "export_refused", Err: c.Errf(c.ErrUnavailable,
			"%s was refused (HTTP %d): its key needs the export scope. Its list is not copied until it is allowed.", name, resp.StatusCode)}
	case resp.StatusCode == http.StatusTooManyRequests:
		return fail(name, "http_429", retryAfter(resp.Header.Get("Retry-After"), clock()))
	case resp.StatusCode != http.StatusOK:
		return fail(name, "http_"+strconv.Itoa(resp.StatusCode), 0)
	case !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/csv"):
		return fail(name, "content_type", 0)
	}
	stamp := strings.TrimSpace(resp.Header.Get("X-Generated-At"))
	generated, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		if generated, err = http.ParseTime(stamp); err != nil {
			return fail(name, "schema", 0)
		}
	}
	if age := clock().Sub(generated); age > exportMaxAge || age < -exportMaxAhead {
		return fail(name, "stale_export", 0)
	}
	// An export cut short would read as one that lost entries, so one over
	// the limit fails rather than ending early.
	r := csv.NewReader(&capped{r: resp.Body, left: listExportMaxBytes})
	r.FieldsPerRecord = 1
	r.ReuseRecord = true
	head, err := r.Read()
	if err != nil || strings.TrimPrefix(strings.TrimSpace(head[0]), "\ufeff") != column {
		return fail(name, readCode(err), 0)
	}
	batch := make([]c.GuardListEntry, 0, listBatch)
	for {
		row, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fail(name, readCode(err), 0)
		}
		v := strings.ToLower(strings.TrimSpace(row[0]))
		switch {
		case v == "":
			continue
		case view == "emails":
			if v = address(v); v == "" {
				return fail(name, "schema", 0)
			}
		case len(v) > 253 || !domainShape.MatchString(v):
			return fail(name, "schema", 0)
		}
		batch = append(batch, c.GuardListEntry{Value: v, Domain: view == "domains"})
		if len(batch) == listBatch {
			if err := emit(batch, ""); err != nil {
				return err
			}
			batch = make([]c.GuardListEntry, 0, listBatch)
		}
	}
	if len(batch) > 0 {
		return emit(batch, "")
	}
	return nil
}

// errTooLong is an export over listExportMaxBytes.
var errTooLong = errors.New("export over the size limit")

// capped reads r and fails, rather than ending, past left bytes.
type capped struct {
	r    io.Reader
	left int64
}

func (c *capped) Read(p []byte) (int, error) {
	if c.left <= 0 {
		var one [1]byte
		if n, _ := c.r.Read(one[:]); n > 0 {
			return 0, errTooLong
		}
		return c.r.Read(p)
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

// readCode is the code for an export that could not be read to its end:
// a timeout, a body cut off, or rows that are not the export's.
func readCode(err error) string {
	switch {
	case err == nil:
		return "schema"
	case timedOut(err):
		return "timeout"
	case errors.Is(err, errTooLong):
		return "body_limit"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "transport"
	}
	var pe *csv.ParseError
	if errors.As(err, &pe) {
		return "schema"
	}
	return "transport"
}
