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
	"github.com/outflareai/outflare/engine/internal/rules"
	"github.com/outflareai/outflare/engine/internal/smartlists"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestJourneyActions(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// The step picker's catalog: every kind, with its outputs.
	kinds := owner.must(200, "GET", base+"/journeys/step-kinds", nil)["data"].([]any)
	byKind := map[string]map[string]any{}
	for _, k := range kinds {
		m := k.(map[string]any)
		byKind[m["kind"].(string)] = m
	}
	for _, k := range journeys.KindNames() {
		if byKind[k] == nil {
			t.Errorf("step-kinds misses %s", k)
		}
	}
	if o := byKind["branch"]["outputs"].([]any); len(o) != 2 || byKind["add_tag"]["error_output"] != "on_error" {
		t.Fatalf("outputs = %v, %v", o, byKind["add_tag"])
	}

	person := func(addr string) string {
		t.Helper()
		rec, err := customers.EnsurePerson(ctx, e.pool, ws, "email", addr, true, "")
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}
	ana, bo := person("ana@acme.example"), person("bo@acme.example")
	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme"})["id"].(string)
	for _, p := range []string{ana, bo} {
		owner.must(200, "PATCH", base+"/records/"+p, map[string]any{"attributes": map[string]any{"companies": []string{acme}}})
	}

	// Tags are tidied, and owner is a member.
	me := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	rec := owner.must(200, "PATCH", base+"/records/"+ana, map[string]any{"attributes": map[string]any{"tags": []string{"VIP ", "vip", "beta"}, "owner": me}})
	if tags := rec["attributes"].(map[string]any)["tags"].([]any); len(tags) != 2 || tags[0] != "vip" {
		t.Fatalf("tags = %v", tags)
	}
	owner.must(400, "PATCH", base+"/records/"+ana, map[string]any{"attributes": map[string]any{"owner": "00000000-0000-0000-0000-000000000000"}})

	// A static list: people go in and out by hand; refreshing changes nothing.
	static := owner.must(201, "POST", base+"/smart-lists", map[string]any{"name": "Webinar", "kind": "static"})
	sid := static["id"].(string)
	if static["kind"] != "static" || len(static["rules"].([]any)) != 0 {
		t.Fatalf("static list = %v", static)
	}
	owner.must(400, "PATCH", base+"/smart-lists/"+sid, map[string]any{"rules": []map[string]any{{"attribute": "plan", "op": "exists"}}})
	if c := owner.must(200, "POST", base+"/smart-lists/"+sid+"/members", map[string]any{"record_ids": []string{bo, acme}}); c["changed"].(float64) != 1 {
		t.Fatalf("added = %v (a company doesn't count)", c)
	}
	smart := owner.must(201, "POST", base+"/smart-lists", map[string]any{"name": "Everyone"})["id"].(string)
	owner.must(409, "POST", base+"/smart-lists/"+smart+"/members", map[string]any{"record_ids": []string{bo}})

	seq := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "Nudge", "steps": []map[string]any{{"subject": "Hello", "body": "Hi"}}})["id"].(string)
	other := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "Old", "steps": []map[string]any{{"subject": "Hello", "body": "Hi"}}})["id"].(string)
	owner.must(201, "POST", base+"/email-topics", map[string]any{"name": "News"})
	// Bo is in the old sequence already, through his lead.
	owner.must(201, "POST", base+"/leads", map[string]any{"email": "bo@acme.example"})
	var boLead string
	if err := e.pool.QueryRow(ctx, `SELECT id::text FROM leads WHERE workspace_id = $1 AND lower(email) = 'bo@acme.example'`, ws).Scan(&boLead); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "POST", base+"/sequences/"+other+"/enrollments", map[string]any{"lead_ids": []string{boLead}})

	// A second journey the first one starts.
	welcome := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Welcome back", "trigger": map[string]any{"kind": "manual"},
		"steps": []map[string]any{{"id": "tag", "kind": "add_tag", "tag": "welcomed"}}})["id"].(string)
	owner.must(200, "POST", base+"/journeys/"+welcome+"/activate", nil)

	steps := []map[string]any{
		{"id": "tag", "kind": "add_tag", "tag": "Churn-Risk", "next": "stage"},
		{"id": "stage", "kind": "set_field", "field": "lifecycle_stage", "value": "churned", "next": "plan"},
		{"id": "plan", "kind": "set_field", "field": "plan_name", "value": "{{event.plan}}", "next": "list", "on_error": "continue"},
		{"id": "list", "kind": "add_to_list", "list_id": sid, "next": "stop"},
		{"id": "stop", "kind": "stop_sequences", "scope": "company", "next": "enroll"},
		{"id": "enroll", "kind": "enroll_in_sequence", "sequence_id": seq, "next": "topic"},
		{"id": "topic", "kind": "unsubscribe_topic", "topic": "news", "next": "alert"},
		{"id": "alert", "kind": "alert", "title": "Went quiet", "text": "Check in", "next": "again"},
		{"id": "again", "kind": "enter_journey", "journey_id": welcome},
	}
	// Bad action steps are refused.
	owner.must(400, "POST", base+"/journeys", map[string]any{"name": "Bad", "steps": []map[string]any{{"id": "a", "kind": "add_tag"}}})
	owner.must(400, "POST", base+"/journeys", map[string]any{"name": "Bad", "steps": []map[string]any{{"id": "a", "kind": "teleport"}}})
	owner.must(400, "POST", base+"/journeys", map[string]any{"name": "Bad", "steps": []map[string]any{
		{"id": "a", "kind": "add_tag", "tag": "x", "on_error": "nowhere"}}})
	owner.must(400, "POST", base+"/journeys", map[string]any{"name": "Bad", "exit_events": []string{"Not An Event"}})

	j := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Win back", "steps": steps,
		"trigger":     map[string]any{"kind": "event", "event": "customer.stalled"},
		"exit_events": []string{"order_completed"}, "exit_conditions": []map[string]any{{"attribute": "tags", "op": "contains", "value": "do-not-touch"}}})
	jid := j["id"].(string)
	if len(j["exit_events"].([]any)) != 1 || len(j["exit_conditions"].([]any)) != 1 {
		t.Fatalf("exit criteria = %v %v", j["exit_events"], j["exit_conditions"])
	}
	// A list that isn't static can't be used.
	owner.must(200, "PATCH", base+"/journeys/"+jid, map[string]any{"steps": append(slices.Clone(steps[:3]), map[string]any{"id": "list", "kind": "add_to_list", "list_id": smart})})
	owner.must(400, "POST", base+"/journeys/"+jid+"/activate", nil)
	owner.must(200, "PATCH", base+"/journeys/"+jid, map[string]any{"steps": steps})
	owner.must(200, "POST", base+"/journeys/"+jid+"/activate", nil)

	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	svc := &journeys.Service{Pool: e.pool, Log: log, Fields: e.srv}
	svc.Register(q)
	(&rules.Service{Pool: e.pool, Log: log}).Register(q)
	(&smartlists.Service{Pool: e.pool, Log: log}).Register(q)
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
	runOf := func(journey, rec string) map[string]any {
		t.Helper()
		for _, r := range owner.must(200, "GET", base+"/journeys/"+journey+"/runs?record="+rec, nil)["data"].([]any) {
			return r.(map[string]any)
		}
		return nil
	}
	finished := func(journey, rec string) map[string]any {
		t.Helper()
		var r map[string]any
		waitFor("the run to finish", func() bool { r = runOf(journey, rec); return r != nil && r["status"] != "active" })
		return r
	}

	// Ana stalls: every step runs.
	if _, err := customers.Record(ctx, e.pool, ws, customers.Event{RecordID: ana, Name: "customer.stalled", Properties: map[string]any{"plan": "pro"}}); err != nil {
		t.Fatal(err)
	}
	r := finished(jid, ana)
	if r["status"] != "completed" {
		t.Fatalf("ana's run = %v", r)
	}
	attrs := owner.must(200, "GET", base+"/records/"+ana, nil)["attributes"].(map[string]any)
	if tags := attrs["tags"].([]any); !slices.Contains(tags, "churn-risk") || !slices.Contains(tags, "vip") {
		t.Errorf("tags = %v (added, keeping the ones set by hand)", tags)
	}
	if attrs["lifecycle_stage"] != "churned" {
		t.Errorf("lifecycle_stage = %v", attrs["lifecycle_stage"])
	}
	stats := owner.must(200, "GET", base+"/journeys/"+jid, nil)["step_stats"].(map[string]any)
	if stats["plan"].(map[string]any)["failed"] == nil || stats["list"].(map[string]any)["done"] == nil || stats["enroll"].(map[string]any)["done"] == nil {
		t.Errorf("step stats = %v (plan_name is no field, so it fails and goes on)", stats)
	}
	// The whole company's cold email stopped, and Ana is in the new one.
	var boActive, anaIn int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE e.lead_id = $1 AND e.status = 'active'),
			count(*) FILTER (WHERE l.record_id = $2 AND e.sequence_id = $3)
		FROM enrollments e JOIN leads l ON l.id = e.lead_id WHERE e.workspace_id = $4`, boLead, ana, seq, ws).Scan(&boActive, &anaIn); err != nil {
		t.Fatal(err)
	}
	if boActive != 0 || anaIn != 1 {
		t.Errorf("bo active %d, ana enrolled %d", boActive, anaIn)
	}
	if in, _ := smartlists.InStatic(ctx, e.pool, sid, ana); !in {
		t.Error("ana is not in the static list")
	}
	if st, _, _ := customers.Consent(ctx, e.pool, ws, ana, "email", "news", "marketing"); st != customers.Withdrawn {
		t.Errorf("news consent = %q", st)
	}
	var alertsN int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM alerts WHERE workspace_id = $1 AND kind = $2 AND owner_id::text = $3`, ws, journeys.AlertKind, me).Scan(&alertsN)
	if alertsN != 1 {
		t.Errorf("alerts to the owner = %d", alertsN)
	}
	if w := finished(welcome, ana); w["status"] != "completed" {
		t.Errorf("the second journey = %v", w)
	}

	// A value someone set by hand isn't overwritten unless the step says.
	owner.must(200, "PATCH", base+"/records/"+bo, map[string]any{"attributes": map[string]any{"lifecycle_stage": "customer"}})
	if _, err := customers.Record(ctx, e.pool, ws, customers.Event{RecordID: bo, Name: "customer.stalled"}); err != nil {
		t.Fatal(err)
	}
	finished(jid, bo)
	attrs = owner.must(200, "GET", base+"/records/"+bo, nil)["attributes"].(map[string]any)
	if attrs["lifecycle_stage"] != "customer" {
		t.Errorf("a hand edit was overwritten: %v", attrs["lifecycle_stage"])
	}
	stats = owner.must(200, "GET", base+"/journeys/"+jid, nil)["step_stats"].(map[string]any)
	if stats["stage"].(map[string]any)["skipped"] == nil {
		t.Errorf("stage stats = %v", stats["stage"])
	}

	// Exit criteria: someone who already meets them never starts.
	cy := person("cy@example.org")
	owner.must(200, "PATCH", base+"/records/"+cy, map[string]any{"attributes": map[string]any{"tags": []string{"do-not-touch"}}})
	owner.must(409, "POST", base+"/journeys/"+jid+"/runs", map[string]any{"record_id": cy})

	names := []string{}
	for _, ev := range got() {
		names = append(names, ev.Name)
	}
	for _, want := range []string{"smart_list.members_added", "journey_run.entered", "enrollment.stopped", "sequence.leads_enrolled", "alert.raised"} {
		if !slices.Contains(names, want) {
			t.Errorf("no %s event", want)
		}
	}
}

