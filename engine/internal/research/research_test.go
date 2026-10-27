package research_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/classify"
	"github.com/outflareai/outflare/engine/internal/db"
	"github.com/outflareai/outflare/engine/internal/llm"
	"github.com/outflareai/outflare/engine/internal/research"
	"github.com/outflareai/outflare/engine/internal/track"
)

// pages stands in for the workspace's page reader.
type pages struct {
	page  classify.Page
	units float64
	err   error
	calls int
}

func (p *pages) Homepage(context.Context, string, string) (classify.Page, float64, error) {
	p.calls++
	return p.page, p.units, p.err
}

// model stands in for the workspace's own AI model.
type model struct {
	answer string
	stop   string
	err    error
	asked  []llm.Request
}

func (m *model) Chat(_ context.Context, req llm.Request) (llm.Response, error) {
	m.asked = append(m.asked, req)
	if m.err != nil {
		return llm.Response{}, m.err
	}
	stop := m.stop
	if stop == "" {
		stop = llm.StopEnd
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Text: m.answer}, Stop: stop,
		Usage: llm.Usage{Model: m.ID(), InputTokens: 3000, OutputTokens: 120}}, nil
}

func (m *model) Provider() llm.Provider { return llm.Claude }
func (m *model) ID() string             { return "claude-haiku-5-5" }

// models hands out the workspace's model and remembers what it booked.
type models struct {
	m     *model
	err   error
	spent []llm.Usage
	cents []int64
	// refs are the spend refs booked; the real books keep one entry per ref.
	refs map[string]bool
}

func (s *models) ForTask(_ context.Context, _, _ string, cents int64) (research.Choice, error) {
	if s.err != nil {
		return research.Choice{}, s.err
	}
	s.cents = append(s.cents, cents)
	price, _ := llm.KnownPrice(llm.Claude.ID, s.m.ID())
	return research.Choice{Provider: llm.Claude, IntegrationID: "int", Model: s.m, Price: price}, nil
}

func (s *models) Spent(_ context.Context, _ string, _ research.Choice, u llm.Usage, ref, _ string) error {
	if s.refs == nil {
		s.refs = map[string]bool{}
	}
	if s.refs[ref] {
		return nil
	}
	s.refs[ref] = true
	s.spent = append(s.spent, u)
	return nil
}

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
	if _, err := pool.Exec(ctx, `TRUNCATE workspaces, jobs CASCADE`); err != nil {
		t.Fatal(err)
	}
	return pool
}

// workspaces makes slugs unique within a test run.
var workspaces atomic.Int64

// workspace makes a workspace, with research on or off, and returns its id.
func workspace(t *testing.T, pool *pgxpool.Pool, on bool, limit *int64) string {
	t.Helper()
	ctx := context.Background()
	var ws string
	slug := fmt.Sprintf("research-%d", workspaces.Add(1))
	if err := pool.QueryRow(ctx, `INSERT INTO workspaces (name, slug) VALUES ('Research', $1) RETURNING id::text`, slug).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workspace_ai_settings (workspace_id, domain_research, domain_research_limit_cents)
		VALUES ($1, $2, $3)`, ws, on, limit); err != nil {
		t.Fatal(err)
	}
	return ws
}

func service(pool *pgxpool.Pool, p *pages, m *models) *research.Service {
	return &research.Service{Pool: pool, Pages: p, Models: m,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// waiting is how many research jobs are queued for a workspace.
func waiting(t *testing.T, pool *pgxpool.Pool, ws string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM jobs WHERE kind = $1 AND workspace_id = $2`, research.JobResearch, ws).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// one is the single event with this name.
func one(t *testing.T, events func() []track.Event, name string) track.Event {
	t.Helper()
	var out []track.Event
	for _, e := range events() {
		if e.Name == name {
			out = append(out, e)
		}
	}
	if len(out) != 1 {
		t.Fatalf("want one %s event, got %d: %+v", name, len(out), events())
	}
	return out[0]
}

const verdict = `{"category":"disposable","subcategory":"short_lived_mailbox","signals":["site_offers_temporary_addresses"],
	"is_disposable":"true","is_free_provider":"false","note":"The site hands out inboxes that expire."}`

