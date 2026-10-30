package api_test

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// storyPrompts keeps what the story writers were sent, for the test to read.
var storyPrompts struct {
	sync.Mutex
	ideas, draft string
}

// answerStories answers fakeClaude's story ideas and release drafts.
func answerStories(w http.ResponseWriter, body string) {
	storyPrompts.Lock()
	defer storyPrompts.Unlock()
	if strings.Contains(body, "You suggest digital PR story ideas") {
		storyPrompts.ideas = body
		answerJSON(w, map[string]any{"ideas": []map[string]any{
			{"title": "Which sites block the bots their customers use", "kind": "data", "summary": "Count blocks by site from the October crawl.", "article_id": ""},
			{"title": "Our CEO on the new bot rules", "kind": "reactive", "summary": "Comment on the rules.", "article_id": "00000000-0000-0000-0000-000000000000"},
			{"title": "A third idea past the count", "kind": "comment", "summary": "x", "article_id": ""},
		}})
		return
	}
	storyPrompts.draft = body
	answerJSON(w, map[string]any{"release": "# A third of sites block their customers' bots\n\nNorthpeak measured 1,200 sites in October; 37% blocked the bots their own customers use.\n\n" +
		`"Blocking every bot also blocks the people your customers send," said Ana Ruiz, CEO.`})
}

func TestStoriesAI(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	e.runServerJobs(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})
	owner.must(201, "POST", base+"/pr-experts", map[string]any{"brand_id": "northpeak", "name": "Ana Ruiz", "email": "ana@northpeak.example", "title": "CEO", "topics": []string{"bot traffic"}})
	sid := owner.must(201, "POST", base+"/stories", map[string]any{"brand_id": "northpeak", "title": "A third of sites block their own customers' bots"})["id"].(string)
	path := base + "/stories/" + sid

	// Both need the workspace's own AI model.
	if c := owner.must(409, "POST", base+"/stories/ideas", map[string]any{"brand_id": "northpeak"})["error"].(map[string]any)["code"]; c != "connector_needed" {
		t.Fatalf("ideas without AI: %v", c)
	}
	owner.must(409, "POST", path+"/draft", nil)
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})

	// Ideas: as many as asked for, and only an article it was shown.
	owner.must(400, "POST", base+"/stories/ideas", map[string]any{"brand_id": "northpeak", "count": 11})
	owner.must(202, "POST", base+"/stories/ideas", map[string]any{"brand_id": "northpeak", "count": 2, "brief": "Something for the bot traffic beat"})
	ideas := waitFor(t, owner, base+"/stories?status=idea", func(m map[string]any) bool { return m["total"] == 3.0 })["data"].([]any)
	var reactive map[string]any
	for _, x := range ideas {
		if x := x.(map[string]any); x["kind"] == "reactive" {
			reactive = x
		}
	}
	if reactive == nil || reactive["source"] != "ai_ideas" || reactive["article_id"] != nil {
		t.Fatalf("ideas: %v", ideas)
	}
	storyPrompts.Lock()
	for _, want := range []string{"Suggest 2 story ideas", "bot traffic", "Something for the bot traffic beat", "A third of sites block their own customers' bots"} {
		if !strings.Contains(storyPrompts.ideas, want) {
			t.Errorf("ideas prompt has no %q", want)
		}
	}
	storyPrompts.Unlock()

	// A draft uses the facts and only the approved quotes, and is checked.
	owner.must(201, "POST", path+"/facts", map[string]any{"statement": "We measured 1,200 sites in October", "source_kind": "own_data", "source_note": "Crawl, October"})
	owner.must(201, "POST", path+"/facts", map[string]any{"statement": "37% blocked their customers' bots", "source_kind": "own_data", "source_note": "Crawl, October"})
	q := owner.must(201, "POST", path+"/quotes", map[string]any{"body": "Blocking every bot also blocks the people your customers send."})["quotes"].([]any)[0].(map[string]any)
	owner.must(200, "PATCH", path+"/quotes/"+q["id"].(string), map[string]any{"status": "approved"})
	owner.must(201, "POST", path+"/quotes", map[string]any{"body": "A line Ana never approved."})
	owner.must(400, "POST", path+"/draft", map[string]any{"guidance": strings.Repeat("x", 1001)})
	owner.must(202, "POST", path+"/draft", map[string]any{"guidance": "Lead with the 37%"})
	owner.must(409, "POST", path+"/draft", nil)
	d := waitFor(t, owner, path, func(m map[string]any) bool { return m["ai_status"] != "drafting" })
	if d["ai_status"] != "" || d["status"] != "draft" || !strings.Contains(d["release"].(string), "1,200 sites") || d["last_check"].(map[string]any)["ok"] != true {
		t.Fatalf("drafted: %v", d)
	}
	storyPrompts.Lock()
	sent := storyPrompts.draft
	storyPrompts.Unlock()
	for _, want := range []string{"We measured 1,200 sites in October", "Blocking every bot also blocks the people your customers send.", "Ana Ruiz, CEO", "Lead with the 37%"} {
		if !strings.Contains(sent, want) {
			t.Errorf("draft prompt has no %q", want)
		}
	}
	if strings.Contains(sent, "A line Ana never approved") {
		t.Error("the draft prompt has an unapproved quote")
	}

	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
	}
	if names["story.ideas_requested"] != 1 || names["story.ideas_suggested"] != 1 || names["story.draft_requested"] != 1 || names["story.drafted"] != 1 {
		t.Fatalf("events: %v", names)
	}
}
