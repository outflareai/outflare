package guards

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

var (
	lookupCreds    = c.Creds{"base_url": "https://admin.product.example/api/admin/v1/account-lookup", "api_key": "test_not_a_real_key"}
	blocklistCreds = c.Creds{"base_url": "https://blocklist.company.example", "api_key": "test_not_a_real_key"}
)

// set changes a limit for one test.
func set[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// calls counts the requests a fake got and keeps what they asked.
type calls struct {
	mu   sync.Mutex
	n    int
	asks [][]string
}

func (k *calls) add(ask []string) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.n++
	k.asks = append(k.asks, ask)
	return k.n
}

func (k *calls) count() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.n
}

// lookupRowJSON is one result as the account lookup writes it.
func lookupRowJSON(input, result string) map[string]any {
	row := map[string]any{"input": input, "result": result, "matchedBy": nil, "sources": []string{}, "reason": nil}
	switch result {
	case "match":
		row["matchedBy"], row["sources"] = "email", []string{"account"}
		if !strings.Contains(input, "@") {
			row["matchedBy"] = "domain"
		}
	case "invalid":
		row["reason"] = "not an email address or a domain"
	}
	return row
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// fakeLookup is an account lookup API: answer decides each q's result
// (match, no_match or invalid), and edit, when set, changes the result
// object before it is sent.
func fakeLookup(t *testing.T, answer func(q string) string, edit func(result map[string]any)) (*c.Client, *calls) {
	t.Helper()
	k := &calls{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/admin/v1/account-lookup/check" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test_not_a_real_key" {
			t.Errorf("Authorization = %q", got)
		}
		for key := range r.URL.Query() {
			if key != "q" {
				t.Errorf("unexpected query parameter %q", key)
			}
		}
		qs := r.URL.Query()["q"]
		if len(qs) < 1 || len(qs) > 50 {
			t.Errorf("a call asked about %d values, want 1 to 50", len(qs))
		}
		k.add(qs)
		rows := make([]any, len(qs))
		for i, q := range qs {
			rows[i] = lookupRowJSON(q, answer(q))
		}
		result := map[string]any{"checkedAt": time.Now().UTC().Format(time.RFC3339Nano), "results": rows}
		if edit != nil {
			edit(result)
		}
		writeJSON(w, map[string]any{"success": true, "result": result})
	}))
	t.Cleanup(srv.Close)
	return &c.Client{BaseURLs: map[string]string{AccountLookupID: srv.URL + "/api/admin/v1/account-lookup"}}, k
}

func noMatch(string) string { return "no_match" }

func contacts(emails ...string) []c.GuardedContact {
	out := make([]c.GuardedContact, len(emails))
	for i, e := range emails {
		out[i] = c.GuardedContact{Email: e}
	}
	return out
}

// wantAll checks that every verdict is unknown with the detail, and that
// the failure carries the same code: nothing was cleared.
func wantHeld(t *testing.T, got []c.ContactVerdict, err error, n int, code string) {
	t.Helper()
	if len(got) != n {
		t.Fatalf("got %d verdicts, want %d", len(got), n)
	}
	for _, v := range got {
		if v.Status != c.GuardUnknown || v.Detail != code {
			t.Errorf("%s: %s (%s), want unknown (%s)", v.Email, v.Status, v.Detail, code)
		}
	}
	if err == nil {
		t.Fatalf("no error beside the held verdicts, want %s", code)
	}
	if gotCode, _ := c.GuardFailure(err); gotCode != code {
		t.Errorf("failure code = %q, want %q", gotCode, code)
	}
}

