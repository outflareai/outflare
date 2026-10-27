package api

import (
	"testing"

	"github.com/outflareai/outflare/engine/internal/llm"
)

func TestHeldForBuiltin(t *testing.T) {
	ws := "/v1/workspaces/acme"
	for _, c := range []struct {
		method, path string
		held         bool
	}{
		{"POST", ws + "/inbox/abc/reply", true},
		{"POST", ws + "/mailboxes/abc/test-send", true},
		{"POST", ws + "/mailboxes", true},
		{"POST", ws + "/mailboxes/import", true},
		{"PATCH", ws + "/mailboxes/6f1c2a10-9a1b-4c55-8d0e-1234567890ab", true},
		{"POST", ws + "/sending-servers", true},
		{"POST", ws + "/webhooks", true},
		{"PATCH", ws + "/context", true},
		{"DELETE", ws + "/suppressions/abc", true},
		{"POST", ws + "/integrations", true},
		{"POST", ws + "/mailboxes/assign", false},
		{"POST", ws + "/leads", false},
		{"PATCH", ws + "/sequences/abc", false},
		{"GET", ws + "/inbox/abc/reply", false},
		{"POST", ws + "/memories", false},
	} {
		if got := heldForBuiltin(c.method, c.path); got != c.held {
			t.Errorf("%s %s held = %v, want %v", c.method, c.path, got, c.held)
		}
	}
}

func TestForTask(t *testing.T) {
	opus, _ := llm.KnownPrice("claude", "claude-opus-5-5")
	haiku, _ := llm.KnownPrice("claude", "claude-haiku-5-5")
	none := aiSettingsRow{}
	for _, c := range []struct {
		name       string
		row        aiSettingsRow
		p          llm.Provider
		model      string
		price      llm.Price
		task, want string
		custom     bool
	}{
		{"plans use the main model", none, llm.Claude, "claude-opus-5-5", opus, taskPlan, "claude-opus-5-5", false},
		{"turns use the main model", none, llm.Claude, "claude-opus-5-5", opus, taskTurn, "claude-opus-5-5", false},
		{"check-ins use the cheaper model", none, llm.Claude, "claude-opus-5-5", opus, taskCheckIn, "claude-sonnet-5-5", false},
		{"reply labelling uses the cheapest model", none, llm.Claude, "claude-opus-5-5", opus, taskReplyLabelling, "claude-haiku-5-5", false},
		{"domain research too", none, llm.Claude, "claude-sonnet-5-5", llm.Price{Input: 200, Output: 1000}, taskDomainResearch, "claude-haiku-5-5", false},
		{"never a dearer one", none, llm.Claude, "claude-haiku-5-5", haiku, taskCheckIn, "claude-haiku-5-5", false},
		{"no light model: the main one", none, llm.OpenAI, "gpt-x", llm.Price{Input: 100, Output: 400}, taskCheckIn, "gpt-x", false},
		{"the workspace's choice", aiSettingsRow{Tasks: map[string]taskModel{taskPlan: {Provider: "claude", Model: "claude-fable-5-1"}}},
			llm.Claude, "claude-opus-5-5", opus, taskPlan, "claude-fable-5-1", true},
		{"a choice for another provider is ignored", aiSettingsRow{Tasks: map[string]taskModel{taskTurn: {Provider: "openai", Model: "gpt-x"}}},
			llm.Claude, "claude-opus-5-5", opus, taskTurn, "claude-opus-5-5", false},
		{"an unknown model without a price is ignored", aiSettingsRow{Tasks: map[string]taskModel{taskCheckIn: {Provider: "openai", Model: "gpt-mini"}}},
			llm.OpenAI, "gpt-x", llm.Price{Input: 100, Output: 400}, taskCheckIn, "gpt-x", false},
	} {
		got, _, _, custom := forTask(c.row, c.p, c.model, c.price, "list", c.task)
		if got != c.want || custom != c.custom {
			t.Errorf("%s: got %s (custom %v), want %s (custom %v)", c.name, got, custom, c.want, c.custom)
		}
	}
}
