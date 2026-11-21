package guardsync_test

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/db"
	"github.com/outflareai/outflare/engine/internal/guardsync"
	"github.com/outflareai/outflare/engine/internal/secrets"
	"github.com/outflareai/outflare/engine/internal/track"
)

// feedGuard is a guard whose list is a feed held in memory: each page is
// a slice of entries, and the cursor is the number of pages read.
type feedGuard struct {
	pages [][]connectors.GuardListEntry
	// from are the cursors each read started at.
	from []string
	fail error
}

func (f *feedGuard) Definition() connectors.Definition {
	return connectors.Definition{ID: "fakelookup", Name: "Fake lookup", Category: connectors.CategoryData, Auth: connectors.AuthAPIKey,
		Fields: []connectors.Field{{Name: "api_key", Label: "API key", Secret: true}}, Capabilities: []string{connectors.CapContactGuard}}
}

func (f *feedGuard) Test(context.Context, *connectors.Client, connectors.Creds) (connectors.Account, error) {
	return connectors.Account{}, nil
}

func (f *feedGuard) CheckContacts(context.Context, *connectors.Client, connectors.Creds, []connectors.GuardedContact) ([]connectors.ContactVerdict, error) {
	return nil, errors.New("not asked in these tests")
}

func (f *feedGuard) GuardList() connectors.GuardListInfo {
	return connectors.GuardListInfo{Mode: connectors.GuardListFeed, Reason: connectors.GuardCustomer, Every: 5 * time.Minute}
}

func (f *feedGuard) ReadGuardList(_ context.Context, _ *connectors.Client, _ connectors.Creds, cursor string, pages int,
	emit func([]connectors.GuardListEntry, string) error) (bool, error) {
	f.from = append(f.from, cursor)
	if f.fail != nil {
		return true, f.fail
	}
	at := 0
	if cursor != "" {
		at, _ = strconv.Atoi(cursor)
	}
	for range pages {
		if at >= len(f.pages) {
			return false, emit(nil, strconv.Itoa(at))
		}
		if err := emit(f.pages[at], strconv.Itoa(at+1)); err != nil {
			return true, err
		}
		at++
	}
	return at < len(f.pages), nil
}

// snapGuard is a guard whose list is a snapshot held in memory.
type snapGuard struct {
	list []connectors.GuardListEntry
	fail error
}

func (s *snapGuard) Definition() connectors.Definition {
	return connectors.Definition{ID: "fakeblock", Name: "Fake blocklist", Category: connectors.CategoryData, Auth: connectors.AuthAPIKey,
		Fields: []connectors.Field{{Name: "api_key", Label: "API key", Secret: true}}, Capabilities: []string{connectors.CapContactGuard}}
}

func (s *snapGuard) Test(context.Context, *connectors.Client, connectors.Creds) (connectors.Account, error) {
	return connectors.Account{}, nil
}

func (s *snapGuard) CheckContacts(context.Context, *connectors.Client, connectors.Creds, []connectors.GuardedContact) ([]connectors.ContactVerdict, error) {
	return nil, errors.New("not asked in these tests")
}

func (s *snapGuard) GuardList() connectors.GuardListInfo {
	return connectors.GuardListInfo{Mode: connectors.GuardListSnapshot, Reason: connectors.GuardBlocklisted, Every: 30 * time.Minute}
}

func (s *snapGuard) ReadGuardList(_ context.Context, _ *connectors.Client, _ connectors.Creds, _ string, _ int,
	emit func([]connectors.GuardListEntry, string) error) (bool, error) {
	// Two batches, so a read that fails after the first changes nothing.
	half := len(s.list) / 2
	if err := emit(s.list[:half], ""); err != nil {
		return false, err
	}
	if s.fail != nil {
		return false, s.fail
	}
	return false, emit(s.list[half:], "")
}

type env struct {
	pool     *pgxpool.Pool
	svc      *guardsync.Service
	feed     *feedGuard
	snap     *snapGuard
	reg      *connectors.Registry
	ws       string
	lookup   string // the feed guard's connection
	blocking string // the snapshot guard's connection
	now      time.Time
}

func setup(t *testing.T) *env {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE workspaces, jobs CASCADE`); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.NewBox(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{pool: pool, feed: &feedGuard{}, snap: &snapGuard{}, now: time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)}
	e.reg = connectors.NewRegistry([]connectors.Connector{e.feed, e.snap}, nil)
	store := &connectors.Store{Pool: pool, Box: box, Registry: e.reg, Client: &connectors.Client{}}
	e.svc = &guardsync.Service{Pool: pool, Store: store, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return e.now }}
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO workspaces (name, slug) VALUES ('Acme', 'acme') RETURNING id::text`).Scan(&e.ws); err != nil {
			return err
		}
		i, err := store.Insert(ctx, tx, e.ws, "fakelookup", connectors.AuthAPIKey, "Product", connectors.Creds{"api_key": "oft_test_not_a_real_token"}, connectors.Account{}, "")
		if err != nil {
			return err
		}
		e.lookup = i.ID
		i, err = store.Insert(ctx, tx, e.ws, "fakeblock", connectors.AuthAPIKey, "Blocklist", connectors.Creds{"api_key": "oft_test_not_a_real_token"}, connectors.Account{}, "")
		e.blocking = i.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func named(events []track.Event, name string) int {
	n := 0
	for _, e := range events {
		if e.Name == name {
			n++
		}
	}
	return n
}

