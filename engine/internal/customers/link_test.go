package customers_test

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/db"
	"github.com/outflareai/outflare/engine/internal/leads"
)

func testPool(t *testing.T) *pgxpool.Pool {
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
	return pool
}

func workspace(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var ws string
	if err := pool.QueryRow(context.Background(), `INSERT INTO workspaces (name, slug) VALUES ('Acme', 'acme-' || substr(gen_random_uuid()::text, 1, 8)) RETURNING id::text`).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	return ws
}

func inTx(t *testing.T, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) {
	t.Helper()
	if err := pgx.BeginFunc(context.Background(), pool, fn); err != nil {
		t.Fatal(err)
	}
}

// Every column that points at a record is either moved by a merge or
// handled by it on purpose, so a new link is never left on a merged record.
func TestEveryRecordLinkIsMerged(t *testing.T) {
	pool := testPool(t)
	rows, err := pool.Query(context.Background(), `
		SELECT c.conrelid::regclass::text, a.attname FROM pg_constraint c
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY(c.conkey)
		WHERE c.contype = 'f' AND c.confrelid = 'records'::regclass
		UNION
		SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND column_name = 'record_id'`)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (customers.Ref, error) {
		var ref customers.Ref
		return ref, r.Scan(&ref.Table, &ref.Column)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) < 10 {
		t.Fatalf("found only %d record links: %v", len(refs), refs)
	}
	for _, ref := range refs {
		if !slices.Contains(customers.Follows, ref) && !slices.Contains(customers.Owned, ref) {
			t.Errorf("%s.%s points at records but is in neither customers.Follows nor customers.Owned: add it to Follows so merges move it", ref.Table, ref.Column)
		}
	}
}

func TestLeadsBecomePeopleAndActivityFollows(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ws := workspace(t, pool)

	// Someone already known (they signed up) and someone new.
	var known string
	inTx(t, pool, func(tx pgx.Tx) error {
		var err error
		known, err = customers.EnsurePerson(ctx, tx, ws, "email", "ana@acme.example", true, "")
		return err
	})
	inTx(t, pool, func(tx pgx.Tx) error {
		_, err := leads.Import(ctx, tx, ws, "csv", nil, []leads.Lead{
			{Email: "Ana@acme.example", FirstName: "Ana"},
			{Email: "bo@northpeak.example", FirstName: "Bo", LastName: "Lind", Title: "Head of growth"},
		})
		return err
	})
	var anaRec, boRec, boName, boOrigin, boTitle string
	if err := pool.QueryRow(ctx, `SELECT record_id::text FROM leads WHERE workspace_id = $1 AND email = 'Ana@acme.example'`, ws).Scan(&anaRec); err != nil {
		t.Fatal(err)
	}
	if anaRec != known {
		t.Fatalf("Ana's lead links to %s, want her existing record %s", anaRec, known)
	}
	if err := pool.QueryRow(ctx, `SELECT l.record_id::text, r.name, r.origin, r.attributes->>'job_title' FROM leads l JOIN records r ON r.id = l.record_id
		WHERE l.workspace_id = $1 AND l.email = 'bo@northpeak.example'`, ws).Scan(&boRec, &boName, &boOrigin, &boTitle); err != nil {
		t.Fatal(err)
	}
	if boName != "Bo Lind" || boOrigin != "prospect" || boTitle != "Head of growth" {
		t.Fatalf("Bo's record = %q %q %q", boName, boOrigin, boTitle)
	}
	var unverified bool
	if err := pool.QueryRow(ctx, `SELECT NOT verified FROM record_identities WHERE record_id = $1 AND namespace = 'email'`, boRec).Scan(&unverified); err != nil || !unverified {
		t.Fatalf("a lead's email must stay unverified: %v %v", unverified, err)
	}

	// A reply from Bo links through his lead; one from a stranger stays
	// unlinked; a meeting with Ana links by her address.
	var mailbox string
	if err := pool.QueryRow(ctx, `INSERT INTO mailboxes (workspace_id, email, provider, smtp_host, smtp_port, smtp_username, imap_host, imap_port, imap_username, credentials) VALUES ($1, 'sam@send.example', 'other', 'h', 587, 'u', 'h', 993, 'u', '\x00') RETURNING id::text`, ws).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	var boReply, strangerReply, meeting *string
	if err := pool.QueryRow(ctx, `INSERT INTO inbound_messages (workspace_id, mailbox_id, uidvalidity, uid, kind, from_email, subject, lead_id, received_at)
		SELECT $1, $2, 1, 1, 'reply', 'bo@northpeak.example', 'Re: hi', id, now() FROM leads WHERE workspace_id = $1 AND email = 'bo@northpeak.example' RETURNING record_id::text`, ws, mailbox).Scan(&boReply); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO inbound_messages (workspace_id, mailbox_id, uidvalidity, uid, kind, from_email, subject, received_at)
		VALUES ($1, $2, 1, 2, 'reply', 'who@else.example', 'Hello', now()) RETURNING record_id::text`, ws, mailbox).Scan(&strangerReply); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO meetings (workspace_id, source, title, attendee_email, starts_at, booked_at)
		VALUES ($1, 'manual', 'Intro', 'ANA@acme.example', now(), now()) RETURNING record_id::text`, ws).Scan(&meeting); err != nil {
		t.Fatal(err)
	}
	if boReply == nil || *boReply != boRec || strangerReply != nil || meeting == nil || *meeting != known {
		t.Fatalf("activity links: bo %v stranger %v meeting %v", boReply, strangerReply, meeting)
	}

	// Both are in the same journey: the merge keeps Ana's run and ends Bo's.
	var journey string
	if err := pool.QueryRow(ctx, `INSERT INTO journeys (workspace_id, name, status) VALUES ($1, 'Welcome', 'active') RETURNING id::text`, ws).Scan(&journey); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO journey_runs (workspace_id, journey_id, record_id) VALUES ($1, $2, $3), ($1, $2, $4)`, ws, journey, known, boRec); err != nil {
		t.Fatal(err)
	}

	// Merging Bo into Ana moves his lead and reply with him.
	inTx(t, pool, func(tx pgx.Tx) error {
		moved, err := customers.Follow(ctx, tx, ws, boRec, known)
		if moved["leads"] != 1 || moved["inbound_messages"] != 1 || moved["journey_runs"] != 1 {
			t.Errorf("moved = %v", moved)
		}
		return err
	})
	var active, merged int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'active'), count(*) FILTER (WHERE exit_reason = 'merged')
		FROM journey_runs WHERE journey_id = $1 AND record_id = $2`, journey, known).Scan(&active, &merged); err != nil || active != 1 || merged != 1 {
		t.Fatalf("journey runs after merge: %d active, %d merged, %v", active, merged, err)
	}
}

