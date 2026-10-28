package api_test

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/rules"
	"github.com/outflareai/outflare/engine/internal/smartlists"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestRules(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	owner.must(201, "POST", base+"/catalog/types/person/fields", map[string]any{"label": "Plan", "value_type": "text"})

	person := func(addr, attrs string) string {
		t.Helper()
		rec, err := customers.EnsurePerson(ctx, e.pool, ws, "email", addr, true, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.pool.Exec(ctx, `UPDATE records SET attributes = $2 WHERE id = $1`, rec, attrs); err != nil {
			t.Fatal(err)
		}
		return rec
	}
	event := func(ev customers.Event) {
		t.Helper()
		if _, err := customers.Record(ctx, e.pool, ws, ev); err != nil {
			t.Fatal(err)
		}
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for !ok() {
			var failed string
			_ = e.pool.QueryRow(ctx, `SELECT COALESCE(string_agg(kind || ': ' || last_error, '; '), '') FROM jobs
				WHERE workspace_id = $1 AND kind LIKE 'rule.%' AND last_error IS NOT NULL`, ws).Scan(&failed)
			if failed != "" {
				t.Fatalf("a rule job failed: %s", failed)
			}
			if time.Now().After(deadline) {
				t.Fatalf("waited 60s for %s", what)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	// settled waits until every job of the workspace has run.
	settled := func() {
		t.Helper()
		waitFor("jobs to run", func() bool {
			var n int
			_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE workspace_id = $1 AND status IN ('queued', 'running') AND run_at <= now() + interval '1 second'`, ws).Scan(&n)
			return n == 0
		})
	}
	firings := func(ruleID, rec string) int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM rule_firings WHERE rule_id = $1 AND record_id = $2`, ruleID, rec).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	emitted := func(rec, name string) []map[string]any {
		t.Helper()
		rows, err := e.pool.Query(ctx, `SELECT properties FROM customer_events WHERE record_id = $1 AND name = $2 AND source = 'rule' ORDER BY received_at`, rec, name)
		if err != nil {
			t.Fatal(err)
		}
		list, err := pgx.CollectRows(rows, pgx.RowTo[map[string]any])
		if err != nil {
			t.Fatal(err)
		}
		return list
	}

	// Bad rules are refused.
	var list map[string]any
	for _, bad := range []map[string]any{
		{"name": "Bad", "trigger": map[string]any{"kind": "event", "event": "Signed Up"}, "emit_event": "a.b"},
		{"name": "Bad", "trigger": map[string]any{"kind": "event", "event": "signed_up"}, "emit_event": "signed_up"},
		{"name": "Bad", "trigger": map[string]any{"kind": "change"}, "emit_event": "a.b"},
		{"name": "Bad", "trigger": map[string]any{"kind": "change"}, "emit_event": "a.b", "conditions": []map[string]any{{"property": "plan", "op": "exists"}}},
		{"name": "Bad", "trigger": map[string]any{"kind": "absence", "event": "a", "within_seconds": 10}, "emit_event": "a.b"},
		{"name": "Bad", "trigger": map[string]any{"kind": "audience", "list_id": "00000000-0000-0000-0000-000000000000", "on": "enter"}, "emit_event": "a.b"},
		{"name": "Bad", "trigger": map[string]any{"kind": "event", "event": "signed_up"}, "emit_event": "a.b", "record_type": "deal"},
		{"name": "Bad", "trigger": map[string]any{"kind": "change"}, "emit_event": "a.b", "record_type": "nope",
			"conditions": []map[string]any{{"attribute": "stage", "op": "eq", "value": "won"}}},
		{"name": "Bad", "trigger": map[string]any{"kind": "teleport"}, "emit_event": "a.b"},
		{"trigger": map[string]any{"kind": "event", "event": "signed_up"}, "emit_event": "a.b"},
	} {
		owner.must(400, "POST", base+"/rules", bad)
	}
	// A change rule can watch deals.
	owner.must(201, "POST", base+"/rules", map[string]any{"name": "Deal won", "record_type": "deal", "trigger": map[string]any{"kind": "change"},
		"conditions": []map[string]any{{"attribute": "stage", "op": "eq", "value": "won"}}, "emit_event": "deal.closed_won"})

	// An event rule: product_activated on the pro plan.
	r1 := owner.must(201, "POST", base+"/rules", map[string]any{"name": "Pro activation", "trigger": map[string]any{"kind": "event", "event": "product_activated"},
		"conditions": []map[string]any{{"property": "plan", "op": "eq", "value": "pro"}}, "emit_event": "customer.activated_pro"})
	if r1["status"] != "draft" || r1["version"].(float64) != 1 || r1["fire"] != "every_time" || r1["record_type"] != "person" {
		t.Fatalf("rule = %v", r1)
	}
	id1 := r1["id"].(string)
	owner.must(409, "POST", base+"/rules", map[string]any{"name": "pro activation", "trigger": map[string]any{"kind": "event", "event": "x"}, "emit_event": "y"})

	ana := person("ana@example.org", `{"plan": "pro"}`)
	bo := person("bo@example.org", `{}`)
	cy := person("cy@example.org", `{}`)
	dee := person("dee@example.org", `{}`)
	// Before the rule is live, an event is only history; the dry run sees it.
	event(customers.Event{RecordID: ana, Name: "product_activated", Properties: map[string]any{"plan": "pro"}})
	event(customers.Event{RecordID: bo, Name: "product_activated", Properties: map[string]any{"plan": "free"}})
	sim := owner.must(200, "POST", base+"/rules/"+id1+"/simulate", map[string]any{"days": 7})
	if sim["records"].(float64) != 1 || sim["firings"].(float64) != 1 || sim["checked"].(float64) != 2 || sim["sample"].([]any)[0] != ana {
		t.Fatalf("simulation = %v", sim)
	}
	owner.must(400, "POST", base+"/rules/"+id1+"/simulate", map[string]any{"days": 0})

	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	svc := &rules.Service{Pool: e.pool, Log: log}
	svc.Register(q)
	(&smartlists.Service{Pool: e.pool, Log: log}).Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	defer func() { cancel(); <-done }()

	owner.must(409, "POST", base+"/rules/"+id1+"/pause", nil)
	r1 = owner.must(200, "POST", base+"/rules/"+id1+"/activate", nil)
	if r1["status"] != "active" || r1["activated_at"] == nil {
		t.Fatalf("activated = %v", r1)
	}
	owner.must(409, "PATCH", base+"/rules/"+id1, map[string]any{"emit_event": "other.event"})
	owner.must(200, "PATCH", base+"/rules/"+id1, map[string]any{"description": "Pro customers who switched it on"})

	// Ana activates on pro: the rule fires and emits its event, with the
	// trigger's properties. Bo is on free; a browser event and one that
	// arrives two days late set nothing off.
	event(customers.Event{RecordID: ana, Name: "product_activated", Properties: map[string]any{"plan": "pro"}, MessageID: "m1"})
	event(customers.Event{RecordID: bo, Name: "product_activated", Properties: map[string]any{"plan": "free"}})
	event(customers.Event{RecordID: cy, Name: "product_activated", Properties: map[string]any{"plan": "pro"}, Source: "browser"})
	event(customers.Event{RecordID: cy, Name: "product_activated", Properties: map[string]any{"plan": "pro"}, OccurredAt: time.Now().Add(-48 * time.Hour)})
	settled()
	if firings(id1, ana) != 1 || firings(id1, bo) != 0 || firings(id1, cy) != 0 {
		t.Fatalf("firings ana %d bo %d cy %d", firings(id1, ana), firings(id1, bo), firings(id1, cy))
	}
	ev := emitted(ana, "customer.activated_pro")
	if len(ev) != 1 || ev[0]["plan"] != "pro" || ev[0]["rule_id"] != id1 {
		t.Fatalf("emitted = %v", ev)
	}
	// The same job running again fires nothing new.
	var eventID string
	if err := e.pool.QueryRow(ctx, `SELECT id::text FROM customer_events WHERE message_id = 'm1'`).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Enqueue(ctx, e.pool, jobs.NewJob{Kind: customers.JobRuleEvent, WorkspaceID: &ws, Payload: map[string]string{"event_id": eventID}}); err != nil {
		t.Fatal(err)
	}
	settled()
	if firings(id1, ana) != 1 {
		t.Fatalf("a replayed event fired again: %d", firings(id1, ana))
	}

	// A rule on the first rule's event runs one deeper. A rule that would
	// close the circle can't be activated.
	r2 := owner.must(201, "POST", base+"/rules", map[string]any{"name": "Welcome pro", "trigger": map[string]any{"kind": "event", "event": "customer.activated_pro"},
		"emit_event": "customer.welcomed", "fire": "once"})
	id2 := r2["id"].(string)
	owner.must(200, "POST", base+"/rules/"+id2+"/activate", nil)
	loop := owner.must(201, "POST", base+"/rules", map[string]any{"name": "Loop", "trigger": map[string]any{"kind": "event", "event": "customer.welcomed"},
		"emit_event": "product_activated"})
	owner.must(409, "POST", base+"/rules/"+loop["id"].(string)+"/activate", nil)
	event(customers.Event{RecordID: ana, Name: "product_activated", Properties: map[string]any{"plan": "pro"}})
	settled()
	event(customers.Event{RecordID: ana, Name: "product_activated", Properties: map[string]any{"plan": "pro"}})
	settled()
	if firings(id1, ana) != 3 || firings(id2, ana) != 1 {
		t.Fatalf("firings rule 1 %d, rule 2 (once) %d", firings(id1, ana), firings(id2, ana))
	}
	var depth int
	if err := e.pool.QueryRow(ctx, `SELECT depth FROM rule_firings WHERE rule_id = $1`, id2).Scan(&depth); err != nil || depth != 2 {
		t.Fatalf("depth = %d, %v", depth, err)
	}
	page := owner.must(200, "GET", base+"/rules/"+id1+"/firings?limit=2", nil)
	if len(page["data"].([]any)) != 2 || page["next_cursor"] == nil {
		t.Fatalf("firings page = %v", page)
	}
	if one := owner.must(200, "GET", base+"/rules/"+id1, nil); one["stats"].(map[string]any)["firings"].(float64) != 3 {
		t.Fatalf("stats = %v", one["stats"])
	}

	// A change rule fires when the condition becomes true, not while it
	// stays true, and not for a change someone made by hand.
	r4 := owner.must(201, "POST", base+"/rules", map[string]any{"name": "Enterprise", "trigger": map[string]any{"kind": "change"},
		"conditions": []map[string]any{{"attribute": "plan", "op": "eq", "value": "enterprise"}}, "emit_event": "customer.upgraded"})
	id4 := r4["id"].(string)
	owner.must(200, "POST", base+"/rules/"+id4+"/activate", nil)
	setPlan := func(rec, plan, source string) {
		t.Helper()
		owner.must(200, "PATCH", base+"/records/"+rec, map[string]any{"attributes": map[string]any{"plan": plan}, "source": source})
		settled()
	}
	setPlan(dee, "enterprise", "manual")
	if firings(id4, dee) != 0 {
		t.Fatal("a manual edit fired a change rule")
	}
	setPlan(bo, "pro", "first_party")
	setPlan(bo, "enterprise", "first_party")
	setPlan(bo, "enterprise", "first_party")
	if firings(id4, bo) != 1 {
		t.Fatalf("change firings = %d, want 1", firings(id4, bo))
	}
	setPlan(bo, "pro", "first_party")
	setPlan(bo, "enterprise", "first_party")
	if firings(id4, bo) != 2 {
		t.Fatalf("change firings after it stopped and held again = %d, want 2", firings(id4, bo))
	}

	// An absence rule: signed up, then no activation within an hour.
	r5 := owner.must(201, "POST", base+"/rules", map[string]any{"name": "Stalled", "trigger": map[string]any{"kind": "absence", "start_event": "signed_up",
		"event": "product_activated", "within_seconds": 3600}, "emit_event": "customer.stalled"})
	id5 := r5["id"].(string)
	owner.must(200, "POST", base+"/rules/"+id5+"/activate", nil)
	event(customers.Event{RecordID: cy, Name: "signed_up"})
	event(customers.Event{RecordID: ana, Name: "signed_up"})
	settled()
	event(customers.Event{RecordID: ana, Name: "product_activated", Properties: map[string]any{"plan": "free"}})
	settled()
	if n, err := svc.Sweep(ctx); err != nil || n != 0 {
		t.Fatalf("sweep before time = %d, %v", n, err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE rule_states SET due_at = now() - interval '1 second' WHERE rule_id = $1 AND due_at IS NOT NULL`, id5); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.Sweep(ctx); err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
	if firings(id5, cy) != 1 || firings(id5, ana) != 0 {
		t.Fatalf("absence firings cy %d ana %d", firings(id5, cy), firings(id5, ana))
	}
	if ev := emitted(cy, "customer.stalled"); len(ev) != 1 || ev[0]["start_event"] != "signed_up" {
		t.Fatalf("stalled = %v", ev)
	}

	// An audience rule fires for people who join a smart list after it is
	// live, not for those already in it.
	sl := owner.must(201, "POST", base+"/smart-lists", map[string]any{"name": "Enterprise", "rules": []map[string]any{{"attribute": "plan", "op": "eq", "value": "enterprise"}}})
	waitFor("the list to fill", func() bool {
		return owner.must(200, "GET", base+"/smart-lists/"+sl["id"].(string), nil)["refreshed_at"] != nil
	})
	r6 := owner.must(201, "POST", base+"/rules", map[string]any{"name": "Joined enterprise", "trigger": map[string]any{"kind": "audience",
		"list_id": sl["id"], "on": "enter"}, "emit_event": "customer.joined_enterprise"})
	id6 := r6["id"].(string)
	if sim := owner.must(200, "POST", base+"/rules/"+id6+"/simulate", nil); sim["records"].(float64) != 2 {
		t.Fatalf("audience simulation = %v", sim)
	}
	owner.must(200, "POST", base+"/rules/"+id6+"/activate", nil)
	if _, err := e.pool.Exec(ctx, `UPDATE records SET attributes = '{"plan": "enterprise"}' WHERE id = $1`, cy); err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		_, err := smartlists.Refresh(ctx, tx, ws, sl["id"].(string))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	settled()
	if firings(id6, cy) != 1 || firings(id6, bo) != 0 || firings(id6, dee) != 0 {
		t.Fatalf("audience firings cy %d bo %d dee %d", firings(id6, cy), firings(id6, bo), firings(id6, dee))
	}

	// Archived rules leave the default list and stop.
	owner.must(200, "POST", base+"/rules/"+id5+"/archive", nil)
	owner.must(409, "PATCH", base+"/rules/"+id5, map[string]any{"name": "Again"})
	list = owner.must(200, "GET", base+"/rules", nil)
	for _, r := range list["data"].([]any) {
		if r.(map[string]any)["id"] == id5 {
			t.Fatal("an archived rule is listed by default")
		}
	}
	if len(owner.must(200, "GET", base+"/rules?status=archived", nil)["data"].([]any)) != 1 {
		t.Fatal("status=archived should list the archived rule")
	}

	names := []string{}
	for _, ev := range got() {
		names = append(names, ev.Name)
	}
	for _, want := range []string{"rule.created", "rule.updated", "rule.activated", "rule.archived", "rule.fired"} {
		if !slices.Contains(names, want) {
			t.Errorf("no %s event", want)
		}
	}
}
