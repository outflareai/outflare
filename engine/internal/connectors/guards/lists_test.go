package guards

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// fakeFeed is an account lookup whose feed serves accounts (each an
// address, or "" for none) a page at a time; the cursor is the index of
// the next account, written as a number. edit, when set, changes a page's
// result before it is sent.
func fakeFeed(t *testing.T, accounts []map[string]any, edit func(result map[string]any)) (*c.Client, *calls) {
	t.Helper()
	k := &calls{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/admin/v1/account-lookup/accounts" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test_not_a_real_key" {
			t.Errorf("Authorization = %q", got)
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		after := r.URL.Query().Get("after")
		k.add([]string{after})
		from := 0
		if after != "" {
			from, _ = strconv.Atoi(after)
		}
		to := min(from+limit, len(accounts))
		page := accounts[from:to]
		var next any
		if len(page) > 0 {
			next = strconv.Itoa(to)
		} else if after != "" {
			next = after
		}
		result := map[string]any{"accounts": page, "nextCursor": next, "hasMore": to < len(accounts)}
		if edit != nil {
			edit(result)
		}
		writeJSON(w, map[string]any{"success": true, "result": result})
	}))
	t.Cleanup(srv.Close)
	return &c.Client{BaseURLs: map[string]string{AccountLookupID: srv.URL + "/api/admin/v1/account-lookup"}}, k
}

func account(email any, free bool) map[string]any {
	return map[string]any{"id": "a1", "email": email, "emailDomain": nil, "freeEmailProvider": free, "companyName": "Kept out", "deleted": false}
}

// read reads a whole list and returns what emit got, as kind:value, and
// the cursors it was given.
func read(t *testing.T, g c.GuardLister, cl *c.Client, creds c.Creds, cursor string, pages int) ([]string, []string, bool, error) {
	t.Helper()
	var got, cursors []string
	more, err := g.ReadGuardList(context.Background(), cl, creds, cursor, pages, func(batch []c.GuardListEntry, next string) error {
		for _, e := range batch {
			kind := "email"
			if e.Domain {
				kind = "domain"
			}
			got = append(got, kind+":"+e.Value)
		}
		cursors = append(cursors, next)
		return nil
	})
	return got, cursors, more, err
}

// The account feed is read a page at a time from a cursor. Each address
// joins the list, and its company's domain unless that is a mailbox
// provider's; nothing else about an account is kept.
func TestAccountFeed(t *testing.T) {
	set(t, &feedPage, 2)
	cl, k := fakeFeed(t, []map[string]any{
		account("Ana@Customer-One.example", false),
		account("someone@gmail.com", true),
		account(nil, false),
		account("bo@mail.customer-two.example", false),
		account("cy@private.example", true),
	}, nil)
	g := AccountLookup{}
	if info := g.GuardList(); info.Mode != c.GuardListFeed || info.Reason != c.GuardCustomer || info.Every != 5*time.Minute {
		t.Fatalf("GuardList %+v", info)
	}
	got, cursors, more, err := read(t, g, cl, lookupCreds, "", 2)
	if err != nil || !more {
		t.Fatalf("first two pages: more %v, %v", more, err)
	}
	want := []string{"email:ana@customer-one.example", "domain:customer-one.example", "email:someone@gmail.com",
		"email:bo@mail.customer-two.example", "domain:customer-two.example"}
	if !slices.Equal(got, want) || !slices.Equal(cursors, []string{"2", "4"}) {
		t.Fatalf("got %v with cursors %v, want %v", got, cursors, want)
	}
	// Carrying on from the cursor reads the rest.
	got, cursors, more, err = read(t, g, cl, lookupCreds, "4", 5)
	if err != nil || more {
		t.Fatalf("the rest: more %v, %v", more, err)
	}
	// freeEmailProvider: the account's own word that its domain is a
	// mailbox provider's keeps it off too.
	if !slices.Equal(got, []string{"email:cy@private.example"}) || !slices.Equal(cursors, []string{"5"}) {
		t.Fatalf("got %v with cursors %v", got, cursors)
	}
	if k.asks[0][0] != "" || k.asks[1][0] != "2" || k.asks[2][0] != "4" {
		t.Fatalf("asked after %v", k.asks)
	}
	// At the end: an empty page keeps the cursor, for accounts added later.
	got, cursors, more, err = read(t, g, cl, lookupCreds, "5", 5)
	if err != nil || more || len(got) != 0 || !slices.Equal(cursors, []string{"5"}) {
		t.Fatalf("at the end: %v %v %v %v", got, cursors, more, err)
	}
	// The subdomain's address: the company is its registrable domain.
	got, _, _, _ = read(t, g, cl, lookupCreds, "2", 1)
	if !slices.Equal(got, []string{"email:bo@mail.customer-two.example", "domain:customer-two.example"}) {
		t.Fatalf("got %v", got)
	}
}

