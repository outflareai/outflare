package api_test

import (
	"strings"
	"testing"
)

// A test's versions over the API: people and agents set a version's visual
// changes as the editor saves them. An agent builds a draft on its own
// grants, but anything visitors would see (a live version, starting or
// shipping the test, turning a flag on) always waits for a person.
func TestABTestVersionsForAgents(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	signup := []map[string]any{{"kind": "event", "name": "signup_started"}}
	ab := owner.must(201, "POST", base+"/ab-tests", map[string]any{"goals": signup, "page": map[string]any{"url": "https://northpeak.example/pricing"}})
	v := base + "/ab-tests/" + ab["id"].(string) + "/versions/"

	// A person sets a version; the control and unknown versions are refused.
	got := owner.must(200, "PUT", v+"b", map[string]any{"changes": []map[string]any{{"selector": "h1", "text": "Ship faster"}}, "css": "h1{color:#0f766e}"})
	if got["version"] != "b" || got["live"] != false || got["css"] != "h1{color:#0f766e}" || len(got["changes"].([]any)) != 1 {
		t.Fatalf("set: %v", got)
	}
	owner.must(400, "PUT", v+"control", map[string]any{"changes": []any{}})
	owner.must(404, "PUT", v+"z", map[string]any{"changes": []any{}})
	owner.must(400, "PUT", v+"b", map[string]any{"changes": []any{}, "redirect": "https://elsewhere.example/"})
	if r := owner.must(200, "GET", v+"b", nil); r["css"] != "h1{color:#0f766e}" {
		t.Fatalf("read: %v", r)
	}

	// An agent allowed to change tests builds the draft's versions...
	agent, agentID := gatedAgent(t, owner, base, "Testing agent", "analytics.read", "analytics.manage", "actions.read")
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "ab-tests/**", "mode": "run"})
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "flags/**", "mode": "run"})
	if r := agent.must(200, "PUT", v+"b", map[string]any{"changes": []map[string]any{{"selector": "h1", "rich": "**Ship** faster"}}}); len(r["changes"].([]any)) != 1 {
		t.Fatalf("agent draft: %v", r)
	}
	// ...but starting it waits for a person, whatever its grants say.
	held := agent.must(202, "POST", base+"/ab-tests/"+ab["id"].(string)+"/start", nil)
	if !strings.Contains(held["action"].(map[string]any)["hold_reason"].(string), "a person always approves it") {
		t.Fatalf("start held: %v", held)
	}
	agent.must(202, "PATCH", base+"/flags/"+ab["flag_key"].(string), map[string]any{"status": "on"})

	// Once a person starts it, the agent's edits to a version wait too.
	owner.must(200, "POST", base+"/ab-tests/"+ab["id"].(string)+"/start", nil)
	agent.must(202, "PUT", v+"b", map[string]any{"changes": []map[string]any{{"selector": "h1", "text": "Sneaky"}}})
	if r := owner.must(200, "GET", v+"b", nil); r["live"] != true || strings.Contains(r["changes"].([]any)[0].(map[string]any)["rich"].(string), "Sneaky") {
		t.Fatalf("live version changed by the agent: %v", r)
	}
	agent.must(202, "POST", base+"/ab-tests/"+ab["id"].(string)+"/ship", map[string]any{"variant": "b"})
	agent.must(202, "PATCH", base+"/flags/"+ab["flag_key"].(string), map[string]any{"rollout": 50})
	// Renaming a live flag changes nothing visitors see.
	agent.must(200, "PATCH", base+"/flags/"+ab["flag_key"].(string), map[string]any{"name": "Pricing page test"})

	// An ended test's versions can't change.
	owner.must(200, "POST", base+"/ab-tests/"+ab["id"].(string)+"/end", map[string]any{"decision": ""})
	owner.must(409, "PUT", v+"b", map[string]any{"changes": []any{}})
}
