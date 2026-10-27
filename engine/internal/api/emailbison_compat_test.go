package api_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/mailbox/mailtest"
)

func TestEmailBisonCompat(t *testing.T) {
	e := freshEnv(t)
	srv := mailtest.Start(t, map[string]string{"ana@acme.test": "app-pass-1", "bo@acme.test": "app-pass-2"})
	e.mail.Dialer.TLSConfig = srv.ClientTLS
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "cheap inboxes",
		"scopes": []string{"mailboxes.read", "mailboxes.manage"}})
	ci := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	eb := "/emailbison/" + ws + "/api"

	// No token, no answer.
	anon := &client{t: t, h: e.h}
	anon.must(401, "GET", eb+"/users", nil)

	me := ci.must(200, "GET", eb+"/users", nil)["data"].(map[string]any)
	if me["name"] != "Acme Outbound" {
		t.Fatalf("users: %v", me)
	}

	// Create one sender the way EmailBison's imap-smtp endpoint takes it.
	one := map[string]any{"name": "Ana", "email": "ana@acme.test", "password": "app-pass-1",
		"smtp_server": srv.Host, "smtp_port": srv.SMTPPort, "imap_server": srv.Host, "imap_port": srv.IMAPPort,
		"daily_limit": 20}
	ana := ci.must(200, "POST", eb+"/sender-emails/imap-smtp", one)["data"].(map[string]any)
	if ana["email"] != "ana@acme.test" || ana["daily_limit"] != 20.0 || ana["id"] == nil || ana["outflare_id"] == nil {
		t.Fatalf("create: %v", ana)
	}
	if _, leaked := ana["password"]; leaked {
		t.Fatal("password in response")
	}

	// Bulk, with one bad row reported and the good one kept.
	bulk := map[string]any{"sender_emails": []any{
		map[string]any{"email": "bo@acme.test", "password": "app-pass-2", "smtp_server": srv.Host, "smtp_port": srv.SMTPPort,
			"imap_server": srv.Host, "imap_port": srv.IMAPPort},
		map[string]any{"email": "not an email", "password": "x"},
	}}
	res := ci.must(200, "POST", eb+"/sender-emails/bulk", bulk)
	if got := res["data"].([]any); len(got) != 1 {
		t.Fatalf("bulk data: %v", res)
	}
	if bad := res["errors"].([]any); len(bad) != 1 {
		t.Fatalf("bulk errors: %v", res)
	}
	ci.must(422, "POST", eb+"/sender-emails/imap-smtp", map[string]any{"email": "cy@acme.test"})

	// The mailboxes are real Outflare mailboxes and get verified.
	e.runJobs(t)
	anaPath := base + "/mailboxes/" + ana["outflare_id"].(string)
	got := waitFor(t, owner, anaPath, func(m map[string]any) bool { return m["status"] != "verifying" })
	if got["status"] != "active" || got["source"] != "emailbison_api" {
		t.Fatalf("verified: %v", got)
	}

	// List, paginated like EmailBison.
	list := ci.must(200, "GET", eb+"/sender-emails?per_page=1", nil)
	meta := list["meta"].(map[string]any)
	if meta["total"] != 2.0 || meta["last_page"] != 2.0 || len(list["data"].([]any)) != 1 || list["links"].(map[string]any)["next"] == nil {
		t.Fatalf("list: %v", list)
	}
	if first := list["data"].([]any)[0].(map[string]any); first["status"] != "Connected" {
		t.Fatalf("status: %v", first)
	}
	if found := ci.must(200, "GET", eb+"/sender-emails?search=bo@", nil)["data"].([]any); len(found) != 1 {
		t.Fatalf("search: %v", found)
	}

	// Show by number or by address, then update the limit.
	id := fmt.Sprint(int(ana["id"].(float64)))
	ci.must(200, "GET", eb+"/sender-emails/"+id, nil)
	ci.must(200, "GET", eb+"/sender-emails/ana@acme.test", nil)
	ci.must(404, "GET", eb+"/sender-emails/999999", nil)
	up := ci.must(200, "PATCH", eb+"/sender-emails/"+id, map[string]any{"daily_limit": 35})["data"].(map[string]any)
	if up["daily_limit"] != 35.0 {
		t.Fatalf("update: %v", up)
	}

	// Warm-up runs only in a connected sequencer, and Ana is in none.
	ci.must(422, "PATCH", eb+"/warmup/sender-emails/enable", map[string]any{"sender_email_ids": []any{ana["id"]}})
	if got := owner.must(200, "GET", anaPath, nil); got["warmup"].(map[string]any)["enabled"] != false {
		t.Fatalf("warmup applied: %v", got)
	}
	ci.must(200, "PATCH", eb+"/warmup/sender-emails/disable", map[string]any{"sender_email_ids": []any{ana["id"]}})

	// A read-only token can read but not write.
	rt := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "reader", "scopes": []string{"mailboxes.read"}})
	reader := &client{t: t, h: e.h, bearer: rt["token"].(string)}
	reader.must(200, "GET", eb+"/sender-emails", nil)
	reader.must(403, "POST", eb+"/sender-emails/imap-smtp", one)

	// Delete pauses the mailbox instead of erasing it.
	ci.must(200, "DELETE", eb+"/sender-emails/"+id, nil)
	if got := owner.must(200, "GET", anaPath, nil); got["status"] != "paused" {
		t.Fatalf("delete: %v", got)
	}

	// An endpoint we don't answer is a 404 and lands in the call log, keys only.
	ci.must(404, "POST", eb+"/campaigns", map[string]any{"name": "secret campaign name"})
	calls := owner.must(200, "GET", base+"/emailbison-compat/calls", nil)["data"].([]any)
	if len(calls) < 10 {
		t.Fatalf("calls: %d", len(calls))
	}
	last := calls[0].(map[string]any)
	if last["path"] != "/api/campaigns" && last["path"] != "/campaigns" {
		t.Fatalf("last call: %v", last)
	}
	if last["supported"] != false || last["status"] != 404.0 || fmt.Sprint(last["body_keys"]) != "[name]" {
		t.Fatalf("last call: %v", last)
	}
	for _, c := range calls {
		if strings.Contains(fmt.Sprint(c), "app-pass") || strings.Contains(fmt.Sprint(c), "secret campaign") {
			t.Fatalf("value logged: %v", c)
		}
	}

	// Another workspace's token cannot use this workspace's address.
	other := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Other"})
	ot := owner.must(201, "POST", "/v1/workspaces/"+other["id"].(string)+"/api-tokens",
		map[string]any{"name": "o", "scopes": []string{"mailboxes.read"}})
	(&client{t: t, h: e.h, bearer: ot["token"].(string)}).must(404, "GET", eb+"/sender-emails", nil)
}
