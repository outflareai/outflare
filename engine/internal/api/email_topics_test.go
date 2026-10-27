package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestEmailTopicsAndPreferences(t *testing.T) {
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
	pid := owner.must(200, "GET", base+"/email-providers", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	if err := svc.SetUpEvents(ctx, pid); err != nil {
		t.Fatal(err)
	}
	did := owner.must(201, "POST", base+"/email-provider-domains", map[string]any{"domain": "example.com"})["id"].(string)
	e.dns.txt["resend._domainkey.example.com"] = []string{"p=KEY"}
	if err := svc.RunDomainCheck(ctx, did); err != nil {
		t.Fatal(err)
	}

	// Topics: the key comes from the name and can't change.
	news := owner.must(201, "POST", base+"/email-topics", map[string]any{"name": "Product updates", "description": "New features, once a month."})
	if news["key"] != "product_updates" || news["subscribed_by_default"] != true || news["visibility"] != "public" || news["status"] != "active" {
		t.Fatalf("topic = %v", news)
	}
	beta := owner.must(201, "POST", base+"/email-topics", map[string]any{"name": "Beta program", "key": "beta", "subscribed_by_default": false})
	owner.must(409, "POST", base+"/email-topics", map[string]any{"name": "Again", "key": "beta"})
	owner.must(400, "POST", base+"/email-topics", map[string]any{"name": "Bad", "key": "Not A Key"})
	owner.must(400, "PATCH", base+"/email-topics/beta", map[string]any{"key": "beta2"})
	if v := owner.must(200, "PATCH", base+"/email-topics/beta", map[string]any{"name": "Beta"}); v["name"] != "Beta" || v["id"] != beta["id"] {
		t.Fatalf("rename = %v", v)
	}
	if n := len(owner.must(200, "GET", base+"/email-topics", nil)["data"].([]any)); n != 2 {
		t.Fatalf("topics = %d", n)
	}

	// Only lifecycle templates and sends have a topic.
	owner.must(400, "POST", base+"/email-templates", map[string]any{"name": "Receipt", "topic": "product_updates"})
	tpl := owner.must(201, "POST", base+"/email-templates", map[string]any{"name": "Monthly", "purpose": "lifecycle", "topic": "product_updates",
		"subject": "What's new", "html": "<p>News</p>", "from": "news@example.com", "publish": true})
	if tp, _ := tpl["topic"].(map[string]any); tp == nil || tp["key"] != "product_updates" {
		t.Fatalf("template topic = %v", tpl)
	}
	owner.must(400, "PATCH", base+"/email-templates/monthly", map[string]any{"purpose": "transactional"})
	owner.must(400, "POST", base+"/emails", map[string]any{"to": "ana@example.org", "from": "news@example.com", "subject": "x", "text": "x", "topic": "beta"})
	owner.must(400, "POST", base+"/emails", map[string]any{"to": "ana@example.org", "from": "news@example.com", "subject": "x", "text": "x", "purpose": "lifecycle", "topic": "nope"})

	send := func(body map[string]any) map[string]any {
		t.Helper()
		em := owner.must(202, "POST", base+"/emails", body)
		if err := svc.Send(ctx, em["id"].(string), false); err != nil {
			t.Fatal(err)
		}
		return owner.must(200, "GET", base+"/emails/"+em["id"].(string), nil)
	}
	// Subscribed by default: it goes, with its topic on it.
	first := send(map[string]any{"to": "ana@example.org", "template": "monthly"})
	if first["status"] != "sent" || first["topic"].(map[string]any)["key"] != "product_updates" {
		t.Fatalf("first = %v", first)
	}
	if n := len(owner.must(200, "GET", base+"/emails?topic=product_updates", nil)["data"].([]any)); n != 1 {
		t.Fatalf("by topic = %d", n)
	}
	// Opt-in: nobody has opted in yet.
	if v := send(map[string]any{"to": "ana@example.org", "from": "news@example.com", "subject": "Beta", "text": "Try it", "purpose": "lifecycle", "topic": "beta"}); v["status"] != "suppressed" ||
		!strings.Contains(v["status_reason"].(string), "opt in") {
		t.Fatalf("opt-in topic = %v", v)
	}

	// The unsubscribe link opens the preference page; a GET changes nothing.
	link := svc.UnsubscribeURL(first["id"].(string))
	path := strings.TrimPrefix(link, "https://outflare.test")
	anon := &client{t: t, h: e.h}
	code, page := anon.html("GET", path, "")
	if code != 200 || !strings.Contains(page, "Unsubscribe from Product updates") {
		t.Fatalf("page = %d %s", code, page)
	}
	if strings.Contains(page, `value="beta" checked`) || !strings.Contains(page, `value="product_updates" checked`) {
		t.Fatalf("page choices = %s", page)
	}
	// One click (the button, or a mail app) leaves that topic only.
	if code, page = anon.html("POST", path, "List-Unsubscribe=One-Click"); code != 200 || !strings.Contains(page, "You&#39;re unsubscribed") {
		t.Fatalf("one-click = %d %s", code, page)
	}
	if v := send(map[string]any{"to": "Ana@example.org", "template": "monthly"}); v["status"] != "suppressed" ||
		!strings.Contains(v["status_reason"].(string), "unsubscribed from Product updates") {
		t.Fatalf("after unsubscribe = %v", v)
	}
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM suppressions WHERE workspace_id = $1 AND value = 'ana@example.org'`, ws).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a topic unsubscribe is not a suppression: %d %v", n, err)
	}
	// The consent is on her customer record, made for the address.
	var status, topic, source string
	if err := e.pool.QueryRow(ctx, `SELECT c.status, c.topic, c.source FROM consents c JOIN record_identities i ON i.record_id = c.record_id
		WHERE c.workspace_id = $1 AND i.value = 'ana@example.org' AND i.verified ORDER BY c.occurred_at DESC LIMIT 1`, ws).Scan(&status, &topic, &source); err != nil ||
		status != "withdrawn" || topic != "product_updates" || source != "unsubscribe_link" {
		t.Fatalf("consent = %s %s %s %v", status, topic, source, err)
	}

	// The page saves choices: back into product updates, and into beta.
	if code, page = anon.html("POST", path, "action=save&topic=product_updates&topic=beta"); code != 200 ||
		!strings.Contains(page, `value="beta" checked`) || !strings.Contains(page, `value="product_updates" checked`) {
		t.Fatalf("save = %d %s", code, page)
	}
	if v := send(map[string]any{"to": "ana@example.org", "from": "news@example.com", "subject": "Beta", "text": "Try it", "purpose": "lifecycle", "topic": "beta"}); v["status"] != "sent" {
		t.Fatalf("opted in = %v", v)
	}
	// Leaving the topic again after coming back works too.
	if code, _ := anon.html("POST", path, "List-Unsubscribe=One-Click"); code != 200 {
		t.Fatalf("second one-click = %d", code)
	}
	anon.html("POST", path, "action=save&topic=product_updates&topic=beta")
	// All marketing off stops every lifecycle email, but not receipts.
	if code, page = anon.html("POST", path, "action=save&all=off"); code != 200 {
		t.Fatalf("all off = %d %s", code, page)
	}
	if v := send(map[string]any{"to": "ana@example.org", "template": "monthly"}); v["status"] != "suppressed" {
		t.Fatalf("all off = %v", v)
	}
	if v := send(map[string]any{"to": "ana@example.org", "from": "billing@example.com", "subject": "Receipt", "text": "Thanks"}); v["status"] != "sent" {
		t.Fatalf("receipt = %v", v)
	}
	ev := owner.must(200, "GET", base+"/emails/"+first["id"].(string), nil)["events"].([]any)
	if last := ev[len(ev)-1].(map[string]any); last["type"] != "preferences_changed" {
		t.Fatalf("events = %v", ev)
	}

	// Archiving stops new sends naming the topic.
	owner.must(200, "PATCH", base+"/email-topics/beta", map[string]any{"status": "archived"})
	owner.must(400, "POST", base+"/emails", map[string]any{"to": "ana@example.org", "from": "news@example.com", "subject": "x", "text": "x", "purpose": "lifecycle", "topic": "beta"})

	seen := map[string]bool{}
	for _, ev := range got() {
		seen[ev.Name] = true
	}
	for _, name := range []string{"email_topic.created", "email_topic.updated", "email_topic.archived", "email.unsubscribed", "email.preferences_changed", "consent.withdrawn", "consent.granted"} {
		if !seen[name] {
			t.Fatalf("no %s event", name)
		}
	}
}

// html sends a form (or nothing) to a public page and returns its HTML.
func (c *client) html(method, path, form string) (int, string) {
	c.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(form))
	if form != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}
