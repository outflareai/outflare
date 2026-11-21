package outflare

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// testGuard is a contact guard: it blocks the addresses in block with its
// reason, fails when fail is set, and answers for one contact too few
// when short is set.
type testGuard struct {
	id, reason string
	block      map[string]bool
	fail       error
	short      bool
	got        *[]string
}

func (g testGuard) Definition() connectors.Definition {
	return connectors.Definition{ID: g.id, Name: "Test guard", Category: connectors.CategoryData, Auth: connectors.AuthAPIKey,
		Capabilities: []string{CapContactGuard}}
}
func (testGuard) Test(context.Context, *connectors.Client, connectors.Creds) (connectors.Account, error) {
	return connectors.Account{}, nil
}
func (g testGuard) CheckContacts(_ context.Context, _ *connectors.Client, cr connectors.Creds, cs []GuardedContact) ([]ContactVerdict, error) {
	if cr["api_key"] != "test-not-a-real-key" {
		return nil, errors.New("wrong connection credential")
	}
	*g.got = append(*g.got, g.id)
	out := make([]ContactVerdict, len(cs))
	for i, c := range cs {
		out[i] = ContactVerdict{Email: c.Email, Status: GuardClear}
		switch {
		case g.fail != nil:
			out[i] = ContactVerdict{Email: c.Email, Status: GuardUnknown, Detail: "http_401"}
		case g.block[c.Email]:
			out[i] = ContactVerdict{Email: c.Email, Status: GuardBlocked, Reason: g.reason}
		}
	}
	if g.short {
		out = out[:len(out)-1]
	}
	return out, g.fail
}

// ContactGuards returns every guard that serves the brand, the preferred
// connectors first, and never leaves one out: a guard whose key was
// refused or that can't be opened still answers, with unknown for
// everyone, so a broken guard holds people instead of letting them
// through.
func TestContactGuards(t *testing.T) {
	var got []string
	down := &connectors.GuardError{Err: connectors.Errf(connectors.ErrAuth, "the key was refused"), Code: "http_401", RetryAfter: time.Minute}
	e := newConnEnv(t,
		testGuard{id: "guard_customers", reason: GuardCustomer, block: map[string]bool{"ana@customer.example": true}, got: &got},
		testGuard{id: "guard_blocklist", reason: GuardBlocklisted, block: map[string]bool{"ben@blocked.example": true}, got: &got},
		testGuard{id: "guard_down", fail: down, got: &got},
		testGuard{id: "guard_short", short: true, got: &got},
		testSequencer{})
	ctx := context.Background()
	cs := e.h.Connections()
	if gs, err := cs.ContactGuards(ctx, e.ws, nil); err != nil || len(gs) != 0 {
		t.Fatalf("no guard connected: %d guards, %v; want none and no error", len(gs), err)
	}
	blocklist := e.connect("guard_blocklist", "")
	e.connect("test_sequencer", "") // another capability
	customers := e.connect("guard_customers", "")

	gs, err := cs.ContactGuards(ctx, e.ws, nil, "guard_customers")
	if err != nil || len(gs) != 2 || gs[0].ID != customers || gs[1].ID != blocklist || gs[0].Connector != "guard_customers" {
		t.Fatalf("guards = %+v, %v; want the preferred one, then the other", gs, err)
	}
	people := []GuardedContact{{Email: "ana@customer.example"}, {Email: "ben@blocked.example", Domain: "blocked.example"}, {Email: "cy@prospect.example"}}
	v, err := gs[0].Check(ctx, people, "run-1")
	if err != nil || len(v) != 3 || v[0].Status != GuardBlocked || v[0].Reason != GuardCustomer || v[1].Status != GuardClear || v[2].Status != GuardClear {
		t.Fatalf("customer guard: %+v, %v", v, err)
	}
	v, err = gs[1].Check(ctx, people, "run-1")
	if err != nil || v[0].Status != GuardClear || v[1].Status != GuardBlocked || v[1].Reason != GuardBlocklisted {
		t.Fatalf("blocklist guard: %+v, %v", v, err)
	}

	held := func(what string, v []ContactVerdict, err error, detail string) {
		t.Helper()
		if err == nil || len(v) != len(people) {
			t.Fatalf("%s: %d verdicts, %v; want one for each person and the failure", what, len(v), err)
		}
		for _, x := range v {
			if x.Status != GuardUnknown || x.Detail != detail {
				t.Errorf("%s: %+v, want unknown (%s)", what, x, detail)
			}
		}
	}
	// A guard whose call fails: everyone unknown, the failure beside
	// them, and a refused key marks the connection.
	failing := e.connect("guard_down", "")
	// One marked broken at its last check is still asked.
	if _, err := e.pool.Exec(ctx, `UPDATE integrations SET status = 'error', last_error = 'refused' WHERE id = $1`, blocklist); err != nil {
		t.Fatal(err)
	}
	// One that can't be opened (its credentials can't be read) is still a
	// guard.
	broken := e.connect("guard_customers", "")
	if _, err := e.pool.Exec(ctx, `UPDATE integrations SET credentials = '\x00'::bytea WHERE id = $1`, broken); err != nil {
		t.Fatal(err)
	}
	short := e.connect("guard_short", "")
	gs, err = cs.ContactGuards(ctx, e.ws, nil)
	if err != nil || len(gs) != 5 {
		t.Fatalf("guards = %d, %v; want all 5, broken ones included", len(gs), err)
	}
	by := map[string]*ContactGuard{}
	for _, g := range gs {
		by[g.ID] = g
	}
	v, err = by[failing].Check(ctx, people, "run-1")
	held("a failing guard", v, err, "http_401")
	if code, after := GuardFailure(err); code != "http_401" || after != time.Minute {
		t.Errorf("failure = %s after %s, want http_401 after 1m", code, after)
	}
	var status string
	if err := e.pool.QueryRow(ctx, `SELECT status FROM integrations WHERE id = $1`, failing).Scan(&status); err != nil || status != connectors.StatusError {
		t.Errorf("a guard whose key was refused: status %q, %v; want error", status, err)
	}
	if v, err = by[blocklist].Check(ctx, people, "run-1"); err != nil || v[1].Status != GuardBlocked {
		t.Errorf("a guard marked broken is still asked: %+v, %v", v, err)
	}
	v, err = by[broken].Check(ctx, people, "run-1")
	held("a guard that can't be opened", v, err, "connection_unavailable")
	v, err = by[short].Check(ctx, people, "run-1")
	held("a guard that answers for too few", v, err, "internal")
	if code, _ := GuardFailure(err); code != "internal" {
		t.Errorf("failure = %s, want internal", code)
	}

	// A brand's own guard replaces the workspace's connection to the same
	// tool for that brand, as every connection does.
	brand := uuid.NewString()
	if _, err := e.pool.Exec(ctx, `INSERT INTO brands(id,workspace_id,name,handle) VALUES($1,$2,$3,$3)`, brand, e.ws, "b-"+brand[:8]); err != nil {
		t.Fatal(err)
	}
	own := e.connect("guard_blocklist", brand)
	gs, err = cs.ContactGuards(ctx, e.ws, &brand)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, g := range gs {
		seen[g.ID] = true
	}
	if !seen[own] || seen[blocklist] || !seen[customers] {
		t.Errorf("for the brand: %v; want its own blocklist guard, not the workspace's, and the shared customer guard", seen)
	}
}

