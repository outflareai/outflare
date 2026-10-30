package api_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/journeys"
	"github.com/outflareai/outflare/engine/internal/track"
)

// A sync_audience step adds the person's hashed email to a Meta custom
// audience, or takes it out, following the ad consent rules for adding.
func TestJourneySyncAudience(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	metaAudience.Lock()
	metaAudience.calls = nil
	metaAudience.Unlock()

	// Without an ad platform that fills audiences the step can't be met.
	for _, k := range owner.must(200, "GET", base+"/journeys/step-kinds", nil)["data"].([]any) {
		if k := k.(map[string]any); k["kind"] == "sync_audience" && k["requires"].([]any)[0].(map[string]any)["connection"] != "ad_audiences" {
			t.Fatalf("kind: %v", k)
		}
	}
	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	if _, err := e.ads.SyncOnce(ctx, ws, i["id"].(string)); err != nil {
		t.Fatal(err)
	}
	acct := owner.must(200, "GET", base+"/ads/accounts", nil)["data"].([]any)[0].(map[string]any)["id"].(string)

	// The platform lists the customer list audiences only.
	auds := owner.must(200, "GET", base+"/ads/audiences?account_id="+acct, nil)["data"].([]any)
	if len(auds) != 1 || auds[0].(map[string]any)["id"] != "801" || auds[0].(map[string]any)["name"] != "Trial users" {
		t.Fatalf("audiences: %v", auds)
	}
	owner.must(400, "GET", base+"/ads/audiences?account_id=nope", nil)

	for _, bad := range []map[string]any{
		{"id": "a", "kind": "sync_audience", "audience_id": "801"},
		{"id": "a", "kind": "sync_audience", "ad_account_id": acct, "audience_id": "abc"},
		{"id": "a", "kind": "sync_audience", "ad_account_id": acct, "audience_id": "801", "change": "clear"},
	} {
		owner.must(400, "POST", base+"/journeys", map[string]any{"name": "Bad", "steps": []any{bad}})
	}

	j := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Trial", "trigger": map[string]any{"kind": "event", "event": "trial.started"},
		"steps": []any{
			map[string]any{"id": "add", "kind": "sync_audience", "ad_account_id": acct, "audience_id": "801", "next": "out"},
			map[string]any{"id": "out", "kind": "sync_audience", "ad_account_id": acct, "audience_id": "801", "change": "remove", "next": "bad"},
			map[string]any{"id": "bad", "kind": "sync_audience", "ad_account_id": acct, "audience_id": "802", "on_error": "continue"},
		}})
	jid := j["id"].(string)
	owner.must(200, "POST", base+"/journeys/"+jid+"/activate", nil)

	// Ana is in the US, Ben in Germany without consent, Cleo in Germany
	// with it, and Dan has no email or phone.
	person := func(email, country string) string {
		t.Helper()
		id, err := customers.EnsurePerson(ctx, e.pool, ws, "email", email, true, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.pool.Exec(ctx, `INSERT INTO site_sessions (workspace_id, anonymous_id, record_id, country, started_at, last_seen_at)
			VALUES ($1, $2, $3, $4, now(), now())`, ws, "anon-"+email, id, country); err != nil {
			t.Fatal(err)
		}
		return id
	}
	ana, ben, cleo := person("ana@acme.example", "US"), person("ben@northpeak.example", "DE"), person("cleo@northpeak.example", "DE")
	for _, purpose := range []string{"ad_user_data", "ad_personalization"} {
		owner.must(201, "POST", base+"/records/"+cleo+"/consents", map[string]any{"channel": "ads", "purpose": purpose, "status": "granted"})
	}
	dan := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Dan"})["id"].(string)

	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	(&journeys.Service{Pool: e.pool, Log: log, Fields: e.srv, Store: e.integrations, Box: e.box}).Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	defer func() { cancel(); <-done }()

	finished := func(record string) map[string]string {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for {
			var run map[string]any
			for _, r := range owner.must(200, "GET", base+"/journeys/"+jid+"/runs?record="+record, nil)["data"].([]any) {
				run = r.(map[string]any)
			}
			if run != nil && run["status"] != "active" {
				run = owner.must(200, "GET", base+"/journeys/"+jid+"/runs/"+run["id"].(string), nil)
				out := map[string]string{"status": run["status"].(string)}
				for _, h := range run["history"].([]any) {
					m := h.(map[string]any)
					d, _ := m["detail"].(string)
					out[m["step"].(string)] = m["outcome"].(string) + ": " + d
				}
				return out
			}
			if time.Now().After(deadline) {
				t.Fatalf("the run didn't finish: %v", run)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	for _, who := range []string{ana, ben, cleo, dan} {
		if _, err := customers.Record(ctx, e.pool, ws, customers.Event{RecordID: who, Name: "trial.started"}); err != nil {
			t.Fatal(err)
		}
	}
	out := finished(ana)
	if out["status"] != "completed" || out["add"] != "done: added to the audience in Meta Ads" || out["out"] != "done: removed from the audience in Meta Ads" ||
		!strings.HasPrefix(out["bad"], "failed: ") {
		t.Fatalf("ana: %v", out)
	}
	if out := finished(ben); out["add"] != "skipped: no consent for ad data (needed in the EEA, the UK and Switzerland)" || out["out"] != "done: removed from the audience in Meta Ads" {
		t.Fatalf("ben: %v", out)
	}
	if out := finished(cleo); out["add"] != "done: added to the audience in Meta Ads" {
		t.Fatalf("cleo: %v", out)
	}
	if out := finished(dan); out["add"] != "skipped: no email or phone number to match on" || out["out"] != "skipped: no email or phone number to match on" {
		t.Fatalf("dan: %v", out)
	}

	// Only hashes reached Meta: Ana and Cleo in, Ana, Ben and Cleo out.
	count := map[string]int{}
	metaAudience.Lock()
	for _, c := range metaAudience.calls {
		count[c]++
		if strings.Contains(c, "@") {
			t.Fatalf("plain text reached Meta: %s", c)
		}
	}
	metaAudience.Unlock()
	h := connectors.HashID
	if count["POST EMAIL_SHA256 "+h("ana@acme.example")] != 1 || count["POST EMAIL_SHA256 "+h("cleo@northpeak.example")] != 1 ||
		count["POST EMAIL_SHA256 "+h("ben@northpeak.example")] != 0 || count["DELETE EMAIL_SHA256 "+h("ben@northpeak.example")] != 1 || len(count) != 5 {
		t.Fatalf("audience calls: %v", count)
	}

	var audited int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'ads.audience_synced'`, ws).Scan(&audited); err != nil || audited != 5 {
		t.Fatalf("audited %d, %v", audited, err)
	}
	var granted bool
	if err := e.pool.QueryRow(ctx, `SELECT (data->>'consent_granted')::bool FROM audit_events WHERE workspace_id = $1 AND action = 'ads.audience_synced'
		AND target_id = $2 AND data->>'change' = 'add'`, ws, cleo).Scan(&granted); err != nil || !granted {
		t.Fatalf("cleo's consent: %v %v", granted, err)
	}
	if n := len(named(got(), "ads.audience_synced")); n != 5 {
		t.Fatalf("events: %d", n)
	}
}
