package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/api"
)

// A loop's agent token acts through the action layer exactly as an outside
// agent's token does, and never holds more than an operator.
func TestAgentsActThroughTheActionLayer(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	bb := brandBase(t, e, ws)
	ctx := context.Background()

	create := func(spec api.AgentSpec) (string, error) {
		tx, err := e.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		id, err := e.srv.CreateAgentToken(ctx, tx, spec)
		if err != nil {
			return "", err
		}
		return id, tx.Commit(ctx)
	}
	for _, bad := range [][]string{{}, {"members.remove"}, {"actions.approve"}, {"missions.approve"}, {"tokens.manage"}, {"nope"}} {
		if _, err := create(api.AgentSpec{WorkspaceID: ws, Name: "Agent", Scopes: bad}); !errors.Is(err, api.ErrAgentSpec) {
			t.Fatalf("scopes %v: %v", bad, err)
		}
	}
	if _, err := create(api.AgentSpec{WorkspaceID: ws, Name: "Agent", Scopes: []string{"sequences.manage"}, BrandID: strPtr("00000000-0000-0000-0000-000000000000")}); !errors.Is(err, api.ErrAgentSpec) {
		t.Fatalf("unknown brand: %v", err)
	}
	id, err := create(api.AgentSpec{WorkspaceID: ws, Name: "Brand agent: Acme", Scopes: []string{"sequences.read", "sequences.manage"}})
	if err != nil {
		t.Fatal(err)
	}

	// It is a token on the action layer, with no usable secret.
	var found map[string]any
	for _, tok := range owner.must(200, "GET", base+"/api-tokens", nil)["data"].([]any) {
		if tok.(map[string]any)["id"] == id {
			found = tok.(map[string]any)
		}
	}
	if found == nil || found["action_gate"] != true || found["name"] != "Brand agent: Acme" {
		t.Fatalf("token: %v", found)
	}

	call := func(method, path string, body any, reason string) (int, map[string]any) {
		t.Helper()
		var raw json.RawMessage
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		code, out, err := e.srv.CallAsAgent(ctx, id, method, path, raw, reason)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		return code, m
	}
	// Reads run; a change is held for a person with the agent's reason.
	if code, _ := call("GET", base+"/sequences", nil, ""); code != 200 {
		t.Fatalf("read: %d", code)
	}
	seq := map[string]any{"name": "Agent sequence", "steps": []map[string]any{{"subject": "Hi", "body": "Hello"}}}
	code, res := call("POST", base+"/sequences", seq, "Starting outreach to the list")
	if code != 202 || res["action"] == nil {
		t.Fatalf("held: %d %v", code, res)
	}
	act := res["action"].(map[string]any)
	if act["status"] != "held" || act["decided_by_rule"] != "no_grant" || act["reason"] != "Starting outreach to the list" || act["actor_name"] != "Brand agent: Acme" {
		t.Fatalf("action: %v", act)
	}
	// A person approves it, and it runs as the agent.
	if v := owner.must(200, "POST", bb+"/actions/"+act["id"].(string)+"/approve", nil); v["status"] != "executed" {
		t.Fatalf("approved: %v", v)
	}
	if n := len(owner.must(200, "GET", base+"/sequences", nil)["data"].([]any)); n != 1 {
		t.Fatalf("sequences: %d", n)
	}
	// A grant lets the next one through.
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": id, "method": "POST", "path": "sequences", "mode": "run"})
	seq["name"] = "Second"
	if code, _ := call("POST", base+"/sequences", seq, "Another"); code != 201 {
		t.Fatalf("run grant: %d", code)
	}
	// Scope still applies: no leads scope.
	if code, _ := call("GET", base+"/leads", nil, ""); code != 403 {
		t.Fatalf("scope: %d", code)
	}
	if _, _, err := e.srv.CallAsAgent(ctx, id, "GET", "/healthz", nil, ""); err == nil {
		t.Fatal("a non-API path was accepted")
	}
	// A change with no body (a DELETE) goes through the gate like any
	// other, and a loop's calls are their own surface.
	if code, out, err := e.srv.CallAsAgent(ctx, id, "DELETE", base+"/sequences/00000000-0000-0000-0000-000000000009", nil, "Remove it"); err != nil || code != 202 {
		t.Fatalf("body-less change: %d %s %v", code, out, err)
	}
	// The request log is written in the background.
	var surface string
	for i := 0; surface == "" && i < 50; i++ {
		_ = e.pool.QueryRow(ctx, `SELECT surface FROM request_logs WHERE workspace_id = $1 AND method = 'DELETE' ORDER BY id DESC LIMIT 1`, ws).Scan(&surface)
		if surface == "" {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if surface != "agent" {
		t.Fatalf("surface: %q", surface)
	}
	// Revoked by the extension, the agent is gone, with its audit event;
	// revoking again changes nothing.
	for range 2 {
		tx, err := e.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.srv.RevokeAgentToken(ctx, tx, ws, id, ""); err != nil {
			t.Fatalf("revoke: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := e.srv.CallAsAgent(ctx, id, "GET", base+"/sequences", nil, ""); !errors.Is(err, api.ErrAgentGone) {
		t.Fatalf("revoked: %v", err)
	}
	var revoked int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'token.revoked' AND target_id = $2`, ws, id).Scan(&revoked); err != nil || revoked != 1 {
		t.Fatalf("token.revoked events: %d %v", revoked, err)
	}
}
