package api_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/recordevents"
	"github.com/outflareai/outflare/engine/internal/rules"
)

// Deals, support conversations and tasks reach timelines as events, and
// event and absence rules about companies hear what their people do.
func TestCompanyRulesAndRecordEvents(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	q := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	names := func(rec, source string) []string {
		t.Helper()
		rows, err := e.pool.Query(ctx, `SELECT name FROM customer_events WHERE record_id = $1 AND source = $2 ORDER BY occurred_at, name`, rec, source)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := []string{}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				t.Fatal(err)
			}
			out = append(out, n)
		}
		return out
	}
	same := func(what string, got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: got %v, want %v", what, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%s: got %v, want %v", what, got, want)
			}
		}
	}
	round := func() {
		t.Helper()
		if _, err := recordevents.Round(ctx, e.pool, 100); err != nil {
			t.Fatal(err)
		}
	}

	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme"})["id"].(string)
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana",
		"identities": []any{map[string]any{"namespace": "email", "value": "ana@acme.example"}}})["id"].(string)
	exec(`INSERT INTO record_relationships (workspace_id, from_id, to_id, kind) VALUES ($1, $2, $3, 'companies')`, ws, ana, acme)

	// Rules about the company: a deal won by one of its people, and a
	// stretch with no usage from anyone there.
	won := owner.must(201, "POST", base+"/rules", map[string]any{"name": "Company won a deal", "record_type": "account",
		"trigger": map[string]any{"kind": "event", "event": "deal.won"}, "emit_event": "account.deal_won"})["id"].(string)
	quiet := owner.must(201, "POST", base+"/rules", map[string]any{"name": "Company went quiet", "record_type": "account",
		"trigger": map[string]any{"kind": "absence", "event": "proxy.request", "within_seconds": 3600}, "emit_event": "account.went_quiet"})["id"].(string)
	owner.must(200, "POST", base+"/rules/"+won+"/activate", nil)
	owner.must(200, "POST", base+"/rules/"+quiet+"/activate", nil)

	jq := jobs.New(e.pool, log)
	jq.PollInterval = 20 * time.Millisecond
	svc := &rules.Service{Pool: e.pool, Log: log}
	svc.Register(jq)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { jq.Run(runCtx, 2); close(done) }() //nolint:errcheck
	defer func() { cancel(); <-done }()
	settled := func() {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for {
			var n int
			var failed string
			_ = e.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status IN ('queued', 'running')),
				COALESCE(string_agg(kind || ': ' || last_error, '; ') FILTER (WHERE last_error IS NOT NULL), '')
				FROM jobs WHERE workspace_id = $1 AND kind LIKE 'rule.%'`, ws).Scan(&n, &failed)
			if failed != "" {
				t.Fatalf("a job failed: %s", failed)
			}
			if n == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("waited 60s for jobs")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	// A deal with Ana: created, moved a stage, then won.
	deal := q(`INSERT INTO deals (workspace_id, brand_id, record_id, source, name, stage, amount_cents)
		VALUES ($1, $2, $3, 'manual', 'Acme annual', 'Qualified', 120000) RETURNING id::text`, ws, brand, ana)
	// One from before record events: nothing is recorded for it.
	old := q(`INSERT INTO deals (workspace_id, brand_id, record_id, source, name, state, closed_at, event_state)
		VALUES ($1, $2, $3, 'manual', 'Old deal', 'won', now() - interval '90 days', 'history') RETURNING id::text`, ws, brand, ana)
	round()
	same("a new deal", names(ana, "deals"), "deal.created")
	exec(`UPDATE deals SET stage = 'Proposal', updated_at = now() WHERE id = $1`, deal)
	round()
	exec(`UPDATE deals SET state = 'won', closed_at = now(), updated_at = now() WHERE id = $1`, deal)
	round()
	round() // a second round records nothing twice
	same("the deal's events", names(ana, "deals"), "deal.created", "deal.stage_changed", "deal.won")
	if got := q(`SELECT event_state FROM deals WHERE id = $1`, old); got != "won:" {
		t.Fatalf("the old deal's state is noted: %q", got)
	}
	var amount float64
	if err := e.pool.QueryRow(ctx, `SELECT (properties ->> 'amount_minor')::float FROM customer_events WHERE record_id = $1 AND name = 'deal.won'`, ana).Scan(&amount); err != nil || amount != 120000 {
		t.Fatalf("deal.won carries the amount: %v %v", amount, err)
	}
	settled()
	same("the company's rule heard its person's deal", names(acme, "rule"), "account.deal_won")

	// A deal with no person goes on its company.
	lone := q(`INSERT INTO deals (workspace_id, brand_id, source, name) VALUES ($1, $2, 'manual', 'Acme add-on') RETURNING id::text`, ws, brand)
	exec(`INSERT INTO record_relationships (workspace_id, from_id, to_id, kind) VALUES ($1, $2, $3, 'associated_company')`, ws, lone, acme)
	round()
	same("a company's deal", names(acme, "deals"), "deal.created")

	// Support conversations and tasks.
	conv := q(`INSERT INTO support_conversations (workspace_id, brand_id, record_id, subject, contact_email)
		VALUES ($1, $2, $3, 'Cannot log in', 'ana@acme.example') RETURNING id::text`, ws, brand, ana)
	task := q(`INSERT INTO crm_activities (workspace_id, brand_id, record_id, kind, title) VALUES ($1, $2, $3, 'task', 'Call Ana') RETURNING id::text`, ws, brand, ana)
	exec(`INSERT INTO crm_activities (workspace_id, brand_id, record_id, kind, title) VALUES ($1, $2, $3, 'note', 'Met at the fair')`, ws, brand, ana)
	round()
	exec(`UPDATE support_conversations SET status = 'closed', closed_at = now(), updated_at = now() WHERE id = $1`, conv)
	exec(`UPDATE crm_activities SET status = 'done', updated_at = now() WHERE id = $1`, task)
	round()
	exec(`UPDATE support_conversations SET status = 'open', closed_at = NULL, updated_at = now() + interval '1 second' WHERE id = $1`, conv)
	round()
	same("support events", names(ana, "support"), "support.conversation_opened", "support.conversation_closed", "support.conversation_reopened")
	same("task events", names(ana, "tasks"), "task.created", "task.completed")

	// The timeline shows the deal, the conversation and the task once:
	// their own items, not a second event beside them.
	tl := owner.must(200, "GET", base+"/records/"+ana+"/timeline?kinds=event", nil)["data"].([]any)
	seen := map[string]bool{}
	for _, it := range tl {
		seen[it.(map[string]any)["title"].(string)] = true
	}
	if seen["deal.created"] || seen["task.created"] || seen["support.conversation_opened"] || !seen["deal.won"] || !seen["task.completed"] {
		t.Fatalf("timeline events: %v", seen)
	}

	// No usage from anyone at Acme for an hour: the company's clock starts
	// at a person's event and fires when it runs out.
	if _, err := customers.Record(ctx, e.pool, ws, customers.Event{RecordID: ana, Name: "proxy.request", Source: "api", MessageID: "use-1"}); err != nil {
		t.Fatal(err)
	}
	settled()
	if got := q(`SELECT count(*)::text FROM rule_states WHERE rule_id = $1 AND record_id = $2 AND due_at IS NOT NULL`, quiet, acme); got != "1" {
		t.Fatalf("the company's clock started: %s", got)
	}
	exec(`UPDATE rule_states SET due_at = now() - interval '1 minute' WHERE rule_id = $1`, quiet)
	if _, err := svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	same("the company went quiet", names(acme, "rule"), "account.deal_won", "account.went_quiet")

	// A rule about a date coming up is tested once a day: a renewal that
	// is 90 days out fires when it comes within 60, with no change to the
	// record.
	owner.must(201, "POST", base+"/catalog/types/account/fields", map[string]any{"label": "Renews on", "key": "renews_on", "value_type": "date"})
	owner.must(200, "PATCH", base+"/records/"+acme, map[string]any{"attributes": map[string]any{"renews_on": time.Now().AddDate(0, 0, 90).Format("2006-01-02")}})
	renewal := owner.must(201, "POST", base+"/rules", map[string]any{"name": "Renewal coming up", "record_type": "account",
		"trigger": map[string]any{"kind": "change"}, "emit_event": "account.renewal_due",
		"conditions": []map[string]any{{"attribute": "renews_on", "op": "lt", "value": "+60d"}, {"attribute": "renews_on", "op": "gt", "value": "today"}}})["id"].(string)
	owner.must(200, "POST", base+"/rules/"+renewal+"/activate", nil)
	daily := func(want int) {
		t.Helper()
		n, err := svc.Daily(ctx)
		if err != nil || n != want {
			t.Fatalf("daily checks queued: %d, want %d (%v)", n, want, err)
		}
		settled()
	}
	daily(1)
	daily(0) // once a day
	exec(`UPDATE records SET attributes = attributes || jsonb_build_object('renews_on', to_char(now() + interval '30 days', 'YYYY-MM-DD')) WHERE id = $1`, acme)
	exec(`UPDATE rules SET checked_at = now() - interval '1 day', activated_at = now() - interval '2 days' WHERE id = $1`, renewal)
	daily(1)
	same("the renewal came within 60 days", names(acme, "rule"), "account.deal_won", "account.went_quiet", "account.renewal_due")
	exec(`UPDATE rules SET checked_at = now() - interval '1 day', activated_at = now() - interval '2 days' WHERE id = $1`, renewal)
	daily(1)
	same("it fires once", names(acme, "rule"), "account.deal_won", "account.went_quiet", "account.renewal_due")

	// The dry run counts a person's events for their company.
	if sim := owner.must(200, "POST", base+"/rules/"+won+"/simulate", map[string]any{"days": 7}); sim["firings"].(float64) != 1 || sim["records"].(float64) != 1 {
		t.Fatalf("dry run of a company rule: %v", sim)
	}
}