func TestAccountLookupMatchNoMatchInvalid(t *testing.T) {
	cl, k := fakeLookup(t, func(q string) string {
		switch q {
		case "ana@customer.example", "bigco.example":
			return "match"
		case "odd@strange.example":
			return "invalid"
		}
		return "no_match"
	}, nil)
	got, err := AccountLookup{}.CheckContacts(t.Context(), cl, lookupCreds, []c.GuardedContact{
		{Email: " Ana@Customer.example "},                                           // the address has an account
		{Email: "ben@prospect.example", Domain: "prospect.example"},                 // nothing matches
		{Email: "cy@mail.bigco.example", Domain: "https://www.bigco.example/about"}, // the company has one
		{Email: "odd@strange.example"},                                              // the lookup refuses the input
		{Email: "not an address"},                                                   // never asked
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []c.ContactVerdict{
		{Email: " Ana@Customer.example ", Status: c.GuardBlocked, Reason: c.GuardCustomer},
		{Email: "ben@prospect.example", Status: c.GuardClear},
		{Email: "cy@mail.bigco.example", Status: c.GuardBlocked, Reason: c.GuardCustomer},
		{Email: "odd@strange.example", Status: c.GuardUnknown, Detail: "invalid"},
		{Email: "not an address", Status: c.GuardUnknown, Detail: "invalid_email"},
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("verdict %d = %+v, want %+v", i, got[i], w)
		}
	}
	// Both the address and the company's domain are asked, each once, in
	// one call.
	wantAsk := "ana@customer.example customer.example ben@prospect.example prospect.example cy@mail.bigco.example bigco.example odd@strange.example strange.example"
	if k.count() != 1 || strings.Join(k.asks[0], " ") != wantAsk {
		t.Errorf("asked %v, want one call for %s", k.asks, wantAsk)
	}
}

// A mailbox provider's domain is never asked about, and a match on it
// neither blocks nor clears the person.
func TestAccountLookupFreeMailDomain(t *testing.T) {
	cl, k := fakeLookup(t, noMatch, nil)
	got, err := AccountLookup{}.CheckContacts(t.Context(), cl, lookupCreds, []c.GuardedContact{{Email: "ana.lee@gmail.com", Domain: "gmail.com"}})
	if err != nil || got[0].Status != c.GuardClear {
		t.Fatalf("got %+v, %v; want clear by the address alone", got, err)
	}
	if strings.Join(k.asks[0], " ") != "ana.lee@gmail.com" {
		t.Errorf("asked %v, want only the address", k.asks[0])
	}

	// The lookup matches the address by its domain: someone else at
	// gmail.com has an account.
	cl, _ = fakeLookup(t, func(string) string { return "match" }, func(result map[string]any) {
		result["results"].([]any)[0].(map[string]any)["matchedBy"] = "domain"
	})
	got, err = AccountLookup{}.CheckContacts(t.Context(), cl, lookupCreds, contacts("ana.lee@gmail.com"))
	if err != nil || got[0].Status != c.GuardUnknown || got[0].Detail != "mailbox_domain_match" {
		t.Errorf("got %+v, %v; want unknown (mailbox_domain_match)", got, err)
	}

	// The same answer for a company's own address is a customer.
	cl, _ = fakeLookup(t, func(q string) string {
		if strings.Contains(q, "@") {
			return "match"
		}
		return "no_match"
	}, func(result map[string]any) {
		result["results"].([]any)[0].(map[string]any)["matchedBy"] = "domain"
	})
	got, err = AccountLookup{}.CheckContacts(t.Context(), cl, lookupCreds, contacts("ana@acme.example"))
	if err != nil || got[0].Status != c.GuardBlocked || got[0].Reason != c.GuardCustomer {
		t.Errorf("got %+v, %v; want blocked as a customer", got, err)
	}
}

func TestAccountLookupRefusesAnswersItCannotTrust(t *testing.T) {
	rows := func(result map[string]any) []any { return result["results"].([]any) }
	row := func(result map[string]any, i int) map[string]any { return rows(result)[i].(map[string]any) }
	for name, tc := range map[string]struct {
		edit func(result map[string]any)
		code string
	}{
		"wrong order":            {func(r map[string]any) { rs := rows(r); rs[0], rs[1] = rs[1], rs[0] }, "incomplete_check"},
		"another address echoed": {func(r map[string]any) { row(r, 0)["input"] = "someone@else.example" }, "incomplete_check"},
		"one result short":       {func(r map[string]any) { r["results"] = rows(r)[:1] }, "incomplete_check"},
		"stale checkedAt": {func(r map[string]any) {
			r["checkedAt"] = time.Now().Add(-3 * time.Minute).UTC().Format(time.RFC3339)
		}, "incomplete_check"},
		"checkedAt in the future": {func(r map[string]any) {
			r["checkedAt"] = time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
		}, "incomplete_check"},
		"no_match with a reason":     {func(r map[string]any) { row(r, 0)["reason"] = "odd" }, "incomplete_check"},
		"no_match with a source":     {func(r map[string]any) { row(r, 0)["sources"] = []string{"account"} }, "incomplete_check"},
		"match without what matched": {func(r map[string]any) { row(r, 0)["result"] = "match" }, "incomplete_check"},
		"invalid without a reason":   {func(r map[string]any) { row(r, 0)["result"] = "invalid" }, "incomplete_check"},
		"checkedAt missing":          {func(r map[string]any) { delete(r, "checkedAt") }, "schema"},
		"checkedAt not a time":       {func(r map[string]any) { r["checkedAt"] = "yesterday" }, "schema"},
		"matchedBy missing":          {func(r map[string]any) { delete(row(r, 0), "matchedBy") }, "schema"},
		"reason missing":             {func(r map[string]any) { delete(row(r, 0), "reason") }, "schema"},
		"an unknown result":          {func(r map[string]any) { row(r, 0)["result"] = "probably_fine" }, "schema"},
		"an unknown source":          {func(r map[string]any) { row(r, 0)["sources"] = []string{"rumour"} }, "schema"},
		"results not a list":         {func(r map[string]any) { r["results"] = "none" }, "schema"},
		"a result that is null":      {func(r map[string]any) { rows(r)[0] = nil }, "schema"},
	} {
		t.Run(name, func(t *testing.T) {
			cl, _ := fakeLookup(t, noMatch, tc.edit)
			// Two mailbox addresses: two values asked, nothing else.
			got, err := AccountLookup{}.CheckContacts(t.Context(), cl, lookupCreds, contacts("ana@gmail.com", "ben@gmail.com"))
			wantHeld(t, got, err, 2, tc.code)
		})
	}
}

// raw answers every call with one fixed response.
func raw(t *testing.T, id, path string, status int, header map[string]string, body string) (*c.Client, *calls) {
	t.Helper()
	k := &calls{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k.add(nil)
		for h, v := range header {
			w.Header().Set(h, v)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return &c.Client{BaseURLs: map[string]string{id: srv.URL + path}}, k
}

func TestAccountLookupRefusesBadResponses(t *testing.T) {
	jsonType := map[string]string{"Content-Type": "application/json"}
	for name, tc := range map[string]struct {
		status int
		header map[string]string
		body   string
		code   string
		kind   string
	}{
		"server error":     {500, jsonType, `{}`, "http_500", c.ErrUnavailable},
		"refused key":      {401, jsonType, `{}`, "http_401", c.ErrAuth},
		"forbidden":        {403, jsonType, `{}`, "http_403", c.ErrAuth},
		"not found":        {404, jsonType, `{}`, "http_404", c.ErrUnavailable},
		"accepted":         {202, jsonType, `{"success":true,"result":{}}`, "http_202", c.ErrUnavailable},
		"not JSON by type": {200, map[string]string{"Content-Type": "text/html"}, `{"success":true,"result":{}}`, "content_type", c.ErrUnavailable},
		"not JSON":         {200, jsonType, `<html>sign in</html>`, "schema", c.ErrUnavailable},
		"success false":    {200, jsonType, `{"success":false,"result":{"checkedAt":"2026-01-01T00:00:00Z","results":[]}}`, "schema", c.ErrUnavailable},
		"result a list":    {200, jsonType, `{"success":true,"result":[]}`, "schema", c.ErrUnavailable},
		"result missing":   {200, jsonType, `{"success":true}`, "schema", c.ErrUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			cl, k := raw(t, AccountLookupID, "", tc.status, tc.header, tc.body)
			got, err := AccountLookup{}.CheckContacts(t.Context(), cl, lookupCreds, contacts("ana@gmail.com"))
			wantHeld(t, got, err, 1, tc.code)
			if c.KindOf(err) != tc.kind {
				t.Errorf("kind = %s, want %s", c.KindOf(err), tc.kind)
			}
			if k.count() != 1 {
				t.Errorf("%d calls, want 1: only a rate limit is asked again", k.count())
			}
		})
	}
}

func TestAccountLookupRateLimit(t *testing.T) {
	limited := func(t *testing.T, times int, header map[string]string) (*c.Client, *calls) {
		k := &calls{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			qs := r.URL.Query()["q"]
			if k.add(qs) <= times {
				for h, v := range header {
					w.Header().Set(h, v)
				}
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			rows := make([]any, len(qs))
			for i, q := range qs {
				rows[i] = lookupRowJSON(q, "no_match")
			}
			writeJSON(w, map[string]any{"success": true, "result": map[string]any{"checkedAt": time.Now().UTC().Format(time.RFC3339), "results": rows}})
		}))
		t.Cleanup(srv.Close)
		return &c.Client{BaseURLs: map[string]string{AccountLookupID: srv.URL}}, k
	}

	t.Run("waits as asked, then asks once more", func(t *testing.T) {
		cl, k := limited(t, 1, map[string]string{"Retry-After": "1"})
		started := time.Now()
		got, err := AccountLookup{}.CheckContacts(t.Context(), cl, lookupCreds, contacts("ana@gmail.com"))
		if err != nil || got[0].Status != c.GuardClear {
			t.Fatalf("got %+v, %v; want clear after the wait", got, err)
		}
		if k.count() != 2 || time.Since(started) < time.Second {
			t.Errorf("%d calls in %s, want 2 with a second between them", k.count(), time.Since(started))
		}
	})
	t.Run("a second 429 holds", func(t *testing.T) {
		cl, k := limited(t, 5, map[string]string{"Retry-After": "1"})
		got, err := AccountLookup{}.CheckContacts(t.Context(), cl, lookupCreds, contacts("ana@gmail.com"))
		wantHeld(t, got, err, 1, "http_429")
		if _, after := c.GuardFailure(err); after != time.Second {
			t.Errorf("retry after = %s, want 1s", after)
		}
		if k.count() != 2 || c.KindOf(err) != c.ErrQuota {
			t.Errorf("%d calls (%s), want 2 and a quota error", k.count(), c.KindOf(err))
		}
	})
	t.Run("a wait past the budget is not waited out", func(t *testing.T) {
		cl, k := limited(t, 5, map[string]string{"Retry-After": "600"})
		started := time.Now()
		got, err := AccountLookup{}.CheckContacts(t.Context(), cl, lookupCreds, contacts("ana@gmail.com"))
		wantHeld(t, got, err, 1, "http_429")
		if _, after := c.GuardFailure(err); after != 10*time.Minute {
			t.Errorf("retry after = %s, want 10m", after)
		}
		if k.count() != 1 || time.Since(started) > 5*time.Second {
			t.Errorf("%d calls in %s, want 1 at once", k.count(), time.Since(started))
		}
	})
	t.Run("a caller with less time is not kept waiting", func(t *testing.T) {
		cl, k := limited(t, 1, map[string]string{"Retry-After": "3"})
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		started := time.Now()
		got, err := AccountLookup{}.CheckContacts(ctx, cl, lookupCreds, contacts("ana@gmail.com"))
		wantHeld(t, got, err, 1, "http_429")
		if k.count() != 1 || time.Since(started) > time.Second {
			t.Errorf("%d calls in %s, want 1 at once", k.count(), time.Since(started))
		}
	})
	t.Run("RateLimit-Reset when there is no Retry-After, a minute when neither", func(t *testing.T) {
		cl, _ := limited(t, 5, map[string]string{"RateLimit-Reset": "90"})
		_, err := AccountLookup{}.CheckContacts(t.Context(), cl, lookupCreds, contacts("ana@gmail.com"))
		if _, after := c.GuardFailure(err); after != 90*time.Second {
			t.Errorf("retry after = %s, want 90s", after)
		}
		cl, _ = limited(t, 5, nil)
		_, err = AccountLookup{}.CheckContacts(t.Context(), cl, lookupCreds, contacts("ana@gmail.com"))
		if _, after := c.GuardFailure(err); after != time.Minute {
			t.Errorf("retry after = %s, want 1m", after)
		}
	})
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Duration{
		"":                              time.Minute,
		"30":                            30 * time.Second,
		"0":                             time.Second,
		"2.5":                           2500 * time.Millisecond,
		"999999":                        24 * time.Hour,
		"soon":                          time.Minute,
		"-5":                            time.Minute,
		"Wed, 18 Nov 2026 12:02:00 GMT": 2 * time.Minute,
		"2026-11-18T12:00:45Z":          45 * time.Second,
		"Wed, 18 Nov 2026 11:00:00 GMT": time.Second,
	} {
		if got := retryAfter(in, now); got != want {
			t.Errorf("retryAfter(%q) = %s, want %s", in, got, want)
		}
	}
}

// slow never answers until the test ends.
func slow(t *testing.T, id string) *c.Client {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	return &c.Client{BaseURLs: map[string]string{id: srv.URL}}
}

func TestAccountLookupTimeout(t *testing.T) {
	set(t, &lookupCallWait, 100*time.Millisecond)
	got, err := AccountLookup{}.CheckContacts(t.Context(), slow(t, AccountLookupID), lookupCreds, contacts("ana@gmail.com", "ben@acme.example"))
	wantHeld(t, got, err, 2, "timeout")
	if !c.IsTimeout(err) {
		t.Errorf("want a timeout, got %v", err)
	}
}

// redirecting answers every call with a redirect to another server, which
// would clear everyone and must never be asked: the key would go with it.
func redirecting(t *testing.T, id string, status int) (*c.Client, *calls) {
	t.Helper()
	elsewhere := &calls{}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.add([]string{r.Header.Get("Authorization")})
		if r.Method == http.MethodPost {
			writeJSON(w, map[string]any{"success": true, "result": map[string]any{"results": []any{map[string]any{"input": "ana@gmail.com", "status": "not_blocked"}}}})
			return
		}
		writeJSON(w, map[string]any{"success": true, "result": map[string]any{"checkedAt": time.Now().UTC().Format(time.RFC3339),
			"results": []any{lookupRowJSON("ana@gmail.com", "no_match")}}})
	}))
	t.Cleanup(other.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.RequestURI(), status)
	}))
	t.Cleanup(srv.Close)
	return &c.Client{BaseURLs: map[string]string{id: srv.URL}}, elsewhere
}

