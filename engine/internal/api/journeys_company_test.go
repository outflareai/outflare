package api_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/journeys"
)

// A journey about companies: a company goes through it once, started by
// what one of its people does, and its steps act on the company.
func TestCompanyJourneys(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand

	// The step catalog says which steps a company journey can take.
	for _, k := range owner.must(200, "GET", "/v1/workspaces/"+ws+"/journeys/step-kinds", nil)["data"].([]any) {
		m := k.(map[string]any)
		if (m["kind"] == "raise_signal" || m["kind"] == "create_task") && m["companies"] != true || m["kind"] == "email" && m["companies"] != false {
			t.Fatalf("%s companies = %v", m["kind"], m["companies"])
		}
	}

	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme"})["id"].(string)
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana",
		"identities": []any{map[string]any{"namespace": "email", "value": "ana@acme.example"}}})["id"].(string)
	bo := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Bo",
		"identities": []any{map[string]any{"namespace": "email", "value": "bo@acme.example"}}})["id"].(string)
	for _, p := range []string{ana, bo} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO record_relationships (workspace_id, from_id, to_id, kind) VALUES ($1, $2, $3, 'companies')`, ws, p, acme); err != nil {
			t.Fatal(err)
		}
	}
	// Someone with no company.
	solo := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Solo",
		"identities": []any{map[string]any{"namespace": "email", "value": "solo@example.com"}}})["id"].(string)

	// Steps that need a person are refused, and who it is about is person
	// or account.
	owner.must(400, "POST", base+"/journeys", map[string]any{"name": "Bad", "record_type": "account",
		"steps": []map[string]any{{"id": "mail", "kind": "email", "template": "welcome"}}})
	owner.must(400, "POST", base+"/journeys", map[string]any{"name": "Bad", "record_type": "campaign"})

	steps := []map[string]any{
		{"id": "tag", "kind": "add_tag", "tag": "expanding", "next": "flag"},
		{"id": "flag", "kind": "raise_signal", "signal_type": "upsell", "title": "Adding seats", "next": "task"},
		{"id": "task", "kind": "create_task", "title": "Check in with the company"},
	}
	j := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Expansion", "record_type": "account", "steps": steps,
		"trigger": map[string]any{"kind": "event", "event": "seat.added"}})
	if j["record_type"] != "account" {
		t.Fatalf("record_type = %v", j["record_type"])
	}
	jid := j["id"].(string)
	// A journey about people by default.
	people := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Welcome", "trigger": map[string]any{"kind": "manual"},
		"steps": []map[string]any{{"id": "tag", "kind": "add_tag", "tag": "welcomed"}}})
	if people["record_type"] != "person" {
		t.Fatalf("default record_type = %v", people["record_type"])
	}
	// Turning it into a company journey checks its steps.
	owner.must(200, "PATCH", base+"/journeys/"+people["id"].(string), map[string]any{"record_type": "account"})
	owner.must(200, "PATCH", base+"/journeys/"+people["id"].(string), map[string]any{"record_type": "person"})
	owner.must(200, "POST", base+"/journeys/"+jid+"/activate", nil)

	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	svc := &journeys.Service{Pool: e.pool, Log: log, Fields: e.srv}
	svc.Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	defer func() { cancel(); <-done }()
	runs := func(rec string) []any {
		t.Helper()
		return owner.must(200, "GET", base+"/journeys/"+jid+"/runs?record="+rec, nil)["data"].([]any)
	}
	settle := func() {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for {
			var n int
			_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE workspace_id = $1 AND status IN ('queued', 'running') AND run_at <= now()`, ws).Scan(&n)
			if n == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("waited 60s for jobs")
			}
			time.Sleep(30 * time.Millisecond)
		}
	}

	// Ana adds a seat: Acme starts, not Ana. Bo doing the same starts
	// nothing more, and neither does someone with no company.
	for i, p := range []string{ana, bo, solo} {
		if _, err := customers.Record(ctx, e.pool, ws, customers.Event{RecordID: p, Name: "seat.added", Source: "api", MessageID: "seat-" + string(rune('a'+i))}); err != nil {
			t.Fatal(err)
		}
		settle()
	}
	if n := len(runs(acme)); n != 1 {
		t.Fatalf("Acme's runs: %d, want 1", n)
	}
	if len(runs(ana)) != 0 || len(runs(solo)) != 0 {
		t.Fatal("a person started a journey about companies")
	}
	if st := runs(acme)[0].(map[string]any)["status"]; st != "completed" {
		t.Fatalf("Acme's run = %v", st)
	}
	// Its steps acted on the company.
	tags := owner.must(200, "GET", base+"/records/"+acme, nil)["attributes"].(map[string]any)["tags"].([]any)
	if len(tags) != 1 || tags[0] != "expanding" {
		t.Fatalf("Acme's tags = %v", tags)
	}
	sig := owner.must(200, "GET", base+"/signals?record="+acme, nil)["data"].([]any)
	if len(sig) != 1 || sig[0].(map[string]any)["type"] != "upsell" || sig[0].(map[string]any)["source"] != "journey" || sig[0].(map[string]any)["title"] != "Adding seats" {
		t.Fatalf("Acme's signals = %v", sig)
	}
	// Once it has run, who it is about can't change.
	owner.must(200, "POST", base+"/journeys/"+jid+"/pause", nil)
	owner.must(400, "PATCH", base+"/journeys/"+jid, map[string]any{"record_type": "person"})
}

