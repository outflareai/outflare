package api_test

import (
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// A test that ends gets its learning from the workspace's own AI model;
// the library lists finished tests, searchable; and questions are
// answered over them, citing only tests that exist.
func TestABTestLearnings(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	e.runServerJobs(t)
	owner.must(201, "POST", base+"/flags", map[string]any{"key": "hero", "name": "Pricing headline",
		"variants": []map[string]any{{"key": "control", "weight": 50}, {"key": "short", "weight": 50}}})
	signup := []map[string]any{{"kind": "event", "name": "signup_started"}}

	// Without an AI model, ending a test writes no learning, and the
	// library still lists it.
	first := owner.must(201, "POST", base+"/ab-tests", map[string]any{"flag": "hero", "name": "Long vs short", "hypothesis": "Short headlines read faster", "goals": signup})
	id1 := first["id"].(string)
	owner.must(409, "POST", base+"/ab-tests/"+id1+"/learning", nil)
	owner.must(200, "POST", base+"/ab-tests/"+id1+"/start", nil)
	owner.must(200, "POST", base+"/ab-tests/"+id1+"/end", map[string]any{"decision": "short"})
	lib := owner.must(200, "GET", base+"/ab-tests/learnings", nil)["data"].([]any)
	if len(lib) != 1 || lib[0].(map[string]any)["learning"] != nil || lib[0].(map[string]any)["outcome"] != "No visitors were counted." {
		t.Fatalf("library without AI: %v", lib)
	}
	if c := owner.must(409, "POST", base+"/ab-tests/"+id1+"/learning", nil)["error"].(map[string]any)["code"]; c != "connector_needed" {
		t.Fatalf("learning without AI: %v", c)
	}

	// With one, ending a test queues its learning.
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})
	second := owner.must(201, "POST", base+"/ab-tests/"+id1+"/duplicate", nil)
	id2 := second["id"].(string)
	owner.must(200, "POST", base+"/ab-tests/"+id2+"/start", nil)
	owner.must(200, "POST", base+"/ab-tests/"+id2+"/end", map[string]any{"decision": ""})
	runs := waitFor(t, owner, base+"/ab-test-ai-runs?kind=learning", func(m map[string]any) bool {
		l := m["data"].([]any)
		return len(l) == 1 && l[0].(map[string]any)["status"] == "ready"
	})["data"].([]any)
	out := runs[0].(map[string]any)["output"].(map[string]any)
	if out["headline"] != "Shorter headlines lift signups" || strings.Join(toStrings(out["tags"]), ",") != "headline,pricing" {
		t.Fatalf("learning: %v", out)
	}
	// It is kept on the test, and the library finds it by its words.
	found := owner.must(200, "GET", base+"/ab-tests/learnings?q=five-word", nil)["data"].([]any)
	if len(found) != 1 || found[0].(map[string]any)["id"] != id2 {
		t.Fatalf("search: %v", found)
	}
	if l := owner.must(200, "GET", base+"/ab-tests/learnings?q=nothing-like-this", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("search for nothing: %v", l)
	}
	// Rewriting it by hand.
	owner.must(202, "POST", base+"/ab-tests/"+id1+"/learning", nil)

	// A question is answered over the finished tests, citing real ones.
	owner.must(400, "POST", base+"/ab-tests/ask", map[string]any{"question": ""})
	r := owner.must(202, "POST", base+"/ab-tests/ask", map[string]any{"question": "What do we know about headlines?"})
	ans := waitFor(t, owner, base+"/ab-test-ai-runs/"+r["id"].(string), func(m map[string]any) bool { return m["status"] == "ready" || m["status"] == "failed" })
	ao := ans["output"].(map[string]any)
	if ans["status"] != "ready" || ao["answer"] != "Short headlines won before." || ao["tests_considered"] != 2.0 || len(ao["test_ids"].([]any)) != 1 {
		t.Fatalf("answer: %v", ans)
	}

	abAIPrompts.Lock()
	sent := abAIPrompts.last
	abAIPrompts.Unlock()
	for kind, want := range map[string][]string{
		"learning": {"Long vs short", "Short headlines read faster", "Outcome:", "Never invent facts"},
		"ask":      {"What do we know about headlines?", "Long vs short", "Learning: A five-word headline"},
	} {
		for _, w := range want {
			if !strings.Contains(sent[kind], w) {
				t.Errorf("%s prompt lacks %q", kind, w)
			}
		}
	}
	seen := map[string]int{}
	for _, ev := range got() {
		seen[ev.Name]++
	}
	if seen["ab_test.ai_requested"] < 3 || seen["ab_test.ai_ready"] < 2 {
		t.Errorf("events: %v", seen)
	}
}
