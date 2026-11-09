package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/outflareai/outflare/engine/internal/llm"
)

const openAIKey = "sk-proj-good-0123456789abcdef"

// connectFakeOpenAI connects a fake OpenAI as the workspace's AI model, for
// tests that start agent runs and never take a turn.
func connectFakeOpenAI(t *testing.T, e *env, owner *client, ws string) {
	t.Helper()
	oa := httptest.NewServer(fakeOpenAI{t, &sync.Mutex{}, new([]string)})
	t.Cleanup(oa.Close)
	e.integrations.Client.BaseURLs["openai"] = oa.URL
	conn := owner.must(201, "POST", "/v1/workspaces/"+ws+"/integrations", map[string]any{"connector": "openai", "credentials": map[string]any{"api_key": openAIKey}})
	owner.must(200, "PUT", "/v1/workspaces/"+ws+"/ai-settings", map[string]any{"integration_id": conn["id"], "model": "gpt-test",
		"input_cents_per_million": 250, "output_cents_per_million": 1000, "monthly_limit_cents": 5000})
}

// fakeOpenAI answers OpenAI's API: the model list, the planner and the
// built-in agent's turns. It notes which model each chat asked for.
type fakeOpenAI struct {
	t      *testing.T
	mu     *sync.Mutex
	models *[]string
}