func TestBigImportLinksInTheJob(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ws := workspace(t, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO leads (workspace_id, email, first_name) SELECT $1, 'p' || g || '@bulk.example', 'P' FROM generate_series(1, 1200) g`, ws); err != nil {
		t.Fatal(err)
	}
	total := 0
	for {
		var n int
		inTx(t, pool, func(tx pgx.Tx) error {
			var err error
			n, err = customers.LinkLeads(ctx, tx, ws, nil, 500)
			return err
		})
		total += n
		if n == 0 {
			break
		}
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM leads WHERE workspace_id = $1 AND record_id IS NULL`, ws).Scan(&left); err != nil || left != 0 || total != 1200 {
		t.Fatalf("linked %d, %d left, %v", total, left, err)
	}
}

func TestUnsubscribeWithdrawsConsentUntilThePersonOptsIn(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ws := workspace(t, pool)

	// Cara unsubscribes from a cold sequence.
	inTx(t, pool, func(tx pgx.Tx) error {
		_, err := leads.AddSuppressions(ctx, tx, ws, "unsubscribed", "unsubscribe_link", "", []leads.Suppression{{Kind: "email", Value: "cara@acme.example"}})
		return err
	})
	var rec string
	if err := pool.QueryRow(ctx, `SELECT record_id::text FROM suppressions WHERE workspace_id = $1 AND value = 'cara@acme.example'`, ws).Scan(&rec); err != nil {
		t.Fatal(err)
	}
	status, _, err := customers.Consent(ctx, pool, ws, rec, "email", "", "marketing")
	if err != nil || status != customers.Withdrawn {
		t.Fatalf("consent after unsubscribe = %q, %v", status, err)
	}

	// An import saying she agreed does not clear it.
	inTx(t, pool, func(tx pgx.Tx) error {
		return customers.RecordConsent(ctx, tx, ws, customers.ConsentChange{RecordID: rec, Channel: "email", Status: customers.Granted, Source: "import"})
	})
	if !suppressed(t, pool, ws, "cara@acme.example") {
		t.Fatal("an imported grant cleared an unsubscribe")
	}

	// Signing up and agreeing to email does.
	inTx(t, pool, func(tx pgx.Tx) error {
		return customers.RecordConsent(ctx, tx, ws, customers.ConsentChange{RecordID: rec, Channel: "email", Status: customers.Granted, Source: "signup_form"})
	})
	if suppressed(t, pool, ws, "cara@acme.example") {
		t.Fatal("opting in again left the unsubscribe in place")
	}

	// A bounce is never cleared by opting in.
	inTx(t, pool, func(tx pgx.Tx) error {
		if _, err := leads.AddSuppressions(ctx, tx, ws, "bounced", "bounce", "", []leads.Suppression{{Kind: "email", Value: "cara@acme.example"}}); err != nil {
			return err
		}
		return customers.RecordConsent(ctx, tx, ws, customers.ConsentChange{RecordID: rec, Channel: "email", Status: customers.Granted, Source: "preference_center"})
	})
	if !suppressed(t, pool, ws, "cara@acme.example") {
		t.Fatal("opting in cleared a bounce")
	}
}

func TestOlderUnsubscribesGetTheirConsent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ws := workspace(t, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO suppressions (workspace_id, kind, value, reason, source, created_at)
		VALUES ($1, 'email', 'dee@acme.example', 'unsubscribed', 'instantly_import', '2026-01-02T00:00:00Z')`, ws); err != nil {
		t.Fatal(err)
	}
	inTx(t, pool, func(tx pgx.Tx) error {
		_, err := customers.WithdrawEmails(ctx, tx, ws, "suppression", "", []customers.Unsubscribe{{Email: "dee@acme.example", At: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}})
		return err
	})
	var at string
	if err := pool.QueryRow(ctx, `SELECT c.status FROM consents c JOIN suppressions s ON s.record_id = c.record_id WHERE s.workspace_id = $1`, ws).Scan(&at); err != nil || at != "withdrawn" {
		t.Fatalf("older unsubscribe consent = %q, %v", at, err)
	}
}

func suppressed(t *testing.T, pool *pgxpool.Pool, ws, email string) bool {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM suppressions WHERE workspace_id = $1 AND value = $2`, ws, email).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}
