package api_test

import (
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestFlowKits(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	// A flow kit's lists, rules and journeys show in the preview, and its
	// inputs in the catalog.
	k := owner.must(200, "GET", "/v1/kits/trial-activation", nil)
	if len(k["inputs"].([]any)) != 2 || len(k["journeys"].([]any)) != 1 {
		t.Fatalf("kit: %v", k)
	}
	plan := owner.must(200, "GET", base+"/kits/trial-activation/preview", nil)
	acts := kitActions(plan)
	if acts["smart_list:trial_not_activated"] != "add" || acts["rule:stalled_after_signup"] != "add" || acts["journey:activate_new_trials"] != "add" ||
		acts["email_template:activation-nudge"] != "add" {
		t.Fatalf("preview: %v", acts)
	}

	// Inputs are checked before anything is added.
	owner.must(400, "POST", base+"/kits/trial-activation/apply", nil)
	owner.must(400, "POST", base+"/kits/trial-activation/apply", map[string]any{"inputs": map[string]any{"activated_event": "Project Created"}})
	owner.must(400, "POST", base+"/kits/trial-activation/apply", map[string]any{"inputs": map[string]any{"activated_event": "project_created", "plan": "pro"}})

	// A list of the workspace's own with the kit's list's name is a conflict.
	list := owner.must(201, "POST", base+"/smart-lists", map[string]any{"name": "Trials not activated",
		"rules": []map[string]any{{"event": "signed_up", "op": "exists"}}})
	if kitActions(owner.must(200, "GET", base+"/kits/trial-activation/preview", nil))["smart_list:trial_not_activated"] != "conflict" {
		t.Fatal("a list of the same name isn't a conflict")
	}
	res := owner.must(409, "POST", base+"/kits/trial-activation/apply", map[string]any{"inputs": map[string]any{"activated_event": "project_created"}})
	if !strings.Contains(res["error"].(map[string]any)["message"].(string), "Trials not activated") {
		t.Fatalf("conflict: %v", res)
	}
	owner.must(204, "DELETE", base+"/smart-lists/"+list["id"].(string), nil)

	// Applied, each part is a draft that remembers its kit, with the
	// inputs filled in.
	plan = owner.must(200, "POST", base+"/kits/trial-activation/apply", map[string]any{"inputs": map[string]any{"activated_event": "project_created"}})
	if plan["applied"] != true || plan["conflicts"] != float64(0) {
		t.Fatalf("apply: %v", plan)
	}
	lists := owner.must(200, "GET", base+"/smart-lists", nil)["data"].([]any)
	if len(lists) != 1 || lists[0].(map[string]any)["kit"].(map[string]any)["key"] != "trial_not_activated" {
		t.Fatalf("lists: %v", lists)
	}
	if r := lists[0].(map[string]any)["rules"].([]any); r[1].(map[string]any)["event"] != "project_created" || r[0].(map[string]any)["event"] != "signed_up" {
		t.Fatalf("list rules: %v", r)
	}
	rls := owner.must(200, "GET", base+"/rules", nil)["data"].([]any)
	rule := rls[0].(map[string]any)
	if len(rls) != 1 || rule["status"] != "draft" || rule["trigger"].(map[string]any)["event"] != "project_created" || rule["emit_event"] != "trial.stalled" ||
		rule["kit"].(map[string]any)["id"] != "trial-activation" {
		t.Fatalf("rules: %v", rls)
	}
	js := owner.must(200, "GET", base+"/journeys", nil)["data"].([]any)
	j := js[0].(map[string]any)
	if len(js) != 1 || j["status"] != "draft" || j["goal_event"] != "project_created" || len(j["steps"].([]any)) != 5 ||
		j["kit"].(map[string]any)["key"] != "activate_new_trials" {
		t.Fatalf("journeys: %v", js)
	}
	// Its email drafts are unpublished, so the journey can't go live yet.
	owner.must(400, "POST", base+"/journeys/"+j["id"].(string)+"/activate", nil)

	// Applying it again leaves every part as it is.
	plan = owner.must(200, "POST", base+"/kits/trial-activation/apply", map[string]any{"inputs": map[string]any{"activated_event": "first_login"}})
	acts = kitActions(plan)
	if plan["changes"] != float64(0) || acts["journey:activate_new_trials"] != "reuse" || acts["rule:stalled_after_signup"] != "reuse" {
		t.Fatalf("again: %v", plan)
	}
	if n := len(owner.must(200, "GET", base+"/journeys", nil)["data"].([]any)); n != 1 {
		t.Fatalf("journeys after applying again: %d", n)
	}

	// Archived, the kit's journey is added afresh.
	owner.must(200, "POST", base+"/journeys/"+j["id"].(string)+"/archive", nil)
	if kitActions(owner.must(200, "GET", base+"/kits/trial-activation/preview", nil))["journey:activate_new_trials"] != "add" {
		t.Fatal("an archived kit journey still counts")
	}

	// Every shipped flow kit applies in one workspace, side by side.
	owner.must(400, "POST", base+"/kits/outbound-handoff/apply", map[string]any{"inputs": map[string]any{"high_intent_event": "demo_requested",
		"sequence": "00000000-0000-4000-8000-000000000001"}})
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "Demo follow-up", "steps": []map[string]any{{"subject": "Hello", "body": "Hi"}}})["id"].(string)
	for id, inputs := range map[string]map[string]any{
		"win-back":         {"payment_failed_event": "payment.failed"},
		"outbound-handoff": {"high_intent_event": "demo_requested", "sequence": seq},
		"account-health":   {"usage_event": "project_updated", "key_plan": "enterprise", "nps_event": "nps.submitted"},
	} {
		if plan := owner.must(200, "POST", base+"/kits/"+id+"/apply", map[string]any{"inputs": inputs}); plan["applied"] != true {
			t.Fatalf("%s: %v", id, plan)
		}
	}
	for _, j := range owner.must(200, "GET", base+"/journeys", nil)["data"].([]any) {
		j := j.(map[string]any)
		if j["kit"].(map[string]any)["key"] == "follow_up_high_intent" && j["steps"].([]any)[0].(map[string]any)["sequence_id"] != seq {
			t.Fatalf("the sequence input: %v", j["steps"])
		}
	}
	// Six from these three, beside the archived trial journey.
	if n := len(owner.must(200, "GET", base+"/journeys", nil)["data"].([]any)); n != 6 {
		t.Fatalf("journeys from the three kits: %d", n)
	}

	// Each part's own event, as if made by hand (the workspace's own list
	// above is the second smart_list.created).
	seen := map[string]int{}
	for _, ev := range got() {
		seen[ev.Name]++
	}
	if seen["smart_list.created"] != 5 || seen["rule.created"] != 3 || seen["journey.created"] != 7 || seen["kit.applied"] != 5 {
		t.Fatalf("events: %v", seen)
	}
}
