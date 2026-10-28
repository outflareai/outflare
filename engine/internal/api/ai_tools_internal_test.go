package api

import (
	"testing"

	"github.com/outflareai/outflare/engine/internal/llm"
)

func TestExtensionAITool(t *testing.T) {
	finder := AITool{ID: "lead_finder", Name: "Lead finder", Description: "Finding companies.", ModelTask: taskDomainResearch}
	if err := CheckAITool(finder); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []AITool{
		{ID: "Lead Finder", Name: "x", ModelTask: taskDomainResearch},
		{ID: "other", Name: "x", ModelTask: taskDomainResearch},
		{ID: taskPlan, Name: "x", ModelTask: taskPlan},
		{ID: taskSemanticSearch, Name: "x", ModelTask: taskPlan},
		{ID: "lead_finder", ModelTask: taskDomainResearch},
		{ID: "lead_finder", Name: "x", ModelTask: "lead_finder"},
	} {
		if CheckAITool(bad) == nil {
			t.Errorf("%+v was allowed", bad)
		}
	}

	s := &Server{aiTools: []AITool{finder}}
	if got := s.aiToolName("lead_finder"); got != "Lead finder" {
		t.Errorf("name = %q", got)
	}
	if got := (&Server{}).aiToolName("lead_finder"); got != "Lead finder" {
		t.Errorf("name without the extension = %q", got)
	}
	if s.modelTask("lead_finder") != taskDomainResearch || s.modelTask(taskPlan) != taskPlan {
		t.Error("model task")
	}
	// Its calls use domain research's cheaper model, and the workspace's
	// choice for domain research.
	opus, _ := llm.KnownPrice("claude", "claude-opus-5-5")
	if got, _, _, _ := forTask(aiSettingsRow{}, llm.Claude, "claude-opus-5-5", opus, "list", s.modelTask("lead_finder")); got != "claude-haiku-5-5" {
		t.Errorf("model = %s", got)
	}
	chosen := aiSettingsRow{Tasks: map[string]taskModel{taskDomainResearch: {Provider: "claude", Model: "claude-sonnet-5-5"}}}
	if got, _, _, _ := forTask(chosen, llm.Claude, "claude-opus-5-5", opus, "list", s.modelTask("lead_finder")); got != "claude-sonnet-5-5" {
		t.Errorf("chosen model = %s", got)
	}
	tools := s.usageTools()
	if last := tools[len(tools)-1]; last.ID != "lead_finder" || last.Description != "Finding companies." {
		t.Errorf("tools end with %+v", last)
	}
}
