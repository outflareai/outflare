package datanetwork

import (
	"context"
	"io"
	"log/slog"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/db"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// fakeNetwork records what it is given and answers from a fixed answer.
type fakeNetwork struct {
	off       map[string]bool // workspaces it isn't turned on for
	mu        sync.Mutex
	got       map[string][]Contribution
	withdrawn []string
	answer    Answer
	err       error
	queries   []Query
}

func (f *fakeNetwork) Lookup(_ context.Context, _ string, q Query) (Answer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, q)
	return f.answer, f.err
}

func (f *fakeNetwork) Contribute(_ context.Context, ws string, batch []Contribution) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.got == nil {
		f.got = map[string][]Contribution{}
	}
	f.got[ws] = append(f.got[ws], batch...)
	return nil
}

func (f *fakeNetwork) Withdraw(_ context.Context, ws string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.withdrawn = append(f.withdrawn, ws)
	delete(f.got, ws)
	return nil
}

func (f *fakeNetwork) Enabled(_ context.Context, ws string) bool { return !f.off[ws] }

func (f *fakeNetwork) Allowance(_ context.Context, _ string, member bool) (Allowance, error) {
	if member {
		return Allowance{Unlimited: true}, nil
	}
	return Allowance{Limit: 25}, nil
}

func testService(t *testing.T) (*Service, *pgxpool.Pool) {
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
	return &Service{Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, pool
}

func newWS(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var ws string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO workspaces (name, slug) VALUES ('Network', 'dn-' || substr(gen_random_uuid()::text, 1, 8)) RETURNING id::text`).
		Scan(&ws); err != nil {
		t.Fatal(err)
	}
	return ws
}

// sent records a sequence send to a lead, sent ago.
func sent(t *testing.T, pool *pgxpool.Pool, ws, first, last, email string, ago time.Duration) {
	t.Helper()
	ctx := context.Background()
	var lead string
	if err := pool.QueryRow(ctx, `INSERT INTO leads (workspace_id, email, first_name, last_name) VALUES ($1, $2, $3, $4) RETURNING id::text`,
		ws, email, first, last).Scan(&lead); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO messages (workspace_id, lead_id, from_email, to_email, subject, body, message_id, status, sent_at)
		VALUES ($1, $2, 'sales@northpeak.example', $3, 'Hello', 'Hi there', gen_random_uuid()::text, 'sent', now() - $4 * interval '1 second')`,
		ws, lead, email, int64(ago/time.Second)); err != nil {
		t.Fatal(err)
	}
}

func suppress(t *testing.T, pool *pgxpool.Pool, ws, email, reason, source string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `INSERT INTO suppressions (workspace_id, kind, value, reason, source) VALUES ($1, 'email', $2, $3, $4)`,
		ws, email, reason, source); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, h jobs.Handler, ws string) {
	t.Helper()
	if err := h(context.Background(), jobs.Job{WorkspaceID: &ws}); err != nil {
		t.Fatal(err)
	}
}

func TestShareOnlyWhatThePlanAllows(t *testing.T) {
	s, pool := testService(t)
	ctx := context.Background()
	f := &fakeNetwork{}
	s.Set(f)
	ws := newWS(t, pool)
	week := 8 * 24 * time.Hour
	sent(t, pool, ws, "Jane", "Roe", "jane.roe@acme.example", week)
	sent(t, pool, ws, "Bob", "Smith", "bob.smith@acme.example", week)
	suppress(t, pool, ws, "bob.smith@acme.example", "bounced", "bounce_report")
	sent(t, pool, ws, "Ann", "Lee", "ann.lee@gmail.com", week)                // personal provider
	sent(t, pool, ws, "Sam", "Fox", "sam.fox@acme.example", week)             // opted out
	suppress(t, pool, ws, "sam.fox@acme.example", "unsubscribed", "link")     //
	sent(t, pool, ws, "Kim", "Park", "kim.park@acme.example", 2*24*time.Hour) // not settled yet
	sent(t, pool, ws, "Lou", "Ray", "lou.ray@acme.example", week)             // bounced, but added by hand
	suppress(t, pool, ws, "lou.ray@acme.example", "bounced", "manual")        //

	p, err := PreviewCounts(ctx, pool, ws)
	if err != nil {
		t.Fatal(err)
	}
	if p != (Preview{Addresses: 2, Delivered: 1, Bounced: 1, Domains: 1}) {
		t.Fatalf("preview = %+v", p)
	}

	// Not a member: nothing is shared.
	run(t, s.shareJob, ws)
	if len(f.got[ws]) != 0 {
		t.Fatalf("shared before joining: %+v", f.got[ws])
	}

	if changed, err := Join(ctx, pool, ws, ""); err != nil || !changed {
		t.Fatalf("join: %v %v", changed, err)
	}
	if changed, _ := Join(ctx, pool, ws, ""); changed {
		t.Fatal("joining twice changed something")
	}
	// The hourly tick queues sharing for members, once an hour.
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var queued int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = $1 AND workspace_id = $2`, JobShare, ws).Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("share jobs = %d (%v)", queued, err)
	}
	run(t, s.shareJob, ws)
	got := f.got[ws]
	slices.SortFunc(got, func(a, b Contribution) int {
		if a.Email < b.Email {
			return -1
		}
		return 1
	})
	if len(got) != 2 || got[0].Email != "bob.smith@acme.example" || got[0].Outcome != Bounced ||
		got[1].Email != "jane.roe@acme.example" || got[1].Outcome != Delivered ||
		!slices.Equal(got[1].Shapes, []string{"first.last"}) || got[1].Domain != "acme.example" {
		t.Fatalf("shared = %+v", got)
	}

	// The cursor moved: a second run shares nothing again.
	run(t, s.shareJob, ws)
	if len(f.got[ws]) != 2 {
		t.Fatalf("shared again: %+v", f.got[ws])
	}

	// Leaving queues the withdrawal, and the job does it.
	if changed, err := Leave(ctx, pool, ws, ""); err != nil || !changed {
		t.Fatalf("leave: %v %v", changed, err)
	}
	m, _ := Load(ctx, pool, ws)
	if m.Status != Left || m.WithdrawDueAt == nil || time.Until(*m.WithdrawDueAt) < 29*24*time.Hour {
		t.Fatalf("after leaving: %+v", m)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = $1 AND workspace_id = $2`, JobWithdraw, ws).Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("withdraw jobs = %d (%v)", queued, err)
	}
	run(t, s.shareJob, ws) // left: shares nothing
	run(t, s.withdrawJob, ws)
	if !slices.Contains(f.withdrawn, ws) || len(f.got[ws]) != 0 {
		t.Fatalf("not withdrawn: %+v", f)
	}
	m, _ = Load(ctx, pool, ws)
	if m.WithdrawnAt == nil || m.WithdrawDueAt != nil {
		t.Fatalf("withdrawal not recorded: %+v", m)
	}

	// Joining again after the withdrawal shares the past sends again.
	if _, err := Join(ctx, pool, ws, ""); err != nil {
		t.Fatal(err)
	}
	run(t, s.shareJob, ws)
	if len(f.got[ws]) != 2 {
		t.Fatalf("rejoined and shared %d", len(f.got[ws]))
	}
}

