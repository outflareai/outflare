package api_test

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/lifecycle"
	"github.com/outflareai/outflare/engine/internal/track"
)

// TestProductLifecycle follows a company through its products' stages:
// names held and mapped, billing stages on the company and usage stages on
// its person, the company's overall status and its events, inactivity,
// and nothing from before stages were turned on.
func TestProductLifecycle(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand

	// A person and their company, before stages are on: their events are
	// history, and move nobody once stages are turned on (greenfield).
	owner.must(200, "POST", "/v1/workspaces/"+ws+"/customer-events", map[string]any{"type": "identify", "userId": "u1",
		"traits": map[string]any{"email": "ana@acme.example", "email_verified": true}})
	b := owner.must(200, "POST", "/v1/workspaces/"+ws+"/customer-events", map[string]any{"type": "group", "userId": "u1", "groupId": "org_1",
		"traits": map[string]any{"name": "Acme"}})
	ana := b["results"].([]any)[0].(map[string]any)["record_id"].(string)
	var acme string
	if err := e.pool.QueryRow(ctx, `SELECT account_id::text FROM customer_events WHERE record_id = $1 AND kind = 'group'`, ana).Scan(&acme); err != nil {
		t.Fatal(err)
	}

	// Products: a key made from the name, aliases from events and billing.
	res := owner.must(201, "POST", base+"/products", map[string]any{"name": "Residential Proxies", "aliases": []string{"resi", "billing:prod_res"}})
	if res["key"] != "residential-proxies" || len(res["aliases"].([]any)) != 2 || res["churn_mode"] != "cancellation" {
		t.Fatalf("product: %v", res)
	}
	resi := res["id"].(string)
	dc := owner.must(201, "POST", base+"/products", map[string]any{"name": "Datacenter", "key": "dc", "churn_mode": "inactivity", "churn_days": 30})["id"].(string)
	owner.must(409, "POST", base+"/products", map[string]any{"name": "Other", "key": "dc"})
	owner.must(409, "POST", base+"/products", map[string]any{"name": "Other", "aliases": []string{"resi"}})
	owner.must(400, "PATCH", base+"/products/"+dc, map[string]any{"key": "datacenter"})
	owner.must(400, "POST", base+"/products", map[string]any{"name": "Bad", "churn_mode": "never"})

	// Before stages are on, an event with a product is only history.
	early := time.Now().Add(-time.Minute)
	if _, err := customers.Record(ctx, e.pool, ws, customers.Event{RecordID: ana, Name: "subscription.started", OccurredAt: early,
		Properties: map[string]any{"status": "active"}, Product: "prod_res", ProductSource: customers.ProductFromBilling, MessageID: "early"}); err != nil {
		t.Fatal(err)
	}

	lc := owner.must(200, "PATCH", base+"/lifecycle", map[string]any{"enabled": true})
	if lc["enabled"] != true || len(lc["stages"].([]any)) != len(lifecycle.DefaultStages) || len(lc["moves"].([]any)) != len(lifecycle.DefaultMoves) {
		t.Fatalf("lifecycle: %v", lc)
	}

	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	svc := &lifecycle.Service{Pool: e.pool, Log: log}
	svc.Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	defer func() { cancel(); <-done }()
	settled := func() {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for {
			var n int
			var failed string
			_ = e.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status IN ('queued', 'running')),
				COALESCE(string_agg(kind || ': ' || last_error, '; ') FILTER (WHERE last_error IS NOT NULL AND kind LIKE 'lifecycle.%'), '')
				FROM jobs WHERE workspace_id = $1 AND kind LIKE 'lifecycle.%'`, ws).Scan(&n, &failed)
			if failed != "" {
				t.Fatalf("a lifecycle job failed: %s", failed)
			}
			if n == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("waited 60s for lifecycle jobs")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	billing := func(name, product, status, msg string) {
		t.Helper()
		if _, err := customers.Record(ctx, e.pool, ws, customers.Event{RecordID: ana, Name: name, Properties: map[string]any{"status": status},
			Product: product, ProductSource: customers.ProductFromBilling, MessageID: msg}); err != nil {
			t.Fatal(err)
		}
		settled()
	}
	stage := func(rec, product, level string) string {
		t.Helper()
		var s string
		_ = e.pool.QueryRow(ctx, `SELECT stage FROM lifecycle_states WHERE record_id = $1 AND product_id = $2 AND level = $3`, rec, product, level).Scan(&s)
		return s
	}
	emitted := func(rec, name string) int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM customer_events WHERE record_id = $1 AND name = $2 AND source = 'lifecycle'`, rec, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	overall := func() map[string]any {
		t.Helper()
		v := owner.must(200, "GET", base+"/records/"+acme+"/lifecycle", nil)
		o, _ := v["overall"].(map[string]any)
		return o
	}
	settled()
	if s := stage(acme, resi, "company"); s != "" {
		t.Fatalf("an event from before stages were on moved the company to %q", s)
	}

	// Usage from the product: the person is activated; the company's
	// billing stage waits for billing.
	owner.must(201, "POST", base+"/catalog/events", map[string]any{"name": "proxy.request"})
	b = owner.must(200, "POST", "/v1/workspaces/"+ws+"/customer-events", map[string]any{"type": "track", "userId": "u1", "event": "proxy.request",
		"properties": map[string]any{"product": "RESI"}})
	if r := b["results"].([]any)[0].(map[string]any); r["product_id"] != resi {
		t.Fatalf("usage event product: %v", r)
	}
	settled()
	if s := stage(ana, resi, "person"); s != "activated" || emitted(ana, "product.user_activated") != 1 {
		t.Fatalf("person after usage: %q", s)
	}
	if s := stage(acme, resi, "company"); s != "" {
		t.Fatalf("company after usage: %q", s)
	}

	// Billing: a trial, then paying. The company becomes a customer.
	billing("subscription.started", "prod_res", "trialing", "s1")
	if s := stage(acme, resi, "company"); s != "trial" || emitted(acme, "product.trial_started") != 1 {
		t.Fatalf("company after trial: %q", s)
	}
	billing("subscription.changed", "prod_res", "active", "s2")
	if s := stage(acme, resi, "company"); s != "active" || emitted(acme, "product.activated") != 1 || emitted(acme, lifecycle.EvBecameCustomer) != 1 {
		t.Fatalf("company after paying: %q", s)
	}
	// The same event again moves nobody twice.
	var cause string
	if err := e.pool.QueryRow(ctx, `SELECT cause_event_id::text FROM lifecycle_transitions WHERE record_id = $1 AND move = 'trial_converted'`, acme).Scan(&cause); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Enqueue(ctx, e.pool, jobs.NewJob{Kind: customers.JobLifecycleEvent, WorkspaceID: &ws, Payload: map[string]any{"event_id": cause}}); err != nil {
		t.Fatal(err)
	}
	settled()
	if emitted(acme, "product.activated") != 1 {
		t.Fatal("a repeated event moved the company again")
	}

	// A second product: expanded, then trialling another flag is off
	// since both are active.
	billing("subscription.started", "dc", "active", "s3")
	if o := overall(); o["status"] != "active" || o["active_products"].(float64) != 2 || emitted(acme, lifecycle.EvExpanded) != 1 {
		t.Fatalf("overall after a second product: %v", o)
	}

	// Cancelling one: churned for that product only, and partly churned.
	billing("subscription.canceled", "prod_res", "canceled", "s4")
	if s := stage(acme, resi, "company"); s != "churned" || emitted(acme, "product.churned") != 1 {
		t.Fatalf("company after cancelling: %q", s)
	}
	if o := overall(); o["status"] != "active" || !slices.Contains(o["flags"].([]any), any(lifecycle.FlagPartlyChurned)) || emitted(acme, lifecycle.EvPartlyChurned) != 1 {
		t.Fatalf("overall after cancelling one: %v", o)
	}
	// Datacenter churns for no usage (churn_mode inactivity): 20 days idle is
	// at risk, and churned comes due 30 days after the last usage, not 30
	// days after it became at risk.
	idle := func(days int) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `UPDATE lifecycle_states SET idle_since = now() - make_interval(days => $3), due_at = now() - interval '1 minute'
			WHERE record_id = $1 AND product_id = $2 AND level = 'company'`, acme, dc, days); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
	}
	idle(20)
	var dueIn float64
	if err := e.pool.QueryRow(ctx, `SELECT extract(epoch FROM due_at - now()) / 86400 FROM lifecycle_states WHERE record_id = $1 AND product_id = $2 AND level = 'company'`,
		acme, dc).Scan(&dueIn); err != nil {
		t.Fatal(err)
	}
	if s := stage(acme, dc, "company"); s != "at_risk" || emitted(acme, "product.at_risk") != 1 || dueIn < 9.9 || dueIn > 10.1 {
		t.Fatalf("datacenter after 20 idle days: %q, churn due in %.1f days", s, dueIn)
	}
	idle(31)
	if s := stage(acme, dc, "company"); s != "churned" || emitted(acme, lifecycle.EvFullyChurned) != 1 {
		t.Fatalf("datacenter after 31 idle days: %q, overall %v", s, overall())
	}
	// Paying again for residential reactivates it.
	if _, err := customers.Record(ctx, e.pool, ws, customers.Event{RecordID: ana, Name: "payment.succeeded", ProductID: resi, MessageID: "p1"}); err != nil {
		t.Fatal(err)
	}
	settled()
	if s := stage(acme, resi, "company"); s != "active" || emitted(acme, "product.reactivated") != 1 || emitted(acme, lifecycle.EvBecameCustomer) != 1 {
		t.Fatalf("after paying again: %q", s)
	}

	// The person's usage clock: no usage for at_risk_days is at risk.
	if _, err := e.pool.Exec(ctx, `UPDATE lifecycle_states SET idle_since = now() - interval '20 days', due_at = now() - interval '1 minute'
		WHERE record_id = $1 AND product_id = $2 AND level = 'person'`, ana, resi); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if s := stage(ana, resi, "person"); s != "at_risk" || emitted(ana, "product.user_at_risk") != 1 {
		t.Fatalf("person after no usage: %q", s)
	}

	// An unknown name is held, then mapped.
	owner.must(200, "POST", "/v1/workspaces/"+ws+"/customer-events", map[string]any{"type": "track", "userId": "u1", "event": "proxy.request",
		"properties": map[string]any{"product": "mobile"}})
	held := owner.must(200, "GET", base+"/product-names?status=held", nil)["data"].([]any)
	if len(held) != 1 || held[0].(map[string]any)["value"] != "mobile" {
		t.Fatalf("held names: %v", held)
	}
	name := held[0].(map[string]any)["id"].(string)
	owner.must(400, "PATCH", base+"/product-names/"+name, map[string]any{"status": "mapped"})
	if n := owner.must(200, "PATCH", base+"/product-names/"+name, map[string]any{"status": "mapped", "product_id": dc}); n["product_id"] != dc {
		t.Fatalf("mapped: %v", n)
	}
	b = owner.must(200, "POST", "/v1/workspaces/"+ws+"/customer-events", map[string]any{"type": "track", "userId": "u1", "event": "proxy.request",
		"properties": map[string]any{"product": "mobile"}})
	if r := b["results"].([]any)[0].(map[string]any); r["product_id"] != dc {
		t.Fatalf("after mapping: %v", r)
	}
	settled()

	// Stages and moves: a stage in use can't go, a bad move is refused.
	owner.must(409, "PUT", base+"/lifecycle/stages", map[string]any{"stages": []map[string]any{{"level": "company", "key": "lead", "name": "Lead", "category": "lead", "rank": 0}}})
	owner.must(400, "PUT", base+"/lifecycle/moves", map[string]any{"moves": []map[string]any{{"level": "company", "key": "x", "name": "X", "kind": "event",
		"to_stage": "nowhere", "emit_event": "x.y", "from_stages": []string{"none"}, "event": "a.b"}}})
	rl := owner.must(200, "GET", base+"/records/"+acme+"/lifecycle", nil)
	if len(rl["states"].([]any)) != 2 || len(rl["moves"].([]any)) < 6 {
		t.Fatalf("record lifecycle: %v", rl)
	}

	// Archiving a product takes it out of the overall status.
	owner.must(200, "POST", base+"/products/"+dc+"/archive", nil)
	if l := owner.must(200, "GET", base+"/products", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("products after archiving: %v", l)
	}
	owner.must(200, "POST", base+"/products/"+dc+"/restore", nil)
	owner.must(200, "PATCH", base+"/lifecycle", map[string]any{"enabled": false})

	for _, name := range []string{"product.created", "product.archived", "product_name.updated", "lifecycle.enabled", "lifecycle.moved", "lifecycle.disabled"} {
		if !slices.ContainsFunc(got(), func(ev track.Event) bool { return ev.Name == name }) {
			t.Errorf("no %s event", name)
		}
	}
}
