package emailfind

import (
	"context"
	"net"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/db"
)

// noMX is a resolver that finds no mail servers, so provider detection stays
// out of the way in tests (no real DNS).
type noMX struct{}

func (noMX) LookupMX(context.Context, string) ([]*net.MX, error) { return nil, nil }

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

func newWS(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var ws string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO workspaces (name, slug) VALUES ('EmailFind', 'ef-' || substr(gen_random_uuid()::text, 1, 8)) RETURNING id::text`).
		Scan(&ws); err != nil {
		t.Fatal(err)
	}
	return ws
}

func addLead(t *testing.T, pool *pgxpool.Pool, ws, first, last, email, status string) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`INSERT INTO leads (workspace_id, email, first_name, last_name) VALUES ($1, $2, $3, $4)`,
		ws, email, first, last); err != nil {
		t.Fatal(err)
	}
	if status != "" {
		if _, err := pool.Exec(ctx,
			`INSERT INTO email_verifications (workspace_id, email, status, provider) VALUES ($1, $2, $3, 'builtin')`,
			ws, email, status); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRebuildLearnsDominantShape(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ws := newWS(t, pool)
	addLead(t, pool, ws, "John", "Doe", "john.doe@acme.example", "deliverable")
	addLead(t, pool, ws, "Jane", "Roe", "jane.roe@acme.example", "deliverable")
	addLead(t, pool, ws, "Bob", "Smith", "bob.smith@acme.example", "deliverable")

	p, err := Rebuild(ctx, pool, noMX{}, ws, "acme.example")
	if err != nil {
		t.Fatal(err)
	}
	if p.TopShape != "first.last" {
		t.Fatalf("TopShape = %q, want first.last", p.TopShape)
	}
	if p.Confidence < confThreshold {
		t.Errorf("confidence = %d, want >= %d", p.Confidence, confThreshold)
	}

	// Reload from the store to confirm it persisted.
	loaded, ok, err := load(ctx, pool, ws, "acme.example")
	if err != nil || !ok {
		t.Fatalf("load after rebuild: ok=%v err=%v", ok, err)
	}
	if loaded.TopShape != "first.last" {
		t.Errorf("loaded TopShape = %q, want first.last", loaded.TopShape)
	}
}

func TestFindEmailsGuessesFromPattern(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ws := newWS(t, pool)
	addLead(t, pool, ws, "John", "Doe", "john.doe@acme.example", "deliverable")
	addLead(t, pool, ws, "Jane", "Roe", "jane.roe@acme.example", "deliverable")

	req := FindRequest{Items: []FindItem{{Ref: "1", FirstName: "Alice", LastName: "Nguyen", Domain: "acme.example"}}}
	res, err := FindEmails(ctx, pool, noMX{}, ws, req)
	if err != nil {
		t.Fatal(err)
	}
	got := res.Items[0]
	if got.Email != "alice.nguyen@acme.example" {
		t.Errorf("email = %q, want alice.nguyen@acme.example", got.Email)
	}
	if got.Code != CodeFound {
		t.Errorf("code = %q, want found", got.Code)
	}
	if got.Evidence != EvidencePattern {
		t.Errorf("evidence = %q, want pattern", got.Evidence)
	}
	if got.Ref != "1" {
		t.Errorf("ref = %q, want echoed 1", got.Ref)
	}
}

func TestFindEmailsUsesHistory(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ws := newWS(t, pool)
	// A known-deliverable address for this exact person.
	addLead(t, pool, ws, "Mark", "Lee", "mark.lee@acme.example", "deliverable")

	req := FindRequest{Items: []FindItem{{Ref: "x", FirstName: "Mark", LastName: "Lee", Domain: "acme.example"}}}
	res, err := FindEmails(ctx, pool, noMX{}, ws, req)
	if err != nil {
		t.Fatal(err)
	}
	got := res.Items[0]
	if got.Code != CodeVerified || got.Evidence != EvidenceHistory {
		t.Errorf("got code=%q evidence=%q, want verified/history", got.Code, got.Evidence)
	}
	if got.Email != "mark.lee@acme.example" {
		t.Errorf("email = %q, want mark.lee@acme.example", got.Email)
	}
}

func TestFindEmailsNoDomainIsNotFound(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ws := newWS(t, pool)
	req := FindRequest{Items: []FindItem{{Ref: "n", FirstName: "No", LastName: "Domain"}}}
	res, err := FindEmails(ctx, pool, noMX{}, ws, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Items[0].Code != CodeNotFound {
		t.Errorf("code = %q, want not_found", res.Items[0].Code)
	}
}
