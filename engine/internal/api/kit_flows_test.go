package api_test

import (
	"context"
	"github.com/outflareai/outflare/engine/internal/customers"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/signals"
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

// A kit's signal types and its journeys about companies: the type joins
// the brand's list once, and the journeys keep who they are about.
func TestKitSignalTypes(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	var brand string
	if err := e.pool.QueryRow(context.Background(), `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	inputs := map[string]any{"inputs": map[string]any{"usage_event": "project_updated"}}

	acts := kitActions(owner.must(200, "GET", base+"/kits/account-signals/preview", nil))
	if acts["signal_type:went_quiet"] != "add" || acts["journey:prepare_renewals"] != "add" || acts["field:account.renews_on"] != "add" {
		t.Fatalf("preview: %v", acts)
	}
	if plan := owner.must(200, "POST", base+"/kits/account-signals/apply", inputs); plan["applied"] != true {
		t.Fatalf("apply: %v", plan)
	}
	var quiet map[string]any
	types := owner.must(200, "GET", base+"/signal-types", nil)["data"].([]any)
	for _, ty := range types {
		if ty := ty.(map[string]any); ty["key"] == "went_quiet" {
			quiet = ty
		}
	}
	// The starter types are there beside it.
	if quiet == nil || quiet["label"] != "Went quiet" || quiet["due_days"] != float64(3) || len(types) != len(signals.Starter)+1 {
		t.Fatalf("signal types: %v", types)
	}
	js := owner.must(200, "GET", base+"/journeys", nil)["data"].([]any)
	if len(js) != 2 {
		t.Fatalf("journeys: %d", len(js))
	}
	for _, j := range js {
		if j := j.(map[string]any); j["record_type"] != "account" || j["status"] != "draft" {
			t.Fatalf("a kit journey about companies: %v", j)
		}
	}
	for _, r := range owner.must(200, "GET", base+"/rules", nil)["data"].([]any) {
		if r := r.(map[string]any); r["record_type"] != "account" {
			t.Fatalf("a kit rule about companies: %v", r)
		}
	}

	// The playbook's home shows what it set up, all still drafts, and the
	// signals of its own type.
	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme"})["id"].(string)
	owner.must(201, "POST", base+"/signals", map[string]any{"record_id": acme, "type": "went_quiet"})
	home := owner.must(200, "GET", base+"/playbooks/flag-renewals-and-quiet-accounts/home", nil)
	parts := map[string]string{}
	for _, part := range home["parts"].([]any) {
		part := part.(map[string]any)
		parts[part["kind"].(string)+":"+part["key"].(string)] = part["status"].(string)
	}
	if home["installed"] != true || len(parts) != 5 || parts["rule:renewal_coming_up"] != "draft" || parts["journey:prepare_renewals"] != "draft" || parts["signal_type:went_quiet"] != "active" ||
		home["signals"].(map[string]any)["open"] != float64(1) {
		t.Fatalf("home: %v", home)
	}
	// A playbook not set up here has no parts; one that doesn't exist is not found.
	if other := owner.must(200, "GET", base+"/playbooks/catch-accounts-at-risk/home", nil); other["installed"] != false || len(other["parts"].([]any)) != 0 {
		t.Fatalf("a playbook not set up: %v", other)
	}
	owner.must(404, "GET", base+"/playbooks/no-such-playbook/home", nil)

	// Again, the type stays as it is; removed, it has to come back first.
	again := owner.must(200, "POST", base+"/kits/account-signals/apply", inputs)
	if again["changes"] != float64(0) || kitActions(again)["signal_type:went_quiet"] != "reuse" {
		t.Fatalf("again: %v", again)
	}
	owner.must(200, "DELETE", base+"/signal-types/went_quiet", nil)
	if kitActions(owner.must(200, "GET", base+"/kits/account-signals/preview", nil))["signal_type:went_quiet"] != "conflict" {
		t.Fatal("a removed signal type isn't a conflict")
	}

	// Exported, a journey that raises a type of the brand's own takes the
	// type along, and stays about companies.
	owner.must(200, "PUT", base+"/signal-types/went_quiet", map[string]any{"label": "Went quiet"})
	var flag string
	for _, j := range js {
		if j := j.(map[string]any); j["kit"].(map[string]any)["key"] == "flag_quiet_companies" {
			flag = j["id"].(string)
		}
	}
	out := owner.must(200, "POST", "/v1/workspaces/"+ws+"/kits/export", map[string]any{"journeys": []string{flag}})
	kit := out["kit"].(map[string]any)
	if st := kit["signal_types"].([]any); len(st) != 1 || st[0].(map[string]any)["key"] != "went_quiet" {
		t.Fatalf("exported signal types: %v", kit["signal_types"])
	}
	if j := kit["journeys"].([]any)[0].(map[string]any); j["record_type"] != "account" {
		t.Fatalf("exported journey: %v", j)
	}
}

// The customer-handover kit needs no inputs: it adds two signal types that
// listen to events Outflare records itself, and one draft journey.
func TestKitCustomerHandover(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	var brand string
	if err := e.pool.QueryRow(context.Background(), `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	if plan := owner.must(200, "POST", base+"/kits/customer-handover/apply", map[string]any{}); plan["applied"] != true {
		t.Fatalf("apply: %v", plan)
	}
	found := map[string]any{}
	for _, ty := range owner.must(200, "GET", base+"/signal-types", nil)["data"].([]any) {
		ty := ty.(map[string]any)
		found[ty["key"].(string)] = ty["events"]
	}
	if ev, _ := found["new_customer"].([]any); len(ev) != 1 || ev[0] != "deal.won" {
		t.Fatalf("new_customer: %v", found)
	}
	if ev, _ := found["support_trouble"].([]any); len(ev) != 1 || ev[0] != "support.conversation_reopened" {
		t.Fatalf("support_trouble: %v", found)
	}
	js := owner.must(200, "GET", base+"/journeys", nil)["data"].([]any)
	if len(js) != 1 || js[0].(map[string]any)["status"] != "draft" {
		t.Fatalf("journeys: %v", js)
	}
	// Its playbook's home lists all three.
	home := owner.must(200, "GET", base+"/playbooks/hand-over-won-customers/home", nil)
	if parts, _ := home["parts"].([]any); len(parts) != 3 {
		t.Fatalf("home: %v", home)
	}
}

// The usage-health kit brings two metrics and a health score with its
// rule and journey; one the brand already has under the same key stays.
func TestKitUsageHealth(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	var brand string
	if err := e.pool.QueryRow(context.Background(), `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	in := map[string]any{"inputs": map[string]any{"usage_event": "project_updated"}}
	acts := kitActions(owner.must(200, "GET", base+"/kits/usage-health/preview", nil))
	if acts["metric:key_action_days_30d"] != "add" || acts["metric:key_action_days_prev_30d"] != "add" || acts["score:health"] != "add" {
		t.Fatalf("preview: %v", acts)
	}
	if plan := owner.must(200, "POST", base+"/kits/usage-health/apply", in); plan["applied"] != true {
		t.Fatalf("apply: %v", plan)
	}
	var events []string
	if err := e.pool.QueryRow(context.Background(), `SELECT events FROM metric_definitions WHERE brand_id = $1 AND key = 'key_action_days_prev_30d' AND offset_days = 30`,
		brand).Scan(&events); err != nil || len(events) != 1 || events[0] != "project_updated" {
		t.Fatalf("metric: %v %v", events, err)
	}
	scores := owner.must(200, "GET", base+"/scores", nil)["data"].([]any)
	if len(scores) != 1 || scores[0].(map[string]any)["key"] != "health" || len(scores[0].(map[string]any)["factors"].([]any)) != 5 {
		t.Fatalf("scores: %v", scores)
	}
	home := owner.must(200, "GET", base+"/playbooks/score-account-health/home", nil)
	kinds := map[string]int{}
	for _, p := range home["parts"].([]any) {
		kinds[p.(map[string]any)["kind"].(string)]++
	}
	if kinds["metric"] != 2 || kinds["score"] != 1 || kinds["rule"] != 1 || kinds["journey"] != 1 {
		t.Fatalf("home: %v", home)
	}
	// Applied again, nothing is added, and a changed score stays changed.
	owner.must(200, "PATCH", base+"/scores/"+scores[0].(map[string]any)["id"].(string), map[string]any{"label": "Customer health"})
	again := owner.must(200, "POST", base+"/kits/usage-health/apply", in)
	if again["changes"] != float64(0) {
		t.Fatalf("again: %v", again)
	}
	if s := owner.must(200, "GET", base+"/scores", nil)["data"].([]any); len(s) != 1 || s[0].(map[string]any)["label"] != "Customer health" {
		t.Fatalf("scores after: %v", s)
	}
}

// The customer-health kit: three measured scores under a health score,
// sentiment beside it, two predictions on top, and draft rules and
// journeys that flag both.
func TestKitCustomerHealth(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	var brand string
	if err := e.pool.QueryRow(context.Background(), `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	acts := kitActions(owner.must(200, "GET", base+"/kits/customer-health/preview", nil))
	for _, k := range []string{"metric:meetings_90d", "score:usage", "score:billing", "score:engagement", "score:sentiment", "score:health", "score:churn_risk", "score:expansion"} {
		if acts[k] != "add" {
			t.Fatalf("preview %s: %v", k, acts)
		}
	}
	if plan := owner.must(200, "POST", base+"/kits/customer-health/apply", map[string]any{}); plan["applied"] != true {
		t.Fatalf("apply: %v", plan)
	}
	by := map[string]map[string]any{}
	for _, x := range owner.must(200, "GET", base+"/scores", nil)["data"].([]any) {
		m := x.(map[string]any)
		by[m["key"].(string)] = m
	}
	if len(by) != 7 || by["health"]["nature"] != "measured" || len(by["health"]["parts"].([]any)) != 3 || by["sentiment"]["nature"] != "ai" || by["sentiment"]["needs"] != "sentiment.value" ||
		by["churn_risk"]["nature"] != "predicted" || by["churn_risk"]["higher_is_better"] != false || by["expansion"]["higher_is_better"] != true {
		t.Fatalf("scores: %v", by)
	}
	home := owner.must(200, "GET", base+"/playbooks/see-customer-health/home", nil)
	kinds := map[string]int{}
	for _, p := range home["parts"].([]any) {
		kinds[p.(map[string]any)["kind"].(string)]++
	}
	if kinds["metric"] != 3 || kinds["score"] != 7 || kinds["rule"] != 2 || kinds["journey"] != 2 {
		t.Fatalf("home: %v", home)
	}
	if again := owner.must(200, "POST", base+"/kits/customer-health/apply", map[string]any{}); again["changes"] != float64(0) {
		t.Fatalf("again: %v", again)
	}
}

// Setting a kit up with a holdout puts a split in front of its journeys,
// and the playbook's lift compares who went through with who was held back.
func TestPlaybookLift(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	lift := base + "/playbooks/flag-renewals-and-quiet-accounts/lift"

	// Not set up: nothing to measure. Unknown playbook: not found.
	if got := owner.must(200, "GET", lift, nil); got["measured"] != false || len(got["journeys"].([]any)) != 0 {
		t.Fatalf("lift before setup: %v", got)
	}
	owner.must(404, "GET", base+"/playbooks/no-such-playbook/lift", nil)
	owner.must(400, "GET", lift+"?days=900", nil)

	inputs := map[string]any{"usage_event": "project_updated"}
	owner.must(400, "POST", base+"/kits/account-signals/apply", map[string]any{"inputs": inputs, "holdout_percent": 80})
	if plan := owner.must(200, "POST", base+"/kits/account-signals/apply", map[string]any{"inputs": inputs, "holdout_percent": 20}); plan["applied"] != true {
		t.Fatalf("apply: %v", plan)
	}
	got := owner.must(200, "GET", lift, nil)
	js := got["journeys"].([]any)
	if got["measured"] != true || len(js) != 2 {
		t.Fatalf("lift after setup: %v", got)
	}
	var jid, step string
	for _, j := range js {
		j := j.(map[string]any)
		if j["measured"] != true || j["holdout_percent"] != float64(20) || j["step"] != "measure" {
			t.Fatalf("a measured journey: %v", j)
		}
		if ran := j["ran"].(map[string]any); ran["people"] != float64(0) || ran["verdict"] != "too_early" {
			t.Fatalf("ran with nobody in it: %v", ran)
		}
		if j["name"] == "Prepare renewals" {
			jid, step = j["journey_id"].(string), j["step"].(string)
		}
	}
	// The journey itself starts with the split: 80 go on, 20 are held back.
	first := owner.must(200, "GET", base+"/journeys/"+jid, nil)["steps"].([]any)[0].(map[string]any)
	if first["kind"] != "split" || first["split_by"] != "random" || len(first["paths"].([]any)) != 2 {
		t.Fatalf("the first step: %v", first)
	}

	// Three companies went through and one was held back; one that went
	// through paid afterwards, through one of its people.
	company := func(name string) string {
		t.Helper()
		return owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": name})["id"].(string)
	}
	run := func(rec, outcome string) {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, `INSERT INTO journey_runs (workspace_id, journey_id, record_id, status) VALUES ($1, $2, $3, 'exited') RETURNING id::text`,
			ws, jid, rec).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := e.pool.Exec(ctx, `INSERT INTO journey_run_steps (workspace_id, journey_id, run_id, step, kind, outcome, at)
			VALUES ($1, $2, $3, $4, 'split', $5, now() - interval '2 days')`, ws, jid, id, step, outcome); err != nil {
			t.Fatal(err)
		}
	}
	acme := company("Acme")
	run(acme, "run")
	run(company("Northpeak"), "run")
	run(company("Pinecrest"), "run")
	run(company("Harbor"), "held_back")
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana",
		"identities": []any{map[string]any{"namespace": "email", "value": "ana@acme.example"}}})["id"].(string)
	if _, err := e.pool.Exec(ctx, `INSERT INTO record_relationships (workspace_id, from_id, to_id, kind) VALUES ($1, $2, $3, 'companies')`, ws, ana, acme); err != nil {
		t.Fatal(err)
	}
	paid, err := customers.RecordEvent(ctx, e.pool, ws, customers.Event{RecordID: ana, Name: "payment.succeeded", Source: "api", MessageID: "lift-pay"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO attribution_conversions (event_id, workspace_id, brand_id, record_id, name, value_minor, currency, occurred_at)
		VALUES ($1, $2, $3, $4, 'payment.succeeded', 50000, 'USD', now() - interval '1 day')
		ON CONFLICT (event_id) DO UPDATE SET value_minor = 50000, currency = 'USD', occurred_at = now() - interval '1 day', record_id = EXCLUDED.record_id`, paid, ws, brand, ana); err != nil {
		t.Fatal(err)
	}
	for _, j := range owner.must(200, "GET", lift+"?days=30", nil)["journeys"].([]any) {
		j := j.(map[string]any)
		if j["journey_id"] != jid {
			continue
		}
		ran, held := j["ran"].(map[string]any), j["held_back"].(map[string]any)
		if ran["people"] != float64(3) || ran["converted"] != float64(1) || held["people"] != float64(1) || held["converted"] != float64(0) ||
			ran["verdict"] != "too_early" || held["holdout"] != true {
			t.Fatalf("the lift: ran %v, held back %v", ran, held)
		}
		if rev := ran["revenue"].([]any); len(rev) != 1 {
			t.Fatalf("revenue: %v", rev)
		}
	}
}
