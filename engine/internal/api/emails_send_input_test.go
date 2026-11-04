package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/emails"
)

// Input Postgres can't store must never turn a send into a 500: a NUL
// character is refused with a 400, and a link too long to track is sent
// untracked (cloud#375).
func TestSendEmailAwkwardInput(t *testing.T) {
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
	send := func() map[string]any {
		return map[string]any{"to": "ana@example.org", "from": "hi@example.com", "subject": "Hi", "html": "<p>Hi</p>"}
	}
	for name, change := range map[string]func(m map[string]any){
		"subject":      func(m map[string]any) { m["subject"] = "Hi\u0000" },
		"html":         func(m map[string]any) { m["html"] = "<p>Hi\u0000</p>" },
		"text":         func(m map[string]any) { m["text"] = "Hi\u0000" },
		"tag":          func(m map[string]any) { m["tags"] = []string{"a\u0000"} },
		"metadata":     func(m map[string]any) { m["metadata"] = map[string]any{"k": "a\u0000"} },
		"metadata key": func(m map[string]any) { m["metadata"] = map[string]any{"k\u0000": "a"} },
		"data":         func(m map[string]any) { m["subject"] = "Hi {{name}}"; m["data"] = map[string]any{"name": "Ana\u0000"} },
	} {
		m := send()
		change(m)
		if code, body := owner.do("POST", base+"/emails", m); code != 400 {
			t.Errorf("NUL in %s = %d %v", name, code, body)
		}
	}

	// Content filled in with data holding a NUL (from a record in a
	// journey, say) is sent without it.
	subject, html := "Hello {{name}}", "<p>Hello {{name}}</p>"
	out, err := svc.Compose(ctx, e.pool, emails.Draft{NewEmail: emails.NewEmail{WorkspaceID: ws, To: []string{"ana@example.org"}, From: "hi@example.com"},
		Subject: &subject, HTML: &html, Data: map[string]any{"name": "Ana\u0000"}})
	if err != nil || out.Subject != "Hello Ana" || strings.ContainsRune(out.HTML+out.Text, 0) {
		t.Fatalf("composed = %q %q %q, %v", out.Subject, out.HTML, out.Text, err)
	}

	// A signed link several KB long goes out as written; the short one is
	// tracked.
	long := "https://acme.test/magic?a=1&b=" + strings.Repeat("0123456789abcdef", 250)
	escaped := strings.ReplaceAll(long, "&", "&amp;")
	em := owner.must(202, "POST", base+"/emails", map[string]any{"to": "ana@example.org", "from": "hi@example.com", "subject": "Sign in",
		"html":  `<a href="` + escaped + `">Sign in</a> <a href="https://acme.test/help">Help</a>`,
		"text":  "Sign in: " + long + " Help: https://acme.test/help",
		"track": map[string]any{"clicks": true}})
	var text string
	if err := e.pool.QueryRow(ctx, `SELECT html, text_body FROM emails WHERE id = $1`, em["id"]).Scan(&html, &text); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `href="`+escaped+`"`) || strings.Contains(html, "acme.test/help") || !strings.Contains(html, "/c/") ||
		!strings.Contains(text, long) || strings.Contains(text, "acme.test/help") {
		t.Fatalf("tracked content = %.300s / %.300s", html, text)
	}
}
