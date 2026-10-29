package api_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/journeys"
	"github.com/outflareai/outflare/engine/internal/track"
)

// A live journey changes through a draft version: publishing it keeps the
// people already in it on their version, or moves them onto the new one.
func TestJourneyVersions(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	owner.must(201, "POST", base+"/email-templates", map[string]any{"name": "Tips", "purpose": "lifecycle",
		"subject": "Tips", "html": "<p>Tips</p>", "from": "hello@example.com"})
	wait := func(id string, secs int, next string) map[string]any {
		s := map[string]any{"id": id, "kind": "wait", "wait_seconds": secs}
		if next != "" {
			s["next"] = next
		}
		return s
	}
	j := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Nurture",
		"steps": []map[string]any{wait("first", 3600, "second"), wait("second", 7200, "")}})
	id := j["id"].(string)
	if j["draft_updated_at"] != nil {
		t.Fatalf("new journey = %v", j)
	}
	// A journey that hasn't gone live is changed directly.
	owner.must(409, "POST", base+"/journeys/"+id+"/draft", nil)
	owner.must(404, "GET", base+"/journeys/"+id+"/draft", nil)
	owner.must(200, "POST", base+"/journeys/"+id+"/activate", nil)
	vs := owner.must(200, "GET", base+"/journeys/"+id+"/versions", nil)["data"].([]any)
	if len(vs) != 1 || vs[0].(map[string]any)["version"] != 1.0 || vs[0].(map[string]any)["status"] != "published" {
		t.Fatalf("versions = %v", vs)
	}

	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	svc := &emails.Service{Pool: e.pool, Store: e.integrations, Box: e.box, PublicURL: "https://outflare.test", Log: log, Resolver: e.dns}
	(&journeys.Service{Pool: e.pool, Emails: svc, Log: log}).Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	defer func() { cancel(); <-done }()
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("waited 60s for %s", what)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	person := func(addr string) string {
		t.Helper()
		rec, err := customers.EnsurePerson(ctx, e.pool, ws, "email", addr, true, "")
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}
	run := func(runID string) map[string]any {
		return owner.must(200, "GET", base+"/journeys/"+id+"/runs/"+runID, nil)
	}
	// endWait makes a run's wait over and moves it on.
	endWait := func(runID, key string) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `UPDATE journey_runs SET next_at = now() - interval '1 second' WHERE id = $1`, runID); err != nil {
			t.Fatal(err)
		}
		if _, err := jobs.Enqueue(ctx, e.pool, jobs.NewJob{Kind: journeys.JobRun, WorkspaceID: &ws, Payload: map[string]string{"run_id": runID},
			IdempotencyKey: "journey-run:" + runID + ":" + key}); err != nil {
			t.Fatal(err)
		}
	}

	ana := owner.must(201, "POST", base+"/journeys/"+id+"/runs", map[string]any{"record_id": person("ana@example.org")})["id"].(string)
	waitFor("ana to wait", func() bool { r := run(ana); return r["step"] == "first" && r["next_at"] != nil })
	if r := run(ana); r["version"] != 1.0 {
		t.Fatalf("ana = %v", r)
	}

	// Editing a live journey: its steps can't change in place.
	owner.must(409, "PATCH", base+"/journeys/"+id, map[string]any{"steps": []map[string]any{wait("first", 3600, "")}})
	owner.must(404, "PATCH", base+"/journeys/"+id+"/draft", map[string]any{"entry": "again"})
	d := owner.must(201, "POST", base+"/journeys/"+id+"/draft", nil)
	if d["status"] != "draft" || d["version"] != nil || len(d["steps"].([]any)) != 2 {
		t.Fatalf("draft = %v", d)
	}
	owner.must(200, "POST", base+"/journeys/"+id+"/draft", nil)
	if j := owner.must(200, "GET", base+"/journeys/"+id, nil); j["draft_updated_at"] == nil || j["at_step"].(map[string]any)["first"] != 1.0 {
		t.Fatalf("journey = %v", j)
	}
	owner.must(400, "PATCH", base+"/journeys/"+id+"/draft", map[string]any{"steps": []map[string]any{wait("first", 5, "")}})
	owner.must(400, "POST", base+"/journeys/"+id+"/publish", map[string]any{"runs": "sideways"})
	// A step nothing leads to, and an unpublished template, are refused.
	owner.must(200, "PATCH", base+"/journeys/"+id+"/draft", map[string]any{"steps": []map[string]any{wait("first", 3600, ""), {"id": "done", "kind": "exit"}}})
	owner.must(400, "POST", base+"/journeys/"+id+"/publish", nil)
	owner.must(200, "PATCH", base+"/journeys/"+id+"/draft", map[string]any{"steps": []map[string]any{wait("first", 3600, "tips"), {"id": "tips", "kind": "email", "template": "tips"}}})
	owner.must(400, "POST", base+"/journeys/"+id+"/publish", nil)
	owner.must(200, "PATCH", base+"/journeys/"+id+"/draft", map[string]any{"entry": "again",
		"steps": []map[string]any{wait("first", 3600, "done"), {"id": "done", "kind": "exit"}}})
	if j := owner.must(200, "GET", base+"/journeys/"+id, nil); j["entry"] != "once" || len(j["steps"].([]any)) != 2 || j["steps"].([]any)[1].(map[string]any)["id"] != "second" {
		t.Fatalf("the draft changed the live journey: %v", j)
	}

	// Publish, keeping the people in it on their version.
	j = owner.must(200, "POST", base+"/journeys/"+id+"/publish", nil)
	if j["version"] != 2.0 || j["entry"] != "again" || j["draft_updated_at"] != nil || j["steps"].([]any)[1].(map[string]any)["id"] != "done" {
		t.Fatalf("published = %v", j)
	}
	owner.must(404, "POST", base+"/journeys/"+id+"/publish", nil)
	vs = owner.must(200, "GET", base+"/journeys/"+id+"/versions", nil)["data"].([]any)
	if len(vs) != 2 || vs[0].(map[string]any)["version"] != 2.0 || vs[1].(map[string]any)["runs_active"] != 1.0 || vs[0].(map[string]any)["runs_active"] != 0.0 {
		t.Fatalf("versions = %v", vs)
	}
	// Ana finishes version 1: its second wait, which version 2 doesn't have.
	endWait(ana, "v1-first")
	waitFor("ana to reach the second wait", func() bool { r := run(ana); return r["step"] == "second" && r["next_at"] != nil })
	bo := owner.must(201, "POST", base+"/journeys/"+id+"/runs", map[string]any{"record_id": person("bo@example.org")})["id"].(string)
	waitFor("bo to wait", func() bool { r := run(bo); return r["step"] == "first" && r["next_at"] != nil })
	if at := owner.must(200, "GET", base+"/journeys/"+id+"/runs?status=active&step=second", nil)["data"].([]any); len(at) != 1 || at[0].(map[string]any)["id"] != ana {
		t.Fatalf("on the second wait = %v", at)
	}
	if r := run(bo); r["version"] != 2.0 {
		t.Fatalf("bo = %v", r)
	}

	// Publish again, moving everyone onto the new version: Ana's step is
	// gone from it, so she exits when she moves on.
	owner.must(201, "POST", base+"/journeys/"+id+"/draft", nil)
	owner.must(200, "PATCH", base+"/journeys/"+id+"/draft", map[string]any{"steps": []map[string]any{wait("first", 1800, "done"), {"id": "done", "kind": "exit"}}})
	j = owner.must(200, "POST", base+"/journeys/"+id+"/publish", map[string]any{"runs": "move"})
	if j["version"] != 3.0 {
		t.Fatalf("published = %v", j)
	}
	if r := run(ana); r["version"] != 3.0 {
		t.Fatalf("ana = %v", r)
	}
	endWait(ana, "v3-second")
	waitFor("ana to exit", func() bool { r := run(ana); return r["status"] == "exited" && r["exit_reason"] == "step_removed" })

	// A draft can be thrown away.
	owner.must(201, "POST", base+"/journeys/"+id+"/draft", nil)
	owner.must(204, "DELETE", base+"/journeys/"+id+"/draft", nil)
	owner.must(404, "DELETE", base+"/journeys/"+id+"/draft", nil)

	seen := map[string]bool{}
	for _, ev := range got() {
		seen[ev.Name] = true
	}
	for _, name := range []string{"journey.draft_created", "journey.draft_updated", "journey.published", "journey.draft_discarded"} {
		if !seen[name] {
			t.Errorf("no %s event", name)
		}
	}
}