// A journey about deals starts on a deal's own events, which sit on its
// person and name the deal, and its steps act on the deal.
func TestDealJourneys(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana",
		"identities": []any{map[string]any{"namespace": "email", "value": "ana@acme.example"}}})["id"].(string)
	deal := owner.must(201, "POST", base+"/records", map[string]any{"type": "deal", "name": "Acme renewal"})["id"].(string)
	other := owner.must(201, "POST", base+"/records", map[string]any{"type": "deal", "name": "Acme add-on"})["id"].(string)

	owner.must(400, "POST", base+"/journeys", map[string]any{"name": "Bad", "record_type": "deal",
		"steps": []map[string]any{{"id": "mail", "kind": "email", "template": "welcome"}}})
	owner.must(400, "POST", base+"/journeys", map[string]any{"name": "Bad", "record_type": "deal",
		"steps": []map[string]any{{"id": "tag", "kind": "add_tag", "tag": "hot"}}})
	j := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Chase stalled deals", "record_type": "deal",
		"trigger": map[string]any{"kind": "event", "event": "deal.stage_changed"}, "goal_event": "deal.won",
		"steps": []map[string]any{
			{"id": "tag", "kind": "set_field", "field": "lost_note", "value": "Send the proposal", "next": "wait"},
			{"id": "wait", "kind": "wait", "wait_seconds": 86400, "next": "task"},
			{"id": "task", "kind": "create_task", "title": "Chase the proposal"},
		}})
	if j["record_type"] != "deal" {
		t.Fatalf("record_type = %v", j["record_type"])
	}
	jid := j["id"].(string)
	owner.must(200, "POST", base+"/journeys/"+jid+"/activate", nil)

	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	svc := &journeys.Service{Pool: e.pool, Log: log, Fields: e.srv}
	svc.Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	defer func() { cancel(); <-done }()
	settle := func() {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for {
			var n int
			_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE workspace_id = $1 AND status IN ('queued', 'running') AND run_at <= now()`, ws).Scan(&n)
			if n == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("waited 60s for jobs")
			}
			time.Sleep(30 * time.Millisecond)
		}
	}
	runs := func(rec string) []any {
		t.Helper()
		return owner.must(200, "GET", base+"/journeys/"+jid+"/runs?record="+rec, nil)["data"].([]any)
	}
	emit := func(name, id string) {
		t.Helper()
		if _, err := customers.Record(ctx, e.pool, ws, customers.Event{RecordID: ana, Name: name, Source: "deals", MessageID: name + id,
			Properties: map[string]any{"deal_id": id}}); err != nil {
			t.Fatal(err)
		}
		settle()
	}
	// The deal moved a stage: the event is on Ana, the deal starts.
	emit("deal.stage_changed", deal)
	if len(runs(deal)) != 1 || len(runs(ana)) != 0 || len(runs(other)) != 0 {
		t.Fatalf("runs: deal %d, Ana %d, other %d", len(runs(deal)), len(runs(ana)), len(runs(other)))
	}
	if st := runs(deal)[0].(map[string]any)["status"]; st != "active" {
		t.Fatalf("the deal's run = %v", runs(deal)[0])
	}
	if next := owner.must(200, "GET", base+"/records/"+deal, nil)["attributes"].(map[string]any)["lost_note"]; next != "Send the proposal" {
		t.Fatalf("the deal's next step = %v", next)
	}
	// Another deal won ends nothing; this deal won ends its run.
	emit("deal.won", other)
	if st := runs(deal)[0].(map[string]any)["status"]; st != "active" {
		t.Fatalf("another deal's win ended the run: %v", st)
	}
	emit("deal.won", deal)
	if r := runs(deal)[0].(map[string]any); r["status"] != "exited" || r["exit_reason"] != "goal" {
		t.Fatalf("the deal's run after the win = %v", r)
	}
}
