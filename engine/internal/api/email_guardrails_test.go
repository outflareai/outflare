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
	"github.com/outflareai/outflare/engine/internal/journeys"
	"github.com/outflareai/outflare/engine/internal/track"
)

// guardrailsOff turns off a workspace's marketing email guardrails, for
// tests that send a person several journey or broadcast emails at any
// hour.
func guardrailsOff(t *testing.T, owner *client, ws string) {
	t.Helper()
	owner.must(200, "PATCH", "/v1/workspaces/"+ws+"/email-guardrails", map[string]any{"cap_enabled": false, "quiet_hours_enabled": false})
}

// Marketing email is capped per person and held in quiet hours; both are
// on by default, changeable, and every capped send says why.
func TestEmailGuardrails(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	fake := &fakeResend{verified: true}
	rs := httptest.NewServer(fake)
	defer rs.Close()
	e.integrations.Client.BaseURLs["resend"] = rs.URL
	owner, ws := setupOwner(t, e.h)
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

	// The defaults: 1 a day, 3 a week, 8pm to 8am.
	g := owner.must(200, "GET", base+"/email-guardrails", nil)
	if g["cap_enabled"] != true || g["per_day"] != 1.0 || g["per_week"] != 3.0 || g["quiet_hours_enabled"] != true ||
		g["quiet_start"] != 20.0 || g["quiet_end"] != 8.0 || g["time_zone"] != "UTC" {
		t.Fatalf("defaults = %v", g)
	}
	owner.must(400, "PATCH", base+"/email-guardrails", map[string]any{"per_day": -1})
	owner.must(400, "PATCH", base+"/email-guardrails", map[string]any{"time_zone": "Mars/Olympus"})
	owner.must(400, "PATCH", base+"/email-guardrails", map[string]any{"quiet_start": 8})
	// Quiet hours off for now, so the cap is tested at any hour.
	owner.must(200, "PATCH", base+"/email-guardrails", map[string]any{"quiet_hours_enabled": false})

	for _, name := range []string{"One", "Two", "Three"} {
		owner.must(201, "POST", base+"/email-templates", map[string]any{"name": name, "purpose": "lifecycle",
			"subject": name, "html": "<p>" + name + "</p>", "from": "hello@example.com", "publish": true})
	}
	owner.must(201, "POST", base+"/email-templates", map[string]any{"name": "Receipt", "subject": "Receipt", "html": "<p>Paid</p>",
		"from": "billing@example.com", "publish": true})
	j := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Welcome", "steps": []map[string]any{
		{"id": "one", "kind": "email", "template": "one", "next": "two"},
		{"id": "two", "kind": "email", "template": "two", "next": "three"},
		{"id": "three", "kind": "email", "template": "three"},
	}})
	jid := j["id"].(string)
	owner.must(200, "POST", base+"/journeys/"+jid+"/activate", nil)

	ana, err := customers.EnsurePerson(ctx, e.pool, ws, "email", "ana@example.org", true, "")
	if err != nil {
		t.Fatal(err)
	}
	bo, err := customers.EnsurePerson(ctx, e.pool, ws, "email", "bo@example.org", true, "")
	if err != nil {
		t.Fatal(err)
	}
	// Bo is in Sydney.
	if _, err := e.pool.Exec(ctx, `UPDATE records SET attributes = '{"timezone": "Australia/Sydney"}' WHERE id = $1`, bo); err != nil {
		t.Fatal(err)
	}
	// Transactional email is never capped, and doesn't count.
	owner.must(202, "POST", base+"/emails", map[string]any{"to": "ana@example.org", "template": "receipt"})

	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	svc.Register(q)
	(&journeys.Service{Pool: e.pool, Emails: svc, Log: log}).Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	defer func() { cancel(); <-done }()
	finished := func(rec string) []any {
		t.Helper()
		run := owner.must(201, "POST", base+"/journeys/"+jid+"/runs", map[string]any{"record_id": rec})["id"].(string)
		deadline := time.Now().Add(60 * time.Second)
		for {
			r := owner.must(200, "GET", base+"/journeys/"+jid+"/runs/"+run, nil)
			if r["status"] == "completed" {
				return r["history"].([]any)
			}
			if time.Now().After(deadline) {
				t.Fatalf("run = %v", r)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	// Ana gets the first; the cap stops the other two, and says why.
	h := finished(ana)
	outcomes := []string{}
	for _, s := range h {
		outcomes = append(outcomes, s.(map[string]any)["outcome"].(string))
	}
	if strings.Join(outcomes, ",") != "sent,frequency_cap,frequency_cap" ||
		!strings.Contains(h[1].(map[string]any)["detail"].(string), "1 marketing email in the last day (limit 1)") || h[1].(map[string]any)["email_id"] != nil {
		t.Fatalf("history = %v", h)
	}
	stats := owner.must(200, "GET", base+"/journeys/"+jid, nil)["step_stats"].(map[string]any)
	if stats["two"].(map[string]any)["frequency_cap"] != 1.0 || stats["one"].(map[string]any)["sent"] != 1.0 {
		t.Fatalf("step_stats = %v", stats)
	}
	capped := 0
	for _, it := range owner.must(200, "GET", base+"/records/"+ana+"/timeline?kinds=event", nil)["data"].([]any) {
		it := it.(map[string]any)
		if it["title"] == "email.capped" {
			capped++
			if p := it["detail"].(map[string]any)["properties"].(map[string]any); p["reason"] != "frequency_cap" || p["journey_id"] != jid {
				t.Fatalf("capped = %v", it)
			}
		}
	}
	if capped != 2 {
		t.Fatalf("email.capped on the timeline = %d", capped)
	}
	events := 0
	for _, ev := range got() {
		if ev.Name == "email.capped" && ev.WorkspaceID == ws {
			events++
		}
	}
	if events != 2 {
		t.Fatalf("email.capped events = %d", events)
	}
	// A test run shows the cap, and records nothing.
	tr := owner.must(200, "POST", base+"/journeys/"+jid+"/test", map[string]any{"record_id": ana})
	if st := tr["steps"].([]any)[0].(map[string]any); st["outcome"] != "frequency_cap" {
		t.Fatalf("test = %v", tr)
	}
	var stored int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM customer_events WHERE record_id = $1 AND name = 'email.capped'`, ana).Scan(&stored); err != nil || stored != 2 {
		t.Fatalf("email.capped stored = %d, %v", stored, err)
	}

	// A weekly cap of 2 and no daily one; quiet hours cover now in Bo's
	// Sydney, so his first two are held until they end, and the third is
	// capped.
	syd, _ := time.LoadLocation("Australia/Sydney")
	hour := time.Now().In(syd).Hour()
	owner.must(200, "PATCH", base+"/email-guardrails", map[string]any{"per_day": 0, "per_week": 2, "quiet_hours_enabled": true,
		"quiet_start": hour, "quiet_end": (hour + 2) % 24})
	h = finished(bo)
	outcomes = outcomes[:0]
	for _, s := range h {
		outcomes = append(outcomes, s.(map[string]any)["outcome"].(string))
	}
	if strings.Join(outcomes, ",") != "quiet_hours,quiet_hours,frequency_cap" || !strings.Contains(h[0].(map[string]any)["detail"].(string), "held until") {
		t.Fatalf("history = %v", h)
	}
	em := owner.must(200, "GET", base+"/emails/"+h[0].(map[string]any)["email_id"].(string), nil)
	at, _ := time.Parse(time.RFC3339, em["send_at"].(string))
	if em["status"] != "scheduled" || at.In(syd).Hour() != (hour+2)%24 || at.Before(time.Now()) {
		t.Fatalf("held = %v", em)
	}

	// Off: everything goes.
	guardrailsOff(t, owner, ws)
	cy, err := customers.EnsurePerson(ctx, e.pool, ws, "email", "cy@example.org", true, "")
	if err != nil {
		t.Fatal(err)
	}
	h = finished(cy)
	for _, s := range h {
		if s.(map[string]any)["outcome"] != "sent" {
			t.Fatalf("history = %v", h)
		}
	}
}

func TestQuietUntil(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, ny)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, c := range []struct {
		now        string
		start, end int
		want       string
	}{
		{"2026-03-10 21:30", 20, 8, "2026-03-11 08:00"},
		{"2026-03-10 03:00", 20, 8, "2026-03-10 08:00"},
		{"2026-03-10 12:00", 20, 8, ""},
		{"2026-03-10 08:00", 20, 8, ""},
		{"2026-03-10 13:00", 12, 14, "2026-03-10 14:00"},
		{"2026-03-10 14:00", 12, 14, ""},
	} {
		got := emails.QuietUntil(at(c.now), ny, c.start, c.end)
		if (got == nil) != (c.want == "") || (got != nil && !got.Equal(at(c.want))) {
			t.Errorf("QuietUntil(%s, %d-%d) = %v, want %s", c.now, c.start, c.end, got, c.want)
		}
	}
}

// An email step can send over the cap, and skip rather than hold in quiet
// hours; a skip is recorded like a capped send.
func TestEmailStepGuardrailChoices(t *testing.T) {
	e := freshEnv(t)
	fake := &fakeResend{verified: true}
	rs := httptest.NewServer(fake)
	defer rs.Close()
	e.integrations.Client.BaseURLs["resend"] = rs.URL
	owner, ws := setupOwner(t, e.h)
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
	for _, name := range []string{"One", "Two", "Three"} {
		owner.must(201, "POST", base+"/email-templates", map[string]any{"name": name, "purpose": "lifecycle",
			"subject": name, "html": "<p>" + name + "</p>", "from": "hello@example.com", "publish": true})
	}
	owner.must(400, "POST", base+"/journeys", map[string]any{"name": "Bad", "steps": []map[string]any{
		{"id": "one", "kind": "email", "template": "one", "quiet_hours": "later"}}})
	// Two must go even over the cap; three is skipped in quiet hours.
	jid := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Trial", "steps": []map[string]any{
		{"id": "one", "kind": "email", "template": "one", "next": "two"},
		{"id": "two", "kind": "email", "template": "two", "ignore_cap": true, "next": "three"},
		{"id": "three", "kind": "email", "template": "three", "quiet_hours": "skip"},
	}})["id"].(string)
	owner.must(200, "POST", base+"/journeys/"+jid+"/activate", nil)

	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	svc.Register(q)
	(&journeys.Service{Pool: e.pool, Emails: svc, Log: log}).Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	defer func() { cancel(); <-done }()
	outcomes := func(addr string) []string {
		t.Helper()
		rec, err := customers.EnsurePerson(ctx, e.pool, ws, "email", addr, true, "")
		if err != nil {
			t.Fatal(err)
		}
		run := owner.must(201, "POST", base+"/journeys/"+jid+"/runs", map[string]any{"record_id": rec})["id"].(string)
		deadline := time.Now().Add(60 * time.Second)
		for {
			r := owner.must(200, "GET", base+"/journeys/"+jid+"/runs/"+run, nil)
			if r["status"] == "completed" {
				out := []string{}
				for _, s := range r["history"].([]any) {
					out = append(out, s.(map[string]any)["outcome"].(string))
				}
				return out
			}
			if time.Now().After(deadline) {
				t.Fatalf("run = %v", r)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	// The cap at 1 a day, no quiet hours: two goes over the cap, three doesn't.
	owner.must(200, "PATCH", base+"/email-guardrails", map[string]any{"quiet_hours_enabled": false})
	if got := strings.Join(outcomes("ana@example.org"), ","); got != "sent,sent,frequency_cap" {
		t.Fatalf("ana = %s", got)
	}

	// Quiet hours now, no cap: one and two wait for morning, three is skipped.
	hour := time.Now().UTC().Hour()
	owner.must(200, "PATCH", base+"/email-guardrails", map[string]any{"cap_enabled": false, "quiet_hours_enabled": true,
		"quiet_start": hour, "quiet_end": (hour + 2) % 24, "time_zone": "UTC"})
	if got := strings.Join(outcomes("bo@example.org"), ","); got != "quiet_hours,quiet_hours,quiet_hours_skipped" {
		t.Fatalf("bo = %s", got)
	}
	var reason string
	if err := e.pool.QueryRow(ctx, `SELECT properties->>'reason' FROM customer_events WHERE workspace_id = $1 AND name = 'email.capped'
		ORDER BY occurred_at DESC LIMIT 1`, ws).Scan(&reason); err != nil || reason != "quiet_hours_skipped" {
		t.Fatalf("email.capped reason = %q, %v", reason, err)
	}
}
