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

// A journey's open_renewal_deal step opens one deal per renewal, and the
// forecast counts that deal in place of the renewal.
func TestRenewalDeals(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	me := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)

	owner.must(201, "POST", base+"/catalog/types/account/fields", map[string]any{"label": "Renews on", "value_type": "date"})
	soon := time.Now().UTC().AddDate(0, 0, 20).Format(time.DateOnly)
	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme",
		"attributes": map[string]any{"renews_on": soon, "owner": me}})["id"].(string)
	// Northpeak renews too far out to count.
	north := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Northpeak",
		"attributes": map[string]any{"renews_on": time.Now().UTC().AddDate(0, 0, 200).Format(time.DateOnly)}})["id"].(string)
	if _, err := e.pool.Exec(ctx, `INSERT INTO revenue_customers (workspace_id, brand_id, record_id, mrr_minor) VALUES ($1, $2, $3, 100000)`, ws, brand, acme); err != nil {
		t.Fatal(err)
	}
	people := map[string]string{}
	for name, company := range map[string]string{"ana": acme, "nia": north} {
		p := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": name,
			"identities": []any{map[string]any{"namespace": "email", "value": name + "@acme.example"}}})["id"].(string)
		if _, err := e.pool.Exec(ctx, `INSERT INTO record_relationships (workspace_id, from_id, to_id, kind) VALUES ($1, $2, $3, 'companies')`, ws, p, company); err != nil {
			t.Fatal(err)
		}
		people[name] = p
	}

	// Only a journey about companies can take the step.
	step := []map[string]any{{"id": "deal", "kind": "open_renewal_deal", "within_days": 60}}
	owner.must(400, "POST", base+"/journeys", map[string]any{"name": "Bad", "steps": step, "trigger": map[string]any{"kind": "manual"}})
	owner.must(400, "POST", base+"/journeys", map[string]any{"name": "Bad", "record_type": "account", "trigger": map[string]any{"kind": "manual"},
		"steps": []map[string]any{{"id": "deal", "kind": "open_renewal_deal", "within_days": 900}}})
	jid := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Renewals", "record_type": "account", "entry": "again", "steps": step,
		"trigger": map[string]any{"kind": "event", "event": "renewal.checked"}})["id"].(string)
	owner.must(200, "POST", base+"/journeys/"+jid+"/activate", nil)

	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	svc := &journeys.Service{Pool: e.pool, Log: log, Fields: e.srv, Deals: e.srv}
	svc.Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	defer func() { cancel(); <-done }()
	n := 0
	check := func(person string) {
		t.Helper()
		n++
		if _, err := customers.Record(ctx, e.pool, ws, customers.Event{RecordID: people[person], Name: "renewal.checked", Source: "api", MessageID: "check-" + string(rune('a'+n))}); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(60 * time.Second)
		for {
			var waiting, running int
			_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE workspace_id = $1 AND status IN ('queued', 'running') AND run_at <= now()`, ws).Scan(&waiting)
			_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM journey_runs WHERE journey_id = $1 AND status = 'active'`, jid).Scan(&running)
			if waiting == 0 && running == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("waited 60s for the journey")
			}
			time.Sleep(30 * time.Millisecond)
		}
	}
	deals := func() []map[string]any {
		t.Helper()
		var out []map[string]any
		for _, d := range owner.must(200, "GET", base+"/records?type=deal", nil)["data"].([]any) {
			out = append(out, d.(map[string]any))
		}
		return out
	}

	check("ana")
	check("nia")
	list := deals()
	if len(list) != 1 {
		t.Fatalf("deals after the first check: %v", list)
	}
	deal := list[0]
	attrs := deal["attributes"].(map[string]any)
	value, _ := attrs["value"].(map[string]any)
	if deal["name"] != "Renewal: Acme" || attrs["close_date"] != soon || attrs["associated_company"] != acme || value["amount_minor"] != float64(1200000) {
		t.Fatalf("the renewal deal: %v", deal)
	}
	var of, on string
	if err := e.pool.QueryRow(ctx, `SELECT renewal_of::text, renewal_on::text FROM deals WHERE id = $1 AND state = 'open'`, deal["id"]).Scan(&of, &on); err != nil || of != acme || on != soon {
		t.Fatalf("the deal's row: %s %s %v", of, on, err)
	}
	// Checking again opens nothing more, even once the deal is closed.
	check("ana")
	if _, err := e.pool.Exec(ctx, `UPDATE deals SET state = 'lost', closed_at = now() WHERE id = $1`, deal["id"]); err != nil {
		t.Fatal(err)
	}
	check("ana")
	if got := deals(); len(got) != 1 {
		t.Fatalf("deals after checking again: %v", got)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE deals SET state = 'open', closed_at = NULL WHERE id = $1`, deal["id"]); err != nil {
		t.Fatal(err)
	}

	// The forecast counts the deal as the renewal: once, under renewals,
	// by the company's health and not by the deal's stage.
	f := owner.must(200, "GET", base+"/revenue/forecast", nil)
	if f["ends_on"].(string) >= soon {
		renewals := f["renewals"].(map[string]any)
		lines := f["deal_lines"].([]any)
		if len(f["renewal_lines"].([]any)) != 0 || len(lines) != 1 || renewals["count"] != float64(1) || renewals["total_minor"] != float64(1200000) ||
			renewals["expected_minor"] != float64(1200000*85/100) {
			t.Fatalf("the forecast: renewals %v, lines %v, renewal lines %v", renewals, lines, f["renewal_lines"])
		}
		if line := lines[0].(map[string]any); line["kind"] != "renewal" || line["weight_from"] != "health" || line["weight"] != float64(85) || line["owner_id"] != me {
			t.Fatalf("the renewal deal's line: %v", line)
		}
		if owners := f["owners"].([]any); len(owners) != 1 || owners[0].(map[string]any)["renewals_expected_minor"] != float64(1200000*85/100) {
			t.Fatalf("owners: %v", owners)
		}
	}
}
