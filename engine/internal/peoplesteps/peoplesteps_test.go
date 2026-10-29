package peoplesteps

import (
	"context"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/db"
	"github.com/outflareai/outflare/engine/internal/emailfind"
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

// fakeFinder guesses first.last@domain, except for Nobody, and confirms
// the addresses it is asked to verify.
type fakeFinder struct{}

func (fakeFinder) Resolve(_ context.Context, _ string, req emailfind.FindRequest) (emailfind.FindResult, error) {
	var out emailfind.FindResult
	for _, it := range req.Items {
		r := emailfind.ItemResult{Ref: it.Ref, Code: emailfind.CodeFound, Confidence: 80,
			Email: strings.ToLower(it.FirstName + "." + it.LastName + "@" + it.Domain)}
		switch it.FirstName {
		case "Nobody":
			r = emailfind.ItemResult{Ref: it.Ref, Code: emailfind.CodeNotFound}
		case "Maybe":
			r.Code, r.Confidence = emailfind.CodeRisky, 40
		}
		out.Items = append(out.Items, r)
	}
	return out, nil
}

func (fakeFinder) VerifyEmails(_ context.Context, _ string, req emailfind.VerifyRequest) (emailfind.FindResult, error) {
	var out emailfind.FindResult
	for _, it := range req.Items {
		out.Items = append(out.Items, emailfind.ItemResult{Ref: it.Ref, Email: it.Email, Code: emailfind.CodeVerified, Confidence: 95})
	}
	return out, nil
}

func TestPeopleSteps(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var ws string
	if err := pool.QueryRow(ctx, `INSERT INTO workspaces (name, slug) VALUES ('Acme', 'acme-' || substr(gen_random_uuid()::text, 1, 8)) RETURNING id::text`).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	companies, err := customers.EnsureCompanies(ctx, tx, ws, nil, "test", []customers.Company{{Domain: "acme.example", Name: "Acme"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	acme := companies["acme.example"]
	// Bea opted out, Cal's address bounced, Dee is a partner (a warning).
	if _, err := pool.Exec(ctx, `INSERT INTO suppressions (workspace_id, kind, value, reason, method) VALUES
		($1, 'email', 'bea.lee@acme.example', 'unsubscribed', 'manual'), ($1, 'email', 'cal.ng@acme.example', 'bounced', 'automated'),
		($1, 'email', 'dee.ray@acme.example', 'partner', 'manual')`, ws); err != nil {
		t.Fatal(err)
	}

	// The feature's own data: who each item is, and what happened to it.
	people := map[string]*Contact{
		"ana":    {FirstName: "Ana", LastName: "Diaz", Title: "CFO", Domain: "acme.example", CompanyID: acme},
		"bea":    {FirstName: "Bea", LastName: "Lee", Domain: "acme.example", CompanyID: acme},
		"cal":    {FirstName: "Cal", LastName: "Ng", Domain: "acme.example", CompanyID: acme},
		"dee":    {FirstName: "Dee", LastName: "Ray", Domain: "acme.example", CompanyID: acme},
		"info":   {Email: "info@acme.example", Domain: "acme.example", CompanyID: acme},
		"maybe":  {FirstName: "Maybe", LastName: "Soon", Domain: "acme.example", CompanyID: acme},
		"nobody": {FirstName: "Nobody", LastName: "Here", Domain: "acme.example", CompanyID: acme},
	}
	var mu sync.Mutex
	linked := map[string][]string{}
	records := map[string]string{}
	stops := map[string]string{}
	r := pipeline.NewRunner(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.PollInterval = 20 * time.Millisecond
	for _, s := range Stages(pool, fakeFinder{}, Config{
		Pipeline: "people_test",
		Via:      "lead_finder",
		Contact: func(_ context.Context, it pipeline.Item) (Contact, error) {
			mu.Lock()
			defer mu.Unlock()
			return *people[it.Ref], nil
		},
		Found: func(_ context.Context, it pipeline.Item, email string, code emailfind.Code, _ int) error {
			mu.Lock()
			defer mu.Unlock()
			people[it.Ref].Email, people[it.Ref].Risky = email, code == emailfind.CodeRisky
			return nil
		},
		Linked: func(_ context.Context, _ pgx.Tx, it pipeline.Item, id string, warnings []string) error {
			mu.Lock()
			defer mu.Unlock()
			linked[it.Ref], records[it.Ref] = append([]string{}, warnings...), id
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
	if err := pipeline.Start(ctx, pool, ws, "people_test", run); err != nil {
		t.Fatal(err)
	}
	var items []pipeline.NewItem
	for ref := range people {
		items = append(items, pipeline.NewItem{Ref: ref, Stage: FindEmail})
	}
	if _, err := pipeline.Add(ctx, pool, ws, run, items); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		mu.Lock()
		n := len(linked) + len(stops)
		mu.Unlock()
		if n == len(people) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("linked %v, stops %v", linked, stops)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if stops["bea"] != OptedOut || stops["cal"] != Bounced || stops["nobody"] != NotFound || len(stops) != 3 {
		t.Fatalf("stops: %v", stops)
	}
	if !slices.Equal(linked["dee"], []string{"suppressed_email:partner"}) || !slices.Equal(linked["maybe"], []string{RiskyEmail}) || len(linked["ana"]) != 0 {
		t.Fatalf("warnings: %v", linked)
	}
	var origin, via, title string
	var link bool
	if err := pool.QueryRow(ctx, `SELECT origin, acquired_via, attributes->>'job_title',
			EXISTS (SELECT 1 FROM record_relationships WHERE from_id = r.id AND to_id = $3 AND kind = 'companies')
		FROM records r WHERE id = $1 AND workspace_id = $2`, records["ana"], ws, acme).Scan(&origin, &via, &title, &link); err != nil ||
		origin != "prospect" || via != "lead_finder" || title != "CFO" || !link {
		t.Fatalf("ana's record: %s %s %s %v %v", origin, via, title, link, err)
	}
	var email string
	if err := pool.QueryRow(ctx, `SELECT value FROM record_identities WHERE record_id = $1 AND namespace = 'email'`, records["info"]).Scan(&email); err != nil || email != "info@acme.example" {
		t.Fatalf("info's record: %s %v", email, err)
	}
}

func TestEnrollStep(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	ws := q(`INSERT INTO workspaces (name, slug) VALUES ('Acme', 'acme-' || substr(gen_random_uuid()::text, 1, 8)) RETURNING id::text`)
	seq := q(`INSERT INTO sequences (workspace_id, name, status) VALUES ($1, 'Intro', 'active') RETURNING id::text`, ws)
	other := q(`INSERT INTO sequences (workspace_id, name, status) VALUES ($1, 'Other', 'active') RETURNING id::text`, ws)
	gone := q(`INSERT INTO sequences (workspace_id, name, status) VALUES ($1, 'Old', 'archived') RETURNING id::text`, ws)

	people := map[string]*Contact{
		"ana":  {FirstName: "Ana", LastName: "Diaz", Title: "CFO", Email: "ana.diaz@acme.example", Domain: "acme.example"},
		"eve":  {FirstName: "Eve", LastName: "Moss", Email: "eve.moss@acme.example", Domain: "acme.example"},
		"olly": {FirstName: "Olly", LastName: "Fox", Email: "olly.fox@acme.example", Domain: "acme.example"},
		"ivy":  {FirstName: "Ivy", LastName: "Ko", Email: "ivy.ko@acme.example", Domain: "acme.example"},
		"zed":  {FirstName: "Zed", LastName: "Lo", Email: "zed.lo@acme.example", Domain: "acme.example"},
		"una":  {FirstName: "Una", LastName: "Pell", Email: "una.pell@acme.example", Domain: "acme.example"},
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range people {
		ids, err := customers.EnsurePeople(ctx, tx, ws, "prospect", "test", []customers.Person{{Email: p.Email}})
		if err != nil {
			t.Fatal(err)
		}
		p.RecordID = ids[p.Email]
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// Eve is already active in another sequence; Olly opted out after he
	// was added.
	evesLead := q(`INSERT INTO leads (workspace_id, email, record_id) VALUES ($1, $2, $3) RETURNING id::text`, ws, people["eve"].Email, people["eve"].RecordID)
	q(`INSERT INTO enrollments (workspace_id, sequence_id, lead_id) VALUES ($1, $2, $3) RETURNING id::text`, ws, other, evesLead)
	q(`INSERT INTO suppressions (workspace_id, kind, value, reason, method) VALUES ($1, 'email', $2, 'unsubscribed', 'manual') RETURNING id::text`, ws, people["olly"].Email)

	campaign := map[string]string{"ana": seq, "eve": seq, "olly": seq, "ivy": "", "zed": gone, "una": seq}
	// Una waits for a person to approve her.
	held := map[string]bool{"una": true}
	var mu sync.Mutex
	enrolled := map[string]string{}
	stops := map[string]string{}
	r := pipeline.NewRunner(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.PollInterval = 20 * time.Millisecond
	for _, s := range Stages(pool, fakeFinder{}, Config{
		Pipeline: "enroll_test",
		Via:      "lead_finder",
		Contact: func(_ context.Context, it pipeline.Item) (Contact, error) {
			return *people[it.Ref], nil
		},
		Found:  func(context.Context, pipeline.Item, string, emailfind.Code, int) error { return nil },
		Linked: func(context.Context, pgx.Tx, pipeline.Item, string, []string) error { return nil },
		Stopped: func(_ context.Context, it pipeline.Item, code string, _ []string) error {
			mu.Lock()
			defer mu.Unlock()
			stops[it.Ref] = code
			return nil
		},
		Campaign: func(_ context.Context, it pipeline.Item) (string, bool, error) {
			mu.Lock()
			defer mu.Unlock()
			return campaign[it.Ref], held[it.Ref], nil
		},
		Enrolled: func(_ context.Context, _ pgx.Tx, it pipeline.Item, sequenceID, _ string) error {
			mu.Lock()
			defer mu.Unlock()
			enrolled[it.Ref] = sequenceID
			return nil
		},
	}) {
		r.Register(s)
	}
	go r.Run(ctx)

	start := func(refs ...string) string {
		run := q(`SELECT gen_random_uuid()::text`)
		if err := pipeline.Start(ctx, pool, ws, "enroll_test", run); err != nil {
			t.Fatal(err)
		}
		var items []pipeline.NewItem
		for _, ref := range refs {
			items = append(items, pipeline.NewItem{Ref: ref, Stage: Enroll})
		}
		if _, err := pipeline.Add(ctx, pool, ws, run, items); err != nil {
			t.Fatal(err)
		}
		return run
	}
	wait := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(15 * time.Second); !ok(); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("waiting for %s: enrolled %v, stops %v", what, enrolled, stops)
			}
		}
	}

	run := start("ana", "eve", "olly", "ivy", "una")
	wait("the first run", func() bool {
		v, err := pipeline.View(ctx, pool, ws, run)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range v.Stages {
			if s.Waiting+s.Working > 0 {
				return false
			}
		}
		return len(v.Stages) > 0
	})
	if enrolled["ana"] != seq || len(enrolled) != 1 || enrolled["una"] != "" || stops["eve"] != InAnotherCampaign || stops["olly"] != OptedOut || len(stops) != 2 {
		t.Fatalf("enrolled %v, stops %v", enrolled, stops)
	}
	var source, title string
	var n int
	if err := pool.QueryRow(ctx, `SELECT l.source, l.title, (SELECT count(*) FROM enrollments e WHERE e.lead_id = l.id AND e.sequence_id = $3)
		FROM leads l WHERE l.workspace_id = $1 AND l.record_id = $2`, ws, people["ana"].RecordID, seq).Scan(&source, &title, &n); err != nil ||
		source != "lead_finder" || title != "CFO" || n != 1 {
		t.Fatalf("ana's lead: %s %s %d %v", source, title, n, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'sequence.leads_enrolled'
		AND data->>'via' = 'lead_finder'`, ws).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit events: %d %v", n, err)
	}

	// Approved, Una goes in.
	mu.Lock()
	held["una"] = false
	mu.Unlock()
	if n, err := pipeline.Release(ctx, pool, ws, run, []string{"una"}, Enroll, ""); err != nil || n != 1 {
		t.Fatalf("release: %d %v", n, err)
	}
	wait("una", func() bool { mu.Lock(); defer mu.Unlock(); return enrolled["una"] == seq })

	// An archived campaign pauses the run until the feature picks another.
	run = start("zed")
	wait("the pause", func() bool {
		v, err := pipeline.View(ctx, pool, ws, run)
		if err != nil {
			t.Fatal(err)
		}
		return v.PauseCode != nil && *v.PauseCode == CampaignUnavailable
	})
}
