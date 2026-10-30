package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/inboundemail"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// A business forwards its support mailbox to its support address: each
// customer email becomes a conversation, and replies go out through the
// workspace's email provider from the address the customer wrote to.
func TestSupportForwarding(t *testing.T) {
	got := track.Capture(t)
	inbound := &inboundemail.Service{}
	inbound.Enable("in.example.test", inboundSecret)
	e := freshEnvWith(t, func(o *api.Options) { o.InboundEmail = inbound })
	inbound.Pool = e.pool
	fake := &fakeResend{}
	rs := httptest.NewServer(fake)
	defer rs.Close()
	e.integrations.Client.BaseURLs["resend"] = rs.URL
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	// The support address reads like a support inbox.
	if l := owner.must(200, "GET", base+"/inbound-addresses", nil); len(l["purposes"].([]any)) != 1 || l["purposes"].([]any)[0] != "support" {
		t.Fatalf("purposes: %v", l)
	}
	addr := owner.must(201, "POST", base+"/inbound-addresses", map[string]any{"purpose": "support"})["email"].(string)
	if addr != "acme-outbound-support@in.example.test" {
		t.Fatalf("address: %s", addr)
	}

	process := func(raw string) {
		t.Helper()
		code, out := deliver(e.h, addr, []byte(raw), inboundSecret)
		if code != 202 {
			t.Fatalf("delivery: %d %v", code, out)
		}
		payload, _ := json.Marshal(map[string]string{"id": out["id"].(string)})
		if err := inbound.Process(ctx, jobs.Job{Kind: inboundemail.JobProcess, Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	// Gmail forwards the customer's email as it was, naming the mailbox
	// that forwarded it.
	process("From: Ana Silva <ana@acme.example>\r\nTo: support@northpeak.example\r\nSubject: Charged twice\r\n" +
		"Message-ID: <c1@acme.example>\r\nX-Forwarded-For: support@northpeak.example " + addr + "\r\n\r\nI was charged twice this month.\r\n")
	l := owner.must(200, "GET", base+"/support/conversations", nil)
	if l["total"] != 1.0 {
		t.Fatalf("conversations: %v", l)
	}
	c := l["data"].([]any)[0].(map[string]any)
	if c["via"] != "forwarding" || c["inbound_address"] != addr || c["reply_from"] != "support@northpeak.example" ||
		c["contact"].(map[string]any)["email"] != "ana@acme.example" || c["subject"] != "Charged twice" || c["mailbox_id"] != nil {
		t.Fatalf("conversation: %v", c)
	}
	conv := base + "/support/conversations/" + c["id"].(string)

	// A Google Group rewrites From and keeps the customer in Reply-To.
	process("From: 'Li Wei' via Support <support@northpeak.example>\r\nReply-To: Li Wei <li@globex.example>\r\nTo: support@northpeak.example\r\n" +
		"Subject: Export?\r\nMessage-ID: <g1@globex.example>\r\n\r\nWhere is the export?\r\n")
	if l := owner.must(200, "GET", base+"/support/conversations?q=export", nil); l["total"] != 1.0 ||
		l["data"].([]any)[0].(map[string]any)["contact"].(map[string]any)["email"] != "li@globex.example" ||
		l["data"].([]any)[0].(map[string]any)["contact"].(map[string]any)["name"] != "Li Wei" {
		t.Fatalf("group: %v", l)
	}

	// Replying needs the workspace's email provider and the domain verified.
	code, out := owner.do("POST", conv+"/replies", map[string]any{"body": "Refunded."})
	if code != 409 || out["error"].(map[string]any)["code"] != "no_email_provider" {
		t.Fatalf("no provider: %d %v", code, out)
	}
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "resend", "credentials": map[string]any{"api_key": "re_test_not_a_real_key"}})
	code, out = owner.do("POST", conv+"/replies", map[string]any{"body": "Refunded."})
	if code != 409 || out["error"].(map[string]any)["code"] != "email_domain_not_verified" {
		t.Fatalf("unverified: %d %v", code, out)
	}
	d := owner.must(201, "POST", base+"/email-provider-domains", map[string]any{"domain": "northpeak.example"})
	if _, err := e.pool.Exec(ctx, `UPDATE email_domains SET status = 'verified' WHERE id = $1`, d["id"]); err != nil {
		t.Fatal(err)
	}
	reply := owner.must(201, "POST", conv+"/replies", map[string]any{"body": "Sorry Ana, I've refunded the {{second}} charge."})
	var emailID string
	if err := e.pool.QueryRow(ctx, `SELECT email_id::text FROM support_messages WHERE id = $1`, reply["id"]).Scan(&emailID); err != nil {
		t.Fatal(err)
	}
	svc := &emails.Service{Pool: e.pool, Store: e.integrations, Box: e.box, PublicURL: "https://outflare.test",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Resolver: e.dns}
	if err := svc.Send(ctx, emailID, false); err != nil {
		t.Fatal(err)
	}
	sent := fake.last()
	headers, _ := sent["headers"].(map[string]any)
	if sent["from"] != `"Acme Outbound" <support@northpeak.example>` || sent["subject"] != "Re: Charged twice" ||
		!strings.Contains(sent["text"].(string), "the {{second}} charge") || headers["In-Reply-To"] != "<c1@acme.example>" {
		t.Fatalf("sent: %v", sent)
	}
	full := owner.must(200, "GET", conv, nil)
	msgs := full["messages"].([]any)
	if out := msgs[len(msgs)-1].(map[string]any); out["kind"] != "out" || out["status"] != "sent" || out["from_email"] != "support@northpeak.example" {
		t.Fatalf("reply: %v", out)
	}
	if full["conversation"].(map[string]any)["status"] != "pending" {
		t.Fatalf("status: %v", full["conversation"])
	}

	// Their answer, forwarded again, joins the same conversation.
	process("From: Ana Silva <ana@acme.example>\r\nTo: support@northpeak.example\r\nSubject: Re: Charged twice\r\n" +
		"Message-ID: <c2@acme.example>\r\nX-Forwarded-For: support@northpeak.example " + addr + "\r\n\r\nThanks!\r\n")
	if c := owner.must(200, "GET", conv, nil)["conversation"].(map[string]any); c["status"] != "open" || c["messages"] != 3.0 {
		t.Fatalf("threaded: %v", c)
	}
	for _, name := range []string{"inbound_address.created", "support.conversation_started", "support.replied", "email.queued"} {
		if len(named(got(), name)) == 0 {
			t.Errorf("no %s event", name)
		}
	}
}
