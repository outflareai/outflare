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
	"github.com/outflareai/outflare/engine/internal/journeys"
)

// Flow control: wait_until (an event path, a condition path woken by a
// field change, the timeout), split (by value, at random with a holdout)
// and entry limits.
func TestJourneyFlowControl(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	person := func(addr string) string {
		t.Helper()
		rec, err := customers.EnsurePerson(ctx, e.pool, ws, "email", addr, true, "")
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}
	event := func(rec, name string, props map[string]any) {
		t.Helper()
		if _, err := customers.Record(ctx, e.pool, ws, customers.Event{RecordID: rec, Name: name, Properties: props}); err != nil {
			t.Fatal(err)
		}
	}

	// Bad paths are refused.
	for _, bad := range []map[string]any{
		{"id": "w", "kind": "wait_until", "timeout_seconds": 3600},
		{"id": "w", "kind": "wait_until", "paths": []any{map[string]any{"id": "a"}}, "timeout_seconds": 3600},
		{"id": "w", "kind": "wait_until", "paths": []any{map[string]any{"id": "a", "event": "x"}}},
		{"id": "s", "kind": "split", "split_by": "random", "paths": []any{map[string]any{"id": "a", "weight": 0}}},
		{"id": "s", "kind": "split", "paths": []any{map[string]any{"id": "a", "weight": 50}}},
		{"id": "s", "kind": "split", "split_by": "random", "paths": []any{map[string]any{"id": "a", "weight": 10}, map[string]any{"id": "a", "weight": 10}}},
		{"id": "s", "kind": "split", "split_by": "random", "paths": []any{map[string]any{"id": "a", "weight": 10, "holdout": true, "next": "s"}}},
	} {
		owner.must(400, "POST", base+"/journeys", map[string]any{"name": "Bad", "steps": []any{bad}})
	}
	owner.must(400, "POST", base+"/journeys", map[string]any{"name": "Bad", "entry_limit": map[string]any{"count": 0, "per": "hour"}})

	steps := []any{
		map[string]any{"id": "size", "kind": "split", "paths": []any{
			map[string]any{"id": "big", "conditions": []any{map[string]any{"property": "seats", "op": "gt", "value": 5}}, "next": "wait"},
		}, "else": "wait"},
		map[string]any{"id": "wait", "kind": "wait_until", "timeout_seconds": 86400, "timeout_next": "late", "paths": []any{
			map[string]any{"id": "bought", "event": "order_completed", "conditions": []any{map[string]any{"property": "amount", "op": "gt", "value": 10}}, "next": "vip"},
			map[string]any{"id": "upgraded", "conditions": []any{map[string]any{"attribute": "lifecycle_stage", "op": "eq", "value": "churned"}}, "next": "pro"},
		}},
		map[string]any{"id": "vip", "kind": "add_tag", "tag": "vip"},
		map[string]any{"id": "pro", "kind": "add_tag", "tag": "pro"},
		map[string]any{"id": "late", "kind": "add_tag", "tag": "late"},
	}
	jid := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Trial", "steps": steps,
		"trigger": map[string]any{"kind": "event", "event": "trial.started"}})["id"].(string)
	owner.must(200, "POST", base+"/journeys/"+jid+"/activate", nil)

	// Test mode can't wait: a wait_until takes a path that holds now or
	// times out, and paths send a split or a wait_until another way.
	tester := person("tess@acme.example")
	trial := func(paths map[string]any) map[string]string {
		t.Helper()
		body := map[string]any{"record_id": tester, "event": map[string]any{"seats": 2}}
		if paths != nil {
			body["paths"] = paths
		}
		res := owner.must(200, "POST", base+"/journeys/"+jid+"/test", body)
		out := map[string]string{"end": res["end"].(string)}
		for _, st := range res["steps"].([]any) {
			m := st.(map[string]any)
			out[m["step"].(string)] = m["outcome"].(string)
		}
		return out
	}
	if got := trial(nil); got["size"] != "else" || got["wait"] != "timed_out" || got["late"] != "done" || got["end"] != "completed" {
		t.Fatalf("test run: %v", got)
	}
	if got := trial(map[string]any{"size": "big", "wait": "bought"}); got["size"] != "big" || got["wait"] != "bought" || got["vip"] != "done" {
		t.Fatalf("test run down chosen paths: %v", got)
	}
	owner.must(400, "POST", base+"/journeys/"+jid+"/test", map[string]any{"record_id": tester, "paths": map[string]any{"wait": "yes"}})
	owner.must(400, "POST", base+"/journeys/"+jid+"/test", map[string]any{"record_id": tester, "paths": map[string]any{"size": "timed_out"}})

	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	(&journeys.Service{Pool: e.pool, Log: log, Fields: e.srv}).Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	defer func() { cancel(); <-done }()

	runOf := func(journey, rec string) map[string]any {
		t.Helper()
		for _, r := range owner.must(200, "GET", base+"/journeys/"+journey+"/runs?record="+rec, nil)["data"].([]any) {
			return r.(map[string]any)
		}
		return nil
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("waited 30s for %s", what)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	waiting := func(rec string) {
		t.Helper()
		waitFor("the run to wait", func() bool {
			r := runOf(jid, rec)
			return r != nil && r["step"] == "wait" && r["wait_deadline"] != nil
		})
	}
	tagged := func(rec, tag string) {
		t.Helper()
		waitFor("tag "+tag, func() bool {
			tags, _ := owner.must(200, "GET", base+"/records/"+rec, nil)["attributes"].(map[string]any)["tags"].([]any)
			return slices.Contains(tags, any(tag))
		})
		if r := runOf(jid, rec); r == nil || r["status"] != "completed" {
			waitFor("the run to complete", func() bool { r := runOf(jid, rec); return r != nil && r["status"] == "completed" })
		}
	}

	// Ana waits, and an order of 5 isn't the event her path waits for;
	// one of 50 is.
	ana, bo, cy := person("ana@acme.example"), person("bo@acme.example"), person("cy@acme.example")
	event(ana, "trial.started", map[string]any{"seats": 10})
	waiting(ana)
	event(ana, "order_completed", map[string]any{"amount": 5})
	time.Sleep(300 * time.Millisecond)
	if r := runOf(jid, ana); r["step"] != "wait" {
		t.Fatalf("a small order moved ana on: %v", r)
	}
	event(ana, "order_completed", map[string]any{"amount": 50})
	tagged(ana, "vip")

	// Bo's stage changing wakes his run at once.
	event(bo, "trial.started", nil)
	waiting(bo)
	owner.must(200, "PATCH", base+"/records/"+bo, map[string]any{"attributes": map[string]any{"lifecycle_stage": "churned"}})
	tagged(bo, "pro")

	// Cy times out.
	event(cy, "trial.started", nil)
	waiting(cy)
	run := runOf(jid, cy)["id"].(string)
	if _, err := e.pool.Exec(ctx, `UPDATE journey_runs SET wait_deadline = now() - interval '1 second', next_at = now() WHERE id = $1`, run); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Enqueue(ctx, e.pool, jobs.NewJob{Kind: journeys.JobRun, WorkspaceID: &ws, Payload: map[string]string{"run_id": run}}); err != nil {
		t.Fatal(err)
	}
	tagged(cy, "late")

	stats := owner.must(200, "GET", base+"/journeys/"+jid, nil)["step_stats"].(map[string]any)
	split, wait := stats["size"].(map[string]any), stats["wait"].(map[string]any)
	if split["big"] != float64(1) || split["else"] != float64(2) || wait["bought"] != float64(1) || wait["upgraded"] != float64(1) || wait["timed_out"] != float64(1) {
		t.Fatalf("step stats: %v", stats)
	}

	// A random split's holdout ends the run; an entry limit of one an hour
	// turns the second person away.
	hold := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Holdout", "entry_limit": map[string]any{"count": 1, "per": "hour"},
		"trigger": map[string]any{"kind": "event", "event": "signed_up"},
		"steps": []any{
			map[string]any{"id": "ab", "kind": "split", "split_by": "random", "split_key": "company", "paths": []any{
				map[string]any{"id": "send", "weight": 0, "next": "t"},
				map[string]any{"id": "control", "weight": 100, "holdout": true},
			}},
			map[string]any{"id": "t", "kind": "add_tag", "tag": "sent"},
		}})
	if l := hold["entry_limit"].(map[string]any); l["count"] != float64(1) || l["per"] != "hour" {
		t.Fatalf("entry_limit = %v", hold["entry_limit"])
	}
	hid := hold["id"].(string)
	owner.must(200, "POST", base+"/journeys/"+hid+"/activate", nil)
	event(ana, "signed_up", nil)
	waitFor("the holdout", func() bool { r := runOf(hid, ana); return r != nil && r["exit_reason"] == "holdout" })
	event(bo, "signed_up", nil)
	time.Sleep(300 * time.Millisecond)
	if r := runOf(hid, bo); r != nil {
		t.Fatalf("the entry limit let bo in: %v", r)
	}

	// A live journey's draft carries the entry limit.
	owner.must(201, "POST", base+"/journeys/"+hid+"/draft", nil)
	d := owner.must(200, "PATCH", base+"/journeys/"+hid+"/draft", map[string]any{"entry_limit": nil, "reentry_after_seconds": 3600})
	if d["entry_limit"] != nil || d["reentry_after_seconds"] != float64(3600) {
		t.Fatalf("draft = %v", d)
	}
	j := owner.must(200, "POST", base+"/journeys/"+hid+"/publish", nil)
	if j["entry_limit"] != nil || j["reentry_after_seconds"] != float64(3600) {
		t.Fatalf("published = %v", j)
	}
}
