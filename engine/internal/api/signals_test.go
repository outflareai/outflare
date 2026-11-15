package api_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/signals"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Signals: raised by hand and by events, one open per type and five per
// record, accepted, closed with an outcome, reopened, and escalated when
// nobody picks them up.
func TestSignals(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
	}

	// The starter types are there from the first read.
	types := owner.must(200, "GET", base+"/signal-types", nil)["data"].([]any)
	if len(types) != len(signals.Starter) {
		t.Fatalf("starter types: %d", len(types))
	}
	// A type of the brand's own, raised by an event.
	owner.must(400, "PUT", base+"/signal-types/Bad-Key", map[string]any{"label": "Bad"})
	owner.must(400, "PUT", base+"/signal-types/usage_drop", map[string]any{})
	owner.must(200, "PUT", base+"/signal-types/usage_drop", map[string]any{"label": "Usage dropped", "events": []string{"account.usage_dropped"}, "due_days": 3})
	owner.must(200, "PUT", base+"/signal-types/usage_drop", map[string]any{"description": "Usage fell by a third against the month before."})

	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme"})["id"].(string)
	me := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)

	captured := track.Capture(t)
	tracked := func(name string) bool {
		for _, ev := range captured() {
			if ev.Name == name {
				return true
			}
		}
		return false
	}
	// By hand: no type means "other", no title means the type's label.
	owner.must(400, "POST", base+"/signals", map[string]any{"record_id": "nope"})
	owner.must(400, "POST", base+"/signals", map[string]any{"record_id": acme, "type": "no_such_type"})
	owner.must(404, "POST", base+"/signals", map[string]any{"record_id": "00000000-0000-4000-8000-000000000000"})
	first := owner.must(201, "POST", base+"/signals", map[string]any{"record_id": acme, "type": "upsell", "title": "Asked about the bigger plan"})
	if first["status"] != "open" || first["source"] != "manual" || first["type_label"] != "Upsell" || first["record_name"] != "Acme" || first["due_at"] == nil {
		t.Fatalf("a raised signal: %v", first)
	}
	if !tracked("signal.raised") {
		t.Fatal("signal.raised was not tracked")
	}
	id := first["id"].(string)
	// One open signal per type on a record.
	owner.must(409, "POST", base+"/signals", map[string]any{"record_id": acme, "type": "upsell"})

	// The owner hears about it only when someone else raised it; the
	// alert exists either way.
	if n := count(`SELECT count(*) FROM alerts WHERE workspace_id = $1 AND kind = $2 AND target_id = $3`, ws, signals.AlertRaised, id); n != 1 {
		t.Fatalf("alerts for the signal: %d", n)
	}

	// An event one of the types listens to raises a signal, once.
	jq := jobs.New(e.pool, log)
	jq.PollInterval = 20 * time.Millisecond
	svc := &signals.Service{Pool: e.pool, Log: log}
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
				FROM jobs WHERE workspace_id = $1 AND kind = $2`, ws, signals.JobEvent).Scan(&n, &failed)
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
	for _, msg := range []string{"drop-1", "drop-2"} {
		if _, err := customers.Record(ctx, e.pool, ws, customers.Event{RecordID: acme, Name: "account.usage_dropped", Source: "api", MessageID: msg}); err != nil {
			t.Fatal(err)
		}
	}
	// An event no type listens to queues nothing.
	if _, err := customers.Record(ctx, e.pool, ws, customers.Event{RecordID: acme, Name: "page.viewed", Source: "api", MessageID: "view-1"}); err != nil {
		t.Fatal(err)
	}
	settled()
	if n := count(`SELECT count(*) FROM jobs WHERE workspace_id = $1 AND kind = $2`, ws, signals.JobEvent); n != 2 {
		t.Fatalf("signal jobs: %d, want 2", n)
	}
	list := owner.must(200, "GET", base+"/signals?type=usage_drop", nil)
	rows := list["data"].([]any)
	if len(rows) != 1 {
		t.Fatalf("usage_drop signals: %d, want 1", len(rows))
	}
	dropped := rows[0].(map[string]any)
	if dropped["source"] != "event" || dropped["title"] != "Usage dropped" || dropped["event_id"] == nil {
		t.Fatalf("an event's signal: %v", dropped)
	}
	counts := list["counts"].(map[string]any)
	if counts["open"] != float64(2) || counts["no_owner"] != float64(2) {
		t.Fatalf("counts: %v", counts)
	}

	// At most five open signals on a record.
	for _, typ := range []string{"churn_risk", "cross_sell", "renewal"} {
		owner.must(201, "POST", base+"/signals", map[string]any{"record_id": acme, "type": typ})
	}
	owner.must(409, "POST", base+"/signals", map[string]any{"record_id": acme, "type": "billing"})

	// The account's summary leads with them.
	sum := owner.must(200, "GET", base+"/records/"+acme+"/summary", nil)["attention"].([]any)
	if len(sum) == 0 || sum[0].(map[string]any)["kind"] != "signal" || sum[0].(map[string]any)["count"] != float64(5) {
		t.Fatalf("summary attention: %v", sum)
	}

	// Accepting one nobody owns makes it yours.
	got := owner.must(200, "PATCH", base+"/signals/"+id, map[string]any{"status": "accepted"})
	if got["status"] != "accepted" || got["owner_id"] != me {
		t.Fatalf("accepted: %v", got)
	}
	if n := len(owner.must(200, "GET", base+"/signals?owner=me", nil)["data"].([]any)); n != 1 {
		t.Fatalf("my signals: %d", n)
	}
	// Snoozed signals leave the queue until the time passes.
	owner.must(200, "PATCH", base+"/signals/"+id, map[string]any{"snoozed_until": time.Now().Add(48 * time.Hour)})
	if n := len(owner.must(200, "GET", base+"/signals", nil)["data"].([]any)); n != 4 {
		t.Fatalf("open after a snooze: %d", n)
	}
	if n := len(owner.must(200, "GET", base+"/signals?status=snoozed", nil)["data"].([]any)); n != 1 {
		t.Fatalf("snoozed: %d", n)
	}
	owner.must(200, "PATCH", base+"/signals/"+id, map[string]any{"snoozed_until": nil})

	// Closing takes an outcome; an outcome on an open signal is refused.
	owner.must(400, "PATCH", base+"/signals/"+id, map[string]any{"outcome": "won"})
	owner.must(400, "PATCH", base+"/signals/"+id, map[string]any{"status": "done", "outcome": "great"})
	got = owner.must(200, "PATCH", base+"/signals/"+id, map[string]any{"status": "done", "outcome": "won"})
	if got["status"] != "done" || got["outcome"] != "won" || got["closed_at"] == nil {
		t.Fatalf("closed: %v", got)
	}
	if !tracked("signal.accepted") || !tracked("signal.closed") {
		t.Fatal("signal.accepted and signal.closed were not tracked")
	}
	// With it closed there is room for another of its type, and then the
	// closed one can't be reopened.
	again := owner.must(201, "POST", base+"/signals", map[string]any{"record_id": acme, "type": "upsell"})["id"].(string)
	owner.must(409, "PATCH", base+"/signals/"+id, map[string]any{"status": "open"})
	owner.must(200, "PATCH", base+"/signals/"+again, map[string]any{"status": "dismissed"})
	got = owner.must(200, "PATCH", base+"/signals/"+id, map[string]any{"status": "open"})
	if got["status"] != "open" || got["outcome"] != nil || got["closed_at"] != nil {
		t.Fatalf("reopened: %v", got)
	}
	for _, ty := range owner.must(200, "GET", base+"/signal-types", nil)["data"].([]any) {
		if ty := ty.(map[string]any); ty["key"] == "upsell" && (ty["open"] != float64(1) || ty["not_useful_90d"] != float64(1)) {
			t.Fatalf("upsell's results: %v", ty)
		}
	}

	// An open signal nobody accepted two days past its due date goes to
	// the owners and admins, once.
	dropID := dropped["id"].(string)
	if _, err := e.pool.Exec(ctx, `UPDATE signals SET due_at = now() - interval '49 hours' WHERE id = $1`, dropID); err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{1, 0} {
		n, err := svc.Escalate(ctx)
		if err != nil || n != want {
			t.Fatalf("escalation %d: %d %v, want %d", i, n, err, want)
		}
	}
	if n := count(`SELECT count(*) FROM alerts WHERE workspace_id = $1 AND kind = $2 AND target_id = $3`, ws, signals.AlertEscalated, dropID); n != 1 {
		t.Fatalf("escalation alerts: %d", n)
	}
	late := owner.must(200, "GET", base+"/signals?type=usage_drop", nil)["data"].([]any)[0].(map[string]any)
	if late["late"] != true || late["escalated_at"] == nil {
		t.Fatalf("a late signal: %v", late)
	}

	// An archived type raises nothing more; its signals stay.
	owner.must(200, "DELETE", base+"/signal-types/usage_drop", nil)
	owner.must(404, "DELETE", base+"/signal-types/usage_drop", nil)
	owner.must(400, "POST", base+"/signals", map[string]any{"record_id": acme, "type": "usage_drop"})
	if n := len(owner.must(200, "GET", base+"/signals?type=usage_drop", nil)["data"].([]any)); n != 1 {
		t.Fatalf("an archived type's signals: %d", n)
	}
	owner.must(404, "PATCH", base+"/signals/00000000-0000-4000-8000-000000000000", map[string]any{"status": "done"})
}