func email(v string) connectors.GuardListEntry { return connectors.GuardListEntry{Value: v} }
func domain(v string) connectors.GuardListEntry {
	return connectors.GuardListEntry{Value: v, Domain: true}
}

// entries are the opt-out list's entries, as kind:value:reason:source.
func (e *env) entries(t *testing.T) []string {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT kind || ':' || value || ':' || reason || ':' || source FROM suppressions WHERE workspace_id = $1 ORDER BY 1`, e.ws)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A feed's first sync reads it all, in runs that carry on from their
// cursor; the next sync reads only what came after it.
func TestFeedResumesFromItsCursor(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	old := guardsync.FeedPages
	guardsync.FeedPages = 2
	t.Cleanup(func() { guardsync.FeedPages = old })
	e.feed.pages = [][]connectors.GuardListEntry{
		{email("ana@customer-one.example"), domain("customer-one.example")},
		{email("bo@customer-two.example"), domain("customer-two.example")},
		{email("cy@customer-three.example")},
	}
	events := track.Capture(t)

	res, err := e.svc.Sync(ctx, e.ws, e.lookup)
	if err != nil {
		t.Fatal(err)
	}
	if !res.More || res.Added != 4 {
		t.Fatalf("first run %+v, want 4 added and more to read", res)
	}
	fresh, err := guardsync.Fresh(ctx, e.pool, e.reg, e.ws, e.lookup, "fakelookup", e.now)
	if err != nil || fresh {
		t.Fatalf("fresh = %v, %v before the whole feed was read", fresh, err)
	}
	res, err = e.svc.Sync(ctx, e.ws, e.lookup)
	if err != nil || res.More || res.Added != 1 {
		t.Fatalf("second run %+v, %v: want the last page and the end", res, err)
	}
	if got := e.feed.from; !slices.Equal(got, []string{"", "2"}) {
		t.Fatalf("reads started at %v, want the beginning and then the cursor after two pages", got)
	}
	want := []string{
		"domain:customer-one.example:customer:fakelookup_sync",
		"domain:customer-two.example:customer:fakelookup_sync",
		"email:ana@customer-one.example:customer:fakelookup_sync",
		"email:bo@customer-two.example:customer:fakelookup_sync",
		"email:cy@customer-three.example:customer:fakelookup_sync",
	}
	if got := e.entries(t); !slices.Equal(got, want) {
		t.Fatalf("entries %v, want %v", got, want)
	}
	if fresh, err := guardsync.Fresh(ctx, e.pool, e.reg, e.ws, e.lookup, "fakelookup", e.now); err != nil || !fresh {
		t.Fatalf("fresh = %v, %v after the whole feed was read", fresh, err)
	}
	// Stale after twice its interval without a success.
	if fresh, _ := guardsync.Fresh(ctx, e.pool, e.reg, e.ws, e.lookup, "fakelookup", e.now.Add(11*time.Minute)); fresh {
		t.Fatal("fresh after twice its interval")
	}

	// A new account later: only it is read.
	e.feed.pages = append(e.feed.pages, []connectors.GuardListEntry{email("di@customer-four.example")})
	e.now = e.now.Add(5 * time.Minute)
	res, err = e.svc.Sync(ctx, e.ws, e.lookup)
	if err != nil || res.Added != 1 {
		t.Fatalf("third run %+v, %v", res, err)
	}
	if got := e.feed.from[2]; got != "3" {
		t.Fatalf("the third read started at %q, want after the last page", got)
	}
	var last, added, entries int
	var at *time.Time
	if err := e.pool.QueryRow(ctx, `SELECT last_added, added_count, entries, succeeded_at FROM guard_list_syncs WHERE integration_id = $1`, e.lookup).
		Scan(&last, &added, &entries, &at); err != nil {
		t.Fatal(err)
	}
	if last != 1 || added != 6 || entries != 6 || at == nil || !at.Equal(e.now) {
		t.Fatalf("recorded last_added %d, added_count %d, entries %d, succeeded_at %v", last, added, entries, at)
	}
	if n := named(events(), "guard_list.synced"); n != 3 {
		// Three runs, each with something new.
		t.Fatalf("%d guard_list.synced events, want 3", n)
	}
}

// A mailbox provider's domain never joins the list as a domain: one
// customer with a Gmail address must not stop every Gmail user.
func TestFreeMailDomainNeverAdded(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.feed.pages = [][]connectors.GuardListEntry{{email("someone@gmail.com"), domain("gmail.com"), domain("outlook.com"), domain("customer.example")}}
	if _, err := e.svc.Sync(ctx, e.ws, e.lookup); err != nil {
		t.Fatal(err)
	}
	want := []string{"domain:customer.example:customer:fakelookup_sync", "email:someone@gmail.com:customer:fakelookup_sync"}
	if got := e.entries(t); !slices.Equal(got, want) {
		t.Fatalf("entries %v, want %v", got, want)
	}
	if _, ok, _ := guardsync.CompanyListed(ctx, e.pool, e.ws, e.lookup, "fakelookup", "gmail.com"); ok {
		t.Fatal("the copy answered for a mailbox provider's domain")
	}
	if listed, ok, err := guardsync.CompanyListed(ctx, e.pool, e.ws, e.lookup, "fakelookup", "app.customer.example"); err != nil || !ok || !listed {
		t.Fatalf("a customer's subdomain: listed %v, ok %v, %v", listed, ok, err)
	}
	if listed, ok, err := guardsync.CompanyListed(ctx, e.pool, e.ws, e.lookup, "fakelookup", "prospect.example"); err != nil || !ok || listed {
		t.Fatalf("a prospect: listed %v, ok %v, %v", listed, ok, err)
	}
}

// A snapshot is read whole each time: what left it is removed, but only
// entries this sync added; one a person added stays.
func TestSnapshotDiffRemovesOnlyItsOwn(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `INSERT INTO suppressions (workspace_id, kind, value, reason, source) VALUES ($1, 'domain', 'kept.example', 'manual', 'manual')`, e.ws); err != nil {
		t.Fatal(err)
	}
	e.snap.list = []connectors.GuardListEntry{email("no@blocked.example"), domain("blocked.example"), domain("gone.example"), domain("kept.example")}
	res, err := e.svc.Sync(ctx, e.ws, e.blocking)
	if err != nil || res.Added != 3 || res.Removed != 0 {
		t.Fatalf("first sync %+v, %v", res, err)
	}
	// gone.example and kept.example leave the export.
	e.snap.list = []connectors.GuardListEntry{email("no@blocked.example"), domain("blocked.example"), domain("new.example")}
	e.now = e.now.Add(30 * time.Minute)
	events := track.Capture(t)
	res, err = e.svc.Sync(ctx, e.ws, e.blocking)
	if err != nil || res.Added != 1 || res.Removed != 1 {
		t.Fatalf("second sync %+v, %v: want new.example added and gone.example removed", res, err)
	}
	want := []string{
		"domain:blocked.example:blocklist:fakeblock_sync",
		"domain:kept.example:manual:manual",
		"domain:new.example:blocklist:fakeblock_sync",
		"email:no@blocked.example:blocklist:fakeblock_sync",
	}
	if got := e.entries(t); !slices.Equal(got, want) {
		t.Fatalf("entries %v, want %v", got, want)
	}
	var removed int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'suppressions.removed'`, e.ws).Scan(&removed); err != nil || removed != 1 {
		t.Fatalf("suppressions.removed recorded %d times, %v", removed, err)
	}
	if named(events(), "guard_list.synced") != 1 {
		t.Fatal("no guard_list.synced event")
	}

	// A read that fails half way changes nothing, and is recorded.
	e.snap.list = []connectors.GuardListEntry{domain("other.example"), domain("another.example")}
	e.snap.fail = &connectors.GuardError{Code: "http_503", Err: connectors.Errf(connectors.ErrUnavailable, "down")}
	e.now = e.now.Add(30 * time.Minute)
	if _, err := e.svc.Sync(ctx, e.ws, e.blocking); err == nil {
		t.Fatal("a failed read reported no error")
	}
	if got := e.entries(t); !slices.Equal(got, want) {
		t.Fatalf("a failed read changed the list: %v", got)
	}
	var code *string
	var retry *time.Time
	if err := e.pool.QueryRow(ctx, `SELECT error_code, retry_at FROM guard_list_syncs WHERE integration_id = $1`, e.blocking).Scan(&code, &retry); err != nil {
		t.Fatal(err)
	}
	if code == nil || *code != "connector_unavailable" || retry == nil || !retry.After(e.now) {
		t.Fatalf("error_code %v, retry_at %v", code, retry)
	}
	if named(events(), "guard_list.sync_failed") != 1 {
		t.Fatal("no guard_list.sync_failed event")
	}
}

// Only one sync of a connection runs at a time, and one turned off never
// runs; due connections are queued.
func TestLeaseOffAndQueue(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if err := e.svc.QueueDue(ctx); err != nil {
		t.Fatal(err)
	}
	var queued int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = $1`, guardsync.JobSync).Scan(&queued); err != nil || queued != 2 {
		t.Fatalf("%d jobs queued, %v; want one per guard", queued, err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO guard_list_syncs (integration_id, workspace_id, running_until) VALUES ($1, $2, $3)`,
		e.lookup, e.ws, e.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Sync(ctx, e.ws, e.lookup); !errors.Is(err, guardsync.ErrBusy) {
		t.Fatalf("a second sync: %v, want ErrBusy", err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE guard_list_syncs SET running_until = NULL, enabled = false WHERE integration_id = $1`, e.lookup); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Sync(ctx, e.ws, e.lookup); !errors.Is(err, guardsync.ErrOff) {
		t.Fatalf("a sync turned off: %v, want ErrOff", err)
	}
}
