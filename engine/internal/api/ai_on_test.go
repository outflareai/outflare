package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/llm"
)

// A feature that calls several models lists the workspace's AI
// connections and calls a chosen one, under the same limits.
func TestAIModelOnAChosenConnection(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws

	if conns, err := e.srv.AIConnectionsOf(ctx, ws); err != nil || len(conns) != 0 {
		t.Fatalf("no connections: %v %v", conns, err)
	}
	id := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})["id"].(string)
	conns, err := e.srv.AIConnectionsOf(ctx, ws)
	if err != nil || len(conns) != 1 || conns[0].ID != id || conns[0].Provider.ID != llm.Claude.ID {
		t.Fatalf("connections: %v %v", conns, err)
	}

	// A connection that isn't the workspace's, and a model whose price
	// Outflare doesn't know, are refused.
	if _, err := e.srv.AIModelOn(ctx, ws, "lead_finder", "00000000-0000-0000-0000-000000000000", "", 5); err == nil || !strings.Contains(err.Error(), "no longer connected") {
		t.Fatalf("unknown connection: %v", err)
	}
	if _, err := e.srv.AIModelOn(ctx, ws, "lead_finder", id, "claude-made-up", 5); err == nil || !strings.Contains(err.Error(), "doesn't know what") {
		t.Fatalf("unknown price: %v", err)
	}

	c, err := e.srv.AIModelOn(ctx, ws, "lead_finder", id, "claude-haiku-5-5", 5)
	if err != nil || c.Model.ID() != "claude-haiku-5-5" || c.Task != "lead_finder" || !c.Price.Known() {
		t.Fatalf("chosen model: %+v %v", c, err)
	}
	resp, err := c.Model.Chat(ctx, llm.Request{System: "You check which model answers.", Messages: []llm.Message{{Role: "user", Text: "hi"}}, MaxTokens: 50})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.srv.AIModels().Spent(ctx, ws, c, resp.Usage, "test:ai-on", "lead_finder"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM spend_entries WHERE workspace_id = $1 AND ref = 'test:ai-on'`, ws).Scan(&n); err != nil || n != 1 {
		t.Fatalf("spend booked: %d %v", n, err)
	}

	// The tool's own monthly limit applies.
	if _, err := e.pool.Exec(ctx, `INSERT INTO workspace_ai_settings (workspace_id, task_limits) VALUES ($1, '{"lead_finder": 0}')
		ON CONFLICT (workspace_id) DO UPDATE SET task_limits = excluded.task_limits`, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.AIModelOn(ctx, ws, "lead_finder", id, "", 5); err == nil || !strings.Contains(err.Error(), "monthly limit") {
		t.Fatalf("over the tool's limit: %v", err)
	}
}
