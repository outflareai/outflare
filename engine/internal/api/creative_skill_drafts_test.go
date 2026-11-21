package api_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

var creativeSkillDraftBodies struct {
	sync.Mutex
	all []string
}

// answerCreativeSkillDraft answers fakeClaude's describe-your-ad calls: a
// draft with no picture prompt first, then a good one once told why.
func answerCreativeSkillDraft(w http.ResponseWriter, body string) {
	creativeSkillDraftBodies.Lock()
	creativeSkillDraftBodies.all = append(creativeSkillDraftBodies.all, body)
	creativeSkillDraftBodies.Unlock()
	if !strings.Contains(body, "cannot be used") {
		answerJSON(w, map[string]any{"name": "Night shift", "summary": "x", "picture": "", "copy": "x"})
		return
	}
	answerJSON(w, map[string]any{"name": "Night shift", "summary": "A tired hiker at dusk, leading with proof.",
		"picture": "A hiker at dusk checking a map by torchlight, calm empty sky on the left for words. No text in the picture.",
		"copy":    "Headline of at most 8 words that leads with {Proof point}. Second line says what the pack does. Button: Start free."})
}

// TestCreativeSkillDrafts: an ad described in words becomes the draft of
// a skill on the workspace's own AI model only, and saves as a skill.
func TestCreativeSkillDrafts(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	e.runServerJobs(t)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	owner.must(200, "PATCH", "/v1/workspaces/"+ws+"/brands/"+brand+"/context", map[string]any{"about": "Northpeak makes ultralight packs for long trails."})

	if c := owner.must(409, "POST", base+"/creative-skill-drafts", map[string]any{"description": "An ad for hikers"})["error"].(map[string]any)["code"]; c != "connector_needed" {
		t.Fatalf("no AI: %v", c)
	}
	owner.must(201, "POST", "/v1/workspaces/"+ws+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})
	owner.must(400, "POST", base+"/creative-skill-drafts", map[string]any{"description": " "})
	owner.must(400, "POST", base+"/creative-skill-drafts", map[string]any{"description": strings.Repeat("x", 1001)})
	owner.must(404, "GET", base+"/creative-skill-drafts/00000000-0000-0000-0000-000000000000", nil)

	const said = "An ad for hikers tired of heavy packs; lead with our weight"
	r := owner.must(202, "POST", base+"/creative-skill-drafts", map[string]any{"description": said})
	if r["status"] != "queued" || r["description"] != said || r["picture"] != "" {
		t.Fatalf("drafted: %v", r)
	}
	d := waitFor(t, owner, base+"/creative-skill-drafts/"+r["id"].(string), func(m map[string]any) bool { return m["status"] == "ready" || m["status"] == "failed" })
	if d["status"] != "ready" || d["name"] != "Night shift" || !strings.Contains(d["picture"].(string), "torchlight") || !strings.Contains(d["copy"].(string), "{Proof point}") {
		t.Fatalf("draft: %v", d)
	}
	creativeSkillDraftBodies.Lock()
	bodies := strings.Join(creativeSkillDraftBodies.all, "\n")
	n := len(creativeSkillDraftBodies.all)
	creativeSkillDraftBodies.Unlock()
	if n != 2 || !strings.Contains(bodies, "ultralight packs") || !strings.Contains(bodies, "tired of heavy packs") || !strings.Contains(bodies, "left the picture prompt empty") {
		t.Fatalf("the model was asked %d times, or without the brand's context, the description or the engine's reason", n)
	}

	// The draft saves as a skill as it is; only the picture model is the person's.
	skill := owner.must(201, "POST", base+"/creative-skills", map[string]any{"name": d["name"], "description": d["summary"], "kind": "image", "model": "gpt-image-1",
		"variants": []map[string]any{{"picture": d["picture"], "copy": d["copy"]}}})
	if fill := skill["current"].(map[string]any)["variants"].([]any)[0].(map[string]any)["fill_ins"].([]any); len(fill) != 1 || fill[0] != "proof_point" {
		t.Fatalf("fill-ins: %v", fill)
	}

	// Agents read drafts through Query.
	q := owner.must(200, "POST", base+"/query", map[string]any{"sql": "select name, status from creative_skill_drafts"})
	if rows := q["rows"].([]any); len(rows) != 1 {
		t.Fatalf("query: %v", q)
	}

	seen := map[string]int{}
	for _, ev := range got() {
		seen[ev.Name]++
		if strings.HasPrefix(ev.Name, "creative_skill_draft.") {
			for k, v := range ev.Props {
				if s, ok := v.(string); ok && strings.Contains(s, "hikers") {
					t.Fatalf("%s carries the description in %s", ev.Name, k)
				}
			}
		}
	}
	if seen["creative_skill_draft.requested"] != 1 || seen["creative_skill_draft.written"] != 1 {
		t.Fatalf("events: %v", seen)
	}
}
