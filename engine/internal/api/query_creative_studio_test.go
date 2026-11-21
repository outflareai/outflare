package api_test

import (
	"strings"
	"testing"
)

// The creative studio in Query: files, provider requests, talking videos
// with their stages and versions, creatives, skills, lessons and history,
// behind content.read, without a file's hash or the request and answer
// exchanged with the provider.
func TestQueryCreativeStudio(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws
	np := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	owner.must(201, "POST", "/v1/brands/"+np+"/creative-lessons", map[string]any{"kind": "say", "word": "Northpeak", "say": "north peek"})
	got := owner.must(200, "POST", "/v1/brands/"+np+"/query", map[string]any{"sql": "select word, say from creative_lessons where removed_at is null"})
	if rows := got["rows"].([]any); len(rows) != 1 || rows[0].([]any)[1] != "north peek" {
		t.Fatalf("lessons: %v", got)
	}
	for _, sql := range []string{
		"select count(*) from brand_assets",
		"select count(*) from brand_generations",
		"select count(*) from brand_videos v join brand_video_steps s on s.video_id = v.id",
		"select count(*) from brand_video_versions",
		"select count(*) from creatives c join creative_skills s on s.id = c.skill_id",
		"select count(*) from creative_skill_versions",
		"select count(*) from creative_history",
	} {
		owner.must(200, "POST", "/v1/brands/"+np+"/query", map[string]any{"sql": sql})
	}
	for _, sql := range []string{
		"select sha256 from brand_assets",
		"select request from brand_generations",
		"select response from brand_generations",
		"select run_id from brand_generations",
	} {
		code, got := owner.do("POST", "/v1/brands/"+np+"/query", map[string]any{"sql": sql})
		if code != 400 || !strings.Contains(strings.ToLower(got["error"].(map[string]any)["message"].(string)), "does not exist") {
			t.Fatalf("%s: %d %v", sql, code, got)
		}
	}
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "CRM agent", "scopes": []string{"customers.read"}, "brand": "northpeak"})
	agent := &client{t: t, h: h, bearer: tok["token"].(string)}
	if code, _ := agent.do("POST", "/v1/brands/"+np+"/query", map[string]any{"sql": "select count(*) from creatives"}); code == 200 {
		t.Fatalf("creatives read without content.read")
	}
}
