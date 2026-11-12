package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// People imported into a static list, a marketing broadcast to the list,
// and the same list sent a service message.
func TestBroadcastToImportedList(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	rs := httptest.NewServer(&fakeResend{verified: true})
	defer rs.Close()
	e.integrations.Client.BaseURLs["resend"] = rs.URL
	owner, ws := setupOwner(t, e.h)
	guardrailsOff(t, owner, ws)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	svc := &emails.Service{Pool: e.pool, Store: e.integrations, Box: e.box, PublicURL: "https://outflare.test",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Resolver: e.dns}
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "resend", "credentials": map[string]any{"api_key": "re_test_not_a_real_key"}})
	did := owner.must(201, "POST", base+"/email-provider-domains", map[string]any{"domain": "example.com"})["id"].(string)
	e.dns.setTXT("resend._domainkey.example.com", []string{"p=KEY"})
	if err := svc.RunDomainCheck(ctx, did); err != nil {
		t.Fatal(err)
	}
	owner.must(201, "POST", base+"/email-topics", map[string]any{"name": "News"})
	owner.must(201, "POST", base+"/email-templates", map[string]any{"name": "Monthly", "purpose": "lifecycle", "topic": "news",
		"subject": "News", "html": "<p>Hi</p>", "from": "news@example.com", "publish": true})
	owner.must(201, "POST", base+"/email-templates", map[string]any{"name": "Notice", "subject": "A change to your plan",
		"html": "<p>Hi {{person.first_name}}</p>", "from": "news@example.com", "publish": true})
	brand := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	bb := "/v1/brands/" + brand

	// Bo is already here and left all marketing email; Cy is here too, and
	// suppressed.
	bo, err := customers.EnsurePerson(ctx, e.pool, ws, "email", "bo@example.org", true, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := customers.RecordConsent(ctx, e.pool, ws, customers.ConsentChange{RecordID: bo, Channel: "email", Status: customers.Withdrawn}); err != nil {
		t.Fatal(err)
	}
	if _, err := customers.EnsurePerson(ctx, e.pool, ws, "email", "cy@example.org", true, ""); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "POST", bb+"/suppressions", map[string]any{"values": []string{"cy@example.org"}, "reason": "bounced"})

	list := owner.must(201, "POST", bb+"/smart-lists", map[string]any{"name": "Search users", "kind": "static"})["id"].(string)
	smart := owner.must(201, "POST", bb+"/smart-lists", map[string]any{"name": "Everyone", "rules": []any{}})["id"].(string)

	// The import: Ana is new (with her user id), Bo and Cy are matched by
	// email; an unknown user id alone, a bad email and a repeat are skipped.
	owner.must(400, "POST", bb+"/records/import", map[string]any{"people": []any{}})
	owner.must(400, "POST", bb+"/records/import", map[string]any{"people": []any{map[string]any{"email": "x@example.org"}}, "origin": "bought"})
	owner.must(409, "POST", bb+"/records/import", map[string]any{"people": []any{map[string]any{"email": "x@example.org"}}, "list_id": smart})
	imp := owner.must(200, "POST", bb+"/records/import", map[string]any{"origin": "first_party", "list_id": list, "people": []any{
		map[string]any{"email": "Ana@Example.org", "user_id": "u-1", "name": "Ana"},
		map[string]any{"email": "bo@example.org"},
		map[string]any{"email": "cy@example.org"},
		map[string]any{"user_id": "u-404"},
		map[string]any{"email": "not an email"},
		map[string]any{"email": "bo@example.org"},
	}})
	if imp["created"] != 1.0 || imp["matched"] != 2.0 || len(imp["skipped"].([]any)) != 3 || imp["list_members"] != 3.0 || len(imp["record_ids"].([]any)) != 3 {
		t.Fatalf("import = %v", imp)
	}
	ana := imp["record_ids"].([]any)[0].(string)
	// Imported again by user id alone: the same person.
	again := owner.must(200, "POST", bb+"/records/import", map[string]any{"people": []any{map[string]any{"user_id": "u-1"}}})
	if again["created"] != 0.0 || again["matched"] != 1.0 || again["record_ids"].([]any)[0] != ana || again["list_id"] != nil {
		t.Fatalf("again = %v", again)
	}
	rec := owner.must(200, "GET", bb+"/records/"+ana, nil)
	if rec["origin"] != "first_party" {
		t.Fatalf("ana = %v", rec)
	}

	// The rules on a broadcast's kind.
	aud := map[string]any{"kind": "list", "ref": list}
	owner.must(400, "POST", base+"/email-broadcasts", map[string]any{"name": "x", "template": "monthly", "audience": map[string]any{"kind": "list"}})
	owner.must(400, "POST", base+"/email-broadcasts", map[string]any{"name": "x", "template": "monthly", "audience": map[string]any{"kind": "list", "ref": "00000000-0000-0000-0000-000000000000"}})
	owner.must(400, "POST", base+"/email-broadcasts", map[string]any{"name": "x", "template": "notice", "audience": aud})
	owner.must(400, "POST", base+"/email-broadcasts", map[string]any{"name": "x", "template": "monthly", "service": true, "audience": aud})
	owner.must(400, "POST", base+"/email-broadcasts", map[string]any{"name": "x", "template": "notice", "service": true})
	owner.must(400, "POST", base+"/email-broadcasts", map[string]any{"name": "x", "template": "notice", "service": true, "audience": aud, "holdout_percent": 10})

	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 20 * time.Millisecond
	svc.Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	defer func() { cancel(); <-done }()
	// run sends the broadcast and waits until want of its emails went out
	// (the emails send in their own jobs after the fan-out finishes).
	run := func(body map[string]any, want float64) (map[string]any, map[string]string) {
		t.Helper()
		b := owner.must(201, "POST", base+"/email-broadcasts", body)
		if b["audience_size"] != 3.0 || b["audience"].(map[string]any)["ref"] != list {
			t.Fatalf("draft = %v", b)
		}
		id := b["id"].(string)
		owner.must(200, "POST", base+"/email-broadcasts/"+id+"/send", nil)
		for i := 0; ; i++ {
			b = owner.must(200, "GET", base+"/email-broadcasts/"+id, nil)
			if b["status"] == "sent" && b["stats"].(map[string]any)["sent"] == want {
				break
			}
			if i > 400 {
				t.Fatalf("broadcast = %v", b)
			}
			time.Sleep(25 * time.Millisecond)
		}
		out := map[string]string{}
		for _, r := range owner.must(200, "GET", base+"/email-broadcasts/"+id+"/recipients", nil)["data"].([]any) {
			r := r.(map[string]any)
			if s, _ := r["skipped"].(string); s != "" {
				out[r["record_id"].(string)] = "skipped: " + s
			} else {
				em := owner.must(200, "GET", base+"/emails/"+r["email_id"].(string), nil)
				out[r["record_id"].(string)] = "sent " + em["purpose"].(string)
			}
		}
		return b, out
	}

	// Marketing to the list: Ana's imported email isn't verified, but the
	// list vouches for her; Bo left marketing email; Cy is suppressed, so
	// the policy check stops her email.
	b, who := run(map[string]any{"name": "News to search users", "template": "monthly", "audience": aud}, 1)
	sent := func(b map[string]any) any { return b["stats"].(map[string]any)["sent"] }
	if b["service"] != false || sent(b) != 1.0 || who[ana] != "sent lifecycle" || !strings.HasPrefix(who[bo], "skipped") {
		t.Fatalf("marketing = %v %v", b, who)
	}

	// A service message to the same list: Bo gets it too; Cy is still
	// suppressed, and it has no topic.
	b, who = run(map[string]any{"name": "Plan change", "template": "notice", "service": true, "audience": aud}, 2)
	if b["service"] != true || b["topic"] != nil || sent(b) != 2.0 || b["skipped"] != 0.0 || who[ana] != "sent transactional" || who[bo] != "sent transactional" {
		t.Fatalf("service = %v %v", b, who)
	}

	// A lead list is a list too: its leads' people are the audience.
	lead := owner.must(201, "POST", base+"/leads", map[string]any{"email": "dee@example.org"})["id"].(string)
	leads := owner.must(201, "POST", bb+"/lead-lists", map[string]any{"name": "Affiliates"})["id"].(string)
	owner.must(200, "POST", bb+"/lead-lists/"+leads+"/leads", map[string]any{"lead_ids": []string{lead}})
	d := owner.must(201, "POST", base+"/email-broadcasts", map[string]any{"name": "To affiliates", "template": "notice", "service": true,
		"audience": map[string]any{"kind": "list", "ref": leads}})
	if d["audience_size"] != 1.0 || d["audience"].(map[string]any)["ref"] != leads {
		t.Fatalf("lead list draft = %v", d)
	}

	n := map[string]int{}
	for _, ev := range got() {
		n[ev.Name]++
	}
	if n["records.imported"] != 2 || n["smart_list.members_added"] != 1 {
		t.Fatalf("events = %v", n)
	}
}
