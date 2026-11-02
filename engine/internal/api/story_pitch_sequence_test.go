package api_test

import (
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestStoryPitchSequence(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	e.runServerJobs(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	out := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Agent", "scopes": []string{"sequences.read", "sequences.manage", "seo.read", "seo.manage"}})
	agent := &client{t: t, h: e.h, bearer: out["token"].(string)}
	reader := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Reader", "scopes": []string{"sequences.read"}})
	readOnly := &client{t: t, h: e.h, bearer: reader["token"].(string)}
	bid := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	owner.must(201, "POST", base+"/pr-experts", map[string]any{"brand_id": "northpeak", "name": "Ana Ruiz", "email": "ana@northpeak.example", "title": "CEO"})

	// The starter sequence: a draft for the brand, two steps with the
	// headline and the opener, stopping on any reply.
	agent.must(400, "POST", base+"/stories/pitch-sequence", map[string]any{"brand_id": "nobody"})
	agent.must(400, "POST", base+"/stories/pitch-sequence", map[string]any{"brand_id": "northpeak", "name": strings.Repeat("x", 201)})
	readOnly.must(403, "POST", base+"/stories/pitch-sequence", map[string]any{"brand_id": "northpeak"})
	seq := agent.must(201, "POST", base+"/stories/pitch-sequence", map[string]any{"brand_id": "northpeak"})
	steps := seq["steps"].([]any)
	if seq["status"] != "draft" || seq["stop_on_reply"] != true || seq["brand_id"] != bid || len(steps) != 2 || seq["name"] != "Story pitches: Northpeak" {
		t.Fatalf("sequence: %v", seq)
	}
	first, second := steps[0].(map[string]any), steps[1].(map[string]any)
	if first["subject"] != "{{custom.story_title}}" || !strings.Contains(first["body"].(string), "{{custom.opener}}") ||
		second["subject"] != "" || second["delay_days"] != 3.0 || strings.Contains(first["body"].(string)+second["body"].(string), "release_url") {
		t.Fatalf("steps: %v", steps)
	}
	if named := owner.must(201, "POST", base+"/stories/pitch-sequence", map[string]any{"brand_id": bid, "name": "Bot traffic pitches"}); named["name"] != "Bot traffic pitches" {
		t.Fatalf("named: %v", named)
	}

	// It passes startStoryPitches' rules: an approved story is pitched
	// through it.
	sid := owner.must(201, "POST", base+"/stories", map[string]any{"brand_id": "northpeak", "title": "A third of sites block their own customers' bots",
		"release": "Northpeak measured 1,200 sites."})["id"].(string)
	path := base + "/stories/" + sid
	owner.must(201, "POST", path+"/facts", map[string]any{"statement": "We measured 1,200 sites", "source_kind": "own_data", "source_note": "Crawl, October"})
	owner.must(200, "POST", path+"/approve", nil)
	kim := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Kim Lee",
		"identities": []any{map[string]any{"namespace": "email", "value": "kim@news.example"}}})["id"].(string)
	started := agent.must(201, "POST", path+"/pitches", map[string]any{"sequence_id": seq["id"], "record_ids": []string{kim}})
	if len(started["data"].([]any)) != 1 || len(started["skipped"].([]any)) != 0 {
		t.Fatalf("pitched: %v", started)
	}

	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
	}
	if names["story.pitch_sequence_created"] != 2 || names["sequence.created"] < 2 {
		t.Fatalf("events: %v", names)
	}
}