func TestAccountLookupNeverFollowsARedirect(t *testing.T) {
	for _, status := range []int{301, 302, 307, 308} {
		cl, elsewhere := redirecting(t, AccountLookupID, status)
		got, err := AccountLookup{}.CheckContacts(t.Context(), cl, lookupCreds, contacts("ana@gmail.com"))
		wantHeld(t, got, err, 1, fmt.Sprintf("http_%d", status))
		if elsewhere.count() != 0 {
			t.Errorf("HTTP %d: the redirect was followed %d times", status, elsewhere.count())
		}
	}
}

func TestAccountLookupBatchesOfFifty(t *testing.T) {
	var cs []c.GuardedContact
	for i := range 60 {
		cs = append(cs, c.GuardedContact{Email: fmt.Sprintf("person%d@company%d.example", i, i)})
	}
	// The second call fails: its contacts are held, the others stand.
	k := &calls{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		qs := r.URL.Query()["q"]
		if k.add(qs) == 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		rows := make([]any, len(qs))
		for i, q := range qs {
			rows[i] = lookupRowJSON(q, "no_match")
		}
		writeJSON(w, map[string]any{"success": true, "result": map[string]any{"checkedAt": time.Now().UTC().Format(time.RFC3339), "results": rows}})
	}))
	t.Cleanup(srv.Close)
	got, err := AccountLookup{}.CheckContacts(t.Context(), &c.Client{BaseURLs: map[string]string{AccountLookupID: srv.URL}}, lookupCreds, cs)
	if code, _ := c.GuardFailure(err); code != "http_502" {
		t.Fatalf("failure = %v, want http_502", err)
	}
	if k.count() != 3 || len(k.asks[0]) != 50 || len(k.asks[1]) != 50 || len(k.asks[2]) != 20 {
		t.Fatalf("calls = %d, want 3 of 50, 50 and 20 values", k.count())
	}
	clear, held := 0, 0
	for i, v := range got {
		switch {
		case v.Status == c.GuardClear && (i < 25 || i >= 50):
			clear++
		case v.Status == c.GuardUnknown && v.Detail == "http_502" && i >= 25 && i < 50:
			held++
		default:
			t.Errorf("contact %d: %+v", i, v)
		}
	}
	if clear != 35 || held != 25 {
		t.Errorf("%d clear and %d held, want 35 and 25", clear, held)
	}
}

