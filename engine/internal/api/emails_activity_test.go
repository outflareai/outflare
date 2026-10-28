package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestEmailTriggersAndTracking(t *testing.T) {
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
	e.dns.setTXT("resend._domainkey.example.com", []string{"p=KEY"})
	if err := svc.RunDomainCheck(ctx, did); err != nil {
		t.Fatal(err)
	}

	// Tracking is off unless the template turns it on, and changes without
	// a publish.
	tpl := owner.must(201, "POST", base+"/email-templates", map[string]any{
		"name": "Welcome", "subject": "Welcome", "publish": true, "from": "hello@example.com",
		"html": `<html><body><p><a href="https://acme.test/start?a=1&amp;b=2">Start</a> or <a href='https://acme.test/docs'>docs</a></p></body></html>`,
		"text": "Start: https://acme.test/start?a=1&b=2",
	})
	if tpl["track_opens"] != false || tpl["track_clicks"] != false {
		t.Fatalf("defaults = %v", tpl)
	}
	tpl = owner.must(200, "PATCH", base+"/email-templates/welcome", map[string]any{"track_opens": true, "track_clicks": true})
	if tpl["track_opens"] != true || tpl["track_clicks"] != true || tpl["unpublished_changes"] != false {
		t.Fatalf("tracking on = %v", tpl)
	}

	// The send records what triggered it: this API call.
	em := owner.must(202, "POST", base+"/emails", map[string]any{"to": "Ana@Example.org", "cc": "boss@example.org", "template": "welcome"})
	eid := em["id"].(string)
	tr := em["trigger"].(map[string]any)
	if tr["kind"] != "api" || tr["request_id"] == nil || tr["surface"] != "web" || tr["actor_name"] != "Jack" || em["track_opens"] != true {
		t.Fatalf("trigger = %v", em)
	}
	if n := len(owner.must(200, "GET", base+"/emails?request_id="+url.QueryEscape(tr["request_id"].(string)), nil)["data"].([]any)); n != 1 {
		t.Fatalf("by request = %d", n)
	}
	if n := len(owner.must(200, "GET", base+"/emails?to=boss@example.org", nil)["data"].([]any)); n != 1 {
		t.Fatalf("by cc = %d", n)
	}
	owner.must(400, "GET", base+"/emails?trigger=nope", nil)
	if err := svc.Send(ctx, eid, false); err != nil {
		t.Fatal(err)
	}
	m := fake.last()
	html, text := m["html"].(string), m["text"].(string)
	clicks := regexp.MustCompile(`https?://[^/"']+(/c/[0-9a-f-]{36})`).FindAllStringSubmatch(html, -1)
	pixel := regexp.MustCompile(`https?://[^/"']+(/o/[^"]+)"`).FindStringSubmatch(html)
	if len(clicks) != 2 || pixel == nil || strings.Contains(html, "acme.test") || !strings.Contains(text, "/c/") ||
		strings.Index(html, "/o/") > strings.Index(html, "</body>") {
		t.Fatalf("tracked content = %s / %s", html, text)
	}
	// The same link in HTML and text shares one click link.
	if !strings.Contains(text, clicks[0][1]) {
		t.Fatalf("text link = %s, html links = %v", text, clicks)
	}

	anon := &client{t: t, h: e.h}
	// Right after sending, a load is a scanner's: on the timeline, not counted.
	anon.rawUA("GET", pixel[1], "Mozilla/5.0")
	if v := owner.must(200, "GET", base+"/emails/"+eid, nil); v["opens"].(float64) != 0 {
		t.Fatalf("scanner open counted = %v", v)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE emails SET sent_at = now() - interval '1 hour' WHERE id = $1`, eid); err != nil {
		t.Fatal(err)
	}
	anon.rawUA("GET", pixel[1], "Mozilla/5.0 (Macintosh)")
	anon.rawUA("GET", pixel[1], "Mozilla/5.0 (Macintosh)")
	code, loc := anon.rawUA("GET", clicks[0][1], "Mozilla/5.0 (Macintosh)")
	if code != 302 || loc != "https://acme.test/start?a=1&b=2" {
		t.Fatalf("click = %d %s", code, loc)
	}
	if code, _ := anon.rawUA("GET", "/o/"+eid+".bad", "Mozilla/5.0"); code != 200 {
		t.Fatalf("bad pixel = %d", code)
	}
	v := owner.must(200, "GET", base+"/emails/"+eid, nil)
	types := []string{}
	for _, ev := range v["events"].([]any) {
		types = append(types, ev.(map[string]any)["type"].(string))
	}
	if v["opens"].(float64) != 2 || v["clicks"].(float64) != 1 || v["opened_at"] == nil || v["clicked_at"] == nil ||
		strings.Join(types, ",") != "queued,sent,opened_automatic,opened,opened,clicked" {
		t.Fatalf("engagement = %v %v", v, types)
	}

	// The app shows the content as written, so viewing it counts nothing.
	if h := v["html"].(string); strings.Contains(h, "/o/") || strings.Contains(h, "/c/") || !strings.Contains(h, `href="https://acme.test/start?a=1&amp;b=2"`) ||
		!strings.Contains(v["text"].(string), "https://acme.test/start?a=1&b=2") {
		t.Fatalf("shown content = %s / %s", h, v["text"])
	}

	// One recipient's history.
	r := owner.must(200, "GET", base+"/email-recipients/ana@example.org", nil)
	c := r["counts"].(map[string]any)
	if c["emails"].(float64) != 1 || c["opened"].(float64) != 1 || c["clicked"].(float64) != 1 || r["suppressed"] != nil ||
		len(r["events"].([]any)) != 6 || r["events"].([]any)[0].(map[string]any)["type"] != "clicked" {
		t.Fatalf("recipient = %v", r)
	}
	owner.must(400, "GET", base+"/email-recipients/not-an-address", nil)

	// A test send is marked as one; a send can turn tracking off.
	ts := owner.must(202, "POST", base+"/emails", map[string]any{"to": "jack@example.com", "template": "welcome", "test": true, "track": map[string]any{"opens": false, "clicks": false}})
	if ts["trigger"].(map[string]any)["kind"] != "test" || ts["track_opens"] != false {
		t.Fatalf("test send = %v", ts)
	}
	if err := svc.Send(ctx, ts["id"].(string), false); err != nil {
		t.Fatal(err)
	}
	if h := fake.last()["html"].(string); !strings.Contains(h, `href="https://acme.test/start?a=1&amp;b=2"`) || strings.Contains(h, "/o/") {
		t.Fatalf("untracked = %s", h)
	}

	names := map[string]bool{}
	for _, ev := range got() {
		names[ev.Name] = true
	}
	for _, n := range []string{"email.opened", "email.clicked", "email_template.updated"} {
		if !names[n] {
			t.Errorf("no %s event", n)
		}
	}
}

// rawUA sends a GET with a user agent and returns the status and Location.
func (c *client) rawUA(method, path, userAgent string) (int, string) {
	c.t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("User-Agent", userAgent)
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec.Code, rec.Header().Get("Location")
}
