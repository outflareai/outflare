package platforms_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/allocation"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/db"
	"github.com/outflareai/outflare/engine/internal/platforms"
	"github.com/outflareai/outflare/engine/internal/secrets"
)

// fake is a platform account held in memory.
type fake struct {
	mailboxes []connectors.MailboxState
	campaigns []connectors.CampaignState
	attached  map[string][]string // campaign -> mailbox external ids
	calls     []string
	blocked   []connectors.Blocked // the platform's blocklist
	pushed    []connectors.Blocked // what Outflare wrote to it
}

func (f *fake) ListBlocked(context.Context, *connectors.Client, connectors.Creds) ([]connectors.Blocked, error) {
	return f.blocked, nil
}

func (f *fake) Block(_ context.Context, _ *connectors.Client, _ connectors.Creds, entries []connectors.Blocked) error {
	f.pushed = append(f.pushed, entries...)
	return nil
}

func (f *fake) Definition() connectors.Definition {
	return connectors.Definition{ID: "fakeseq", Name: "FakeSeq", Category: connectors.CategorySequencer, Auth: connectors.AuthAPIKey,
		Fields: []connectors.Field{{Name: "api_key", Label: "API key", Secret: true}}, Capabilities: []string{connectors.CapControlMailboxes}}
}

func (f *fake) Test(context.Context, *connectors.Client, connectors.Creds) (connectors.Account, error) {
	return connectors.Account{Name: "Fake"}, nil
}

func (f *fake) Observe(context.Context, *connectors.Client, connectors.Creds) (connectors.Snapshot, error) {
	snap := connectors.Snapshot{Campaigns: f.campaigns}
	for _, m := range f.mailboxes {
		m.CampaignIDs = nil
		for c, ids := range f.attached {
			if slices.Contains(ids, m.ExternalID) {
				m.CampaignIDs = append(m.CampaignIDs, c)
			}
		}
		snap.Mailboxes = append(snap.Mailboxes, m)
	}
	return snap, nil
}

func (f *fake) Attach(_ context.Context, _ *connectors.Client, _ connectors.Creds, campaign string, ms []connectors.MailboxState) error {
	for _, m := range ms {
		f.calls = append(f.calls, "attach "+m.Email+" "+campaign)
		if !slices.Contains(f.attached[campaign], m.ExternalID) {
			f.attached[campaign] = append(f.attached[campaign], m.ExternalID)
		}
	}
	return nil
}

func (f *fake) Detach(_ context.Context, _ *connectors.Client, _ connectors.Creds, campaign string, ms []connectors.MailboxState) error {
	for _, m := range ms {
		f.calls = append(f.calls, "detach "+m.Email+" "+campaign)
		f.attached[campaign] = slices.DeleteFunc(f.attached[campaign], func(id string) bool { return id == m.ExternalID })
	}
	return nil
}

func (f *fake) SetDailyLimit(context.Context, *connectors.Client, connectors.Creds, connectors.MailboxState, int) error {
	return nil
}

func ptr(n int) *int { return &n }