func TestConnectionAddressRules(t *testing.T) {
	for _, tc := range []struct {
		raw      string
		withPath bool
		want     string
	}{
		{"https://admin.product.example/api/admin/v1/account-lookup/", true, "https://admin.product.example/api/admin/v1/account-lookup"},
		{" https://blocklist.company.example/ ", false, "https://blocklist.company.example"},
		{"https://blocklist.company.example", false, "https://blocklist.company.example"},
		{"https://blocklist.company.example/api", false, ""},
		{"http://blocklist.company.example", false, ""},
		{"https://user:secret@blocklist.company.example", false, ""},
		{"https://blocklist.company.example?key=1", false, ""},
		{"https://blocklist.company.example/?", false, ""},
		{"https://blocklist.company.example#top", false, ""},
		{"https://127.0.0.1", false, ""},
		{"https://[::1]", false, ""},
		{"https://localhost", false, ""},
		{"https://intranet", false, ""},
		{"blocklist.company.example", false, ""},
		{"", false, ""},
	} {
		got, ok := baseURL(tc.raw, tc.withPath)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("baseURL(%q, %v) = %q, %v; want %q", tc.raw, tc.withPath, got, ok, tc.want)
		}
	}
	// A bad address or key asks nobody and clears nobody.
	cl, k := fakeLookup(t, noMatch, nil)
	for code, creds := range map[string]c.Creds{
		"base_url":   {"base_url": "http://admin.product.example/lookup", "api_key": "test_not_a_real_key"},
		"credential": {"base_url": "https://admin.product.example/lookup", "api_key": "two words"},
	} {
		got, err := AccountLookup{}.CheckContacts(t.Context(), cl, creds, contacts("ana@gmail.com"))
		wantHeld(t, got, err, 1, code)
		if c.KindOf(err) != c.ErrInvalid {
			t.Errorf("%s: kind = %s, want invalid", code, c.KindOf(err))
		}
	}
	got, err := OutreachBlocklist{}.CheckContacts(t.Context(), cl, c.Creds{"base_url": "https://blocklist.company.example/v2", "api_key": "test_not_a_real_key"}, contacts("ana@gmail.com"))
	wantHeld(t, got, err, 1, "base_url")
	if k.count() != 0 {
		t.Errorf("%d calls with a bad connection, want none", k.count())
	}
}