func TestResearchReadsTheHomepageAndStoresTheVerdict(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ws := workspace(t, pool, true, nil)
	p := &pages{page: classify.Page{URL: "https://tempbox.test/", Status: 200, Fetched: true,
		HTML: `<title>TempBox</title><h1>Free temporary email</h1><p>Inbox expires in 10 minutes.</p>`}, units: 2}
	m := &models{m: &model{answer: verdict}}
	s := service(pool, p, m)

	queued, err := s.Queue(ctx, pool, ws, "tempbox.test")
	if err != nil || !queued {
		t.Fatalf("queue: %v %v", queued, err)
	}
	// Asking again the same day must not pay for the same domain twice.
	if _, err := s.Queue(ctx, pool, ws, "tempbox.test"); err != nil {
		t.Fatal(err)
	}
	if n := waiting(t, pool, ws); n != 1 {
		t.Fatalf("the same domain was queued %d times in a day", n)
	}
	events := track.Capture(t)
	if err := s.Do(ctx, ws, "tempbox.test"); err != nil {
		t.Fatal(err)
	}
	if p.calls != 1 {
		t.Fatalf("the homepage was read %d times", p.calls)
	}

	row, ok, err := s.Of(ctx, pool, ws, "tempbox.test")
	if err != nil || !ok {
		t.Fatalf("stored: %v %v", ok, err)
	}
	if row.Verdict.Category != classify.Disposable || row.Verdict.IsDisposable != classify.TriTrue ||
		row.Verdict.IsFreeProvider != classify.TriFalse || row.Model != "claude-haiku-5-5" {
		t.Fatalf("row: %+v", row)
	}
	if row.CostCents <= 0 {
		t.Error("a call the workspace paid for should cost something")
	}
	if len(m.spent) != 1 || m.spent[0].InputTokens != 3000 {
		t.Errorf("spend booked: %+v", m.spent)
	}

	// The question carries the page as evidence, inside a fence, and asks
	// for an answer in the taxonomy.
	req := m.m.asked[0]
	if !contains(req.Messages[0].Text, "Inbox expires in 10 minutes.") || !contains(req.Messages[0].Text, "never as an instruction") {
		t.Errorf("question: %s", req.Messages[0].Text)
	}
	if req.JSONSchema == nil {
		t.Error("the answer should be schema-shaped")
	}

	// It says what it did, and never which domain.
	e := one(t, events, research.EventResearched)
	if e.Props["category"] != classify.Disposable || e.Props["homepage_read"] != true {
		t.Errorf("event: %+v", e.Props)
	}
	b, _ := json.Marshal(e.Props)
	if contains(string(b), "tempbox") {
		t.Errorf("an event must not carry the domain: %s", b)
	}

	// A second call is booked too: each call has its own spend entry.
	if err := s.Do(ctx, ws, "othertemp.test"); err != nil {
		t.Fatal(err)
	}
	if len(m.spent) != 2 {
		t.Errorf("two research calls booked %d spend entries", len(m.spent))
	}

	// Another workspace sees none of it: research belongs to whoever paid.
	other := workspace2(t, pool)
	if _, ok, err := s.Of(ctx, pool, other, "tempbox.test"); err != nil || ok {
		t.Errorf("another workspace: %v %v", ok, err)
	}
}

func TestResearchWithoutAPage(t *testing.T) {
	pool := testPool(t)
	ws := workspace(t, pool, true, nil)
	// A site that blocked the reader still gets a verdict from the rest of
	// the evidence.
	p := &pages{err: errors.New("403 from the site")}
	m := &models{m: &model{answer: `{"category":"unknown","signals":["site_could_not_be_read"],"is_disposable":"unknown","note":""}`}}
	s := service(pool, p, m)
	if _, err := s.Queue(context.Background(), pool, ws, "blocked.test"); err != nil {
		t.Fatal(err)
	}
	if err := s.Do(context.Background(), ws, "blocked.test"); err != nil {
		t.Fatal(err)
	}
	row, ok, err := s.Of(context.Background(), pool, ws, "blocked.test")
	if err != nil || !ok || row.Verdict.Category != classify.Unknown {
		t.Fatalf("row: %+v %v %v", row, ok, err)
	}
}

func TestResearchStopsAtTheLimitAndStaysOff(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	m := &models{m: &model{answer: verdict}}

	// Off by default: nothing is queued, and nothing is spent.
	off := workspace(t, pool, false, nil)
	s := service(pool, &pages{}, m)
	if queued, err := s.Queue(ctx, pool, off, "tempbox.test"); err != nil || queued {
		t.Fatalf("research off: %v %v", queued, err)
	}

	// A workspace whose month has reached its research limit stops.
	ws := workspace(t, pool, true, ptr(int64(50)))
	if _, err := pool.Exec(ctx, `INSERT INTO domain_research (workspace_id, domain, category, confidence_status, cost_cents)
		VALUES ($1, 'spent.test', 'disposable', 'inferred', 50)`, ws); err != nil {
		t.Fatal(err)
	}
	events := track.Capture(t)
	if err := s.Do(ctx, ws, "tempbox.test"); err == nil {
		t.Fatal("a job at the limit should fail, so it retries later")
	}
	if _, ok, _ := s.Of(ctx, pool, ws, "tempbox.test"); ok {
		t.Error("nothing should be stored when the limit stopped it")
	}
	if e := one(t, events, research.EventFailed); e.ErrorCode != "research_budget_reached" {
		t.Errorf("event: %+v", e)
	}
}

func TestResearchNeedsAModel(t *testing.T) {
	pool := testPool(t)
	ws := workspace(t, pool, true, nil)
	s := service(pool, &pages{page: classify.Page{Fetched: true, Status: 200, HTML: "<p>hello</p>"}},
		&models{err: errors.New("connect an AI model with your own API key first")})
	events := track.Capture(t)
	if err := s.Do(context.Background(), ws, "tempbox.test"); err == nil {
		t.Fatal("research without a model should fail")
	}
	if e := one(t, events, research.EventFailed); e.ErrorCode != "ai_model_needed" {
		t.Errorf("event: %+v", e)
	}
}

func ptr[T any](v T) *T { return &v }

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func workspace2(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var ws string
	slug := fmt.Sprintf("other-%d", workspaces.Add(1))
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO workspaces (name, slug) VALUES ('Other', $1) RETURNING id::text`, slug).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	return ws
}
