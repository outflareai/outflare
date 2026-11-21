package api_test

import (
	"strings"
	"testing"
)

// Agent work in Query: actions and mission calls without the request
// bodies or results (they can carry keys an action set up), each behind
// its own scope.
func TestQueryAgentWork(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws
	np := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	for _, sql := range []string{
		"select count(*) from actions",
		"select count(*) from mission_actions",
		"select count(*) from agent_runs",
		"select count(*) from ab_tests",
		"select count(*) from social_publications",
		"select count(*) from content_items",
	} {
		owner.must(200, "POST", "/v1/brands/"+np+"/query", map[string]any{"sql": sql})
	}
	for _, sql := range []string{
		"select body from actions",
		"select result from actions",
		"select body from mission_actions",
		"select result from mission_actions",
		"select address from email_broadcast_recipients",
	} {
		code, got := owner.do("POST", "/v1/brands/"+np+"/query", map[string]any{"sql": sql})
		if code != 400 || !strings.Contains(strings.ToLower(got["error"].(map[string]any)["message"].(string)), "does not exist") {
			t.Fatalf("%s: %d %v", sql, code, got)
		}
	}
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "CRM agent", "scopes": []string{"customers.read"}, "brand": "northpeak"})
	agent := &client{t: t, h: h, bearer: tok["token"].(string)}
	if code, _ := agent.do("POST", "/v1/brands/"+np+"/query", map[string]any{"sql": "select count(*) from actions"}); code == 200 {
		t.Fatalf("actions read without actions.read")
	}
}
