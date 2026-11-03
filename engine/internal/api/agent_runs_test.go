package api_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// A playbook starts as an agent run: Outflare's agent takes turns as an
// agent token on the action layer, its changes are held for a person in
// copilot mode, a person writes to it and it answers, and stopping it
// revokes its token.
func TestAgentRunsPlaybook(t *testing.T) {
	e := freshEnv(t)
	var used []string
	oa := httptest.NewServer(fakeOpenAI{t, &sync.Mutex{}, &used})
	defer oa.Close()
	e.integrations.Client.BaseURLs["openai"] = oa.URL
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	pb := "answer-interested-replies"
	inputs := map[string]string{"booking_link": "https://cal.example/ana"}

	// Without an AI model, a run can't start.
	code, out := owner.do("POST", base+"/playbooks/"+pb+"/agent-runs", map[string]any{"inputs": inputs})
	if code != 409 {
		t.Fatalf("without a model: %d %v", code, out)
	}
	conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "openai", "credentials": map[string]any{"api_key": openAIKey}})
	owner.must(200, "PUT", base+"/ai-settings", map[string]any{"integration_id": conn["id"], "model": "gpt-test",
		"input_cents_per_million": 250, "output_cents_per_million": 1000, "monthly_limit_cents": 5000})
	owner.must(400, "POST", base+"/playbooks/"+pb+"/agent-runs", map[string]any{"inputs": inputs, "mode": "wild"})
	owner.must(400, "POST", base+"/playbooks/"+pb+"/agent-runs", map[string]any{"inputs": inputs, "mode": "autonomous", "max_spend_cents": 0})
	owner.must(404, "POST", base+"/playbooks/nope/agent-runs", map[string]any{})

	run := owner.must(201, "POST", base+"/playbooks/"+pb+"/agent-runs", map[string]any{"inputs": inputs})
	id := run["id"].(string)
	if run["status"] != "running" || run["mode"] != "copilot" || run["playbook_id"] != pb || run["token_id"] == nil || run["next_turn_at"] == nil {
		t.Fatalf("run: %v", run)
	}
	tokenID := run["token_id"].(string)
	var gate bool
	if err := e.pool.QueryRow(ctx, `SELECT action_gate FROM api_tokens WHERE id = $1`, tokenID).Scan(&gate); err != nil || !gate {
		t.Fatalf("the run's token is on the action layer: %v %v", gate, err)
	}
	if n := len(owner.must(200, "GET", base+"/agent-runs", nil)["data"].([]any)); n != 1 {
		t.Fatalf("list: %d", n)
	}
	ci := owner.must(200, "GET", base+"/agent-runs/"+id, nil)
	if !strings.Contains(ci["how_to_act"].(map[string]any)["changes"].(string), "waits for a person") || len(ci["updates"].([]any)) != 1 {
		t.Fatalf("check-in: %v", ci)
	}

	// The first turn: the fake model adds a lead and drafts a reply; both
	// wait for a person in copilot mode.
	if n, err := e.srv.RunDueAgentTurns(ctx); err != nil || n != 1 {
		t.Fatalf("turn = %d, %v", n, err)
	}
	run = owner.must(200, "GET", base+"/agent-runs/"+id, nil)["run"].(map[string]any)
	if run["turns"] != 1.0 || run["in_turn"] != false || run["held_actions"] != 2.0 || run["actions"] != 2.0 || !strings.Contains(run["turn_summary"].(string), "lead") {
		t.Fatalf("after turn 1: %v", run)
	}
	held := owner.must(200, "GET", base+"/actions?status=held&token_id="+tokenID, nil)["data"].([]any)
	if len(held) != 2 {
		t.Fatalf("held changes: %v", held)
	}
	if n := len(owner.must(200, "GET", base+"/leads", nil)["data"].([]any)); n != 0 {
		t.Fatalf("a held change created %d leads", n)
	}
	// Nothing is due until the agent's next check-in.
	if n, err := e.srv.RunDueAgentTurns(ctx); err != nil || n != 0 {
		t.Fatalf("second pass = %d, %v", n, err)
	}
	// A person writes: the next turn comes forward and answers.
	owner.must(400, "POST", base+"/agent-runs/"+id+"/updates", map[string]any{"kind": "progress", "body": "x"})
	owner.must(201, "POST", base+"/agent-runs/"+id+"/updates", map[string]any{"kind": "instruction", "body": "Only fintech leads."})
	if n, err := e.srv.RunDueAgentTurns(ctx); err != nil || n != 1 {
		t.Fatalf("turn after an instruction = %d, %v", n, err)
	}
	run = owner.must(200, "GET", base+"/agent-runs/"+id, nil)["run"].(map[string]any)
	if run["turns"] != 2.0 {
		t.Fatalf("after turn 2: %v", run)
	}
	// The web app links a run by a slug from its name.
	if l := owner.must(200, "GET", base+"/links/run/"+id, nil); l["id"] != id || l["slug"] == "" || l["name"] != run["name"] {
		t.Fatalf("run link: %v", l)
	}
	// Pause and resume, then stop: its token is revoked.
	owner.must(200, "POST", base+"/agent-runs/"+id+"/pause", nil)
	owner.must(409, "POST", base+"/agent-runs/"+id+"/pause", nil)
	owner.must(200, "POST", base+"/agent-runs/"+id+"/resume", nil)
	stopped := owner.must(200, "POST", base+"/agent-runs/"+id+"/stop", nil)
	if stopped["status"] != "stopped" || stopped["ended_at"] == nil {
		t.Fatalf("stopped: %v", stopped)
	}
	var revoked bool
	if err := e.pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM api_tokens WHERE id = $1`, tokenID).Scan(&revoked); err != nil || !revoked {
		t.Fatalf("token after stop: %v %v", revoked, err)
	}
	owner.must(409, "POST", base+"/agent-runs/"+id+"/resume", nil)
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action IN ('agent_run.created', 'playbook.started', 'agent_run.paused', 'agent_run.resumed', 'agent_run.stopped')`, ws).Scan(&n); err != nil || n != 5 {
		t.Fatalf("events: %d %v", n, err)
	}

	// Autonomous: a run grant with the spend cap; dry run: a refuse grant.
	auto := owner.must(201, "POST", base+"/playbooks/"+pb+"/agent-runs", map[string]any{"inputs": inputs, "mode": "autonomous", "max_spend_cents": 2000})
	grants := owner.must(200, "GET", base+"/action-grants?token_id="+auto["token_id"].(string), nil)["data"].([]any)
	if len(grants) != 1 || grants[0].(map[string]any)["mode"] != "run" || grants[0].(map[string]any)["path"] != "**" || grants[0].(map[string]any)["total_spend_cents"] != 2000.0 {
		t.Fatalf("autonomous grants: %v", grants)
	}
	owner.must(200, "POST", base+"/agent-runs/"+auto["id"].(string)+"/stop", nil)
	dry := owner.must(201, "POST", base+"/playbooks/"+pb+"/agent-runs", map[string]any{"inputs": inputs, "mode": "dry_run"})
	grants = owner.must(200, "GET", base+"/action-grants?token_id="+dry["token_id"].(string), nil)["data"].([]any)
	if len(grants) != 1 || grants[0].(map[string]any)["mode"] != "refuse" {
		t.Fatalf("dry run grants: %v", grants)
	}
	if n, err := e.srv.RunDueAgentTurns(ctx); err != nil || n != 1 {
		t.Fatalf("dry run turn = %d, %v", n, err)
	}
	if refused := owner.must(200, "GET", base+"/actions?status=refused&token_id="+dry["token_id"].(string), nil)["data"].([]any); len(refused) != 2 {
		t.Fatalf("a dry run keeps what it would have done: %v", refused)
	}

	// An automation Outflare's agent runs starts agent runs, with its mode
	// as the run's grants and its spend limit as the cap.
	automation := owner.must(201, "POST", base+"/playbook-automations", map[string]any{"playbook_id": pb, "inputs": inputs, "kind": "schedule", "cadence": "daily", "runner": "builtin", "mode": "autonomous", "max_spend_cents_per_run": 1500})
	aid := automation["id"].(string)
	owner.must(200, "POST", base+"/playbook-automations/"+aid+"/approve", nil)
	fired := owner.must(201, "POST", base+"/playbook-automations/"+aid+"/run", nil)
	if fired["automation_id"] != aid || fired["mode"] != "autonomous" || fired["max_spend_cents"] != 1500.0 || fired["status"] != "running" {
		t.Fatalf("automation's run: %v", fired)
	}
	runs := owner.must(200, "GET", base+"/playbook-automations/"+aid+"/runs", nil)
	if len(runs["agent_runs"].([]any)) != 1 || len(runs["data"].([]any)) != 0 {
		t.Fatalf("automation runs: %v", runs)
	}
	if v := owner.must(200, "GET", base+"/playbook-automations/"+aid, nil); v["last_run_id"] != fired["id"] || v["runs"] != 1.0 {
		t.Fatalf("automation after the run: %v", v)
	}
	grants = owner.must(200, "GET", base+"/action-grants?token_id="+fired["token_id"].(string), nil)["data"].([]any)
	if len(grants) != 1 || grants[0].(map[string]any)["total_spend_cents"] != 1500.0 {
		t.Fatalf("automation run grants: %v", grants)
	}
	// Another agent on the action layer can't manage or instruct a run.
	other, _ := gatedAgent(t, owner, base, "Other agent", "missions.read", "missions.run")
	other.must(403, "POST", base+"/agent-runs/"+fired["id"].(string)+"/stop", nil)
	other.must(403, "POST", base+"/agent-runs/"+fired["id"].(string)+"/updates", map[string]any{"kind": "instruction", "body": "Stop everything"})
	// A person's own app on a plain token instructs like a person.
	app := agentToken(t, owner, base, "App", "missions.read", "missions.run")
	app.must(201, "POST", base+"/agent-runs/"+fired["id"].(string)+"/updates", map[string]any{"kind": "instruction", "body": "Only fintech leads."})
	// The agents scopes are the runs' own: reading lists and reads them,
	// running starts one and writes to it; the mission scopes above stand
	// for them while missions retire.
	reader := agentToken(t, owner, base, "Reader", "agents.read")
	reader.must(200, "GET", base+"/agent-runs", nil)
	reader.must(200, "GET", base+"/playbooks/"+pb, nil)
	reader.must(403, "POST", base+"/playbooks/"+pb+"/agent-runs", map[string]any{"inputs": inputs})
	reader.must(403, "POST", base+"/agent-runs/"+fired["id"].(string)+"/updates", map[string]any{"kind": "instruction", "body": "x"})
	runner := agentToken(t, owner, base, "Runner", "agents.run")
	runner.must(200, "GET", base+"/agent-runs/"+fired["id"].(string), nil)
	runner.must(201, "POST", base+"/agent-runs/"+fired["id"].(string)+"/updates", map[string]any{"kind": "instruction", "body": "Only fintech leads."})
	if r := runner.must(201, "POST", base+"/playbooks/"+pb+"/agent-runs", map[string]any{"inputs": inputs}); r["status"] != "running" {
		t.Fatalf("run on agents.run: %v", r)
	}
}