// A feed answer that would lose or repeat accounts is refused.
func TestAccountFeedRefusesBadCursors(t *testing.T) {
	for name, edit := range map[string]func(map[string]any){
		"more with no cursor": func(r map[string]any) { r["nextCursor"] = nil },
		"cursor that stays":   func(r map[string]any) { r["nextCursor"] = "" },
		"no hasMore":          func(r map[string]any) { delete(r, "hasMore") },
		"accounts not a list": func(r map[string]any) { r["accounts"] = "x" },
		"too many on one page": func(r map[string]any) {
			r["accounts"] = append(r["accounts"].([]map[string]any), account("x@y.example", false))
		},
	} {
		t.Run(name, func(t *testing.T) {
			set(t, &feedPage, 1)
			cl, _ := fakeFeed(t, []map[string]any{account("ana@customer.example", false), account("bo@customer.example", false)}, edit)
			_, _, _, err := read(t, AccountLookup{}, cl, lookupCreds, "", 3)
			if err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// listExports is a blocklist service with both exports.
func listExports(t *testing.T, emails, domains string, generated time.Time) *c.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/exports" || r.URL.Query().Get("format") != "csv" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.RequestURI())
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("X-Generated-At", generated.Format(time.RFC3339))
		switch r.URL.Query().Get("view") {
		case "emails":
			_, _ = io.WriteString(w, emails)
		case "domains":
			_, _ = io.WriteString(w, domains)
		default:
			t.Errorf("view %q", r.URL.Query().Get("view"))
		}
	}))
	t.Cleanup(srv.Close)
	return &c.Client{BaseURLs: map[string]string{OutreachBlocklistID: srv.URL}}
}

// The blocklist's list is both its exports, read whole, in batches.
func TestBlocklistExports(t *testing.T) {
	now := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	set(t, &clock, func() time.Time { return now })
	set(t, &listBatch, 2)
	g := OutreachBlocklist{}
	if info := g.GuardList(); info.Mode != c.GuardListSnapshot || info.Reason != c.GuardBlocklisted || info.Every != 30*time.Minute {
		t.Fatalf("GuardList %+v", info)
	}
	cl := listExports(t, "email\nNo@Blocked.example\nnever@blocked.example\n\nalso@blocked.example\n", "\ufeffdomain\nblocked.example\n", now.Add(-time.Hour))
	got, cursors, more, err := read(t, g, cl, blocklistCreds, "ignored", 1)
	if err != nil || more {
		t.Fatalf("more %v, %v", more, err)
	}
	want := []string{"email:no@blocked.example", "email:never@blocked.example", "email:also@blocked.example", "domain:blocked.example"}
	if !slices.Equal(got, want) || len(cursors) != 3 {
		t.Fatalf("got %v in %d batches, want %v in 3", got, len(cursors), want)
	}

	// An export over a day old, or not the export's shape, fails whole.
	for name, cl := range map[string]*c.Client{
		"stale":       listExports(t, "email\na@b.example\n", "domain\nb.example\n", now.Add(-25*time.Hour)),
		"bad email":   listExports(t, "email\nnot an address\n", "domain\nb.example\n", now),
		"bad column":  listExports(t, "address\na@b.example\n", "domain\nb.example\n", now),
		"bad domain":  listExports(t, "email\na@b.example\n", "domain\nhttps://b.example/x\n", now),
		"two columns": listExports(t, "email\na@b.example\n", "domain,x\nb.example,1\n", now),
	} {
		if _, _, _, err := read(t, g, cl, blocklistCreds, "", 1); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// One cut off at the size limit fails rather than reading as shorter.
	set(t, &listExportMaxBytes, 30)
	if _, _, _, err := read(t, g, listExports(t, "email\n"+strings.Repeat("a@blocked.example\n", 5), "domain\n", now), blocklistCreds, "", 1); err == nil {
		t.Error("an export over the limit was accepted")
	}
}
