package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/journeys"
)

// The transactional email, consent, owner and deal steps.
func TestJourneyCRMActions(t *testing.T) {
	e := freshEnv(t)
	fake := &fakeResend{verified: true}
	rs := httptest.NewServer(fake)
	defer rs.Close()
	e.integrations.Client.BaseURLs["resend"] = rs.URL
	owner, ws := setupOwner(t, e.h)
	guardrailsOffIfAny(t, owner, ws)
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
	owner.must(201, "POST", base+"/email-templates", map[string]any{"name": "Welcome pack", "subject": "Your account", "html": "<p>Hi {{person.first_name}}</p>",
		"from": "hello@example.com", "publish": true})
	owner.must(201, "POST", base+"/email-templates", map[string]any{"name": "News", "purpose": "lifecycle", "subject": "News", "html": "<p>News</p>",
		"from": "hello@example.com", "publish": true})
	owner.must(201, "POST", base+"/email-topics", map[string]any{"name": "Product updates"})

	// A second member, to spread people between.
	me := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	acc := &api.Accounts{Pool: e.pool, PublicURL: "https://app.example.com", Mailer: e.outbox, Log: log}
	bea, _, err := acc.CreateAccount(ctx, nil, httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil),
		api.NewAccount{Email: "bea@example.com", Name: "Bea", Password: "correct horse battery", WorkspaceName: "Bea's"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO memberships (workspace_id, user_id, role) VALUES ($1, $2, 'operator')`, ws, bea); err != nil {
		t.Fatal(err)
	}

	// Bad steps are refused.
	deal := map[string]any{"type": "deal", "field": "stage", "stage": "qualified", "closed": []string{"won", "lost"}}
	for _, st := range []map[string]any{
		{"id": "a", "kind": "transactional_email", "template": "news"},
		{"id": "a", "kind": "set_consent", "consent": "maybe"},
		{"id": "a", "kind": "assign_owner", "members": []string{me}},
		{"id": "a", "kind": "assign_owner", "owner_id": "00000000-0000-0000-0000-000000000000"},
		{"id": "a", "kind": "move_deal", "deal": map[string]any{"type": "deal", "field": "stage", "stage": "signed"}},
	} {
		owner.must(400, "POST", base+"/journeys", map[string]any{"name": "Bad", "steps": []map[string]any{st}})
	}
	jid := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Onboard", "steps": []map[string]any{
		{"id": "pack", "kind": "transactional_email", "template": "welcome-pack", "next": "opt"},
		{"id": "opt", "kind": "set_consent", "consent": "granted", "topic": "product_updates", "next": "own"},
		{"id": "own", "kind": "assign_owner", "members": []string{me, bea}, "next": "deal"},
		{"id": "deal", "kind": "move_deal", "deal": deal},
	}})["id"].(string)
	owner.must(200, "POST", base+"/journeys/"+jid+"/activate", nil)

	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	svc.Register(q)
	(&journeys.Service{Pool: e.pool, Emails: svc, Log: log, Fields: e.srv, Deals: e.srv}).Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	defer func() { cancel(); <-done }()
	finished := func(addr string) (string, map[string]any) {
		t.Helper()
		rec, err := customers.EnsurePerson(ctx, e.pool, ws, "email", addr, true, "")
		if err != nil {
			t.Fatal(err)
		}
		// A person who said no to marketing still gets the transactional email.
		if err := customers.RecordConsent(ctx, e.pool, ws, customers.ConsentChange{RecordID: rec, Channel: "email", Status: customers.Withdrawn}); err != nil {
			t.Fatal(err)
		}
		run := owner.must(201, "POST", base+"/journeys/"+jid+"/runs", map[string]any{"record_id": rec})["id"].(string)
		deadline := time.Now().Add(60 * time.Second)
		for {
			r := owner.must(200, "GET", base+"/journeys/"+jid+"/runs/"+run, nil)
			if r["status"] != "active" {
				return rec, r
			}
			if time.Now().After(deadline) {
				t.Fatalf("run = %v", r)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	outcomes := func(r map[string]any) string {
		out := []string{}
		for _, s := range r["history"].([]any) {
			s := s.(map[string]any)
			out = append(out, s["step"].(string)+":"+s["outcome"].(string))
		}
		return strings.Join(out, ",")
	}
	ownerOf := func(rec string) string {
		var o string
		if err := e.pool.QueryRow(ctx, `SELECT COALESCE(attributes->>'owner', '') FROM records WHERE id = $1`, rec).Scan(&o); err != nil {
			t.Fatal(err)
		}
		return o
	}

	ana, r := finished("ana@example.org")
	if r["status"] != "completed" || outcomes(r) != "pack:done,opt:done,own:done,deal:done" {
		t.Fatalf("ana = %v", r)
	}
	var sent int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM emails WHERE workspace_id = $1 AND purpose = 'transactional' AND trigger_kind = 'journey'`, ws).Scan(&sent); err != nil || sent != 1 {
		t.Fatalf("transactional emails = %d, %v", sent, err)
	}
	if status, _, err := customers.Consent(ctx, e.pool, ws, ana, "email", "product_updates", "marketing"); err != nil || status != customers.Granted {
		t.Fatalf("consent = %q, %v", status, err)
	}
	var deals int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM records r JOIN record_relationships rr ON rr.from_id = r.id AND rr.to_id = $2
		WHERE r.workspace_id = $1 AND r.record_type = 'deal' AND r.attributes->>'stage' = 'qualified'`, ws, ana).Scan(&deals); err != nil || deals != 1 {
		t.Fatalf("deals = %d, %v", deals, err)
	}
	// The next person goes to the other member.
	bo, _ := finished("bo@example.org")
	if a, b := ownerOf(ana), ownerOf(bo); a == "" || b == "" || a == b {
		t.Fatalf("owners = %q, %q", a, b)
	}
}

// guardrailsOffIfAny turns off marketing email guardrails where the
// server has them.
func guardrailsOffIfAny(t *testing.T, owner *client, ws string) {
	t.Helper()
	if code, _ := owner.do("PATCH", "/v1/workspaces/"+ws+"/email-guardrails", map[string]any{"cap_enabled": false, "quiet_hours_enabled": false}); code != 200 && code != 404 && code != 405 {
		t.Fatalf("guardrails off: %d", code)
	}
}