func (f fakeOpenAI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer "+openAIKey {
		w.WriteHeader(401)
		io.WriteString(w, `{"error":{"message":"Incorrect API key provided: `+strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")+`"}}`)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/models" {
		io.WriteString(w, `{"data":[{"id":"gpt-test"},{"id":"text-embedding-3-large"},{"id":"gpt-other"}]}`)
		return
	}
	body, _ := io.ReadAll(r.Body)
	if strings.Contains(string(body), openAIKey) {
		f.t.Error("the API key appeared in what the model reads")
	}
	var req struct {
		Model string `json:"model"`
	}
	json.Unmarshal(body, &req)
	f.mu.Lock()
	*f.models = append(*f.models, req.Model)
	f.mu.Unlock()
	answer := func(msg map[string]any, finish string) {
		json.NewEncoder(w).Encode(map[string]any{"model": "gpt-test", "choices": []any{map[string]any{"message": msg, "finish_reason": finish}},
			"usage": map[string]any{"prompt_tokens": 2000, "completion_tokens": 500}})
	}
	call := func(id, name string, args any) map[string]any {
		b, _ := json.Marshal(args)
		return map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(b)}}
	}
	if strings.Contains(string(body), `"finish_turn"`) {
		if !strings.Contains(string(body), `"role":"tool"`) {
			answer(map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{
				call("c1", "call_api", map[string]any{"method": "POST", "path": "/leads", "reason": "First CFO", "body": map[string]any{"email": "sam@example.org"}}),
				call("c2", "call_api", map[string]any{"method": "POST", "path": "/inbox/00000000-0000-0000-0000-000000000000/reply", "reason": "Answer the reply",
					"body": map[string]any{"body": "Here is our lead list"}}),
			}}, "tool_calls")
			return
		}
		answer(map[string]any{"role": "assistant", "content": "", "tool_calls": []any{
			call("c3", "finish_turn", map[string]any{"summary": "Added a lead and drafted a reply.", "next_check_in_minutes": 60, "done": false})}}, "tool_calls")
		return
	}
	draft, _ := json.Marshal(map[string]any{
		"name": "Fintech CFOs", "goal": "Book 5 meetings with fintech CFOs.", "goal_metric": "meetings", "goal_target": 5,
		"steps": []map[string]string{{"title": "Find CFOs", "detail": "Import CFOs."}}, "connectors": []any{}, "questions": []any{}, "assumptions": []any{},
	})
	answer(map[string]any{"role": "assistant", "content": string(draft)}, "stop")
}

func TestAISettings(t *testing.T) {
	e := freshEnv(t)
	var used []string
	oa := httptest.NewServer(fakeOpenAI{t, &sync.Mutex{}, &used})
	defer oa.Close()
	e.integrations.Client.BaseURLs["openai"] = oa.URL
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	// Nothing connected: the settings say what to do.
	s := owner.must(200, "GET", base+"/ai-settings", nil)
	if s["problem"].(map[string]any)["code"] != "connector_needed" || s["monthly_limit_cents"].(float64) != 5000 || len(s["providers"].([]any)) != len(llm.Providers) {
		t.Fatalf("empty settings = %v", s)
	}

	// Wrong secrets are refused before anything is sent.
	for _, key := range []string{"oft_abcdefghijklmnop", "AIzaSyNotAnOpenAIKey0000000000", "sk-proj-bad-0123456789"} {
		code, out := owner.do("POST", base+"/integrations", map[string]any{"connector": "openai", "credentials": map[string]any{"api_key": key}})
		msg, _ := json.Marshal(out)
		if code != 422 || strings.Contains(string(msg), key) {
			t.Errorf("connect with %s = %d %s", key, code, msg)
		}
	}
	conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "openai", "credentials": map[string]any{"api_key": openAIKey}})
	cid := conn["id"].(string)
	if raw, _ := json.Marshal(conn); strings.Contains(string(raw), openAIKey) {
		t.Fatal("the key came back in the integration")
	}

	// OpenAI has no default model, so the workspace picks one.
	s = owner.must(200, "GET", base+"/ai-settings", nil)
	if s["problem"].(map[string]any)["code"] != "ai_model_needed" {
		t.Errorf("settings without a model = %v", s["problem"])
	}
	pb, inputs := "answer-interested-replies", map[string]string{"booking_link": "https://cal.example/ana"}
	code, out := owner.do("POST", base+"/playbooks/"+pb+"/agent-runs", map[string]any{"inputs": inputs})
	if code != 409 {
		t.Errorf("run without a model = %d %v", code, out)
	}
	list := owner.must(200, "GET", base+"/integrations/"+cid+"/models", nil)["data"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["id"] != "gpt-test" {
		t.Errorf("models = %v", list)
	}

	// A model whose price Outflare doesn't know needs one.
	owner.must(400, "PUT", base+"/ai-settings", map[string]any{"integration_id": cid, "model": "gpt-test", "monthly_limit_cents": 5000})
	owner.must(400, "PUT", base+"/ai-settings", map[string]any{"model": "gpt-test"})
	owner.must(400, "PUT", base+"/ai-settings", map[string]any{"integration_id": cid, "model": "bad model!", "input_cents_per_million": 1, "output_cents_per_million": 1})
	s = owner.must(200, "PUT", base+"/ai-settings", map[string]any{"integration_id": cid, "model": "gpt-test",
		"input_cents_per_million": 250, "output_cents_per_million": 1000, "monthly_limit_cents": 5000})
	if act := s["active"].(map[string]any); act["provider"] != "openai" || act["model"] != "gpt-test" || act["price_source"] != "workspace" || s["problem"] != nil {
		t.Fatalf("settings = %v", s)
	}

	// Someone else's integration can't be chosen.
	ws2 := owner.must(201, "POST", "/v1/workspaces", map[string]string{"name": "Other"})["id"].(string)
	otherConn := owner.must(201, "POST", "/v1/workspaces/"+ws2+"/integrations", map[string]any{"connector": "openai", "credentials": map[string]any{"api_key": openAIKey}})
	owner.must(400, "PUT", base+"/ai-settings", map[string]any{"integration_id": otherConn["id"], "model": "gpt-test", "input_cents_per_million": 1, "output_cents_per_million": 1})

	// Outflare's agent runs on OpenAI and its cost is booked to it: each
	// model call is 2000 in at 250 and 500 out at 1000, 1 cent. Even
	// autonomous, a reply it wants to send waits for a person; the lead
	// goes straight in.
	run := owner.must(201, "POST", base+"/playbooks/"+pb+"/agent-runs", map[string]any{"inputs": inputs, "mode": "autonomous", "max_spend_cents": 10000})
	id := run["id"].(string)
	if n, err := e.srv.RunDueAgentTurns(ctx); err != nil || n != 1 {
		t.Fatalf("turn = %d, %v", n, err)
	}
	run = owner.must(200, "GET", base+"/agent-runs/"+id, nil)["run"].(map[string]any)
	if run["turn_summary"] != "Added a lead and drafted a reply." || run["held_actions"].(float64) != 1 {
		t.Errorf("after the turn = %v", run)
	}
	if leads := owner.must(200, "GET", base+"/leads", nil)["data"].([]any); len(leads) != 1 {
		t.Errorf("leads = %d", len(leads))
	}
	s = owner.must(200, "GET", base+"/ai-settings", nil)
	spent := s["spent_this_month_cents"].(float64)
	if spent < 1 {
		t.Errorf("month spend = %v", spent)
	}

	// The monthly limit pauses the agent.
	owner.must(200, "PUT", base+"/ai-settings", map[string]any{"integration_id": cid, "model": "gpt-test",
		"input_cents_per_million": 250, "output_cents_per_million": 1000, "monthly_limit_cents": int(spent)})
	owner.must(201, "POST", base+"/agent-runs/"+id+"/updates", map[string]any{"kind": "instruction", "body": "Any news?"})
	e.srv.RunDueAgentTurns(ctx)
	run = owner.must(200, "GET", base+"/agent-runs/"+id, nil)["run"].(map[string]any)
	if run["status"] != "paused" || !strings.Contains(run["status_reason"].(string), "monthly limit") {
		t.Errorf("over the limit = %v / %v", run["status"], run["status_reason"])
	}

	// Each task can have its own model. Routine check-ins move to a
	// cheaper one; plans keep the main model.
	owner.must(400, "PUT", base+"/ai-settings", map[string]any{"integration_id": cid, "model": "gpt-test",
		"input_cents_per_million": 250, "output_cents_per_million": 1000, "monthly_limit_cents": nil,
		"task_models": map[string]any{"check_in": map[string]any{"model": "gpt-other"}}})
	owner.must(400, "PUT", base+"/ai-settings", map[string]any{"integration_id": cid, "model": "gpt-test",
		"input_cents_per_million": 250, "output_cents_per_million": 1000, "monthly_limit_cents": nil,
		"task_models": map[string]any{"summaries": map[string]any{"model": "gpt-other", "input_cents_per_million": 10, "output_cents_per_million": 40}}})
	s = owner.must(200, "PUT", base+"/ai-settings", map[string]any{"integration_id": cid, "model": "gpt-test",
		"input_cents_per_million": 250, "output_cents_per_million": 1000, "monthly_limit_cents": nil,
		"task_models": map[string]any{"check_in": map[string]any{"model": "gpt-other", "input_cents_per_million": 10, "output_cents_per_million": 40}}})
	byTask := map[string]map[string]any{}
	for _, v := range s["tasks"].([]any) {
		byTask[v.(map[string]any)["task"].(string)] = v.(map[string]any)
	}
	if byTask["plan"]["model"] != "gpt-test" || byTask["check_in"]["model"] != "gpt-other" || byTask["check_in"]["custom"] != true || byTask["reply_labelling"]["model"] != "gpt-test" || len(byTask) != 22 || byTask["coverage_review"]["model"] != "gpt-test" {
		t.Errorf("tasks = %v", s["tasks"])
	}
	// Saving without task_models keeps them.
	s = owner.must(200, "PUT", base+"/ai-settings", map[string]any{"integration_id": cid, "model": "gpt-test",
		"input_cents_per_million": 250, "output_cents_per_million": 1000, "monthly_limit_cents": nil})
	if s["task_models"].(map[string]any)["check_in"] == nil {
		t.Errorf("task models were dropped: %v", s["task_models"])
	}
	owner.must(200, "POST", base+"/agent-runs/"+id+"/resume", nil)
	e.srv.RunDueAgentTurns(ctx) // a person resumed it: the main model
	e.pool.Exec(ctx, `UPDATE agent_runs SET next_turn_at = now() WHERE id = $1`, id)
	used = nil
	e.srv.RunDueAgentTurns(ctx) // nothing new: a routine check-in
	if len(used) == 0 || used[0] != "gpt-other" {
		t.Errorf("check-in used %v, want gpt-other", used)
	}

	// Viewers see the settings but can't change them.
	viewer := agentToken(t, owner, base, "Reader", "integrations.read")
	viewer.must(200, "GET", base+"/ai-settings", nil)
	viewer.must(403, "PUT", base+"/ai-settings", map[string]any{"monthly_limit_cents": 100})
}