// A live journey's draft carries the exit criteria, and publishing swaps
// them in; an error path counts as a way to reach a step.
func TestJourneyDraftExitCriteria(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	steps := []map[string]any{
		{"id": "tag", "kind": "add_tag", "tag": "trial", "on_error": "oops"},
		{"id": "oops", "kind": "alert", "title": "Tagging failed"},
	}
	jid := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Trial", "steps": steps,
		"trigger": map[string]any{"kind": "event", "event": "trial.started"}})["id"].(string)
	owner.must(200, "POST", base+"/journeys/"+jid+"/activate", nil)

	d := owner.must(201, "POST", base+"/journeys/"+jid+"/draft", nil)
	if len(d["exit_events"].([]any)) != 0 {
		t.Fatalf("draft = %v", d)
	}
	owner.must(400, "PATCH", base+"/journeys/"+jid+"/draft", map[string]any{"exit_events": []string{"Not An Event"}})
	d = owner.must(200, "PATCH", base+"/journeys/"+jid+"/draft", map[string]any{"exit_events": []string{"order_completed"},
		"exit_conditions": []map[string]any{{"attribute": "plan", "op": "eq", "value": "pro"}}})
	if len(d["exit_events"].([]any)) != 1 || len(d["exit_conditions"].([]any)) != 1 {
		t.Fatalf("draft = %v", d)
	}
	// oops is only reached when tagging fails: publishing still accepts it.
	j := owner.must(200, "POST", base+"/journeys/"+jid+"/publish", nil)
	if j["exit_events"].([]any)[0] != "order_completed" || len(j["exit_conditions"].([]any)) != 1 || j["version"].(float64) != 2 {
		t.Fatalf("published = %v", j)
	}
	// The version that went out keeps what it had.
	for _, v := range owner.must(200, "GET", base+"/journeys/"+jid+"/versions", nil)["data"].([]any) {
		v := v.(map[string]any)
		if v["version"] == float64(1) && len(v["exit_events"].([]any)) != 0 {
			t.Fatalf("version 1 = %v", v)
		}
	}
}
