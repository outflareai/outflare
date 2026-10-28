package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/journeys"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestJourneys(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	fake := &fakeResend{verified: true}
	rs := httptest.NewServer(fake)
	defer rs.Close()
	e.integrations.Client.BaseURLs["resend"] = rs.URL
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := &emails.Service{Pool: e.pool, Store: e.integrations, Box: e.box, PublicURL: "https://outflare.test", Log: log, Resolver: e.dns}
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "resend", "credentials": map[string]any{"api_key": "re_test_not_a_real_key"}})
	did := owner.must(201, "POST", base+"/email-provider-domains", map[string]any{"domain": "example.com"})["id"].(string)
	e.dns.setTXT("resend._domainkey.example.com", []string{"p=KEY"})
	if err := svc.RunDomainCheck(ctx, did); err != nil {
		t.Fatal(err)
	}
	owner.must(201, "POST", base+"/email-templates", map[string]any{"name": "Welcome", "purpose": "lifecycle",
		"subject": "Welcome {{person.first_name}}", "html": "<p>Your {{event.plan}} plan</p>", "from": "hello@example.com", "publish": true})
	tips := owner.must(201, "POST", base+"/email-templates", map[string]any{"name": "Tips", "purpose": "lifecycle",
		"subject": "Tips", "html": "<p>Tips</p>", "from": "hello@example.com"})
	owner.must(201, "POST", base+"/email-templates", map[string]any{"name": "Receipt", "subject": "x", "html": "x", "publish": true})

	steps := []map[string]any{
		{"id": "welcome", "kind": "email", "template": "welcome", "next": "wait"},
		{"id": "wait", "kind": "wait", "wait_seconds": 3600, "next": "check"},
		{"id": "check", "kind": "branch", "conditions": []map[string]any{{"event": "upgraded", "op": "not_exists"}}, "then": "tips"},
		{"id": "tips", "kind": "email", "template": "tips"},
	}
	// Bad steps and templates are refused.
	owner.must(400, "POST", base+"/journeys", map[string]any{"name": "Loop", "steps": []map[string]any{
		{"id": "a", "kind": "wait", "wait_seconds": 60, "next": "b"}, {"id": "b", "kind": "wait", "wait_seconds": 60, "next": "a"}}})
	owner.must(400, "POST", base+"/journeys", map[string]any{"name": "Receipt", "steps": []map[string]any{{"id": "a", "kind": "email", "template": "receipt"}}})
	owner.must(400, "POST", base+"/journeys", map[string]any{"name": "Bad", "trigger": map[string]any{"kind": "event", "event": "Signed Up"}})
	owner.must(400, "POST", base+"/journeys", map[string]any{"name": "Bad", "trigger": map[string]any{"kind": "event", "event": "signed_up",
		"filter": []map[string]any{{"event": "upgraded", "op": "exists"}}}})

	j := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Onboarding", "goal_event": "upgraded", "steps": steps,
		"trigger": map[string]any{"kind": "event", "event": "signed_up", "filter": []map[string]any{{"attribute": "plan", "op": "neq", "value": "free"}}}})
	if j["status"] != "draft" || j["entry"] != "once" || len(j["steps"].([]any)) != 4 {
		t.Fatalf("journey = %v", j)
	}
	id := j["id"].(string)
	// Tips isn't published yet.
	owner.must(400, "POST", base+"/journeys/"+id+"/activate", nil)
	owner.must(200, "POST", base+"/email-templates/"+tips["id"].(string)+"/publish", nil)
	owner.must(409, "POST", base+"/journeys/"+id+"/pause", nil)
	j = owner.must(200, "POST", base+"/journeys/"+id+"/activate", nil)
	if j["status"] != "active" || j["activated_at"] == nil {
		t.Fatalf("activated = %v", j)
	}
	owner.must(409, "PATCH", base+"/journeys/"+id, map[string]any{"steps": steps[:1]})
	owner.must(200, "PATCH", base+"/journeys/"+id, map[string]any{"description": "New people"})

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
	event := func(rec, name string, props map[string]any) {
		t.Helper()
		if _, err := customers.Record(ctx, e.pool, ws, customers.Event{RecordID: rec, Name: name, Properties: props}); err != nil {
			t.Fatal(err)
		}
	}
	ana := person("ana@example.org", `{"first_name": "Ana", "plan": "pro"}`)
	bo := person("bo@example.org", `{"first_name": "Bo", "plan": "free"}`)

	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	svc.Register(q)
	(&journeys.Service{Pool: e.pool, Emails: svc, Log: log}).Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	defer func() { cancel(); <-done }()

	runs := func(query string) []any {
		t.Helper()
		return owner.must(200, "GET", base+"/journeys/"+id+"/runs"+query, nil)["data"].([]any)
	}
	// Poll until a deadline rather than a fixed number of tries: under a
	// busy CI run the worker can take a while to claim each job.
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

	// Ana signs up on a paid plan: welcome, then she waits. Bo is on the
	// free plan, so he doesn't start.
	event(ana, "signed_up", map[string]any{"plan": "pro"})
	event(bo, "signed_up", map[string]any{"plan": "free"})
	var run map[string]any
	waitFor("ana to wait", func() bool {
		list := runs("?record=" + ana)
		if len(list) == 1 {
			run = list[0].(map[string]any)
		}
		return run != nil && run["step"] == "wait" && run["next_at"] != nil
	})
	if n := len(runs("")); n != 1 {
		t.Fatalf("runs = %d", n)
	}
	runID := run["id"].(string)
	r := owner.must(200, "GET", base+"/journeys/"+id+"/runs/"+runID, nil)
	h := r["history"].([]any)
	if len(h) != 2 || h[0].(map[string]any)["outcome"] != "sent" || h[1].(map[string]any)["outcome"] != "waiting" {
		t.Fatalf("history = %v", h)
	}
	em := owner.must(200, "GET", base+"/emails/"+h[0].(map[string]any)["email_id"].(string), nil)
	if em["subject"] != "Welcome Ana" || em["trigger"].(map[string]any)["kind"] != "journey" || em["trigger"].(map[string]any)["ref"] != id {
		t.Fatalf("welcome = %v", em)
	}

	// The hour passes: she hasn't upgraded, so tips, and she's done.
	if _, err := e.pool.Exec(ctx, `UPDATE journey_runs SET next_at = now() - interval '1 second' WHERE id = $1`, runID); err != nil {
		t.Fatal(err)
	}
	// Queue a fresh run job instead of moving the wait's own job, so the
	// test doesn't depend on that job's state at this moment.
	if _, err := jobs.Enqueue(ctx, e.pool, jobs.NewJob{Kind: journeys.JobRun, WorkspaceID: &ws, Payload: map[string]string{"run_id": runID},
		IdempotencyKey: "journey-run:" + runID + ":test-wait-over"}); err != nil {
		t.Fatal(err)
	}
	waitFor("ana to finish", func() bool {
		r = owner.must(200, "GET", base+"/journeys/"+id+"/runs/"+runID, nil)
		return r["status"] == "completed"
	})
	if h := r["history"].([]any); len(h) != 5 || h[3].(map[string]any)["outcome"] != "yes" || h[4].(map[string]any)["outcome"] != "sent" {
		t.Fatalf("history = %v", h)
	}
	// People go through it once.
	event(ana, "signed_up", map[string]any{"plan": "pro"})

	// Bo is added by hand; he upgrades while he waits, which is the goal.
	owner.must(400, "POST", base+"/journeys/"+id+"/runs", map[string]any{"record_id": "nope"})
	added := owner.must(201, "POST", base+"/journeys/"+id+"/runs", map[string]any{"record_id": bo, "data": map[string]any{"plan": "team"}})
	owner.must(409, "POST", base+"/journeys/"+id+"/runs", map[string]any{"record_id": bo})
	boRun := added["id"].(string)
	waitFor("bo to wait", func() bool {
		return owner.must(200, "GET", base+"/journeys/"+id+"/runs/"+boRun, nil)["step"] == "wait"
	})
	event(bo, "upgraded", nil)
	waitFor("bo to reach the goal", func() bool {
		r = owner.must(200, "GET", base+"/journeys/"+id+"/runs/"+boRun, nil)
		return r["status"] == "exited"
	})
	if r["exit_reason"] != "goal" {
		t.Fatalf("bo = %v", r)
	}
	if n := len(runs("?record=" + ana)); n != 1 {
		t.Fatalf("ana's runs = %d", n)
	}
	j = owner.must(200, "GET", base+"/journeys/"+id, nil)
	st := j["stats"].(map[string]any)
	if st["entered"] != 2.0 || st["completed"] != 1.0 || st["exited"] != 1.0 || j["step_stats"].(map[string]any)["welcome"].(map[string]any)["sent"] != 2.0 {
		t.Fatalf("journey = %v", j)
	}

	// Pause to change the steps, then archive; an archived journey takes no one.
	owner.must(200, "POST", base+"/journeys/"+id+"/pause", nil)
	j = owner.must(200, "PATCH", base+"/journeys/"+id, map[string]any{"steps": []map[string]any{{"id": "welcome", "kind": "email", "template": "welcome"}}})
	if j["version"] != 2.0 {
		t.Fatalf("version = %v", j["version"])
	}
	owner.must(200, "POST", base+"/journeys/"+id+"/archive", nil)
	owner.must(409, "POST", base+"/journeys/"+id+"/runs", map[string]any{"record_id": bo})
	owner.must(409, "POST", base+"/journeys/"+id+"/activate", nil)
	if n := len(owner.must(200, "GET", base+"/journeys", nil)["data"].([]any)); n != 0 {
		t.Fatalf("listed = %d", n)
	}
	if n := len(owner.must(200, "GET", base+"/journeys?status=archived", nil)["data"].([]any)); n != 1 {
		t.Fatalf("archived = %d", n)
	}

	seen := map[string]bool{}
	for _, ev := range got() {
		seen[ev.Name] = true
	}
	for _, name := range []string{"journey.created", "journey.activated", "journey.paused", "journey.archived", "journey_run.entered", "journey_run.completed", "journey_run.exited"} {
		if !seen[name] {
			t.Errorf("no %s event", name)
		}
	}
}
