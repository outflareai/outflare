package companysteps

import (
	"context"
	"io"
	"log/slog"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/db"
	"github.com/outflareai/outflare/engine/internal/pipeline"
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

type linked struct {
	record   string
	existed  bool
	warnings []string
}

func TestCompanySteps(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var ws string
	if err := pool.QueryRow(ctx, `INSERT INTO workspaces (name, slug) VALUES ('Acme', 'acme-' || substr(gen_random_uuid()::text, 1, 8)) RETURNING id::text`).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	// Northpeak is a company already; Globex opted out, Initech bounced,
	// Umbrella is a customer (a warning only), Hooli is new.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	have, err := customers.EnsureCompanies(ctx, tx, ws, nil, "csv", []customers.Company{{Domain: "northpeak.example", Name: "Northpeak"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO suppressions (workspace_id, kind, value, reason, method) VALUES
		($1, 'domain', 'globex.example', 'do_not_contact', 'manual'), ($1, 'domain', 'initech.example', 'bounced', 'automated'),
		($1, 'domain', 'umbrella.example', 'customer', 'manual')`, ws); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	links := map[string]linked{}
	stops := map[string]string{}
	r := pipeline.NewRunner(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.PollInterval = 20 * time.Millisecond
	for _, s := range Stages(pool, Config{
		Pipeline: "companies_test",
		Via:      "lead_finder",
		Company: func(_ context.Context, it pipeline.Item) (customers.Company, *string, error) {
			return customers.Company{Name: "Co " + it.Ref, Attributes: map[string]any{"country": "AU"}}, nil, nil
		},
		Linked: func(_ context.Context, _ pgx.Tx, it pipeline.Item, id string, existed bool, warnings []string) error {
			mu.Lock()
			defer mu.Unlock()
			links[it.Ref] = linked{id, existed, warnings}
			return nil
		},
		Stopped: func(_ context.Context, it pipeline.Item, code string, _ []string) error {
			mu.Lock()
			defer mu.Unlock()
			stops[it.Ref] = code
			return nil
		},
	}) {
		r.Register(s)
	}
	go r.Run(ctx)

	var run string
	if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&run); err != nil {
		t.Fatal(err)
	}
	if err := pipeline.Start(ctx, pool, ws, "companies_test", run); err != nil {
		t.Fatal(err)
	}
	var items []pipeline.NewItem
	for _, d := range []string{"northpeak.example", "globex.example", "initech.example", "umbrella.example", "hooli.example"} {
		items = append(items, pipeline.NewItem{Ref: d, Stage: Known})
	}
	if _, err := pipeline.Add(ctx, pool, ws, run, items); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		mu.Lock()
		n := len(links) + len(stops)
		mu.Unlock()
		if n == 5 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("links %v, stops %v", links, stops)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if l := links["northpeak.example"]; l.record != have["northpeak.example"] || !l.existed {
		t.Fatalf("known company: %+v", l)
	}
	if stops["globex.example"] != OptedOut || stops["initech.example"] != Bounced {
		t.Fatalf("stops: %v", stops)
	}
	if l := links["umbrella.example"]; l.record == "" || l.existed || !slices.Equal(l.warnings, []string{"suppressed_domain:customer"}) {
		t.Fatalf("customer: %+v", l)
	}
	hooli := links["hooli.example"]
	var origin, via, name string
	if err := pool.QueryRow(ctx, `SELECT origin, acquired_via, name FROM records WHERE id = $1 AND workspace_id = $2`, hooli.record, ws).Scan(&origin, &via, &name); err != nil ||
		origin != "prospect" || via != "lead_finder" || name != "Co hooli.example" || len(hooli.warnings) != 0 {
		t.Fatalf("new company: %s %s %s %+v %v", origin, via, name, hooli, err)
	}
	v, err := pipeline.View(ctx, pool, ws, run)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]pipeline.StageCount{}
	for _, s := range v.Stages {
		counts[s.Stage] = s
	}
	if counts[Known].Skipped != 1 || counts[Known].Errors[AlreadyKnown] != 1 || counts[ContactRules].Skipped != 2 || counts[AddToCRM].Done != 2 {
		t.Fatalf("stages: %+v", v.Stages)
	}
}
