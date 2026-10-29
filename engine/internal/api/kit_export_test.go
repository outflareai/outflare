package api_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/playbooks"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestExportKit(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	// Flows set up from two shipped kits, as if made by hand.
	owner.must(200, "POST", base+"/kits/trial-activation/apply", map[string]any{"inputs": map[string]any{"activated_event": "project_created"}})
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "Demo follow-up", "steps": []map[string]any{{"subject": "Hello", "body": "Hi"}}})["id"].(string)
	owner.must(200, "POST", base+"/kits/outbound-handoff/apply", map[string]any{"inputs": map[string]any{"high_intent_event": "demo_requested", "sequence": seq}})
	ids := map[string]string{}
	for _, j := range owner.must(200, "GET", base+"/journeys", nil)["data"].([]any) {
		j := j.(map[string]any)
		ids[j["kit"].(map[string]any)["key"].(string)] = j["id"].(string)
	}

	// The trial journey comes out with the rule that starts it and its two
	// emails; the events picked become inputs that default to themselves.
	out := owner.must(200, "POST", base+"/kits/export", map[string]any{"journeys": []string{ids["activate_new_trials"]}, "inputs": []string{"project_created", "signed_up"}})
	k := out["kit"].(map[string]any)
	if k["id"] != "activate-new-trials" || k["title"] != "Activate new trials" || k["version"] != float64(1) {
		t.Fatalf("kit: %v", k)
	}
	if n := len(k["journeys"].([]any)); n != 1 || len(k["rules"].([]any)) != 1 || len(k["email_templates"].([]any)) != 2 || len(k["smart_lists"].([]any)) != 0 {
		t.Fatalf("parts: %v", k)
	}
	rule := k["rules"].([]any)[0].(map[string]any)
	if rule["key"] != "stalled_after_signup" || rule["emit_event"] != "trial.stalled" || rule["trigger"].(map[string]any)["start_event"] != "{{signed_up}}" ||
		rule["trigger"].(map[string]any)["event"] != "{{project_created}}" || rule["fire"] != "once" {
		t.Fatalf("rule: %v", rule)
	}
	j := k["journeys"].([]any)[0].(map[string]any)
	if j["goal_event"] != "{{project_created}}" || j["trigger"].(map[string]any)["event"] != "trial.stalled" || len(j["steps"].([]any)) != 5 {
		t.Fatalf("journey: %v", j)
	}
	var names []string
	for _, ev := range out["events"].([]any) {
		ev := ev.(map[string]any)
		names = append(names, ev["name"].(string))
		if (ev["name"] == "project_created") != (ev["input"] == "project_created") {
			t.Fatalf("event: %v", ev)
		}
	}
	slices.Sort(names)
	if strings.Join(names, ",") != "project_created,signed_up,trial.stalled" {
		t.Fatalf("events: %v", names)
	}
	if !slices.ContainsFunc(out["notes"].([]any), func(n any) bool { return strings.Contains(n.(string), "isn't published") }) {
		t.Fatalf("notes: %v", out["notes"])
	}
	// The file is a kit file like the shipped ones.
	file, err := playbooks.ParseKit([]byte(out["yaml"].(string)))
	if err != nil || file.Validate() != nil || file.Inputs[0].Default != "project_created" {
		t.Fatalf("yaml: %v %v\n%s", err, file.Validate(), out["yaml"])
	}

	// The handoff journeys bring their static list, and the sequence
	// becomes an input. Their own exits stay as they are.
	out = owner.must(200, "POST", base+"/kits/export", map[string]any{"journeys": []string{ids["hand_off_interested"], ids["follow_up_high_intent"]},
		"title": "Hand off warm prospects", "id": "warm-handoff"})
	k = out["kit"].(map[string]any)
	lists := k["smart_lists"].([]any)
	if k["id"] != "warm-handoff" || len(lists) != 1 || lists[0].(map[string]any)["kind"] != "static" {
		t.Fatalf("handoff: %v", k)
	}
	inputs := k["inputs"].([]any)
	if len(inputs) != 1 || inputs[0].(map[string]any)["kind"] != "sequence" || !strings.Contains(out["yaml"].(string), "sequence_id: '{{sequence}}'") ||
		!strings.Contains(out["yaml"].(string), "list_id: '{{smart_list.handed_over}}'") {
		t.Fatalf("handoff inputs: %v\n%s", inputs, out["yaml"])
	}
	handoff := out["yaml"].(string)

	// What can't go in a kit is a 400 that says why.
	owner.must(400, "POST", base+"/kits/export", map[string]any{"journeys": []string{}})
	owner.must(400, "POST", base+"/kits/export", map[string]any{"journeys": []string{"00000000-0000-4000-8000-000000000001"}})
	res := owner.must(400, "POST", base+"/kits/export", map[string]any{"journeys": []string{ids["activate_new_trials"]}, "inputs": []string{"not_used"}})
	if !strings.Contains(res["error"].(map[string]any)["message"].(string), "not_used") {
		t.Fatalf("unused input: %v", res)
	}
	leads := owner.must(201, "POST", base+"/lead-lists", map[string]any{"name": "Q3"})["id"].(string)
	built := owner.must(201, "POST", base+"/smart-lists", map[string]any{"name": "In Q3", "rules": []map[string]any{{"in_list": leads, "op": "exists"}}})["id"].(string)
	owner.must(201, "POST", base+"/rules", map[string]any{"name": "Joined Q3", "trigger": map[string]any{"kind": "audience", "list_id": built, "on": "enter"}, "emit_event": "q3.joined"})
	q3 := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Welcome Q3", "trigger": map[string]any{"kind": "event", "event": "q3.joined"},
		"steps": []map[string]any{{"id": "done", "kind": "exit"}}})["id"].(string)
	res = owner.must(400, "POST", base+"/kits/export", map[string]any{"journeys": []string{q3}})
	if !strings.Contains(res["error"].(map[string]any)["message"].(string), "In Q3") {
		t.Fatalf("in_list: %v", res)
	}

	// A transactional email goes along as a transactional draft, and a deal
	// move with a note; a step that names members can't go at all.
	owner.must(201, "POST", base+"/email-templates", map[string]any{"name": "Welcome pack", "subject": "Your account", "html": "<p>Hi {{person.first_name}}</p>",
		"from": "hello@example.com", "publish": true})
	crm := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Onboard", "steps": []map[string]any{
		{"id": "pack", "kind": "transactional_email", "template": "welcome-pack", "next": "deal"},
		{"id": "deal", "kind": "move_deal", "deal": map[string]any{"type": "deal", "field": "stage", "stage": "qualified"}},
	}})["id"].(string)
	out = owner.must(200, "POST", base+"/kits/export", map[string]any{"journeys": []string{crm}})
	tmpls := out["kit"].(map[string]any)["email_templates"].([]any)
	if len(tmpls) != 1 || tmpls[0].(map[string]any)["purpose"] != "transactional" || tmpls[0].(map[string]any)["handle"] != "welcome-pack" ||
		!slices.ContainsFunc(out["notes"].([]any), func(n any) bool { return strings.Contains(n.(string), "pipeline") }) {
		t.Fatalf("transactional and deal: %v %v", tmpls, out["notes"])
	}
	me := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	owned := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Route", "steps": []map[string]any{{"id": "own", "kind": "assign_owner", "owner_id": me}}})["id"].(string)
	res = owner.must(400, "POST", base+"/kits/export", map[string]any{"journeys": []string{owned}})
	if !strings.Contains(res["error"].(map[string]any)["message"].(string), "members") {
		t.Fatalf("assign_owner: %v", res)
	}

	// Exporting needs customers.read as well as emails.read.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Emails only", "scopes": []string{"emails.read"}})["token"].(string)
	bot := &client{t: t, h: e.h, bearer: tok}
	bot.must(403, "POST", base+"/kits/export", map[string]any{"journeys": []string{ids["activate_new_trials"]}})

	// In another workspace, the file previews and applies like a shipped
	// kit, with a sequence of its own.
	ws2 := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Northpeak EU"})["id"].(string)
	base2 := "/v1/workspaces/" + ws2
	owner.must(400, "POST", base2+"/kits/preview", map[string]any{"yaml": ""})
	owner.must(400, "POST", base2+"/kits/preview", map[string]any{"yaml": "id: Not An Id\n"})
	plan := owner.must(200, "POST", base2+"/kits/preview", map[string]any{"yaml": handoff})
	if acts := kitActions(plan); acts["smart_list:handed_over"] != "add" || acts["journey:hand_off_interested"] != "add" {
		t.Fatalf("file preview: %v", acts)
	}
	owner.must(400, "POST", base2+"/kits/apply", map[string]any{"yaml": handoff})
	seq2 := owner.must(201, "POST", base2+"/sequences", map[string]any{"name": "EU follow-up", "steps": []map[string]any{{"subject": "Hello", "body": "Hi"}}})["id"].(string)
	plan = owner.must(200, "POST", base2+"/kits/apply", map[string]any{"yaml": handoff, "inputs": map[string]any{"sequence": seq2}})
	if plan["applied"] != true {
		t.Fatalf("file apply: %v", plan)
	}
	js := owner.must(200, "GET", base2+"/journeys", nil)["data"].([]any)
	if len(js) != 2 {
		t.Fatalf("journeys from the file: %v", js)
	}
	for _, j := range js {
		j := j.(map[string]any)
		if j["status"] != "draft" || j["kit"].(map[string]any)["id"] != "warm-handoff" {
			t.Fatalf("journey from the file: %v", j)
		}
		for _, st := range j["steps"].([]any) {
			if id, ok := st.(map[string]any)["sequence_id"]; ok && id != seq2 {
				t.Fatalf("sequence: %v", st)
			}
		}
	}
	// Again, it changes nothing.
	if plan = owner.must(200, "POST", base2+"/kits/apply", map[string]any{"yaml": handoff, "inputs": map[string]any{"sequence": seq2}}); plan["changes"] != float64(0) {
		t.Fatalf("again: %v", plan)
	}

	// A kit file's own id stays out of the product event.
	var fromFile int
	for _, ev := range got() {
		if ev.Name == "kit.applied" && ev.Props["source"] == "file" {
			fromFile++
			if _, ok := ev.Props["kit_id"]; ok || ev.Props["target_id"] != "" {
				t.Fatalf("kit.applied from a file: %v", ev.Props)
			}
		}
	}
	if fromFile != 2 {
		t.Fatalf("kit.applied from a file: %d", fromFile)
	}
}
