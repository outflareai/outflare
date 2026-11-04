package api_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/missions"
)

// inMission sends a request as c inside a mission.
func (c *client) inMission(mission, method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.bearer)
	req.Header.Set("Outflare-Mission", mission)
	req.Header.Set("Outflare-Reason", "testing")
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func agentToken(t *testing.T, owner *client, base, name string, scopes ...string) *client {
	t.Helper()
	out := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": name, "scopes": scopes})
	return &client{t: t, h: owner.h, bearer: out["token"].(string)}
}

// seedMission makes a mission as one made before missions retired, with
// the given status, mode and goal, and returns its id.
func seedMission(t *testing.T, e *env, ws, name, status, mode string, metric *string, target *int, maxSends, maxSpend *int) string {
	t.Helper()
	var id string
	if err := e.pool.QueryRow(t.Context(), `INSERT INTO missions (workspace_id, name, goal, goal_metric, goal_target, status, mode, max_sends, max_spend_cents, started_at)
		VALUES ($1, $2, 'A goal', $3, $4, $5, $6, $7, $8, CASE WHEN $5 = 'draft' THEN NULL ELSE now() END) RETURNING id::text`,
		ws, name, metric, target, status, mode, maxSends, maxSpend).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func ptr[T any](v T) *T { return &v }

// Missions are retired: what they did stays readable, every change to
// one answers 410 mission_retired, and so does a change made with the
// Outflare-Mission header, rather than running without the mission's
// limits and approvals.
func TestMissionsRetired(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := t.Context()
	agent := agentToken(t, owner, base, "Agent", "missions.read", "missions.run", "sequences.read", "sequences.manage")
	id := seedMission(t, e, ws, "Fintech CFOs", "running", "approve_writes", ptr("meetings"), ptr(10), nil, nil)
	var tokenAction string
	if err := e.pool.QueryRow(ctx, `INSERT INTO mission_actions (mission_id, workspace_id, method, path, body, status)
		VALUES ($1, $2, 'POST', $3, '{}', 'pending') RETURNING id::text`, id, ws, base+"/sequences").Scan(&tokenAction); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO mission_updates (mission_id, workspace_id, author, kind, body) VALUES ($1, $2, 'agent', 'progress', 'Sequence drafted')`, id, ws); err != nil {
		t.Fatal(err)
	}

	// History stays readable.
	if l := owner.must(200, "GET", base+"/missions", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("list: %v", l)
	}
	m := owner.must(200, "GET", base+"/missions/"+id, nil)["mission"].(map[string]any)
	if m["name"] != "Fintech CFOs" || m["pending_actions"] != 1.0 {
		t.Fatalf("mission: %v", m)
	}
	owner.must(200, "GET", base+"/missions/"+id+"/calls", nil)
	if l := owner.must(200, "GET", base+"/missions/"+id+"/actions", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("actions: %v", l)
	}
	owner.must(200, "GET", base+"/missions/"+id+"/actions/"+tokenAction, nil)
	if l := owner.must(200, "GET", base+"/missions/"+id+"/updates", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("updates: %v", l)
	}
	owner.must(200, "GET", base+"/mission-actions", nil)

	// Every change to a mission is retired.
	for _, c := range []struct{ method, path string }{
		{"POST", "/missions"}, {"POST", "/missions/plan"}, {"PATCH", "/missions/" + id},
		{"POST", "/missions/" + id + "/submit"}, {"POST", "/missions/" + id + "/approve"}, {"POST", "/missions/" + id + "/pause"},
		{"POST", "/missions/" + id + "/resume"}, {"POST", "/missions/" + id + "/stop"}, {"POST", "/missions/" + id + "/complete"},
		{"POST", "/missions/" + id + "/actions/" + tokenAction + "/approve"}, {"POST", "/missions/" + id + "/actions/" + tokenAction + "/reject"},
		{"POST", "/missions/" + id + "/updates"}, {"POST", "/missions/" + id + "/updates/1/resolve"},
	} {
		code, out := owner.do(c.method, base+c.path, map[string]any{"name": "x"})
		if code != 410 || out["error"].(map[string]any)["code"] != "mission_retired" {
			t.Errorf("%s %s = %d %v", c.method, c.path, code, out)
		}
	}

	// A change made inside a mission is refused; a read still runs.
	code, out := agent.inMission(id, "POST", base+"/sequences", map[string]any{"name": "In mission", "steps": []map[string]any{{"subject": "Hi", "body": "Hello"}}})
	if code != 410 || out["error"].(map[string]any)["code"] != "mission_retired" {
		t.Fatalf("a change in a mission = %d %v", code, out)
	}
	if code, _ := agent.inMission(id, "GET", base+"/sequences", nil); code != 200 {
		t.Fatalf("a read in a mission = %d", code)
	}
	if l := owner.must(200, "GET", base+"/sequences", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("sequences: %v", l)
	}

	// Another workspace sees none of it.
	ws2 := owner.must(201, "POST", "/v1/workspaces", map[string]string{"name": "Second"})["id"].(string)
	owner.must(404, "GET", "/v1/workspaces/"+ws2+"/missions/"+id, nil)
}

func TestMissionSpendAndMeetings(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := t.Context()
	id := seedMission(t, e, ws, "Spend", "draft", "dry_run", ptr("meetings"), ptr(1), nil, ptr(1000))
	if err := missions.CheckSpend(ctx, e.pool, id, 1); !errors.Is(err, missions.ErrNotRunning) {
		t.Fatalf("draft spend: %v", err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE missions SET status = 'running', mode = 'autonomous', started_at = now() WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO spend_entries (workspace_id, mission_id, category, amount_cents) VALUES ($1, $2, 'verification', 600)`, ws, id); err != nil {
		t.Fatal(err)
	}
	if err := missions.CheckSpend(ctx, e.pool, id, 500); !errors.Is(err, missions.ErrSpendLimit) {
		t.Fatalf("over limit: %v", err)
	}
	if err := missions.CheckSpend(ctx, e.pool, id, 400); err != nil {
		t.Fatalf("within limit: %v", err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO meetings (workspace_id, mission_id, source, starts_at, attribution) VALUES ($1, $2, 'manual', now(), 'manual')`, ws, id); err != nil {
		t.Fatal(err)
	}
	got := owner.must(200, "GET", base+"/missions/"+id, nil)["mission"].(map[string]any)
	if p := got["progress"].(map[string]any); p["spend_cents"] != 600.0 || p["meetings"] != 1.0 {
		t.Fatalf("progress: %v", p)
	}
	if g := got["goal"].(map[string]any); g["current"] != 1.0 || g["reached"] != true {
		t.Fatalf("goal: %v", g)
	}
}

// Revenue goals count in whole US dollars: payments credited to the
// campaigns of the mission's sequences, broadcasts and social posts under
// the default model, or won deals tagged with the mission.
func TestMissionRevenueGoals(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := t.Context()
	attributed := seedMission(t, e, ws, "Pipeline", "running", "autonomous", ptr("revenue_attributed"), ptr(40), nil, nil)
	deals := seedMission(t, e, ws, "Deals", "running", "autonomous", ptr("revenue_deals"), ptr(100), nil, nil)

	exec := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	seq := exec(`INSERT INTO sequences (workspace_id, name, mission_id) VALUES ($1, 'Outbound', $2) RETURNING id::text`, ws, attributed)
	other := exec(`INSERT INTO sequences (workspace_id, name) VALUES ($1, 'Other') RETURNING id::text`, ws)
	camp := exec(`INSERT INTO tracking_campaigns (workspace_id, name, slug, code, channel, source, medium, object_kind, object_id)
		VALUES ($1, 'Outbound', 'outbound', 'code000001', 'outbound_email', 'outflare', 'email', 'sequence', $2) RETURNING id::text`, ws, seq)
	otherCamp := exec(`INSERT INTO tracking_campaigns (workspace_id, name, slug, code, channel, source, medium, object_kind, object_id)
		VALUES ($1, 'Other', 'other', 'code000002', 'outbound_email', 'outflare', 'email', 'sequence', $2) RETURNING id::text`, ws, other)
	// A payment credited to the mission's campaign under the default model
	// counts; one under another model, one in euros and one credited to
	// another sequence don't.
	pay := func(minor int, currency, campaign, model string) {
		t.Helper()
		ev := exec(`INSERT INTO customer_events (workspace_id, kind, name, occurred_at) VALUES ($1, 'track', 'payment.succeeded', now()) RETURNING id::text`, ws)
		exec(`INSERT INTO attribution_conversions (event_id, workspace_id, name, occurred_at, value_minor, currency) VALUES ($1, $2, 'payment.succeeded', now(), $3, $4) RETURNING event_id::text`,
			ev, ws, minor, currency)
		exec(`INSERT INTO attribution_credits (event_id, workspace_id, model, position, channel, tracking_campaign_id, weight)
			VALUES ($1, $2, $3, 0, 'outbound_email', $4, 1) RETURNING event_id::text`, ev, ws, model, campaign)
	}
	pay(4950, "USD", camp, "last_non_direct")
	// A broadcast made in the mission counts the same way; a social post
	// and a reply count by their own content tag, since their campaign is
	// the account's or the watch's, shared with posts outside the mission.
	tmpl := exec(`INSERT INTO email_templates (workspace_id, handle, name) VALUES ($1, 'launch', 'Launch') RETURNING id::text`, ws)
	bc := exec(`INSERT INTO email_broadcasts (workspace_id, name, template_id, mission_id) VALUES ($1, 'Launch', $2, $3) RETURNING id::text`, ws, tmpl, attributed)
	bcCamp := exec(`INSERT INTO tracking_campaigns (workspace_id, name, slug, code, channel, source, medium, object_kind, object_id)
		VALUES ($1, 'Launch', 'launch', 'code000003', 'lifecycle_email', 'outflare', 'email', 'email_broadcast', $2) RETURNING id::text`, ws, bc)
	pay(1000, "USD", bcCamp, "last_non_direct")
	acct := exec(`INSERT INTO social_accounts (workspace_id, channel, kind, external_id, name) VALUES ($1, 'linkedin', 'page', 'urn-1', 'Northpeak') RETURNING id::text`, ws)
	exec(`INSERT INTO social_publications (workspace_id, account_id, channel, utm_content, mission_id) VALUES ($1, $2, 'linkedin', 'linkedin-mission', $3) RETURNING id::text`, ws, acct, attributed)
	exec(`INSERT INTO social_actions (workspace_id, channel, type, utm_content, mission_id) VALUES ($1, 'reddit', 'reply', 'reddit-mission', $2) RETURNING id::text`, ws, attributed)
	acctCamp := exec(`INSERT INTO tracking_campaigns (workspace_id, name, slug, code, channel, source, medium, object_kind, object_id)
		VALUES ($1, 'Northpeak', 'northpeak', 'code000004', 'organic_social', 'linkedin', 'social', 'social_account', $2) RETURNING id::text`, ws, acct)
	watchCamp := exec(`INSERT INTO tracking_campaigns (workspace_id, name, slug, code, channel, source, medium, object_kind, object_id)
		VALUES ($1, 'Watch', 'watch', 'code000005', 'reddit', 'reddit', 'social', 'social_watch', gen_random_uuid()::text) RETURNING id::text`, ws)
	payVisit := func(minor int, campaign, content string) {
		t.Helper()
		ss := exec(`INSERT INTO site_sessions (workspace_id, anonymous_id, started_at, channel, source, utm_content, tracking_campaign_id)
			VALUES ($1, 'anon', now(), 'organic_social', 'linkedin', $2, $3) RETURNING id::text`, ws, content, campaign)
		ev := exec(`INSERT INTO customer_events (workspace_id, kind, name, occurred_at) VALUES ($1, 'track', 'payment.succeeded', now()) RETURNING id::text`, ws)
		exec(`INSERT INTO attribution_conversions (event_id, workspace_id, name, occurred_at, value_minor, currency) VALUES ($1, $2, 'payment.succeeded', now(), $3, 'USD') RETURNING event_id::text`,
			ev, ws, minor)
		exec(`INSERT INTO attribution_credits (event_id, workspace_id, model, position, session_id, channel, tracking_campaign_id, weight)
			VALUES ($1, $2, 'last_non_direct', 0, $3, 'organic_social', $4, 1) RETURNING event_id::text`, ev, ws, ss, campaign)
	}
	payVisit(700, acctCamp, "linkedin-mission")
	payVisit(300, watchCamp, "reddit-mission")
	payVisit(10000, acctCamp, "linkedin-other")
	pay(10000, "USD", camp, "first_touch")
	pay(10000, "EUR", camp, "last_non_direct")
	pay(10000, "USD", otherCamp, "last_non_direct")
	exec(`INSERT INTO deals (workspace_id, mission_id, source, state, amount_cents, currency, attribution) VALUES ($1, $2, 'manual', 'won', 12000, 'USD', 'manual') RETURNING id::text`, ws, deals)
	exec(`INSERT INTO deals (workspace_id, mission_id, source, state, amount_cents, currency, attribution) VALUES ($1, $2, 'manual', 'open', 50000, 'USD', 'manual') RETURNING id::text`, ws, deals)

	got := owner.must(200, "GET", base+"/missions/"+attributed, nil)["mission"].(map[string]any)
	if p := got["progress"].(map[string]any); p["revenue_attributed_cents"] != 6950.0 || p["revenue_deals_cents"] != 0.0 {
		t.Fatalf("attributed progress: %v", p)
	}
	if g := got["goal"].(map[string]any); g["current"] != 69.0 || g["reached"] != true {
		t.Fatalf("attributed goal: %v", g)
	}
	got = owner.must(200, "GET", base+"/missions/"+deals, nil)["mission"].(map[string]any)
	if p := got["progress"].(map[string]any); p["revenue_deals_cents"] != 12000.0 || p["revenue_attributed_cents"] != 0.0 {
		t.Fatalf("deals progress: %v", p)
	}
	if g := got["goal"].(map[string]any); g["current"] != 120.0 || g["reached"] != true {
		t.Fatalf("deals goal: %v", g)
	}
}

func jsonInt(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestMissionSendLimitAndPause(t *testing.T) {
	e := freshEnv(t)
	clock := &fakeClock{}
	clock.Set(time.Date(2030, 1, 8, 10, 0, 0, 0, time.UTC))
	owner, base, srv := sendingSetup(t, e, clock)
	// A sequence made in a mission before missions retired keeps its
	// mission's send limit and pause.
	ws := strings.TrimPrefix(base, "/v1/workspaces/")
	id := seedMission(t, e, ws, "Capped", "running", "autonomous", nil, nil, ptr(1), nil)
	code, imp := owner.raw("POST", base+"/leads/import?list=M", "text/csv", "email,first_name\nli@one-io.example,Li\nmo@two-io.example,Mo\n")
	if code != 200 {
		t.Fatalf("import: %d %v", code, imp)
	}
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{
		"name": "In mission", "target_daily": 1000, "steps": []map[string]any{{"subject": "Hi {{first_name}}", "body": "Hello"}},
	})
	seqID := seq["id"].(string)
	if _, err := e.pool.Exec(t.Context(), `UPDATE sequences SET mission_id = $1 WHERE id = $2`, id, seqID); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "POST", base+"/sequences/"+seqID+"/enrollments", map[string]any{"list_id": imp["list_id"]})
	owner.must(200, "PATCH", base+"/sequences/"+seqID, map[string]any{"status": "active"})

	// Two mailboxes could send both, but the mission allows one send.
	e.pump(t)
	clock.Add(10 * time.Minute)
	e.pump(t)
	if n := len(srv.Messages()); n != 1 {
		t.Fatalf("sent %d, want 1", n)
	}
	reasons := map[string]bool{}
	for _, en := range enrollments(t, owner, base, seqID) {
		reasons[fmt.Sprint(en["status_reason"])] = true
	}
	if !reasons["mission_send_limit"] {
		t.Fatalf("reasons: %v", reasons)
	}
	got := owner.must(200, "GET", base+"/missions/"+id, nil)["mission"].(map[string]any)
	if got["progress"].(map[string]any)["sends"] != 1.0 {
		t.Fatalf("progress: %v", got["progress"])
	}

	// Raising the limit and pausing: still nothing goes while paused.
	if _, err := e.pool.Exec(t.Context(), `UPDATE missions SET max_sends = 10, status = 'paused' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	clock.Add(2 * time.Hour)
	e.pump(t)
	if n := len(srv.Messages()); n != 1 {
		t.Fatalf("sent while paused: %d", n)
	}
	if _, err := e.pool.Exec(t.Context(), `UPDATE missions SET status = 'running' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	clock.Add(2 * time.Hour)
	e.pump(t)
	if n := len(srv.Messages()); n != 2 {
		t.Fatalf("after resume: %d", n)
	}
}