func setup(t *testing.T) (*pgxpool.Pool, *platforms.Service, *fake, string, string) {
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
	f := &fake{
		campaigns: []connectors.CampaignState{
			{ExternalID: "c1", Name: "Q4 founders", Status: "active", DailyLimit: ptr(100), SequenceDays: ptr(10)},
			{ExternalID: "c0", Name: "Old", Status: "completed"},
		},
		mailboxes: []connectors.MailboxState{
			{ExternalID: "m-a", Email: "ana@acme.test", Name: "Ana", Status: connectors.MailboxOK, DailyLimit: ptr(30), WarmupScore: ptr(98)},
			{ExternalID: "m-b", Email: "bo@acme.test", Name: "Bo", Status: connectors.MailboxOK, DailyLimit: ptr(30), WarmupScore: ptr(97)},
			{ExternalID: "m-c", Email: "cy@acme.test", Name: "Cy", Status: connectors.MailboxError, StatusReason: "login failed", DailyLimit: ptr(30)},
		},
		attached: map[string][]string{"c1": {"m-a"}},
	}
	reg := connectors.NewRegistry([]connectors.Connector{f}, nil)
	store := &connectors.Store{Pool: pool, Box: box, Registry: reg, Client: &connectors.Client{}}
	svc := &platforms.Service{Pool: pool, Store: store, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	var ws, integration string
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO workspaces (name, slug) VALUES ('Acme', 'acme') RETURNING id::text`).Scan(&ws); err != nil {
			return err
		}
		i, err := store.Insert(ctx, tx, ws, "fakeseq", connectors.AuthAPIKey, "FakeSeq", connectors.Creds{"api_key": "k"}, connectors.Account{}, "")
		integration = i.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return pool, svc, f, ws, integration
}

func TestSyncPlanAttachAndAcceptOutsideEdits(t *testing.T) {
	pool, svc, f, ws, integration := setup(t)
	ctx := context.Background()
	alloc := &allocation.Service{Pool: pool, Log: svc.Log}

	// First sync: the live campaign comes in linked to a new sequence with
	// its attached mailbox as it is; the finished one is left out.
	res, err := svc.Sync(ctx, ws, integration)
	if err != nil {
		t.Fatal(err)
	}
	if res.Campaigns != 1 || res.Mailboxes != 3 || res.Accepted != 1 {
		t.Fatalf("first sync %+v", res)
	}
	var seq, status string
	var target *int
	if err := pool.QueryRow(ctx, `SELECT s.id::text, s.status, s.target_daily FROM sequences s JOIN campaign_links l ON l.sequence_id = s.id`).
		Scan(&seq, &status, &target); err != nil {
		t.Fatal(err)
	}
	if status != "active" || target != nil {
		t.Fatalf("sequence %s target %v", status, target)
	}
	var loginless int
	pool.QueryRow(ctx, `SELECT count(*) FROM mailboxes WHERE NOT has_login`).Scan(&loginless)
	if loginless != 3 {
		t.Fatalf("loginless mailboxes %d", loginless)
	}

	// With a target, the first plan is only suggested until reviewed.
	if _, err := pool.Exec(ctx, `UPDATE sequences SET target_daily = 60 WHERE id = $1`, seq); err != nil {
		t.Fatal(err)
	}
	run, err := alloc.Run(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Applied) != 0 || len(run.Suggested) != 1 {
		t.Fatalf("before review: %+v", run)
	}
	if _, err := pool.Exec(ctx, `UPDATE platform_syncs SET reviewed_at = now()`); err != nil {
		t.Fatal(err)
	}
	run, err = alloc.Run(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Applied) != 1 || run.Applied[0].Action != allocation.Assign || run.Applied[0].LinkID == "" {
		t.Fatalf("after review: %+v", run)
	}

	// The attach runs as a job; the next sync confirms it.
	var payload []byte
	if err := pool.QueryRow(ctx, `SELECT payload FROM jobs WHERE kind = $1`, allocation.JobPlatform).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var op allocation.PlatformOp
	json.Unmarshal(payload, &op)
	if err := svc.Apply(ctx, op); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.calls, []string{"attach bo@acme.test c1"}) {
		t.Fatalf("calls %v", f.calls)
	}
	res, err = svc.Sync(ctx, ws, integration)
	if err != nil {
		t.Fatal(err)
	}
	if res.Confirmed != 1 {
		t.Fatalf("confirm sync %+v", res)
	}

	// Someone removes Ana in the platform: accepted as a manual change.
	f.attached["c1"] = []string{"m-b"}
	res, err = svc.Sync(ctx, ws, integration)
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 1 {
		t.Fatalf("detach sync %+v", res)
	}
	var source string
	if err := pool.QueryRow(ctx, `SELECT source FROM allocation_changes WHERE action = 'release' ORDER BY created_at DESC LIMIT 1`).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if source != "external" {
		t.Fatalf("source %s", source)
	}

	// Releasing a confirmed platform mailbox drains for the campaign's
	// length instead of detaching at once.
	if _, err := pool.Exec(ctx, `UPDATE sequences SET target_daily = 1 WHERE id = $1`, seq); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sequences SET status = 'archived' WHERE id = $1`, seq); err != nil {
		t.Fatal(err)
	}
	run, err = alloc.Run(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Applied) != 1 || run.Applied[0].Action != allocation.Release {
		t.Fatalf("archive: %+v", run)
	}
	var detaches int
	pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = $1 AND payload->>'op' = 'detach'`, allocation.JobPlatform).Scan(&detaches)
	if detaches != 1 {
		t.Fatalf("detach jobs %d", detaches)
	}
}

func TestStaleAccountMakesItsMailboxesBusy(t *testing.T) {
	pool, svc, _, ws, integration := setup(t)
	ctx := context.Background()
	if _, err := svc.Sync(ctx, ws, integration); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sequences SET target_daily = 60`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE platform_syncs SET reviewed_at = now(), synced_at = now() - interval '2 hours'`); err != nil {
		t.Fatal(err)
	}
	alloc := &allocation.Service{Pool: pool, Log: svc.Log}
	run, err := alloc.Run(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range run.Applied {
		if c.Action == allocation.Assign {
			t.Fatalf("assigned from a stale account: %+v", c)
		}
	}
}

// watch is a platform that only reports mailboxes, like Apollo.
type watch struct{ mailboxes []connectors.MailboxState }

func (w *watch) Definition() connectors.Definition {
	return connectors.Definition{ID: "fakewatch", Name: "FakeWatch", Category: connectors.CategoryData, Auth: connectors.AuthAPIKey,
		Fields: []connectors.Field{{Name: "api_key", Label: "API key", Secret: true}}, Capabilities: []string{connectors.CapObserveMailboxes}}
}

func (w *watch) Test(context.Context, *connectors.Client, connectors.Creds) (connectors.Account, error) {
	return connectors.Account{Name: "Watch"}, nil
}

func (w *watch) Observe(context.Context, *connectors.Client, connectors.Creds) (connectors.Snapshot, error) {
	return connectors.Snapshot{Mailboxes: w.mailboxes, Campaigns: []connectors.CampaignState{{ExternalID: "s1", Name: "Apollo seq", Status: "active"}}}, nil
}

func TestWatchedPlatformOnlyFlagsKnownMailboxes(t *testing.T) {
	pool, _, _, ws, _ := setup(t)
	ctx := context.Background()
	w := &watch{mailboxes: []connectors.MailboxState{
		{ExternalID: "a1", Email: "Own@Acme.test", Status: connectors.MailboxOK},
		{ExternalID: "a2", Email: "stranger@acme.test", Status: connectors.MailboxOK},
	}}
	box, _ := secrets.NewBox(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	store := &connectors.Store{Pool: pool, Box: box, Registry: connectors.NewRegistry([]connectors.Connector{w}, nil), Client: &connectors.Client{}}
	svc := &platforms.Service{Pool: pool, Store: store, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	var integration string
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO mailboxes (workspace_id, email, display_name, provider, smtp_host, smtp_port, smtp_username,
			imap_host, imap_port, imap_username, credentials, status, daily_limit) VALUES ($1, 'own@acme.test', 'Own', 'other', 'h', 587, 'u', 'h', 993, 'u', '\x', 'active', 30)`, ws); err != nil {
			return err
		}
		i, err := store.Insert(ctx, tx, ws, "fakewatch", connectors.AuthAPIKey, "FakeWatch", connectors.Creds{"api_key": "k"}, connectors.Account{}, "")
		integration = i.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.Sync(ctx, ws, integration)
	if err != nil {
		t.Fatal(err)
	}
	if res.Mailboxes != 1 || res.Campaigns != 0 {
		t.Fatalf("sync %+v", res)
	}
	var mailboxes, links, flagged int
	pool.QueryRow(ctx, `SELECT count(*) FROM mailboxes WHERE workspace_id = $1`, ws).Scan(&mailboxes)
	pool.QueryRow(ctx, `SELECT count(*) FROM campaign_links WHERE integration_id = $1`, integration).Scan(&links)
	pool.QueryRow(ctx, `SELECT count(*) FROM mailbox_platforms WHERE integration_id = $1`, integration).Scan(&flagged)
	if mailboxes != 1 || links != 0 || flagged != 1 {
		t.Fatalf("mailboxes %d links %d flagged %d", mailboxes, links, flagged)
	}
	// Even out of date, a watched platform never takes the mailbox away.
	if _, err := pool.Exec(ctx, `UPDATE platform_syncs SET synced_at = now() - interval '2 hours'`); err != nil {
		t.Fatal(err)
	}
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		st, err := allocation.Load(ctx, tx, ws, time.Now())
		if err != nil {
			return err
		}
		for _, m := range st.Input.Mailboxes {
			if m.Outside != "" {
				t.Errorf("watched mailbox kept out: %s", m.Outside)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The opt-out list and the platform's blocklist are kept in step: what is
// blocked there comes in, tagged with where it came from, and what the
// list holds goes out, once, never echoing back what came from there.
func TestBlocklistSyncsBothWays(t *testing.T) {
	pool, svc, f, ws, integration := setup(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO suppressions (workspace_id, kind, value, reason, method) VALUES
		($1, 'email', 'zed@example.org', 'manual', 'manual'), ($1, 'domain', 'rival.example', 'competitor', 'manual')`, ws); err != nil {
		t.Fatal(err)
	}
	f.blocked = []connectors.Blocked{{Value: "Old@Example.org"}, {Value: "spam.example", Domain: true}, {Value: "not an address"}}
	res, err := svc.Sync(ctx, ws, integration)
	if err != nil {
		t.Fatal(err)
	}
	if res.OptOutsPulled != 2 || res.OptOutsPushed != 2 || len(f.pushed) != 2 {
		var last *string
		_ = pool.QueryRow(ctx, `SELECT last_error FROM suppression_syncs WHERE integration_id = $1`, integration).Scan(&last)
		t.Fatalf("result = %+v, pushed = %v, error = %v", res, f.pushed, last)
	}
	var reason, source, method, from string
	if err := pool.QueryRow(ctx, `SELECT reason, source, method, integration_id::text FROM suppressions WHERE workspace_id = $1 AND value = 'old@example.org'`,
		ws).Scan(&reason, &source, &method, &from); err != nil {
		t.Fatal(err)
	}
	if reason != "unsubscribed" || source != "fakeseq_sync" || method != "automated" || from != integration {
		t.Fatalf("pulled entry = %s %s %s %s", reason, source, method, from)
	}
	if err := pool.QueryRow(ctx, `SELECT reason FROM suppressions WHERE workspace_id = $1 AND value = 'spam.example'`, ws).Scan(&reason); err != nil || reason != "do_not_contact" {
		t.Fatalf("pulled domain = %q, %v", reason, err)
	}

	// Nothing new either side: nothing moves.
	if res, err = svc.Sync(ctx, ws, integration); err != nil || res.OptOutsPulled != 0 || res.OptOutsPushed != 0 {
		t.Fatalf("again = %+v, %v", res, err)
	}
	// A new opt-out goes out on the next sync, and not once push is off.
	if _, err := pool.Exec(ctx, `INSERT INTO suppressions (workspace_id, kind, value, reason) VALUES ($1, 'email', 'ivy@example.org', 'unsubscribed')`, ws); err != nil {
		t.Fatal(err)
	}
	if res, err = svc.Sync(ctx, ws, integration); err != nil || res.OptOutsPushed != 1 || f.pushed[2].Value != "ivy@example.org" {
		t.Fatalf("new = %+v, %v, %v", res, err, f.pushed)
	}
	if _, err := pool.Exec(ctx, `UPDATE suppression_syncs SET push = false WHERE integration_id = $1`, integration); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO suppressions (workspace_id, kind, value, reason) VALUES ($1, 'email', 'jo@example.org', 'unsubscribed')`, ws); err != nil {
		t.Fatal(err)
	}
	if res, err = svc.Sync(ctx, ws, integration); err != nil || res.OptOutsPushed != 0 {
		t.Fatalf("push off = %+v, %v", res, err)
	}
}