func TestConnectionTests(t *testing.T) {
	cl, k := fakeLookup(t, noMatch, nil)
	if acct, err := (AccountLookup{}).Test(t.Context(), cl, lookupCreds); err != nil || acct.Name == "" {
		t.Errorf("account lookup test: %+v, %v", acct, err)
	}
	if k.count() != 1 || strings.Join(k.asks[0], " ") != "example.com" {
		t.Errorf("the test asked %v, want one reserved domain", k.asks)
	}
	cl, _ = raw(t, AccountLookupID, "", 401, nil, "")
	if _, err := (AccountLookup{}).Test(t.Context(), cl, lookupCreds); c.KindOf(err) != c.ErrAuth {
		t.Errorf("a refused key: %v, want an auth error", err)
	}

	cl, bk := fakeBlocklist(t, func(string) string { return "not_blocked" }, nil)
	if acct, err := (OutreachBlocklist{}).Test(t.Context(), cl, blocklistCreds); err != nil || acct.Name == "" {
		t.Errorf("blocklist test: %+v, %v", acct, err)
	}
	if bk.count() != 1 || strings.Join(bk.asks[0], " ") != "connection-test@example.com" {
		t.Errorf("the test asked %v, want one reserved address", bk.asks)
	}
	cl, _ = raw(t, OutreachBlocklistID, "", 403, nil, "")
	if _, err := (OutreachBlocklist{}).Test(t.Context(), cl, blocklistCreds); c.KindOf(err) != c.ErrAuth {
		t.Errorf("a refused key: %v, want an auth error", err)
	}
	for _, g := range All() {
		d := g.Definition()
		if strings.Contains(d.ID, "_") {
			t.Errorf("%s: a guard's id is one word, so the key check takes no common word of it for a vendor's name", d.ID)
		}
		if _, ok := g.(c.ContactGuard); !ok || !d.Has(c.CapContactGuard) || d.Auth != c.AuthAPIKey || len(d.Fields) != 2 ||
			d.Fields[0].Name != "base_url" || d.Fields[1].Name != "api_key" || !d.Fields[1].Secret {
			t.Errorf("%s: want a contact guard connected with base_url and a secret api_key", d.ID)
		}
	}
}

// fakeBlocklist is an outreach blocklist service: answer decides each
// address's status, and edit, when set, changes the results before they
// are sent.
func fakeBlocklist(t *testing.T, answer func(email string) string, edit func(results []any) []any) (*c.Client, *calls) {
	t.Helper()
	k := &calls{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/check" || r.URL.RawQuery != "" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.RequestURI())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test_not_a_real_key" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		var body map[string]json.RawMessage
		b, _ := io.ReadAll(r.Body)
		var emails []string
		if json.Unmarshal(b, &body) != nil || len(body) != 1 || json.Unmarshal(body["emails"], &emails) != nil {
			t.Errorf("body = %s, want only {\"emails\": [...]}", b)
		}
		if len(emails) < 1 || len(emails) > 100 {
			t.Errorf("a call asked about %d addresses, want 1 to 100", len(emails))
		}
		k.add(emails)
		results := make([]any, len(emails))
		for i, e := range emails {
			results[i] = map[string]any{"input": e, "status": answer(e)}
		}
		if edit != nil {
			results = edit(results)
		}
		writeJSON(w, map[string]any{"success": true, "result": map[string]any{"results": results}})
	}))
	t.Cleanup(srv.Close)
	return &c.Client{BaseURLs: map[string]string{OutreachBlocklistID: srv.URL}}, k
}

