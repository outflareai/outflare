package api_test

import (
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestCommunityLinks(t *testing.T) {
	got := track.Capture(t)
	h := freshEnvWith(t, func(o *api.Options) {
		o.CommunityLinks = true
		o.CommunityXURL = "https://x.example/outflare"
	}).h
	owner, _ := setupOwner(t, h)

	// Both items, neither done, and the app shows them.
	c := owner.must(200, "GET", "/v1/me/community", nil)
	links := c["links"].([]any)
	if len(links) != 2 || c["show"] != true || c["hidden"] != false {
		t.Fatalf("new person: %v", c)
	}
	if gh := links[0].(map[string]any); gh["id"] != "github" || gh["url"] != "https://github.com/outflareai/outflare" || gh["done"] != false {
		t.Fatalf("github item: %v", gh)
	}

	// Opening a link ticks it; opening it again is not news. Unknown links
	// are refused.
	owner.must(400, "PATCH", "/v1/me/community", map[string]any{"clicked": "myspace"})
	c = owner.must(200, "PATCH", "/v1/me/community", map[string]any{"clicked": "github"})
	owner.must(200, "PATCH", "/v1/me/community", map[string]any{"clicked": "github"})
	if c["links"].([]any)[0].(map[string]any)["done"] != true || c["show"] != true {
		t.Fatalf("after starring: %v", c)
	}
	// With both done there is nothing left to show.
	if c = owner.must(200, "PATCH", "/v1/me/community", map[string]any{"clicked": "x"}); c["show"] != false {
		t.Fatalf("after following: %v", c)
	}

	// Hiding is remembered and can be undone.
	if c = owner.must(200, "PATCH", "/v1/me/community", map[string]any{"hidden": true}); c["hidden"] != true || c["hidden_at"] == nil {
		t.Fatalf("hidden: %v", c)
	}
	owner.must(200, "PATCH", "/v1/me/community", map[string]any{"hidden": true})
	if c = owner.must(200, "PATCH", "/v1/me/community", map[string]any{"hidden": false}); c["hidden"] != false {
		t.Fatalf("shown again: %v", c)
	}

	// How they heard about Outflare: one choice from the list, changeable.
	if c["heard_from"] != nil {
		t.Fatalf("heard_from before answering: %v", c["heard_from"])
	}
	owner.must(400, "PATCH", "/v1/me/community", map[string]any{"heard_from": "a friend at Acme"})
	owner.must(200, "PATCH", "/v1/me/community", map[string]any{"heard_from": "x"})
	owner.must(200, "PATCH", "/v1/me/community", map[string]any{"heard_from": "x"})
	if c = owner.must(200, "PATCH", "/v1/me/community", map[string]any{"heard_from": "ai_assistant"}); c["heard_from"] != "ai_assistant" {
		t.Fatalf("heard_from: %v", c)
	}

	// Each click and each hide is an event, and repeats are not.
	ev := got()
	if hf := named(ev, "community.heard_from_answered"); len(hf) != 2 || hf[1].Props["source"] != "ai_assistant" {
		t.Fatalf("heard_from events: %+v", hf)
	}
	if cl := named(ev, "community.link_clicked"); len(cl) != 2 || cl[0].Props["target_id"] != "github" || cl[1].Props["target_id"] != "x" {
		t.Fatalf("click events: %+v", cl)
	}
	if len(named(ev, "community.links_hidden")) != 1 || len(named(ev, "community.links_shown")) != 1 {
		t.Fatalf("hide events: %+v", ev)
	}
}

func TestCommunityLinksOff(t *testing.T) {
	// Off by the operator (and in tests by default): nothing to show or tick.
	owner, _ := setupOwner(t, freshServer(t))
	if c := owner.must(200, "GET", "/v1/me/community", nil); len(c["links"].([]any)) != 0 || c["show"] != false {
		t.Fatalf("off: %v", c)
	}
	owner.must(400, "PATCH", "/v1/me/community", map[string]any{"clicked": "github"})

	// Without an X profile, only GitHub is listed.
	owner, _ = setupOwner(t, freshEnvWith(t, func(o *api.Options) { o.CommunityLinks = true }).h)
	if c := owner.must(200, "GET", "/v1/me/community", nil); len(c["links"].([]any)) != 1 {
		t.Fatalf("no X profile: %v", c)
	}
}

func TestCommunityBuilderLink(t *testing.T) {
	h := freshEnvWith(t, func(o *api.Options) {
		o.CommunityLinks = true
		o.CommunityXURL = "https://x.example/outflare"
		o.CommunityBuilderURL = "https://x.example/builder"
	}).h
	owner, _ := setupOwner(t, h)
	c := owner.must(200, "GET", "/v1/me/community", nil)
	links := c["links"].([]any)
	if len(links) != 3 {
		t.Fatalf("links: %v", links)
	}
	if b := links[2].(map[string]any); b["id"] != "builder" || b["url"] != "https://x.example/builder" || !strings.HasPrefix(b["title"].(string), "Follow the builder") {
		t.Fatalf("builder item: %v", b)
	}
	// Following the builder is an item like the others: ticked once opened,
	// and the card stays until every item is done.
	owner.must(200, "PATCH", "/v1/me/community", map[string]any{"clicked": "github"})
	if c = owner.must(200, "PATCH", "/v1/me/community", map[string]any{"clicked": "x"}); c["show"] != true {
		t.Fatalf("builder still to follow: %v", c)
	}
	if c = owner.must(200, "PATCH", "/v1/me/community", map[string]any{"clicked": "builder"}); c["show"] != false || c["links"].([]any)[2].(map[string]any)["done"] != true {
		t.Fatalf("after following the builder: %v", c)
	}
}

func TestCommunityOnboardingStep(t *testing.T) {
	h := freshEnvWith(t, func(o *api.Options) {
		o.CommunityLinks = true
		o.CommunityXURL = "https://x.example/outflare"
	}).h
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws + "/onboarding"
	step := func(o map[string]any) map[string]any {
		for _, s := range o["steps"].([]any) {
			if st := s.(map[string]any); st["id"] == "community" {
				return st
			}
		}
		return nil
	}

	// The walk-through asks to star and follow, before the first mission.
	o := owner.must(200, "GET", base, nil)
	if st := step(o); st == nil || st["walkthrough"] != true || st["done"] != false || o["total"] != 8.0 {
		t.Fatalf("community step: %v", o)
	}
	ids := []string{}
	for _, s := range o["steps"].([]any) {
		ids = append(ids, s.(map[string]any)["id"].(string))
	}
	if strings.Join(ids, ",") != "brand,ai,goal,website,connect,community,mission,team" {
		t.Fatalf("order: %v", ids)
	}

	// Opening one link isn't enough; opening both does the step.
	owner.must(200, "PATCH", "/v1/me/community", map[string]any{"clicked": "github"})
	if st := step(owner.must(200, "GET", base, nil)); st["done"] != false {
		t.Fatalf("after starring only: %v", st)
	}
	owner.must(200, "PATCH", "/v1/me/community", map[string]any{"clicked": "x"})
	if o = owner.must(200, "GET", base, nil); step(o)["done"] != true || o["done"] != 1.0 {
		t.Fatalf("after both: %v", o)
	}

	// It can be skipped like any other step.
	owner.must(200, "PATCH", base, map[string]any{"skip": []string{"community"}})

	// Without community links there is no such step.
	owner, ws = setupOwner(t, freshServer(t))
	if st := step(owner.must(200, "GET", "/v1/workspaces/"+ws+"/onboarding", nil)); st != nil {
		t.Fatalf("links off: %v", st)
	}
}
