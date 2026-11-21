package api_test

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/recmetrics"
	"github.com/outflareai/outflare/engine/internal/rules"
)

// TestRecordMetrics covers live metrics: turning them on adds the defaults
// and fills them quietly from stored events; a company already past a
// rule's threshold counts as having crossed it, so only a new crossing
// fires; values are live, per product and roll up to the company; windows
// move on as days pass; archiving takes a metric off every record.
func TestRecordMetrics(t *testing.T) {
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
			t.Fatal(err)
		}
	}

	resi := owner.must(201, "POST", base+"/products", map[string]any{"name": "Residential", "key": "residential"})["id"].(string)
	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme"})["id"].(string)
	north := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Northpeak"})["id"].(string)
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana",
		"identities": []any{map[string]any{"namespace": "email", "value": "ana@acme.example"}, map[string]any{"namespace": "user_id", "value": "u-ana"}}})["id"].(string)
	bo := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Bo",
		"identities": []any{map[string]any{"namespace": "email", "value": "bo@northpeak.example"}}})["id"].(string)
	exec(`INSERT INTO record_relationships (workspace_id, from_id, to_id, kind) VALUES ($1, $2, $3, 'companies'), ($1, $4, $5, 'companies')`, ws, ana, acme, bo, north)

	// History from before metrics were on: Acme already spent $1,100.
	past := func(rec, name string, ago int, props map[string]any, usage bool) {
		t.Helper()
		exec(`INSERT INTO customer_events (workspace_id, record_id, brand_id, kind, name, properties, source, occurred_at, received_at, product_id, usage)
			VALUES ($1, $2, $3, 'track', $4, $5, 'api', now() - make_interval(days => $6), now() - make_interval(days => $6), $7, $8)`,
			ws, rec, brand, name, props, ago, resi, usage)
	}
	past(ana, "payment.succeeded", 10, map[string]any{"amount_minor": 60000}, false)
	past(ana, "payment.succeeded", 5, map[string]any{"amount_minor": 50000}, false)
	past(ana, "payment.succeeded", 45, map[string]any{"amount_minor": 99900}, false) // outside 30 days
	past(ana, "proxy.request", 9, map[string]any{}, true)
	past(ana, "proxy.request", 2, map[string]any{}, true)
	past(ana, "balance.updated", 6, map[string]any{"balance_minor": 9000}, false)
	past(ana, "balance.updated", 3, map[string]any{"balance_minor": 4000}, false)

	// A rule: a company spending over $1,000 in 30 days is a big spender.
	rule := owner.must(201, "POST", base+"/rules", map[string]any{"name": "Big spender", "record_type": "account",
		"trigger": map[string]any{"kind": "change"}, "emit_event": "customer.big_spender",
		"conditions": []map[string]any{{"attribute": "metrics.spend_30d", "op": "gt", "value": 100000}}})["id"].(string)
	owner.must(200, "POST", base+"/rules/"+rule+"/activate", nil)

	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	svc := &recmetrics.Service{Pool: e.pool, Log: log}
	svc.Register(q)
	(&rules.Service{Pool: e.pool, Log: log}).Register(q)
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
				COALESCE(string_agg(kind || ': ' || last_error, '; ') FILTER (WHERE last_error IS NOT NULL), '')
				FROM jobs WHERE workspace_id = $1 AND (kind LIKE 'metric.%' OR kind LIKE 'rule.%')`, ws).Scan(&n, &failed)
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
	attr := func(rec string) map[string]any {
		t.Helper()
		v := owner.must(200, "GET", base+"/records/"+rec, nil)
		m, _ := v["attributes"].(map[string]any)["metrics"].(map[string]any)
		return m
	}
	fired := func(rec string) int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM customer_events WHERE record_id = $1 AND name = 'customer.big_spender'`, rec).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Turning metrics on adds the defaults and fills them quietly.
	on := owner.must(200, "PATCH", base+"/metrics", map[string]any{"enabled": true})
	if on["enabled"] != true || len(on["data"].([]any)) != len(recmetrics.Defaults) {
		t.Fatalf("metrics on: %v", on)
	}
	settled()
	m := attr(acme)
	if m["spend_30d"] != 110000.0 || m["payments_90d"] != 3.0 || m["usage_7d"] != 1.0 || m["usage_prev_7d"] != 1.0 {
		t.Fatalf("Acme after the backfill: %v", m)
	}
	if bp := m["by_product"].(map[string]any)["residential"].(map[string]any); bp["spend_30d"] != 110000.0 {
		t.Fatalf("Acme per product: %v", m)
	}
	if pm := attr(ana); pm["active_days_30d"] != 2.0 || pm["last_seen"] == nil {
		t.Fatalf("Ana after the backfill: %v", pm)
	}
	if _, ok := attr(acme)["active_days_30d"]; ok {
		t.Fatal("active_days_30d is kept for people only")
	}
	if fired(acme) != 0 {
		t.Fatal("the backfill fired the rule")
	}

	// Live: another payment keeps Acme over the line, so nothing fires.
	if _, err := customers.Record(ctx, e.pool, ws, customers.Event{RecordID: ana, Name: "payment.succeeded", ProductID: resi,
		Properties: map[string]any{"amount_minor": 10000}, MessageID: "p-live-1"}); err != nil {
		t.Fatal(err)
	}
	settled()
	if m := attr(acme); m["spend_30d"] != 120000.0 || fired(acme) != 0 {
		t.Fatalf("Acme already over $1,000: %v, fired %d", m, fired(acme))
	}
	// Northpeak crosses for the first time: the rule fires once.
	if _, err := customers.Record(ctx, e.pool, ws, customers.Event{RecordID: bo, Name: "payment.succeeded",
		Properties: map[string]any{"amount_minor": 150000}, MessageID: "p-live-2"}); err != nil {
		t.Fatal(err)
	}
	settled()
	if m := attr(north); m["spend_30d"] != 150000.0 || fired(north) != 1 {
		t.Fatalf("Northpeak crossed: %v, fired %d", m, fired(north))
	}
	if _, ok := attr(north)["by_product"]; ok {
		t.Fatal("a payment with no product has no per-product value")
	}

	// Usage from the product, through ingest, counts live.
	owner.must(200, "POST", "/v1/workspaces/"+ws+"/customer-events", map[string]any{"type": "track", "userId": "u-ana", "event": "Proxy Request",
		"properties": map[string]any{"product": "residential"}})
	settled()
	if m := attr(ana); m["usage_7d"] != 2.0 {
		t.Fatalf("usage after a live event: %v", m)
	}
	// The trend against the week before comes with it: one request then,
	// two now.
	if m := attr(ana); m["usage_7d_change"] != 100.0 {
		t.Fatalf("usage trend: %v", m)
	}

	// Days pass: the window moves on at the sweep.
	exec(`UPDATE metric_buckets SET day = day - 40 WHERE record_id = $1`, north)
	exec(`UPDATE metric_values SET due_at = now() - interval '1 minute' WHERE record_id = $1`, north)
	if _, err := svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	settled()
	if m := attr(north); m["spend_30d"] != 0.0 {
		t.Fatalf("Northpeak after 40 days: %v", m)
	}

	// The record's metrics, overall and per product.
	vals := owner.must(200, "GET", base+"/records/"+acme+"/metrics", nil)["data"].([]any)
	if len(vals) < 7 {
		t.Fatalf("Acme's metrics: %v", vals)
	}

	// Making, changing and archiving metrics.
	owner.must(400, "POST", base+"/metrics", map[string]any{"key": "Bad Key", "name": "x", "aggregate": "count", "events": []string{"a.b"}})
	owner.must(400, "POST", base+"/metrics", map[string]any{"key": "x", "name": "x", "aggregate": "sum", "events": []string{"a.b"}})
	owner.must(400, "POST", base+"/metrics", map[string]any{"key": "x", "name": "x", "aggregate": "count"})
	owner.must(409, "POST", base+"/metrics", map[string]any{"key": "spend_30d", "name": "x", "aggregate": "count", "events": []string{"a.b"}})
	big := owner.must(201, "POST", base+"/metrics", map[string]any{"key": "big_payments_30d", "name": "Big payments", "aggregate": "max",
		"events": []string{"payment.succeeded"}, "property": "amount_minor", "levels": []string{"company"}})
	settled()
	if m := attr(acme); m["big_payments_30d"] != 60000.0 {
		t.Fatalf("a new metric is filled from history: %v", m)
	}
	owner.must(400, "PATCH", base+"/metrics/"+big["id"].(string), map[string]any{"key": "other"})
	if v := owner.must(200, "PATCH", base+"/metrics/"+big["id"].(string), map[string]any{"window_days": 7}); v["version"] != 2.0 {
		t.Fatalf("changing what it counts makes a version: %v", v)
	}
	settled()
	if m := attr(acme); m["big_payments_30d"] != 50000.0 {
		t.Fatalf("refilled for 7 days: %v", m)
	}

	// Latest: what the product last reported, such as a prepaid balance.
	owner.must(400, "POST", base+"/metrics", map[string]any{"key": "balance", "name": "Balance", "aggregate": "latest", "events": []string{"balance.updated"}})
	owner.must(201, "POST", base+"/metrics", map[string]any{"key": "balance", "name": "Balance", "aggregate": "latest",
		"events": []string{"balance.updated"}, "property": "balance_minor", "window_days": 365, "levels": []string{"person", "company"}})
	settled()
	if m := attr(acme); m["balance"] != 4000.0 || attr(ana)["balance"] != 4000.0 {
		t.Fatalf("the latest balance from history: %v", m)
	}
	for i, v := range []int{2500, 700} {
		if _, err := customers.Record(ctx, e.pool, ws, customers.Event{RecordID: ana, Name: "balance.updated",
			Properties: map[string]any{"balance_minor": v}, MessageID: "bal-" + strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
		settled()
	}
	if m := attr(acme); m["balance"] != 700.0 {
		t.Fatalf("the latest balance, live: %v", m)
	}
	owner.must(200, "POST", base+"/metrics/"+big["id"].(string)+"/archive", nil)
	if _, ok := attr(acme)["big_payments_30d"]; ok {
		t.Fatal("an archived metric left the record")
	}
	if fired(acme) != 0 {
		t.Fatal("refills fired the rule")
	}
	// Product categories: a metric can count one category's products only.
	owner.must(400, "PATCH", base+"/products/"+resi, map[string]any{"category": "Not A Key!"})
	if v := owner.must(200, "PATCH", base+"/products/"+resi, map[string]any{"category": " Proxies "}); v["category"] != "proxies" {
		t.Fatalf("a product's category: %v", v)
	}
	extract := owner.must(201, "POST", base+"/products", map[string]any{"name": "Extract", "key": "extract", "category": "web_data",
		"aliases": []string{"extract"}})["id"].(string)
	if l := owner.must(200, "GET", base+"/products?category=web_data", nil)["data"].([]any); len(l) != 1 || l[0].(map[string]any)["id"] != extract {
		t.Fatalf("products in a category: %v", l)
	}
	exec(`INSERT INTO customer_events (workspace_id, record_id, brand_id, kind, name, properties, source, occurred_at, received_at, product_id, usage)
		VALUES ($1, $2, $3, 'track', 'product_usage', '{"bandwidth_gb": 2.5}', 'api', now() - interval '2 days', now() - interval '2 days', $4, true),
			($1, $2, $3, 'track', 'product_usage', '{"bandwidth_gb": 1}', 'api', now() - interval '2 days', now() - interval '2 days', $5, true)`,
		ws, ana, brand, resi, extract)
	catMetric := "proxies_gb_30d"
	owner.must(400, "POST", base+"/metrics", map[string]any{"key": catMetric, "name": "Proxies GB", "aggregate": "sum",
		"events": []string{"product_usage"}, "property": "bandwidth_gb", "product_category": "Bad Category!"})
	cat := owner.must(201, "POST", base+"/metrics", map[string]any{"key": catMetric, "name": "Proxies GB", "aggregate": "sum",
		"events": []string{"product_usage"}, "property": "bandwidth_gb", "product_category": "proxies"})
	if cat["product_category"] != "proxies" {
		t.Fatalf("a category metric: %v", cat)
	}
	settled()
	if m := attr(acme); m["proxies_gb_30d"] != 2.5 {
		t.Fatalf("a category metric is filled from that category's products only: %v", m)
	}
	for i, product := range []string{"residential", "extract"} {
		owner.must(200, "POST", "/v1/workspaces/"+ws+"/customer-events", map[string]any{"type": "track", "userId": "u-ana", "event": "Product Usage",
			"messageId": "usage-" + strconv.Itoa(i), "properties": map[string]any{"product": product, "bandwidth_gb": 1.5}})
		settled()
	}
	if m := attr(acme); m["proxies_gb_30d"] != 4.0 {
		t.Fatalf("a category metric counts live events of that category only: %v", m)
	}
	owner.must(200, "PATCH", base+"/metrics", map[string]any{"enabled": false})
}