// listGuard is a testGuard whose whole list can be read, so the workspace
// keeps a copy of it (guardsync).
type listGuard struct{ testGuard }

func (listGuard) GuardList() connectors.GuardListInfo {
	return connectors.GuardListInfo{Mode: connectors.GuardListFeed, Reason: GuardCustomer, Every: 5 * time.Minute}
}

func (listGuard) ReadGuardList(context.Context, *connectors.Client, connectors.Creds, string, int, func([]connectors.GuardListEntry, string) error) (bool, error) {
	return false, nil
}

// CompanyFromList answers for a company from the guard's copy only while
// the copy is fresh, and says so when it can't.
func TestCompanyFromList(t *testing.T) {
	var got []string
	e := newConnEnv(t, listGuard{testGuard{id: "guard_listed", reason: GuardCustomer, got: &got}},
		testGuard{id: "guard_unlisted", reason: GuardBlocklisted, got: &got})
	ctx := context.Background()
	listed := e.connect("guard_listed", "")
	unlisted := e.connect("guard_unlisted", "")
	gs, err := e.h.Connections().ContactGuards(ctx, e.ws, nil, "guard_listed")
	if err != nil || len(gs) != 2 || gs[0].ID != listed || gs[1].ID != unlisted {
		t.Fatalf("guards %+v, %v", gs, err)
	}
	ask := func(g *ContactGuard, domain string) (ContactVerdict, bool) {
		t.Helper()
		v, fresh, err := g.CompanyFromList(ctx, domain)
		if err != nil {
			t.Fatal(err)
		}
		return v, fresh
	}
	if _, fresh := ask(gs[1], "customer.example"); fresh {
		t.Fatal("a guard whose list can't be read answered from a copy")
	}
	if _, fresh := ask(gs[0], "customer.example"); fresh {
		t.Fatal("a guard never synced answered from a copy")
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO suppressions (workspace_id, kind, value, reason, source, integration_id)
		VALUES ($1, 'domain', 'customer.example', 'customer', 'guard_listed_sync', $2),
		       ($1, 'domain', 'manual.example', 'customer', 'manual', NULL)`, e.ws, listed); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO guard_list_syncs (integration_id, workspace_id, backfilled_at, succeeded_at)
		VALUES ($1, $2, now() - interval '1 hour', now() - interval '1 minute')`, listed, e.ws); err != nil {
		t.Fatal(err)
	}
	if v, fresh := ask(gs[0], "www.customer.example"); !fresh || v.Status != GuardBlocked || v.Reason != GuardCustomer {
		t.Fatalf("a listed company: %+v, fresh %v", v, fresh)
	}
	if v, fresh := ask(gs[0], "manual.example"); !fresh || v.Status != GuardClear {
		t.Fatalf("a company on the list by another source is not the guard's to say: %+v, fresh %v", v, fresh)
	}
	if _, fresh := ask(gs[0], "gmail.com"); fresh {
		t.Fatal("the copy answered for a mailbox provider's domain")
	}
	// Stale: the last success is older than twice the interval.
	if _, err := e.pool.Exec(ctx, `UPDATE guard_list_syncs SET succeeded_at = now() - interval '11 minutes' WHERE integration_id = $1`, listed); err != nil {
		t.Fatal(err)
	}
	if _, fresh := ask(gs[0], "customer.example"); fresh {
		t.Fatal("a stale copy answered")
	}
	if len(got) != 0 {
		t.Fatalf("guards were called: %v", got)
	}
}
