package api_test

import "testing"

// Agents send the version they read with every change to a record, so two
// of them never overwrite each other; people and plain API clients may
// leave it out.
func TestAgentsSendRecordVersion(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws
	rec := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ada"})
	id := rec["id"].(string)
	dup := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ada L"})["id"].(string)

	agent := agentToken(t, owner, base, "Agent", "customers.read", "customers.manage")
	agent.header = map[string]string{"Outflare-Client": "mcp/0.1.0", "Outflare-Tool": "update_record"}
	code, out := agent.do("PATCH", base+"/records/"+id, map[string]any{"name": "Ada Lovelace"})
	if code != 400 || out["error"].(map[string]any)["code"] != "version_required" {
		t.Fatalf("agent without version: %d %v", code, out)
	}
	code, out = agent.do("POST", base+"/records/"+id+"/merge", map[string]any{"from": dup})
	if code != 400 || out["error"].(map[string]any)["code"] != "version_required" {
		t.Fatalf("agent merge without version: %d %v", code, out)
	}
	v := int(rec["version"].(float64))
	got := agent.must(200, "PATCH", base+"/records/"+id, map[string]any{"name": "Ada Lovelace", "version": v})
	// A second agent working from the same read is refused, and told what to send.
	code, out = agent.do("PATCH", base+"/records/"+id, map[string]any{"name": "Ada King", "version": v})
	if code != 409 || out["error"].(map[string]any)["code"] != "record_changed" {
		t.Fatalf("stale agent write: %d %v", code, out)
	}

	// A person, and a script with a plain token, may still leave it out.
	owner.must(200, "PATCH", base+"/records/"+id, map[string]any{"name": "Ada"})
	script := agentToken(t, owner, base, "Script", "customers.manage")
	script.must(200, "PATCH", base+"/records/"+id, map[string]any{"name": "Ada B"})
	if int(got["version"].(float64)) <= v {
		t.Fatalf("version did not move: %v", got)
	}
}
