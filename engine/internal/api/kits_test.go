package api_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func kitActions(plan map[string]any) map[string]string {
	out := map[string]string{}
	for _, it := range plan["items"].([]any) {
		it := it.(map[string]any)
		out[it["kind"].(string)+":"+it["key"].(string)] = it["action"].(string)
	}
	return out
}

func TestKits(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	anon := &client{t: t, h: e.h}

	// The kits are public, with the playbooks that use them.
	if len(anon.must(200, "GET", "/v1/kits", nil)["data"].([]any)) == 0 {
		t.Fatal("no kits")
	}
	k := anon.must(200, "GET", "/v1/kits/creators-partnerships", nil)
	if pb := k["playbooks"].([]any); !slices.Contains(pb, any("creator-partnerships")) {
		t.Fatalf("kit playbooks: %v", k["playbooks"])
	}
	anon.must(404, "GET", "/v1/kits/nope", nil)

	// A field of another kind under the same key is a conflict.
	owner.must(201, "POST", base+"/catalog/types/person/fields", map[string]any{"key": "audience_size", "label": "Audience", "value_type": "text"})
	plan := owner.must(200, "GET", base+"/kits/creators-partnerships/preview", nil)
	if plan["conflicts"] != float64(1) || kitActions(plan)["field:person.audience_size"] != "conflict" || plan["applied"] != false {
		t.Fatalf("preview with a conflict: %v", plan)
	}
	res := owner.must(409, "POST", base+"/kits/creators-partnerships/apply", nil)
	if !strings.Contains(res["error"].(map[string]any)["message"].(string), "audience_size") {
		t.Fatalf("conflict: %v", res)
	}
	if n := len(owner.must(200, "GET", base+"/catalog/option-sets", nil)["data"].([]any)); n != 7 {
		t.Fatalf("a refused kit added option lists: %d", n)
	}

	// Archive the clash away under another key: the kit applies.
	owner.must(200, "PATCH", base+"/catalog/types/person/fields/audience_size", map[string]any{"archived": true})
	plan = owner.must(200, "GET", base+"/kits/creators-partnerships/preview", nil)
	if kitActions(plan)["field:person.audience_size"] != "conflict" {
		t.Fatalf("an archived field still blocks its key: %v", plan)
	}
	if _, err := e.pool.Exec(t.Context(), `DELETE FROM field_definitions WHERE workspace_id = $1 AND key = 'audience_size'`, ws); err != nil {
		t.Fatal(err)
	}
	// One option list of the workspace's own is extended, not replaced.
	owner.must(201, "POST", base+"/catalog/option-sets", map[string]any{"key": "creator_platform", "name": "Platforms", "options": []map[string]any{{"key": "youtube", "label": "YT"}, {"key": "mastodon", "label": "Mastodon"}}})
	// A template with a kit's handle stays as it is.
	owner.must(201, "POST", base+"/email-templates", map[string]any{"name": "Mine", "handle": "creator-brief", "purpose": "lifecycle", "subject": "Mine", "text": "Mine"})
	plan = owner.must(200, "GET", base+"/kits/creators-partnerships/preview", nil)
	acts := kitActions(plan)
	if plan["conflicts"] != float64(0) || acts["option_set:creator_platform"] != "extend" || acts["record_type:partnership"] != "add" ||
		acts["field:partnership.stage"] != "add" || acts["email_template:creator-brief"] != "reuse" || acts["email_template:creator-go-live-reminder"] != "add" {
		t.Fatalf("preview: %v", acts)
	}

	// Without emails.manage, a kit that adds email drafts is refused.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Catalog bot", "scopes": []string{"catalog.manage", "customers.read"}})
	bot := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	bot.must(403, "POST", base+"/kits/creators-partnerships/apply", nil)

	applied := owner.must(200, "POST", base+"/kits/creators-partnerships/apply", nil)
	if applied["applied"] != true || applied["changes"] == float64(0) {
		t.Fatalf("apply: %v", applied)
	}
	typ := owner.must(200, "GET", base+"/catalog/types/partnership", nil)
	if typ["plural"] != "Partnerships" || len(typ["fields"].([]any)) != 12 {
		t.Fatalf("partnership type: %v", typ)
	}
	var platform map[string]any
	for _, s := range owner.must(200, "GET", base+"/catalog/option-sets", nil)["data"].([]any) {
		if s.(map[string]any)["key"] == "creator_platform" {
			platform = s.(map[string]any)
		}
	}
	opts := platform["options"].([]any)
	if platform["name"] != "Platforms" || len(opts) != 11 || opts[0].(map[string]any)["label"] != "YT" || opts[1].(map[string]any)["key"] != "mastodon" {
		t.Fatalf("creator_platform: %v", platform)
	}
	// Records can use it at once.
	owner.must(201, "POST", base+"/records", map[string]any{"type": "partnership", "name": "Trail newsletter", "attributes": map[string]any{"stage": "found", "fee": map[string]any{"amount_minor": 50000, "currency": "USD"}}})

	// Applying again changes nothing.
	again := owner.must(200, "POST", base+"/kits/creators-partnerships/apply", nil)
	if again["changes"] != float64(0) {
		t.Fatalf("second apply: %v", again)
	}
	ev := named(got(), "kit.applied")
	if len(ev) != 2 || ev[0].Props["kit_id"] != "creators-partnerships" || ev[0].WorkspaceID != ws {
		t.Fatalf("kit.applied: %+v", ev)
	}
	if len(named(got(), "record_type.created")) != 1 || len(named(got(), "option_set.updated")) != 1 {
		t.Fatal("each part of the kit records its own event")
	}
}

func TestPlaybookAppliesItsKit(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	start := map[string]any{"inputs": map[string]any{"brand": "Northpeak", "topics": "trail running"}, "apply_kit": true}
	owner.must(400, "POST", base+"/playbooks/welcome-new-signups/runs", map[string]any{"inputs": map[string]any{"brand": "Northpeak"}, "apply_kit": true})
	m := owner.must(201, "POST", base+"/playbooks/creator-partnerships/runs", start)
	if !strings.Contains(m["instructions"].(string), "setup kit") {
		t.Fatalf("instructions: %s", m["instructions"])
	}
	owner.must(200, "GET", base+"/catalog/types/partnership", nil)
	if p := owner.must(200, "GET", base+"/kits/creators-partnerships/preview", nil); p["changes"] != float64(0) {
		t.Fatalf("after start: %v", p)
	}
}