func TestLookupAsksOnlyWhatWasBrought(t *testing.T) {
	s, pool := testService(t)
	ctx := context.Background()
	ws := newWS(t, pool)

	// No network: no answer, nothing asked.
	if _, ok := s.LookupNetwork(ctx, ws, "acme.example", []string{"jane.roe@acme.example"}); ok {
		t.Fatal("answered without a network")
	}
	f := &fakeNetwork{answer: Answer{Shape: "first.last", ShapeConfidence: 70, Emails: map[string]Signal{
		"jane.roe@acme.example":  {Outcome: Delivered, Confidence: 88},
		"someone@acme.example":   {Outcome: Delivered, Confidence: 99}, // not asked: must not come back
		"bob.smith@acme.example": {Outcome: Bounced, Confidence: 80},
	}}}
	s.Set(f)
	a, ok := s.LookupNetwork(ctx, ws, "Acme.example", []string{"jane.roe@acme.example", "bob.smith@acme.example"})
	if !ok || a.Shape != "first.last" || len(a.Emails) != 2 || !a.Emails["jane.roe@acme.example"].Deliverable || a.Emails["bob.smith@acme.example"].Deliverable {
		t.Fatalf("answer = %+v %v", a, ok)
	}
	if q := f.queries[0]; q.Member || q.Domain != "acme.example" {
		t.Fatalf("query = %+v", q)
	}

	// Personal providers are never asked about.
	if _, ok := s.LookupNetwork(ctx, ws, "gmail.com", []string{"ann.lee@gmail.com"}); ok || len(f.queries) != 1 {
		t.Fatal("asked about a personal provider")
	}

	// A member says so; a refused lookup is no answer.
	if _, err := Join(ctx, pool, ws, ""); err != nil {
		t.Fatal(err)
	}
	f.err = ErrAllowance
	if _, ok := s.LookupNetwork(ctx, ws, "acme.example", []string{"jane.roe@acme.example"}); ok {
		t.Fatal("a refused lookup answered")
	}
	if !f.queries[1].Member {
		t.Fatal("member not passed on")
	}
}

func TestNothingHappensWhereTheNetworkIsOff(t *testing.T) {
	s, pool := testService(t)
	ctx := context.Background()
	ws := newWS(t, pool)
	f := &fakeNetwork{off: map[string]bool{ws: true}, answer: Answer{Shape: "first.last", ShapeConfidence: 90}}
	s.Set(f)
	sent(t, pool, ws, "Jane", "Roe", "jane.roe@acme.example", 8*24*time.Hour)
	if _, err := Join(ctx, pool, ws, ""); err != nil {
		t.Fatal(err)
	}
	if s.Available(ctx, ws) {
		t.Fatal("available where it is off")
	}
	run(t, s.shareJob, ws)
	if len(f.got[ws]) != 0 {
		t.Fatalf("shared where it is off: %+v", f.got[ws])
	}
	if _, ok := s.LookupNetwork(ctx, ws, "acme.example", []string{"jane.roe@acme.example"}); ok || len(f.queries) != 0 {
		t.Fatal("looked up where it is off")
	}
	if _, ok, _ := s.Allowance(ctx, ws, true); ok {
		t.Fatal("allowance where it is off")
	}
	// Turned on: it shares.
	delete(f.off, ws)
	run(t, s.shareJob, ws)
	if len(f.got[ws]) != 1 {
		t.Fatalf("shared %d once on", len(f.got[ws]))
	}
}
