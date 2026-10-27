package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/digests"
	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestDigests(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	fake := &fakeResend{}
	rs := httptest.NewServer(fake)
	defer rs.Close()
	e.integrations.Client.BaseURLs["resend"] = rs.URL
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mail := &emails.Service{Pool: e.pool, Store: e.integrations, Box: e.box, PublicURL: "https://outflare.test", Log: log, Resolver: e.dns}
	svc := &digests.Service{Pool: e.pool, Emails: mail, PublicURL: "https://outflare.test", Log: log}
	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	svc.Register(q)
	qctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(qctx, 1); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })

	// The defaults: weekly, kept in the app, no email.
	set := owner.must(200, "GET", base+"/digest-settings", nil)
	if set["frequency"] != "weekly" || set["recipients"] != "none" || set["updated_at"] != nil || set["email_problem"] != nil ||
		set["me"].(map[string]any)["opted_out"] != false || set["me"].(map[string]any)["email"] != false {
		t.Fatalf("defaults = %v", set)
	}
	owner.must(400, "PUT", base+"/digest-settings", map[string]any{"recipients": "admins"})
	owner.must(400, "PUT", base+"/digest-settings", map[string]any{"timezone": "Mars/Base"})
	owner.must(400, "PUT", base+"/digest-settings", map[string]any{"frequency": "hourly"})

	// Nothing happened yet: the preview is empty, and a scheduled period
	// with nothing in it is skipped.
	if p := owner.must(200, "GET", base+"/digests/preview", nil); len(p["sections"].([]any)) != 0 {
		t.Fatalf("empty preview = %v", p)
	}
	now := time.Now().UTC()
	svc.Now = func() time.Time { return now }
	empty := digests.Payload{Frequency: "weekly", Trigger: "scheduled", Start: now.AddDate(0, 0, -7), End: now}
	if d, err := svc.Make(ctx, ws, empty); err != nil || d != nil {
		t.Fatalf("empty digest = %v %v", d, err)
	}

	// Something to say: an open error on a site.
	var site string
	if err := e.pool.QueryRow(ctx, `INSERT INTO seo_sites (workspace_id, host, start_url, verify_token) VALUES ($1, 'acme.example', 'https://acme.example/', 'tok')
		RETURNING id::text`, ws).Scan(&site); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO fixes (workspace_id, source, subject_type, subject_id, rule, severity, group_key, count, unit)
		VALUES ($1, 'seo_audit', 'seo_site', $2, 'title_missing', 'error', '/blog/*', 12, 'pages')`, ws, site); err != nil {
		t.Fatal(err)
	}
	p := owner.must(200, "GET", base+"/digests/preview?frequency=daily", nil)
	secs := p["sections"].([]any)
	if p["frequency"] != "daily" || len(secs) != 2 || secs[0].(map[string]any)["id"] != "attention" || secs[1].(map[string]any)["id"] != "fixes" {
		t.Fatalf("preview = %v", p)
	}
	fixes := secs[1].(map[string]any)
	if items := fixes["items"].([]any); len(items) != 1 || !strings.Contains(items[0].(map[string]any)["text"].(string), "acme.example/blog/* (12 pages)") ||
		items[0].(map[string]any)["tab"] != "fixes" {
		t.Fatalf("fixes = %v", fixes)
	}

	// A digest someone asked for, kept and sent as digest.ready.
	if out := owner.must(202, "POST", base+"/digests", map[string]any{"frequency": "weekly"}); out["queued"] != true {
		t.Fatalf("request = %v", out)
	}
	owner.must(400, "POST", base+"/digests", map[string]any{"frequency": "yearly"})
	var list []any
	for range 200 {
		if list = owner.must(200, "GET", base+"/digests", nil)["data"].([]any); len(list) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(list) != 1 {
		t.Fatal("the requested digest was not made")
	}
	d := list[0].(map[string]any)
	if d["trigger"] != "manual" || d["frequency"] != "weekly" || d["emailed"].(float64) != 0 || d["email_error"] != nil || len(d["sections"].([]any)) != 2 {
		t.Fatalf("digest = %v", d)
	}
	if one := owner.must(200, "GET", base+"/digests/"+d["id"].(string), nil); one["id"] != d["id"] {
		t.Fatalf("get = %v", one)
	}
	owner.must(404, "GET", base+"/digests/00000000-0000-0000-0000-000000000000", nil)
	var hooks int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'digest.ready' AND actor_type = 'human'`, ws).Scan(&hooks); err != nil || hooks != 1 {
		t.Fatalf("digest.ready = %d %v", hooks, err)
	}

	// Email, through the workspace's own provider and verified domain.
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "resend", "credentials": map[string]any{"api_key": "re_test_not_a_real_key"}})
	pid := owner.must(200, "GET", base+"/email-providers", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	owner.must(201, "POST", base+"/email-provider-domains", map[string]any{"domain": "acme.example"})
	// Only confirmed addresses get it.
	if _, err := e.pool.Exec(ctx, `UPDATE users SET email_verified_at = now()`); err != nil {
		t.Fatal(err)
	}
	owner.must(400, "PUT", base+"/digest-settings", map[string]any{"provider_id": "00000000-0000-0000-0000-000000000000"})
	set = owner.must(200, "PUT", base+"/digest-settings", map[string]any{"frequency": "daily", "hour": 9, "timezone": "Australia/Sydney",
		"recipients": "admins", "provider_id": pid, "from_address": "Acme <digest@acme.example>"})
	if set["from_address"] != "digest@acme.example" || set["recipient_count"].(float64) != 1 || set["email_problem"] == nil ||
		!strings.Contains(set["email_problem"].(string), "verified sending domain") || set["me"].(map[string]any)["email"] != false {
		t.Fatalf("settings with an unverified domain = %v", set)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE email_domains SET status = 'verified' WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	set = owner.must(200, "GET", base+"/digest-settings", nil)
	if set["email_problem"] != nil || set["me"].(map[string]any)["email"] != true {
		t.Fatalf("settings ready to email = %v", set)
	}
	daily := digests.Payload{Frequency: "daily", Trigger: "scheduled", Start: now.AddDate(0, 0, -1), End: now}
	dg, err := svc.Make(ctx, ws, daily)
	if err != nil || dg == nil || dg.Emailed != 1 {
		t.Fatalf("emailed digest = %+v %v", dg, err)
	}
	// The same period again is not made twice.
	if again, err := svc.Make(ctx, ws, daily); err != nil || again != nil {
		t.Fatalf("again = %v %v", again, err)
	}
	var subject, html, purpose, trigger string
	if err := e.pool.QueryRow(ctx, `SELECT subject, html, purpose, trigger_kind FROM emails WHERE workspace_id = $1 AND $2 = ANY(tags)`, ws, "digest").
		Scan(&subject, &html, &purpose, &trigger); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(subject, "Daily digest for Acme Outbound") || purpose != "transactional" || trigger != "system" ||
		!strings.Contains(html, "https://outflare.test/acme-outbound/fixes") || !strings.Contains(html, "/acme-outbound/digests") {
		t.Fatalf("email = %q %q %q\n%s", subject, purpose, trigger, html)
	}

	// Turning it off for yourself.
	if me := owner.must(200, "PUT", base+"/digest-settings/me", map[string]any{"email": false})["me"].(map[string]any); me["opted_out"] != true || me["email"] != false {
		t.Fatalf("opted out = %v", me)
	}
	if set := owner.must(200, "GET", base+"/digest-settings", nil); set["recipient_count"].(float64) != 0 {
		t.Fatalf("after opting out = %v", set)
	}
	weekly := digests.Payload{Frequency: "weekly", Trigger: "scheduled", Start: now.AddDate(0, 0, -7), End: now}
	if dg, err := svc.Make(ctx, ws, weekly); err != nil || dg.Emailed != 0 || dg.EmailError == nil || !strings.Contains(*dg.EmailError, "nobody to email") {
		t.Fatalf("nobody to email = %+v %v", dg, err)
	}
	owner.must(200, "PUT", base+"/digest-settings/me", map[string]any{"email": true})

	// The schedule: a daily digest at 09:00 Sydney time is due once, the
	// first tick after it.
	syd, _ := time.LoadLocation("Australia/Sydney")
	s := digests.Settings{Frequency: "daily", Hour: 9, Timezone: "Australia/Sydney"}
	at := time.Date(2026, 10, 27, 9, 30, 0, 0, syd)
	if p, ok := s.Due(at); !ok || !p.End.Equal(time.Date(2026, 10, 27, 9, 0, 0, 0, syd)) || !p.Start.Equal(time.Date(2026, 10, 26, 9, 0, 0, 0, syd)) {
		t.Fatalf("daily due = %v %v", p, ok)
	}
	w := digests.Settings{Frequency: "weekly", Weekday: 1, Hour: 8, Timezone: "UTC"}
	if p, ok := w.Due(time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)); !ok || !p.End.Equal(time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("weekly due = %v %v", p, ok)
	}
	if _, ok := (digests.Settings{Frequency: "off"}).Due(at); ok {
		t.Fatal("off is never due")
	}
	svc.Now = func() time.Time { return at }
	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var queued int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'digest.build' AND workspace_id = $1 AND idempotency_key LIKE '%:daily:%'`, ws).Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("scheduled = %d %v", queued, err)
	}
	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	// Brand-limited callers can't see the whole workspace's digest.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Reader", "scopes": []string{"workspace.read"}})["token"].(string)
	bot := &client{t: t, h: e.h, bearer: tok}
	if set := bot.must(200, "GET", base+"/digest-settings", nil); set["me"] == nil {
		t.Fatalf("token made by a person = %v", set)
	}
	bot.must(403, "PUT", base+"/digest-settings", map[string]any{"frequency": "off"})
	bot.must(403, "POST", base+"/digests", map[string]any{})

	owner.must(200, "PUT", base+"/digest-settings", map[string]any{"frequency": "off", "recipients": "none"})
	seen := map[string]bool{}
	for _, ev := range got() {
		seen[ev.Name] = true
	}
	for _, name := range []string{"digest_settings.updated", "digest.requested", "digest.ready", "digest.skipped", "digest.email_failed",
		"digest.email_turned_off", "digest.email_turned_on"} {
		if !seen[name] {
			t.Errorf("no %s event", name)
		}
	}
}
