package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestJourneyTest(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	fake := &fakeResend{verified: true}
	rs := httptest.NewServer(fake)
	defer rs.Close()
	e.integrations.Client.BaseURLs["resend"] = rs.URL
	owner, ws := setupOwner(t, e.h)
	// Quiet hours would hold the email at night (test mode shows that
	// too); this test is about the steps, so guardrails are off.
	guardrailsOff(t, owner, ws)
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

	j := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Onboarding",
		"trigger": map[string]any{"kind": "event", "event": "signed_up"},
		"steps": []map[string]any{
			{"id": "welcome", "kind": "email", "template": "welcome", "next": "wait"},
			{"id": "wait", "kind": "wait", "wait_seconds": 3600, "next": "check"},
			{"id": "check", "kind": "branch", "conditions": []map[string]any{{"event": "upgraded", "op": "exists"}}, "then": "tag", "else": "bye"},
			{"id": "tag", "kind": "add_tag", "tag": "vip"},
			{"id": "bye", "kind": "exit"},
		}})
	id := j["id"].(string)
	ana, err := customers.EnsurePerson(ctx, e.pool, ws, "email", "ana@example.org", true, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE records SET attributes = '{"first_name": "Ana"}' WHERE id = $1`, ana); err != nil {
		t.Fatal(err)
	}
	if _, err := customers.Record(ctx, e.pool, ws, customers.Event{RecordID: ana, Name: "signed_up", Properties: map[string]any{"plan": "pro"}}); err != nil {
		t.Fatal(err)
	}
	audits := func() int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1`, ws).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before, seen := audits(), len(got())
	test := func(body map[string]any) map[string]any {
		t.Helper()
		body["record_id"] = ana
		return owner.must(200, "POST", base+"/journeys/"+id+"/test", body)
	}
	steps := func(res map[string]any) []map[string]any {
		out := []map[string]any{}
		for _, s := range res["steps"].([]any) {
			out = append(out, s.(map[string]any))
		}
		return out
	}

	// Ana hasn't upgraded, so the branch says no for now: welcome, the
	// wait, then exit. The email uses her name and her latest sign-up.
	res := test(map[string]any{})
	st := steps(res)
	if res["version"] != "live" || res["refusal"] != nil || res["event_at"] == nil || res["end"] != "exited" || res["end_reason"] != "exit_step" || len(st) != 4 {
		t.Fatalf("test = %v", res)
	}
	if st[0]["outcome"] != "sent" || st[0]["subject"] != "Welcome Ana" || st[1]["outcome"] != "waited" ||
		st[2]["outcome"] != "no" || st[2]["data"] != "no" || st[2]["can_change"] != true || st[3]["step"] != "bye" {
		t.Fatalf("steps = %v", st)
	}

	// Sent down yes, the tag step runs, but nothing it did is kept.
	res = test(map[string]any{"paths": map[string]any{"check": "yes"}})
	st = steps(res)
	if res["end"] != "completed" || len(st) != 4 || st[2]["outcome"] != "yes" || st[2]["data"] != "no" || st[3]["outcome"] != "done" {
		t.Fatalf("test with yes = %v", res)
	}
	var emailsN, runsN int
	var tags any
	if err := e.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM emails WHERE workspace_id = $1), (SELECT count(*) FROM journey_runs WHERE workspace_id = $1),
		(SELECT attributes->'tags' FROM records WHERE id = $2)`, ws, ana).Scan(&emailsN, &runsN, &tags); err != nil {
		t.Fatal(err)
	}
	if n := audits() - before; emailsN != 0 || runsN != 0 || n != 0 || tags != nil {
		t.Fatalf("a test left emails %d, runs %d, audit events %d, tags %v", emailsN, runsN, n, tags)
	}
	for _, ev := range got()[seen:] {
		if ev.Name != "api.called" {
			t.Fatalf("a test recorded %s", ev.Name)
		}
	}

	// A given event replaces her latest one.
	res = test(map[string]any{"event": map[string]any{"plan": "team"}})
	if res["event_at"] != nil {
		t.Fatalf("given event = %v", res)
	}

	// Bad input.
	owner.must(400, "POST", base+"/journeys/"+id+"/test", map[string]any{"record_id": "nope"})
	owner.must(400, "POST", base+"/journeys/"+id+"/test", map[string]any{"record_id": ana, "paths": map[string]any{"check": "maybe"}})
	owner.must(404, "POST", base+"/journeys/"+id+"/test", map[string]any{"record_id": "00000000-0000-4000-8000-000000000000"})

	// Live, with a draft: each version tests its own steps. Ana already
	// went through it once, so she wouldn't start again.
	owner.must(200, "POST", base+"/journeys/"+id+"/activate", nil)
	owner.must(404, "POST", base+"/journeys/"+id+"/test", map[string]any{"record_id": ana, "version": "draft"})
	if _, err := e.pool.Exec(ctx, `INSERT INTO journey_runs (workspace_id, journey_id, record_id, status, finished_at) VALUES ($1, $2, $3, 'completed', now())`, ws, id, ana); err != nil {
		t.Fatal(err)
	}
	owner.must(201, "POST", base+"/journeys/"+id+"/draft", nil)
	owner.must(200, "PATCH", base+"/journeys/"+id+"/draft", map[string]any{"steps": []map[string]any{{"id": "bye", "kind": "exit"}}})
	res = test(map[string]any{"version": "draft"})
	if st := steps(res); res["version"] != "draft" || len(st) != 1 || st[0]["step"] != "bye" || res["refusal"] == nil {
		t.Fatalf("draft test = %v", res)
	}
	if st := steps(test(map[string]any{})); len(st) != 4 {
		t.Fatalf("live test = %v", st)
	}
}
