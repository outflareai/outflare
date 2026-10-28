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
)

// Lifecycle email states land on each recipient's customer timeline.
func TestEmailStatesOnCustomerTimeline(t *testing.T) {
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
	e.dns.setTXT("resend._domainkey.example.com", []string{"p=KEY"})
	if err := svc.RunDomainCheck(ctx, did); err != nil {
		t.Fatal(err)
	}
	owner.must(201, "POST", base+"/email-topics", map[string]any{"name": "Beta", "subscribed_by_default": false})
	owner.must(201, "POST", base+"/email-templates", map[string]any{"name": "Welcome", "purpose": "lifecycle",
		"subject": "Hi", "html": "<p>Hi</p>", "from": "hello@example.com", "publish": true, "track_opens": true})

	ana, err := customers.EnsurePerson(ctx, e.pool, ws, "email", "ana@example.org", true, "")
	if err != nil {
		t.Fatal(err)
	}
	send := func(body map[string]any) string {
		t.Helper()
		id := owner.must(202, "POST", base+"/emails", body)["id"].(string)
		if err := svc.Send(ctx, id, false); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// Sent and opened (a person, an hour later), once each however often.
	welcome := send(map[string]any{"to": "Ana@example.org", "template": "welcome"})
	for range 2 {
		if err := svc.Opened(ctx, welcome, "Mozilla/5.0", time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	// Not sent: she never joined the opt-in topic.
	beta := send(map[string]any{"to": "ana@example.org", "from": "hello@example.com", "subject": "Beta", "text": "x", "purpose": "lifecycle", "topic": "beta"})
	// Someone without a record gets no record; a test send stays off.
	send(map[string]any{"to": "bo@example.org", "template": "welcome"})
	send(map[string]any{"to": "ana@example.org", "template": "welcome", "test": true})

	items := owner.must(200, "GET", base+"/records/"+ana+"/timeline?kinds=event", nil)["data"].([]any)
	got := []string{}
	for _, it := range items {
		it := it.(map[string]any)
		d := it["detail"].(map[string]any)
		props, _ := d["properties"].(map[string]any)
		if props == nil {
			props = d
		}
		got = append(got, it["title"].(string)+":"+props["email_id"].(string)[:8])
		if it["source"] != "lifecycle" {
			t.Fatalf("source = %v", it)
		}
	}
	// Newest first: the open is stamped an hour on.
	want := []string{"email.opened:" + welcome[:8], "email.rejected:" + beta[:8], "email.sent:" + welcome[:8]}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("timeline = %v, want %v", got, want)
	}
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM records WHERE workspace_id = $1`, ws).Scan(&n); err != nil || n != 1 {
		t.Fatalf("records = %d %v", n, err)
	}
}