func TestOutreachBlocklistStatuses(t *testing.T) {
	cl, k := fakeBlocklist(t, func(email string) string {
		switch email {
		case "blocked@acme.example":
			return "blocked"
		case "who@acme.example":
			return "unknown"
		}
		return "not_blocked"
	}, nil)
	got, err := OutreachBlocklist{}.CheckContacts(t.Context(), cl, blocklistCreds,
		contacts("Blocked@Acme.example", "fine@acme.example", "who@acme.example", "nope", "fine@acme.example"))
	if err != nil {
		t.Fatal(err)
	}
	want := []c.ContactVerdict{
		{Email: "Blocked@Acme.example", Status: c.GuardBlocked, Reason: c.GuardBlocklisted},
		{Email: "fine@acme.example", Status: c.GuardClear},
		{Email: "who@acme.example", Status: c.GuardUnknown, Detail: "unknown"},
		{Email: "nope", Status: c.GuardUnknown, Detail: "invalid_email"},
		{Email: "fine@acme.example", Status: c.GuardClear},
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("verdict %d = %+v, want %+v", i, got[i], w)
		}
	}
	if k.count() != 1 || strings.Join(k.asks[0], " ") != "blocked@acme.example fine@acme.example who@acme.example" {
		t.Errorf("asked %v, want each address once in one call", k.asks)
	}
}

func TestOutreachBlocklistRefusesAnswersItCannotTrust(t *testing.T) {
	row := func(rs []any, i int) map[string]any { return rs[i].(map[string]any) }
	for name, edit := range map[string]func(rs []any) []any{
		"wrong order":            func(rs []any) []any { rs[0], rs[1] = rs[1], rs[0]; return rs },
		"another address echoed": func(rs []any) []any { row(rs, 0)["input"] = "someone@else.example"; return rs },
		"one result short":       func(rs []any) []any { return rs[:1] },
		"one result too many":    func(rs []any) []any { return append(rs, rs[0]) },
		"an unknown status":      func(rs []any) []any { row(rs, 1)["status"] = "probably_fine"; return rs },
		"a status missing":       func(rs []any) []any { delete(row(rs, 1), "status"); return rs },
		"a result that is null":  func(rs []any) []any { rs[1] = nil; return rs },
	} {
		t.Run(name, func(t *testing.T) {
			cl, _ := fakeBlocklist(t, func(string) string { return "not_blocked" }, edit)
			got, err := OutreachBlocklist{}.CheckContacts(t.Context(), cl, blocklistCreds, contacts("ana@acme.example", "ben@acme.example"))
			wantHeld(t, got, err, 2, "incomplete_check")
		})
	}
}

func TestOutreachBlocklistRefusesBadResponses(t *testing.T) {
	jsonType := map[string]string{"Content-Type": "application/json"}
	for name, tc := range map[string]struct {
		status int
		header map[string]string
		body   string
		code   string
		kind   string
		after  time.Duration
	}{
		"server error":     {503, jsonType, `{}`, "http_503", c.ErrUnavailable, 0},
		"refused key":      {401, jsonType, `{}`, "http_401", c.ErrAuth, 0},
		"rate limited":     {429, map[string]string{"Retry-After": "120"}, ``, "http_429", c.ErrQuota, 2 * time.Minute},
		"not JSON by type": {200, map[string]string{"Content-Type": "text/plain"}, `{"success":true,"result":{"results":[]}}`, "content_type", c.ErrUnavailable, 0},
		"not JSON":         {200, jsonType, `ok`, "schema", c.ErrUnavailable, 0},
		"success false":    {200, jsonType, `{"success":false,"result":{"results":[]}}`, "schema", c.ErrUnavailable, 0},
		"result null":      {200, jsonType, `{"success":true,"result":null}`, "schema", c.ErrUnavailable, 0},
		"results missing":  {200, jsonType, `{"success":true,"result":{}}`, "schema", c.ErrUnavailable, 0},
	} {
		t.Run(name, func(t *testing.T) {
			cl, k := raw(t, OutreachBlocklistID, "", tc.status, tc.header, tc.body)
			got, err := OutreachBlocklist{}.CheckContacts(t.Context(), cl, blocklistCreds, contacts("ana@acme.example"))
			wantHeld(t, got, err, 1, tc.code)
			if _, after := c.GuardFailure(err); c.KindOf(err) != tc.kind || after != tc.after {
				t.Errorf("kind %s, retry after %s; want %s, %s", c.KindOf(err), after, tc.kind, tc.after)
			}
			if k.count() != 1 {
				t.Errorf("%d calls, want 1: nothing is asked twice", k.count())
			}
		})
	}
}

func TestOutreachBlocklistTimeoutAndRedirect(t *testing.T) {
	set(t, &blocklistWait, 100*time.Millisecond)
	got, err := OutreachBlocklist{}.CheckContacts(t.Context(), slow(t, OutreachBlocklistID), blocklistCreds, contacts("ana@gmail.com"))
	wantHeld(t, got, err, 1, "timeout")

	for _, status := range []int{301, 302, 307, 308} {
		cl, elsewhere := redirecting(t, OutreachBlocklistID, status)
		got, err := OutreachBlocklist{}.CheckContacts(t.Context(), cl, blocklistCreds, contacts("ana@gmail.com"))
		wantHeld(t, got, err, 1, fmt.Sprintf("http_%d", status))
		if elsewhere.count() != 0 {
			t.Errorf("HTTP %d: the redirect was followed %d times", status, elsewhere.count())
		}
	}
}

