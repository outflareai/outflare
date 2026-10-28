package api_test

import (
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestOnboardingStepsSkipsAndDismissal(t *testing.T) {
	got := track.Capture(t)
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws

	stepsOf := func(o map[string]any) map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, s := range o["steps"].([]any) {
			st := s.(map[string]any)
			out[st["id"].(string)] = st
		}
		return out
	}

	// A new workspace has every step to do, and the app opens the walk-through.
	o := owner.must(200, "GET", base+"/onboarding", nil)
	if o["done"] != 0.0 || o["total"] != 7.0 || o["next_step"] != "brand" || o["complete"] != false || o["show"] != true || o["dismissed"] != false || o["goal"] != nil {
		t.Fatalf("new workspace: %v", o)
	}
	if st := stepsOf(o)["brand"]; !strings.HasSuffix(st["page"].(string), "/brands") || st["tool"] != "create_brand" || st["walkthrough"] != true {
		t.Fatalf("brand step: %v", st)
	}
	if st := stepsOf(o)["team"]; st["walkthrough"] != false {
		t.Fatalf("inviting the team is left to Home: %v", st)
	}

	// Skipping a step moves on and stops the app opening it by itself;
	// unknown steps and goals are refused.
	owner.must(400, "PATCH", base+"/onboarding", map[string]any{"skip": []string{"billing"}})
	owner.must(400, "PATCH", base+"/onboarding", map[string]any{"goal": "world_domination"})
	o = owner.must(200, "PATCH", base+"/onboarding", map[string]any{"skip": []string{"brand", "brand"}})
	if o["next_step"] != "ai" || stepsOf(o)["brand"]["skipped"] != true || o["show"] != false || o["brand"] != nil {
		t.Fatalf("after skipping brand: %v", o)
	}

	// Picking a goal does the goal step; picking the same one again is not news.
	o = owner.must(200, "PATCH", base+"/onboarding", map[string]any{"goal": "search"})
	owner.must(200, "PATCH", base+"/onboarding", map[string]any{"goal": "search"})
	if o["goal"] != "search" || stepsOf(o)["goal"]["done"] != true || o["next_step"] != "ai" {
		t.Fatalf("after picking a goal: %v", o)
	}

	// Doing a step anywhere ticks it off, skipped or not.
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "website": "acme-com.example"})
	owner.must(201, "POST", base+"/invites", map[string]string{"email": "sam@example.com", "role": "operator"})
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})
	o = owner.must(200, "GET", base+"/onboarding", nil)
	st := stepsOf(o)
	if st["brand"]["done"] != true || st["brand"]["skipped"] != false || st["team"]["done"] != true || st["connect"]["done"] != true || st["ai"]["done"] != false || st["mission"]["done"] != false {
		t.Fatalf("after doing steps: %v", o)
	}
	if o["done"] != 4.0 || o["next_step"] != "ai" {
		t.Fatalf("progress: %v", o)
	}
	if b, _ := o["brand"].(map[string]any); b == nil || b["handle"] != "acme" || b["website"] != "acme-com.example" {
		t.Fatalf("the walk-through's brand: %v", o["brand"])
	}
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})
	if o = owner.must(200, "GET", base+"/onboarding", nil); stepsOf(o)["ai"]["done"] != true || o["next_step"] != "website" {
		t.Fatalf("an AI model connects the AI: %v", o)
	}

	// The website step is done once the script is waiting for its first
	// visit and Outflare is looking for the DNS record.
	owner.must(200, "POST", base+"/brands/acme/setup/prepare", map[string]any{"step": "site_script"})
	if o = owner.must(200, "GET", base+"/onboarding", nil); stepsOf(o)["website"]["done"] != false {
		t.Fatalf("only the script: %v", o)
	}
	owner.must(200, "POST", base+"/brands/acme/setup/prepare", map[string]any{"step": "verify_site"})
	owner.must(200, "POST", base+"/brands/acme/setup/check", map[string]any{"step": "verify_site"})
	if o = owner.must(200, "GET", base+"/onboarding", nil); stepsOf(o)["website"]["done"] != true || o["next_step"] != "mission" {
		t.Fatalf("script and DNS under way: %v", o)
	}
	owner.must(201, "POST", base+"/missions", map[string]any{"name": "First", "goal": "Find the pages losing traffic"})
	if o = owner.must(200, "GET", base+"/onboarding", nil); stepsOf(o)["mission"]["done"] != true || o["complete"] != true || o["next_step"] != nil {
		t.Fatalf("a mission completes it: %v", o)
	}

	// Skipping a step that is done changes nothing; unskipping brings a
	// skipped one back.
	owner.must(200, "PATCH", base+"/onboarding", map[string]any{"skip": []string{"team"}})
	if o = owner.must(200, "PATCH", base+"/onboarding", map[string]any{"unskip": []string{"team"}}); o["complete"] != true {
		t.Fatalf("unskipped a done step: %v", o)
	}

	// Closing it is remembered, and it can be opened again.
	if o = owner.must(200, "PATCH", base+"/onboarding", map[string]any{"dismissed": true}); o["dismissed"] != true || o["dismissed_at"] == nil || o["show"] != false {
		t.Fatalf("dismissed: %v", o)
	}
	owner.must(200, "PATCH", base+"/onboarding", map[string]any{"dismissed": true})
	if o = owner.must(200, "PATCH", base+"/onboarding", map[string]any{"dismissed": false}); o["dismissed"] != false {
		t.Fatalf("reopened: %v", o)
	}

	// Each change is an event naming the step, and repeats are not.
	ev := got()
	if s := named(ev, "onboarding.step_skipped"); len(s) != 2 || s[0].Props["target_id"] != "brand" || s[1].Props["target_id"] != "team" {
		t.Fatalf("skipped events: %+v", s)
	}
	if g := named(ev, "onboarding.goal_chosen"); len(g) != 1 || g[0].Props["target_id"] != "search" {
		t.Fatalf("goal events: %+v", g)
	}
	if len(named(ev, "onboarding.step_unskipped")) != 1 || len(named(ev, "onboarding.reopened")) != 1 {
		t.Fatalf("unskip and reopen events: %+v", ev)
	}
	if d := named(ev, "onboarding.dismissed"); len(d) != 1 || d[0].WorkspaceID != ws {
		t.Fatalf("dismissed event: %+v", d)
	}

	// Viewers can read the steps but not change them.
	inv := owner.must(201, "POST", base+"/invites", map[string]string{"email": "viv@example.com", "role": "viewer"})
	viewer := &client{t: t, h: h}
	viewer.must(200, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "name": "Viv", "password": "viv long password"})
	viewer.must(200, "GET", base+"/onboarding", nil)
	viewer.must(403, "PATCH", base+"/onboarding", map[string]any{"dismissed": true})
}
