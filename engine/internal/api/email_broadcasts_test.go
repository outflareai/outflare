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

func TestEmailBroadcasts(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	fake := &fakeResend{verified: true}
	rs := httptest.NewServer(fake)
	defer rs.Close()
	e.integrations.Client.BaseURLs["resend"] = rs.URL
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	svc := &emails.Service{Pool: e.pool, Store: e.integrations, Box: e.box, PublicURL: "https://outflare.test",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Resolver: e.dns}
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "resend", "credentials": map[string]any{"api_key": "re_test_not_a_real_key"}})
	did := owner.must(201, "POST", base+"/email-provider-domains", map[string]any{"domain": "example.com"})["id"].(string)
	e.dns.txt["resend._domainkey.example.com"] = []string{"p=KEY"}
	if err := svc.RunDomainCheck(ctx, did); err != nil {
		t.Fatal(err)
	}
	owner.must(201, "POST", base+"/email-topics", map[string]any{"name": "News"})
	owner.must(201, "POST", base+"/email-templates", map[string]any{"name": "Monthly", "purpose": "lifecycle", "topic": "news",
		"subject": "{{title}}", "html": "<p>Hi {{person.first_name}}</p><p><a href=\"https://northpeak.example/pricing?plan=pro\">Pricing</a> <a href=\"https://acme.example/\">Acme</a></p>",
		"from": "news@example.com", "publish": true})
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "website": "northpeak.example"})
	owner.must(201, "POST", base+"/email-templates", map[string]any{"name": "Receipt", "subject": "x", "html": "x", "publish": true})

	// The people: Ana and Dee get it; Bo left News; Cy is a prospect who
	// never agreed; Eve's address isn't verified, so she isn't in it.
	person := func(addr string, verified bool, sql string) string {
		t.Helper()
		id, err := customers.EnsurePerson(ctx, e.pool, ws, "email", addr, verified, "")
		if err != nil {
			t.Fatal(err)
		}
		if sql != "" {
			if _, err := e.pool.Exec(ctx, `UPDATE records SET `+sql+` WHERE id = $1`, id); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	ana := person("ana@example.org", true, `attributes = '{"first_name": "Ana"}'`)
	bo := person("bo@example.org", true, "")
	cy := person("cy@example.org", true, "origin = 'prospect'")
	dee := person("dee@example.org", true, "origin = 'prospect'")
	person("eve@example.org", false, "")
	consent := func(rec, topic, status string) {
		t.Helper()
		if err := customers.RecordConsent(ctx, e.pool, ws, customers.ConsentChange{RecordID: rec, Channel: "email", Topic: topic, Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	consent(bo, "news", customers.Withdrawn)
	consent(dee, "", customers.Granted)

	// A draft: the template's topic, everyone the topic may go to.
	owner.must(400, "POST", base+"/email-broadcasts", map[string]any{"name": "Oops", "template": "receipt"})
	owner.must(400, "POST", base+"/email-broadcasts", map[string]any{"name": "Oops", "template": "monthly", "audience": map[string]any{"kind": "list"}})
	b := owner.must(201, "POST", base+"/email-broadcasts", map[string]any{"name": "October news", "template": "monthly"})
	if b["status"] != "draft" || b["topic"].(map[string]any)["key"] != "news" || b["audience"].(map[string]any)["kind"] != "topic" || b["audience_size"] != 4.0 {
		t.Fatalf("broadcast = %v", b)
	}
	id := b["id"].(string)
	owner.must(200, "PATCH", base+"/email-broadcasts/"+id, map[string]any{"data": map[string]any{"title": "What's new"}})

	b = owner.must(200, "POST", base+"/email-broadcasts/"+id+"/send", nil)
	if b["status"] != "sending" || b["template_version"] != 1.0 {
		t.Fatalf("send = %v", b)
	}
	owner.must(409, "POST", base+"/email-broadcasts/"+id+"/send", nil)
	owner.must(409, "PATCH", base+"/email-broadcasts/"+id, map[string]any{"data": map[string]any{}})
	owner.must(200, "PATCH", base+"/email-broadcasts/"+id, map[string]any{"name": "October newsletter"})

	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 20 * time.Millisecond
	svc.Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	defer func() { cancel(); <-done }()
	for i := 0; ; i++ {
		b = owner.must(200, "GET", base+"/email-broadcasts/"+id, nil)
		if b["status"] == "sent" && b["stats"].(map[string]any)["sent"] == 2.0 {
			break
		}
		if i > 400 {
			t.Fatalf("broadcast = %v", b)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if b["recipients"] != 2.0 || b["skipped"] != 2.0 || b["audience_size"] != nil {
		t.Fatalf("sent = %v", b)
	}
	skipped := map[string]string{}
	for _, r := range owner.must(200, "GET", base+"/email-broadcasts/"+id+"/recipients?skipped=true", nil)["data"].([]any) {
		r := r.(map[string]any)
		skipped[r["record_id"].(string)] = r["skipped"].(string)
	}
	if !strings.Contains(skipped[bo], "unsubscribed from News") || !strings.Contains(skipped[cy], "third-party lead") || len(skipped) != 2 {
		t.Fatalf("skipped = %v", skipped)
	}
	sent := owner.must(200, "GET", base+"/email-broadcasts/"+id+"/recipients?skipped=false", nil)["data"].([]any)
	if len(sent) != 2 {
		t.Fatalf("sent = %v", sent)
	}
	for _, r := range sent {
		r := r.(map[string]any)
		em := owner.must(200, "GET", base+"/emails/"+r["email_id"].(string), nil)
		tr := em["trigger"].(map[string]any)
		if tr["kind"] != "broadcast" || tr["ref"] != id || em["subject"] != "What's new" || em["topic"].(map[string]any)["key"] != "news" {
			t.Fatalf("email = %v", em)
		}
		if r["record_id"] == ana && !strings.Contains(em["html"].(string), "Hi Ana") {
			t.Fatalf("ana's email = %v", em["html"])
		}
		// Links to the brand's own site carry the broadcast's campaign;
		// links to other sites stay as written.
		h := em["html"].(string)
		if !strings.Contains(h, "https://northpeak.example/pricing?plan=pro&amp;utm_source=outflare&amp;utm_medium=email&amp;utm_campaign=october-newsletter&amp;ofc=") ||
			!strings.Contains(h, `href="https://acme.example/"`) {
			t.Fatalf("links = %v", h)
		}
	}
	if _, ok := skipped[dee]; ok {
		t.Fatal("dee agreed to marketing email")
	}
	owner.must(409, "POST", base+"/email-broadcasts/"+id+"/cancel", nil)
	if n := len(owner.must(200, "GET", base+"/emails?broadcast="+id, nil)["data"].([]any)); n != 2 {
		t.Fatalf("its emails = %d", n)
	}

	// Scheduled, then cancelled before it starts; a list of records.
	later := owner.must(201, "POST", base+"/email-broadcasts", map[string]any{"name": "Just Ana", "template": "monthly",
		"audience": map[string]any{"kind": "records", "record_ids": []string{ana}}, "data": map[string]any{"title": "Hello"}})
	if later["audience_size"] != 1.0 {
		t.Fatalf("records audience = %v", later)
	}
	at := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if v := owner.must(200, "POST", base+"/email-broadcasts/"+later["id"].(string)+"/send", map[string]any{"send_at": at}); v["status"] != "scheduled" {
		t.Fatalf("scheduled = %v", v)
	}
	if v := owner.must(200, "POST", base+"/email-broadcasts/"+later["id"].(string)+"/cancel", nil); v["status"] != "cancelled" {
		t.Fatalf("cancelled = %v", v)
	}
	if n := len(owner.must(200, "GET", base+"/email-broadcasts?status=cancelled", nil)["data"].([]any)); n != 1 {
		t.Fatalf("cancelled list = %d", n)
	}

	seen := map[string]bool{}
	for _, ev := range got() {
		seen[ev.Name] = true
	}
	for _, name := range []string{"email_broadcast.created", "email_broadcast.updated", "email_broadcast.started", "email_broadcast.sent",
		"email_broadcast.scheduled", "email_broadcast.cancelled"} {
		if !seen[name] {
			t.Fatalf("no %s event", name)
		}
	}
}
