package api_test

import (
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestStoriesCheckAndApproval(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	bid := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	out := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Agent", "scopes": []string{"seo.read", "seo.manage"}})
	agent := &client{t: t, h: e.h, bearer: out["token"].(string)}

	// Experts: the first is the brand's default; making another the
	// default moves it.
	owner.must(400, "POST", base+"/pr-experts", map[string]any{"brand_id": "northpeak", "name": "Ana Ruiz"})
	ana := owner.must(201, "POST", base+"/pr-experts", map[string]any{"brand_id": "northpeak", "name": "Ana Ruiz", "email": "ana@northpeak.example",
		"title": "CEO", "topics": []string{"bot traffic", "bot traffic", " web data "}})
	if ana["is_default"] != true || ana["name"] != "Ana Ruiz" || ana["email"] != "ana@northpeak.example" || len(ana["topics"].([]any)) != 2 || ana["brand_id"] != bid {
		t.Fatalf("expert: %v", ana)
	}
	lee := owner.must(201, "POST", base+"/pr-experts", map[string]any{"brand_id": "northpeak", "name": "Lee Park", "email": "lee@northpeak.example"})
	if lee["is_default"] != false {
		t.Fatalf("second expert: %v", lee)
	}
	owner.must(200, "PATCH", base+"/pr-experts/"+lee["id"].(string), map[string]any{"is_default": true})
	experts := owner.must(200, "GET", base+"/pr-experts?brand=northpeak", nil)["data"].([]any)
	if len(experts) != 2 || experts[0].(map[string]any)["id"] != lee["id"] || experts[1].(map[string]any)["is_default"] != false {
		t.Fatalf("experts: %v", experts)
	}
	owner.must(200, "PATCH", base+"/pr-experts/"+ana["id"].(string), map[string]any{"is_default": true})

	// A story starts as an idea and becomes a draft with a release.
	owner.must(400, "POST", base+"/stories", map[string]any{"brand_id": "northpeak", "title": ""})
	owner.must(400, "POST", base+"/stories", map[string]any{"brand_id": "northpeak", "title": "x", "kind": "gossip"})
	story := owner.must(201, "POST", base+"/stories", map[string]any{"brand_id": "northpeak", "title": "A third of sites block their own customers' bots"})
	if story["status"] != "idea" || story["kind"] != "data" || story["source"] != "manual" || len(story["facts"].([]any)) != 0 {
		t.Fatalf("story: %v", story)
	}
	sid := story["id"].(string)
	path := base + "/stories/" + sid
	release := `Northpeak measured 1,200 sites in October. 37% blocked the bots their own customers use. "Blocking every bot also blocks the people your customers send," said Ana Ruiz, CEO.`
	if s := owner.must(200, "PATCH", path, map[string]any{"release": release}); s["status"] != "draft" {
		t.Fatalf("draft: %v", s)
	}

	// The check: the numbers have no facts yet, and the quote isn't approved.
	res := owner.must(200, "POST", path+"/check", nil)
	if res["ok"] != false || len(res["errors"].([]any)) != 3 {
		t.Fatalf("first check: %v", res)
	}
	owner.must(409, "POST", path+"/approve", nil)

	// Facts with sources, and a quote from the default expert.
	owner.must(400, "POST", path+"/facts", map[string]any{"statement": "x", "source_kind": "rumour"})
	owner.must(201, "POST", path+"/facts", map[string]any{"statement": "We measured 1,200 sites in October", "source_kind": "own_data",
		"source_note": "Query: sites measured, October"})
	s := owner.must(201, "POST", path+"/facts", map[string]any{"statement": "37% blocked their customers' bots", "source_kind": "own_data",
		"source_url": "https://northpeak.example/data/bots"})
	if len(s["facts"].([]any)) != 2 {
		t.Fatalf("facts: %v", s["facts"])
	}
	s = owner.must(201, "POST", path+"/quotes", map[string]any{"body": "Blocking every bot also blocks the people your customers send."})
	quote := s["quotes"].([]any)[0].(map[string]any)
	if quote["expert_id"] != ana["id"] || quote["status"] != "draft" {
		t.Fatalf("quote: %v", quote)
	}
	qpath := path + "/quotes/" + quote["id"].(string)

	// Only a person records an expert's approval, or approves a release.
	agent.must(403, "PATCH", qpath, map[string]any{"status": "approved"})
	owner.must(200, "PATCH", qpath, map[string]any{"status": "approved", "approval_note": "Ana said yes by email"})
	agent.must(403, "POST", path+"/approve", nil)
	s = owner.must(200, "POST", path+"/approve", nil)
	if s["status"] != "approved" || s["approved_at"] == nil || s["last_check"].(map[string]any)["ok"] != true {
		t.Fatalf("approved: %v", s)
	}

	// Changing the quote's words takes both approvals away.
	s = owner.must(200, "PATCH", qpath, map[string]any{"body": "Blocking every bot blocks the people your customers send, too."})
	if s["status"] != "draft" || s["quotes"].([]any)[0].(map[string]any)["status"] != "draft" || s["approved_at"] != nil {
		t.Fatalf("after changing the quote: %v", s)
	}
	owner.must(400, "PATCH", qpath, map[string]any{"body": "x", "status": "approved"})
	if res := owner.must(200, "POST", path+"/check", nil); res["ok"] != false {
		t.Fatalf("check with an unapproved quote: %v", res)
	}
	owner.must(200, "PATCH", path, map[string]any{"release": "Northpeak measured 1,200 sites. A revolutionary 37% blocked bots."})
	if res := owner.must(200, "POST", path+"/check", nil); res["ok"] != true || len(res["warnings"].([]any)) != 1 {
		t.Fatalf("check with hype: %v", res)
	}
	owner.must(200, "POST", path+"/approve", nil)
	if s := owner.must(200, "PATCH", path, map[string]any{"summary": "Notes only"}); s["status"] != "approved" {
		t.Fatalf("a summary change keeps the approval: %v", s)
	}
	if s := owner.must(200, "PATCH", path, map[string]any{"title": "New headline"}); s["status"] != "draft" {
		t.Fatalf("a headline change takes it away: %v", s)
	}
	owner.must(400, "PATCH", path, map[string]any{"status": "approved"})
	owner.must(200, "PATCH", path, map[string]any{"status": "archived"})
	if n := owner.must(200, "GET", base+"/stories", nil)["total"]; n != 0.0 {
		t.Fatalf("archived stories leave the list: %v", n)
	}
	if n := owner.must(200, "GET", base+"/stories?status=archived", nil)["total"]; n != 1.0 {
		t.Fatalf("archived: %v", n)
	}

	// A quote needs an expert of the story's brand.
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "handle": "acme", "website": "https://acme.example"})
	other := owner.must(201, "POST", base+"/stories", map[string]any{"brand_id": "acme", "title": "Acme story"})["id"].(string)
	owner.must(400, "POST", base+"/stories/"+other+"/quotes", map[string]any{"body": "No expert yet."})
	owner.must(400, "POST", base+"/stories/"+other+"/quotes", map[string]any{"body": "Not Acme's.", "expert_id": ana["id"]})
	owner.must(204, "DELETE", base+"/stories/"+other, nil)
	owner.must(204, "DELETE", base+"/pr-experts/"+lee["id"].(string), nil)

	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
	}
	if names["story.approved"] != 2 || names["story_quote.approved"] != 1 || names["pr_expert.created"] != 2 || names["story_fact.added"] != 2 ||
		names["story.checked"] != 3 || names["story_quote.reopened"] != 1 || names["story.deleted"] != 1 {
		t.Fatalf("events: %v", names)
	}
}