func TestOutreachBlocklistBatchesOfAHundred(t *testing.T) {
	var cs []c.GuardedContact
	for i := range 250 {
		cs = append(cs, c.GuardedContact{Email: fmt.Sprintf("person%d@acme.example", i)})
	}
	cl, k := fakeBlocklist(t, func(string) string { return "not_blocked" }, nil)
	got, err := OutreachBlocklist{}.CheckContacts(t.Context(), cl, blocklistCreds, cs)
	if err != nil || k.count() != 3 || len(k.asks[0]) != 100 || len(k.asks[2]) != 50 {
		t.Fatalf("calls = %d, err = %v; want 3 calls of 100, 100 and 50", k.count(), err)
	}
	for i, v := range got {
		if v.Status != c.GuardClear {
			t.Fatalf("contact %d: %+v", i, v)
		}
	}

	// A failed call clears nobody it was about, and nothing after it is
	// asked.
	n := &calls{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Emails []string `json:"emails"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if n.add(body.Emails) == 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		results := make([]any, len(body.Emails))
		for i, e := range body.Emails {
			results[i] = map[string]any{"input": e, "status": "not_blocked"}
		}
		writeJSON(w, map[string]any{"success": true, "result": map[string]any{"results": results}})
	}))
	t.Cleanup(srv.Close)
	got, err = OutreachBlocklist{}.CheckContacts(t.Context(), &c.Client{BaseURLs: map[string]string{OutreachBlocklistID: srv.URL}}, blocklistCreds, cs)
	if code, _ := c.GuardFailure(err); code != "http_500" || n.count() != 2 {
		t.Fatalf("failure = %v after %d calls, want http_500 after 2", err, n.count())
	}
	for i, v := range got {
		if want := c.GuardClear; i < 100 && v.Status != want {
			t.Errorf("contact %d: %+v, want clear", i, v)
		}
		if i >= 100 && (v.Status != c.GuardUnknown || v.Detail != "http_500") {
			t.Errorf("contact %d: %+v, want unknown (http_500)", i, v)
		}
	}
}

// A company asked about alone, before anyone there is known: its domain is
// asked, and a mailbox provider's domain never is.
func TestAccountLookupCompanyAlone(t *testing.T) {
	cl, k := fakeLookup(t, func(q string) string {
		if q == "customer.example" {
			return "match"
		}
		return "no_match"
	}, nil)
	got, err := AccountLookup{}.CheckContacts(t.Context(), cl, lookupCreds, []c.GuardedContact{
		{Domain: "https://www.customer.example/pricing"},
		{Domain: "prospect.example"},
		{Domain: "gmail.com"},
		{Domain: "not a domain"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []c.ContactVerdict{
		{Status: c.GuardBlocked, Reason: c.GuardCustomer},
		{Status: c.GuardClear},
		{Status: c.GuardUnknown, Detail: "mailbox_domain"},
		{Status: c.GuardUnknown, Detail: "invalid_domain"},
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("verdict %d = %+v, want %+v", i, got[i], w)
		}
	}
	if k.count() != 1 || strings.Join(k.asks[0], " ") != "customer.example prospect.example" {
		t.Errorf("asked %v, want one call for the two company domains", k.asks)
	}
}

// exportServer is a blocklist service with a domains export: body and
// generated are what it serves, status its status. It counts downloads.
type exportServer struct {
	mu        sync.Mutex
	status    int
	body      string
	generated string
	ctype     string
	downloads int
}

func (e *exportServer) set(f func(e *exportServer)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	f(e)
}

func (e *exportServer) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.downloads
}

func fakeExport(t *testing.T, e *exportServer, check func(email string) string) *c.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test_not_a_real_key" {
			t.Errorf("Authorization = %q", got)
		}
		if r.URL.Path == "/api/v1/check" {
			var body struct {
				Emails []string `json:"emails"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			results := make([]any, len(body.Emails))
			for i, em := range body.Emails {
				results[i] = map[string]any{"input": em, "status": check(em)}
			}
			writeJSON(w, map[string]any{"success": true, "result": map[string]any{"results": results}})
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/exports" || r.URL.Query().Get("format") != "csv" || r.URL.Query().Get("view") != "domains" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.RequestURI())
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		e.downloads++
		if e.status != http.StatusOK {
			w.WriteHeader(e.status)
			return
		}
		w.Header().Set("Content-Type", e.ctype)
		w.Header().Set("X-Generated-At", e.generated)
		_, _ = io.WriteString(w, e.body)
	}))
	t.Cleanup(srv.Close)
	return &c.Client{BaseURLs: map[string]string{OutreachBlocklistID: srv.URL}}
}

