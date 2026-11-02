package api_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/holdout"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// A broadcast with a holdout sends to everyone but the people held out,
// and its lift compares the two sides.
func TestEmailBroadcastHoldout(t *testing.T) {
	e := freshEnv(t)
	fake := &fakeResend{verified: true}
	rs := httptest.NewServer(fake)
	defer rs.Close()
	e.integrations.Client.BaseURLs["resend"] = rs.URL
	owner, ws := setupOwner(t, e.h)
	guardrailsOff(t, owner, ws)
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
	owner.must(201, "POST", base+"/email-templates", map[string]any{"name": "Launch", "purpose": "lifecycle",
		"subject": "New", "html": "<p>Hi</p>", "from": "news@example.com", "publish": true})
	ids := []string{}
	for i := range 60 {
		id, err := customers.EnsurePerson(ctx, e.pool, ws, "email", fmt.Sprintf("p%d@example.org", i), true, "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	audience := map[string]any{"kind": "records", "record_ids": ids}
	owner.must(400, "POST", base+"/email-broadcasts", map[string]any{"name": "Too many", "template": "launch", "audience": audience, "holdout_percent": 51})
	b := owner.must(201, "POST", base+"/email-broadcasts", map[string]any{"name": "Launch", "template": "launch", "audience": audience, "holdout_percent": 50})
	id := b["id"].(string)
	if b["holdout_percent"] != 50.0 || b["held_out"] != 0.0 {
		t.Fatalf("broadcast = %v", b)
	}
	owner.must(200, "PATCH", base+"/email-broadcasts/"+id, map[string]any{"holdout_percent": 40})
	owner.must(200, "POST", base+"/email-broadcasts/"+id+"/send", nil)
	owner.must(409, "PATCH", base+"/email-broadcasts/"+id, map[string]any{"holdout_percent": 10})

	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 20 * time.Millisecond
	svc.Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	defer func() { cancel(); <-done }()
	for i := 0; ; i++ {
		b = owner.must(200, "GET", base+"/email-broadcasts/"+id, nil)
		if b["status"] == "sent" {
			break
		}
		if i > 400 {
			t.Fatalf("broadcast = %v", b)
		}
		time.Sleep(25 * time.Millisecond)
	}
	held, sent := []string{}, []string{}
	for _, rec := range ids {
		if holdout.In(id, rec, 40) {
			held = append(held, rec)
		} else {
			sent = append(sent, rec)
		}
	}
	if b["held_out"] != float64(len(held)) || b["recipients"] != float64(len(sent)) || b["skipped"] != 0.0 || len(held) == 0 {
		t.Fatalf("sent = %v, want %d held out", b, len(held))
	}
	var withEmail int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM email_broadcast_recipients WHERE broadcast_id = $1 AND skipped = 'holdout' AND email_id IS NOT NULL`, id).Scan(&withEmail); err != nil || withEmail != 0 {
		t.Fatalf("held-out people with an email: %d %v", withEmail, err)
	}

	// Two of the people it reached paid, one held-out person did.
	pay := func(rec string, cents int) {
		t.Helper()
		var ev string
		at := time.Now().Add(time.Hour)
		if err := e.pool.QueryRow(ctx, `INSERT INTO customer_events (workspace_id, record_id, kind, name, occurred_at) VALUES ($1, $2, 'track', 'payment.succeeded', $3) RETURNING id::text`, ws, rec, at).Scan(&ev); err != nil {
			t.Fatal(err)
		}
		if _, err := e.pool.Exec(ctx, `INSERT INTO attribution_conversions (event_id, workspace_id, record_id, name, occurred_at, value_minor, currency, touches, matched_by)
			VALUES ($1, $2, $3, 'payment.succeeded', $4, $5, 'USD', 0, 'record')`, ev, ws, rec, at, cents); err != nil {
			t.Fatal(err)
		}
	}
	pay(sent[0], 1000)
	pay(sent[1], 1000)
	pay(held[0], 1000)
	brand := b["brand_id"].(string)
	lift := owner.must(200, "GET", "/v1/brands/"+brand+"/email-broadcasts/"+id+"/lift", nil)
	paths := lift["paths"].([]any)
	s, h := paths[0].(map[string]any), paths[1].(map[string]any)
	if lift["control"] != "held_out" || lift["holdout_percent"] != 40.0 || s["id"] != "sent" || h["id"] != "held_out" || !h["holdout"].(bool) ||
		s["people"] != float64(len(sent)) || s["converted"] != 2.0 || h["people"] != float64(len(held)) || h["converted"] != 1.0 ||
		s["verdict"] != "too_early" || h["verdict"] != nil {
		t.Fatalf("lift = %v", lift)
	}
	owner.must(400, "GET", "/v1/brands/"+brand+"/email-broadcasts/"+id+"/lift?days=0", nil)
	owner.must(404, "GET", "/v1/brands/"+brand+"/email-broadcasts/00000000-0000-0000-0000-000000000000/lift", nil)
}

// A sequence with a holdout stops the held-out enrollments before their
// first email, and its lift compares them with the leads it emailed.
func TestSequenceHoldout(t *testing.T) {
	e := freshEnv(t)
	clock := &fakeClock{t: time.Now()}
	if u := clock.Now().UTC(); u.Hour() == 23 && u.Minute() >= 30 {
		clock.Add(time.Hour)
	}
	owner, base, srv := sendingSetup(t, e, clock)
	ctx := context.Background()
	csv := "email\n"
	for i := range 20 {
		csv += fmt.Sprintf("p%d@one-io.example\n", i)
	}
	_, imp := owner.raw("POST", base+"/leads/import?list=L", "text/csv", csv)
	owner.must(400, "POST", base+"/sequences", map[string]any{"name": "Bad", "holdout_percent": 60})
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{
		"name": "Holdout", "daily_new_limit": 100, "send_days": []int{1, 2, 3, 4, 5, 6, 7}, "send_start": "00:00", "send_end": "23:59",
		"holdout_percent": 30, "steps": []map[string]any{{"subject": "Hi", "body": "Hello {{unsubscribe_url}}"}},
	})
	if seq["holdout_percent"] != 30.0 {
		t.Fatalf("sequence = %v", seq)
	}
	seqID := seq["id"].(string)
	owner.must(200, "POST", base+"/sequences/"+seqID+"/enrollments", map[string]any{"list_id": imp["list_id"]})
	owner.must(200, "PATCH", base+"/sequences/"+seqID, map[string]any{"status": "active"})
	for range 8 {
		e.pump(t)
		clock.Add(5 * time.Minute)
	}
	held := 0
	rows, err := e.pool.Query(ctx, `SELECT lead_id::text, status, coalesce(status_reason, '') FROM enrollments WHERE sequence_id = $1`, seqID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var lead, status, reason string
		if err := rows.Scan(&lead, &status, &reason); err != nil {
			t.Fatal(err)
		}
		want := holdout.In(seqID, lead, 30)
		if want != (status == "stopped" && reason == "holdout") {
			t.Fatalf("lead %s: %s %s, held out %v", lead, status, reason, want)
		}
		if want {
			held++
		}
	}
	rows.Close()
	sent := len(srv.Messages())
	if held == 0 || sent == 0 || sent > 20-held {
		t.Fatalf("held out %d, sent %d", held, sent)
	}
	var leaked int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM messages m JOIN enrollments en ON en.id = m.enrollment_id
		WHERE en.sequence_id = $1 AND en.status_reason = 'holdout'`, seqID).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("held-out leads with a message: %d %v", leaked, err)
	}

	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT brand_id::text FROM sequences WHERE id = $1`, seqID).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	lift := owner.must(200, "GET", "/v1/brands/"+brand+"/sequences/"+seqID+"/lift?event=payment.succeeded", nil)
	paths := lift["paths"].([]any)
	s, h := paths[0].(map[string]any), paths[1].(map[string]any)
	if s["people"] != float64(sent) || h["people"] != float64(held) || s["converted"] != 0.0 || s["verdict"] != "too_early" || lift["holdout_percent"] != 30.0 {
		t.Fatalf("lift = %v", lift)
	}
}
