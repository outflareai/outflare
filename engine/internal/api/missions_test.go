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

func TestMissionLifecycle(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	agent := agentToken(t, owner, base, "Agent", "missions.read", "missions.run", "sequences.read", "sequences.manage", "audit.read")
	other := agentToken(t, owner, base, "Other agent", "missions.read", "missions.run", "sequences.read", "sequences.manage")

	// The agent creates a mission; it is bound to the agent's token and starts as a draft.
	m := agent.must(201, "POST", base+"/missions", map[string]any{
		"name": "Fintech CFOs", "goal": "Book 10 meetings", "goal_metric": "meetings", "goal_target": 10, "max_sends": 500,
	})
	id := m["id"].(string)
	if m["status"] != "draft" || m["mode"] != "dry_run" || m["token_id"] == nil || m["writes"] != "dry_run" || m["started_by_agent"] != true {
		t.Fatalf("new mission: %v", m)
	}
	agent.must(400, "POST", base+"/missions", map[string]any{"name": "x", "goal_metric": "meetings"})

	// A write in a draft is a dry run: answered 202, nothing created.
	seq := map[string]any{"name": "Mission sequence", "steps": []map[string]any{{"subject": "Hi", "body": "Hello"}}}
	code, res := agent.inMission(id, "POST", base+"/sequences", seq)
	if code != 202 || res["dry_run"] != true || res["mission_action"].(map[string]any)["status"] != "simulated" {
		t.Fatalf("dry run: %d %v", code, res)
	}
	if n := len(owner.must(200, "GET", base+"/sequences", nil)["data"].([]any)); n != 0 {
		t.Fatalf("dry run created %d sequences", n)
	}
	// Reads run.
	if code, _ := agent.inMission(id, "GET", base+"/sequences", nil); code != 200 {
		t.Fatalf("read in mission: %d", code)
	}
	// Another token cannot act in this mission.
	if code, _ := other.inMission(id, "GET", base+"/sequences", nil); code != 403 {
		t.Fatalf("other token: %d", code)
	}

	// Submit, and the agent cannot approve itself even with the scope.
	agent.must(200, "POST", base+"/missions/"+id+"/submit", map[string]any{"plan": "1. Build a list\n2. Write a sequence"})
	approver := agentToken(t, owner, base, "Approver", "missions.read", "missions.run", "missions.approve")
	selfish := agentToken(t, owner, base, "Self approver", "missions.read", "missions.run", "missions.approve")
	sm := selfish.must(201, "POST", base+"/missions", map[string]any{"name": "Mine"})
	selfish.must(403, "POST", base+"/missions/"+sm["id"].(string)+"/approve", nil)

	// A person approves with writes held for approval.
	m = owner.must(200, "POST", base+"/missions/"+id+"/approve", map[string]any{"mode": "approve_writes"})
	if m["status"] != "running" || m["writes"] != "wait_for_approval" || m["approved_by"] == nil {
		t.Fatalf("approved: %v", m)
	}
	code, res = agent.inMission(id, "POST", base+"/sequences", seq)
	if code != 202 || res["approval_required"] != true {
		t.Fatalf("held write: %d %v", code, res)
	}
	held := res["mission_action"].(map[string]any)
	if held["summary"] != "Create sequence" {
		t.Fatalf("summary %v", held["summary"])
	}
	inbox := owner.must(200, "GET", base+"/mission-actions", nil)["data"].([]any)
	if len(inbox) != 1 {
		t.Fatalf("approval inbox: %v", inbox)
	}

	// The approver token approves; the write runs as the agent, tagged with the mission.
	act := approver.must(200, "POST", base+"/missions/"+id+"/actions/"+held["id"].(string)+"/approve", map[string]any{"note": "fine"})
	if act["status"] != "executed" || act["result_status"] != 201.0 {
		t.Fatalf("approved action: %v", act)
	}
	seqID := act["result"].(map[string]any)["id"].(string)
	sv := owner.must(200, "GET", base+"/sequences/"+seqID, nil)
	if sv["mission_id"] != id || sv["started_by_agent"] != true {
		t.Fatalf("sequence from mission: %v", sv)
	}
	approver.must(409, "POST", base+"/missions/"+id+"/actions/"+held["id"].(string)+"/approve", nil)

	// The earlier dry run can be approved now the mission runs.
	dry := owner.must(200, "GET", base+"/missions/"+id+"/actions?status=simulated", nil)["data"].([]any)
	if len(dry) != 1 {
		t.Fatalf("simulated: %v", dry)
	}
	// Rejecting with a note leaves an instruction for the agent.
	code, res = agent.inMission(id, "DELETE", base+"/sequences/"+seqID, nil)
	if code != 202 {
		t.Fatalf("held delete: %d %v", code, res)
	}
	rej := owner.must(200, "POST", base+"/missions/"+id+"/actions/"+res["mission_action"].(map[string]any)["id"].(string)+"/reject", map[string]any{"note": "keep it"})
	if rej["status"] != "rejected" {
		t.Fatalf("reject: %v", rej)
	}

	// Autonomous: writes run straight away.
	owner.must(200, "PATCH", base+"/missions/"+id, map[string]any{"mode": "autonomous"})
	agent.must(403, "PATCH", base+"/missions/"+id, map[string]any{"max_sends": 100000}) // limits need approval once running
	code, res = agent.inMission(id, "PATCH", base+"/sequences/"+seqID, map[string]any{"name": "Renamed"})
	if code != 200 || res["name"] != "Renamed" {
		t.Fatalf("autonomous write: %d %v", code, res)
	}

	// Paused: reads only. The agent can pause itself but not resume.
	agent.must(200, "POST", base+"/missions/"+id+"/pause", map[string]any{"reason": "Waiting on a list"})
	if code, _ := agent.inMission(id, "PATCH", base+"/sequences/"+seqID, map[string]any{"name": "No"}); code != 409 {
		t.Fatalf("write while paused: %d", code)
	}
	agent.must(403, "POST", base+"/missions/"+id+"/resume", nil)
	owner.must(200, "POST", base+"/missions/"+id+"/resume", nil)

	// Updates and connect requests.
	agent.must(201, "POST", base+"/missions/"+id+"/updates", map[string]any{"kind": "progress", "body": "Sequence ready"})
	cr := agent.must(201, "POST", base+"/missions/"+id+"/updates", map[string]any{"kind": "connect_request", "connector": "zerobounce", "body": "I need email verification"})
	agent.must(400, "POST", base+"/missions/"+id+"/updates", map[string]any{"kind": "connect_request", "body": "x"})
	got := agent.must(200, "GET", base+"/missions/"+id, nil)
	if reqs := got["open_connect_requests"].([]any); len(reqs) != 1 {
		t.Fatalf("open requests: %v", reqs)
	}
	owner.must(204, "POST", base+"/missions/"+id+"/updates/"+jsonInt(cr["id"])+"/resolve", map[string]any{"note": "Connected"})
	got = agent.must(200, "GET", base+"/missions/"+id, nil)
	if reqs := got["open_connect_requests"].([]any); len(reqs) != 0 {
		t.Fatalf("open requests after resolve: %v", reqs)
	}
	// Asked again, then connected: the request closes by itself with a note,
	// and a request for a connected connector is refused.
	agent.must(201, "POST", base+"/missions/"+id+"/updates", map[string]any{"kind": "connect_request", "connector": "zerobounce", "body": "Still need verification"})
	agent.must(400, "POST", base+"/missions/"+id+"/updates", map[string]any{"kind": "connect_request", "connector": "no-such-thing", "body": "x"})
	link := owner.must(201, "POST", base+"/connect-requests", map[string]any{"connector": "zerobounce", "reason": "Still need verification"})
	if !strings.Contains(link["url"].(string), "/connect#") {
		t.Fatalf("link = %v", link)
	}
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "zerobounce", "credentials": map[string]string{"api_key": "good-key"}})
	got = agent.must(200, "GET", base+"/missions/"+id, nil)
	if reqs := got["open_connect_requests"].([]any); len(reqs) != 0 {
		t.Fatalf("open requests after connecting: %v", reqs)
	}
	if body := got["updates"].([]any)[0].(map[string]any)["body"]; body != "ZeroBounce is connected, so the request for it is closed." {
		t.Fatalf("latest update = %v", body)
	}
	code, out := agent.do("POST", base+"/missions/"+id+"/updates", map[string]any{"kind": "connect_request", "connector": "zerobounce", "body": "again"})
	if code != 409 || out["error"].(map[string]any)["code"] != "already_connected" {
		t.Fatalf("request for a connected connector = %d %v", code, out)
	}
	if got["how_to_act"].(map[string]any)["header"] != "Outflare-Mission: "+id {
		t.Fatalf("how_to_act: %v", got["how_to_act"])
	}

	// Every call is in the log, and audit events carry the mission.
	calls := owner.must(200, "GET", base+"/missions/"+id+"/calls?limit=200", nil)["data"].([]any)
	outcomes := map[string]int{}
	for _, c := range calls {
		cm := c.(map[string]any)
		outcomes[cm["outcome"].(string)]++
		if cm["reason"] != "testing" && cm["outcome"] != "executed" {
			t.Fatalf("call without reason: %v", cm)
		}
	}
	if outcomes["simulated"] != 1 || outcomes["queued"] != 2 || outcomes["refused"] < 2 || outcomes["executed"] < 3 {
		t.Fatalf("call outcomes: %v", outcomes)
	}
	var tagged int
	if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE data->>'mission_id' = $1 AND action = 'sequence.created'`, id).Scan(&tagged); err != nil || tagged != 1 {
		t.Fatalf("audit tagged %d %v", tagged, err)
	}

	// Stop: held writes are canceled, its sequences paused, further calls refused.
	agent.inMission(id, "POST", base+"/sequences", seq) // runs (autonomous)
	owner.must(200, "PATCH", base+"/missions/"+id, map[string]any{"mode": "approve_writes"})
	_, res = agent.inMission(id, "POST", base+"/sequences", seq)
	pending := res["mission_action"].(map[string]any)["id"].(string)
	owner.must(200, "PATCH", base+"/sequences/"+seqID, map[string]any{"status": "paused"}) // no mailboxes; keep paused
	m = agent.must(200, "POST", base+"/missions/"+id+"/stop", map[string]any{"reason": "Goal changed"})
	if m["status"] != "stopped" || m["ended_at"] == nil || m["status_reason"] != "Goal changed" {
		t.Fatalf("stopped: %v", m)
	}
	if a := owner.must(200, "GET", base+"/missions/"+id+"/actions/"+pending, nil); a["status"] != "canceled" {
		t.Fatalf("pending after stop: %v", a)
	}
	if code, _ := agent.inMission(id, "GET", base+"/sequences", nil); code != 409 {
		t.Fatalf("call after stop: %d", code)
	}
	owner.must(409, "POST", base+"/missions/"+id+"/resume", nil)

	// A list shows the mission with its progress.
	list := owner.must(200, "GET", base+"/missions?status=stopped", nil)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["goal"].(map[string]any)["target"] != 10.0 {
		t.Fatalf("list: %v", list)
	}
}

func TestMissionScopesAndIsolation(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	reader := agentToken(t, owner, base, "Reader", "missions.read", "sequences.read")
	reader.must(403, "POST", base+"/missions", map[string]any{"name": "x"})
	// A person's unbound mission accepts any token with missions.run, not a reader.
	m := owner.must(201, "POST", base+"/missions", map[string]any{"name": "Shared"})
	if m["token_id"] != nil {
		t.Fatalf("person's mission bound to %v", m["token_id"])
	}
	if code, _ := reader.inMission(m["id"].(string), "GET", base+"/sequences", nil); code != 403 {
		t.Fatalf("reader acting in mission: %d", code)
	}
	if code, _ := reader.inMission("00000000-0000-0000-0000-000000000000", "GET", base+"/sequences", nil); code != 404 {
		t.Fatalf("unknown mission: %d", code)
	}
	// A token cannot bind a mission to another token.
	runner := agentToken(t, owner, base, "Runner", "missions.read", "missions.run")
	tokens := owner.must(200, "GET", base+"/api-tokens", nil)["data"].([]any)
	var readerID string
	for _, tk := range tokens {
		if tk.(map[string]any)["name"] == "Reader" {
			readerID = tk.(map[string]any)["id"].(string)
		}
	}
	runner.must(403, "POST", base+"/missions", map[string]any{"name": "y", "token_id": readerID})

	// Another workspace cannot see it, nor act in it.
	ws2 := owner.must(201, "POST", "/v1/workspaces", map[string]string{"name": "Second"})["id"].(string)
	owner.must(404, "GET", "/v1/workspaces/"+ws2+"/missions/"+m["id"].(string), nil)
	other := agentToken(t, owner, "/v1/workspaces/"+ws2, "Other", "missions.run", "sequences.read")
	if code, _ := other.inMission(m["id"].(string), "GET", "/v1/workspaces/"+ws2+"/sequences", nil); code != 404 {
		t.Fatalf("mission from another workspace: %d", code)
	}
}

func TestMissionSpendAndMeetings(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	m := owner.must(201, "POST", base+"/missions", map[string]any{"name": "Spend", "max_spend_cents": 1000, "goal_metric": "meetings", "goal_target": 1})
	id := m["id"].(string)
	ctx := t.Context()
	if err := missions.CheckSpend(ctx, e.pool, id, 1); !errors.Is(err, missions.ErrNotRunning) {
		t.Fatalf("draft spend: %v", err)
	}
	owner.must(200, "POST", base+"/missions/"+id+"/approve", map[string]any{"mode": "autonomous"})
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

func jsonInt(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestMissionSendLimitAndPause(t *testing.T) {
	e := freshEnv(t)
	clock := &fakeClock{}
	clock.Set(time.Date(2030, 1, 8, 10, 0, 0, 0, time.UTC))
	owner, base, srv := sendingSetup(t, e, clock)
	agent := agentToken(t, owner, base, "Agent", "missions.read", "missions.run", "sequences.read", "sequences.manage", "leads.read", "leads.manage")
	m := agent.must(201, "POST", base+"/missions", map[string]any{"name": "Capped", "max_sends": 1})
	id := m["id"].(string)
	owner.must(200, "POST", base+"/missions/"+id+"/approve", map[string]any{"mode": "autonomous"})

	code, imp := owner.raw("POST", base+"/leads/import?list=M", "text/csv", "email,first_name\nli@one-io.example,Li\nmo@two-io.example,Mo\n")
	if code != 200 {
		t.Fatalf("import: %d %v", code, imp)
	}
	code, seq := agent.inMission(id, "POST", base+"/sequences", map[string]any{
		"name": "In mission", "target_daily": 1000, "steps": []map[string]any{{"subject": "Hi {{first_name}}", "body": "Hello"}},
	})
	if code != 201 {
		t.Fatalf("create in mission: %d %v", code, seq)
	}
	seqID := seq["id"].(string)
	if code, res := agent.inMission(id, "POST", base+"/sequences/"+seqID+"/enrollments", map[string]any{"list_id": imp["list_id"]}); code != 200 {
		t.Fatalf("enroll: %d %v", code, res)
	}
	if code, res := agent.inMission(id, "PATCH", base+"/sequences/"+seqID, map[string]any{"status": "active"}); code != 200 {
		t.Fatalf("activate: %d %v", code, res)
	}

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
	if got["progress"].(map[string]any)["sends"] != 1.0 || got["progress"].(map[string]any)["leads"] != 2.0 {
		t.Fatalf("progress: %v", got["progress"])
	}

	// Raising the limit and pausing: still nothing goes while paused.
	owner.must(200, "PATCH", base+"/missions/"+id, map[string]any{"max_sends": 10})
	owner.must(200, "POST", base+"/missions/"+id+"/pause", nil)
	clock.Add(2 * time.Hour)
	e.pump(t)
	if n := len(srv.Messages()); n != 1 {
		t.Fatalf("sent while paused: %d", n)
	}
	owner.must(200, "POST", base+"/missions/"+id+"/resume", nil)
	clock.Add(2 * time.Hour)
	e.pump(t)
	if n := len(srv.Messages()); n != 2 {
		t.Fatalf("after resume: %d", n)
	}
}