// A company is checked against the blocklist's domains export, taken once
// a UTC day and used only while it is under a day old.
func TestOutreachBlocklistCompanyDomains(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	set(t, &clock, func() time.Time { return now })
	e := &exportServer{status: http.StatusOK, ctype: "text/csv; charset=utf-8", body: "domain\nblocked.example\nparent.example\n",
		generated: now.Add(-time.Hour).Format(time.RFC3339)}
	cl := fakeExport(t, e, func(email string) string {
		if email == "ana@listed.example" {
			return "blocked"
		}
		return "not_blocked"
	})
	ask := func() ([]c.ContactVerdict, error) {
		return OutreachBlocklist{}.CheckContacts(t.Context(), cl, blocklistCreds, []c.GuardedContact{
			{Domain: "https://www.blocked.example/about"},
			{Domain: "fine.example"},
			{Domain: "shop.parent.example"},
			{Email: "ana@listed.example"},
		})
	}
	got, err := ask()
	if err != nil {
		t.Fatal(err)
	}
	want := []c.ContactVerdict{
		{Status: c.GuardBlocked, Reason: c.GuardBlocklisted},
		{Status: c.GuardClear},
		{Status: c.GuardBlocked, Reason: c.GuardBlocklisted},
		{Email: "ana@listed.example", Status: c.GuardBlocked, Reason: c.GuardBlocklisted},
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("verdict %d = %+v, want %+v", i, got[i], w)
		}
	}
	// The same day: the snapshot is used again, not downloaded.
	now = now.Add(10 * time.Hour)
	if _, err := ask(); err != nil || e.count() != 1 {
		t.Fatalf("downloads = %d (%v), want the one snapshot reused the same day", e.count(), err)
	}

	// The next day it is downloaded again; a failed download keeps the
	// last snapshot while its export is under a day old.
	now = now.Add(8 * time.Hour) // 03:00 the next day, the export is 18h old
	e.set(func(e *exportServer) { e.status = http.StatusServiceUnavailable })
	got, err = ask()
	if err != nil || got[1].Status != c.GuardClear || e.count() != 2 {
		t.Fatalf("got %+v, %v after %d downloads; want the last snapshot still used", got, err, e.count())
	}
	// Not asked again within a minute of the failure.
	if _, _ = ask(); e.count() != 2 {
		t.Errorf("downloads = %d, want no new try within a minute", e.count())
	}
	// Once the export is a day old, nobody is cleared by it.
	now = now.Add(7 * time.Hour)
	got, err = ask()
	if err == nil || got[0].Status != c.GuardUnknown || got[1].Status != c.GuardUnknown || got[1].Detail != "http_503" {
		t.Errorf("got %+v, %v; want every company unknown (http_503)", got, err)
	}
	if got[3].Status != c.GuardBlocked {
		t.Errorf("the address = %+v, want still checked live", got[3])
	}

	// A fresh export clears again.
	now = now.Add(2 * time.Minute)
	e.set(func(e *exportServer) {
		e.status, e.generated = http.StatusOK, now.Add(-time.Minute).UTC().Format(http.TimeFormat)
	})
	if got, err = ask(); err != nil || got[1].Status != c.GuardClear {
		t.Errorf("got %+v, %v; want clear from the new export", got, err)
	}
}

// An export that is old, or not the export, clears nobody.
func TestOutreachBlocklistRefusesBadExports(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	set(t, &clock, func() time.Time { return now })
	fresh := now.Add(-time.Hour).Format(time.RFC3339)
	for name, tc := range map[string]struct {
		edit func(e *exportServer)
		code string
	}{
		"older than a day":    {func(e *exportServer) { e.generated = now.Add(-25 * time.Hour).Format(time.RFC3339) }, "stale_export"},
		"dated ahead":         {func(e *exportServer) { e.generated = now.Add(time.Hour).Format(time.RFC3339) }, "stale_export"},
		"no generated time":   {func(e *exportServer) { e.generated = "" }, "schema"},
		"not csv":             {func(e *exportServer) { e.ctype = "application/json" }, "content_type"},
		"another column":      {func(e *exportServer) { e.body = "email\nana@blocked.example\n" }, "schema"},
		"two columns":         {func(e *exportServer) { e.body = "domain,reason\nblocked.example,x\n" }, "schema"},
		"not a domain":        {func(e *exportServer) { e.body = "domain\nnot a domain\n" }, "schema"},
		"refused export":      {func(e *exportServer) { e.status = http.StatusForbidden }, "export_refused"},
		"empty body, no head": {func(e *exportServer) { e.body = "" }, "schema"},
	} {
		t.Run(name, func(t *testing.T) {
			e := &exportServer{status: http.StatusOK, ctype: "text/csv", body: "domain\nblocked.example\n", generated: fresh}
			tc.edit(e)
			cl := fakeExport(t, e, func(string) string { return "not_blocked" })
			got, err := OutreachBlocklist{}.CheckContacts(t.Context(), cl, blocklistCreds, []c.GuardedContact{{Domain: "fine.example"}})
			wantHeld(t, got, err, 1, tc.code)
		})
	}
}

// A 503 that names a time to come back is a busy service, like a 429; one
// that names none is down, and other answers are neither.
func TestBusyAfter(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	h := func(v string) http.Header {
		out := http.Header{}
		if v != "" {
			out.Set("Retry-After", v)
		}
		return out
	}
	for _, tc := range []struct {
		status int
		after  string
		want   time.Duration
	}{
		{429, "7", 7 * time.Second}, {429, "", time.Minute}, {503, "20", 20 * time.Second}, {503, "", 0}, {500, "20", 0}, {200, "", 0},
	} {
		if got := busyAfter(tc.status, h(tc.after), now); got != tc.want {
			t.Errorf("HTTP %d Retry-After %q: %s, want %s", tc.status, tc.after, got, tc.want)
		}
	}
}
