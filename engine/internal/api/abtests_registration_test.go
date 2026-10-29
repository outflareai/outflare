package api_test

import (
	"strings"
	"testing"
)

// Starting a test registers what it set out to show; the hypothesis is
// fixed from then on, and the results say how the flag changed since.
func TestABTestRegistration(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/flags", map[string]any{"key": "cta", "name": "Call to action", "rollout": 100,
		"variants": []map[string]any{{"key": "control", "weight": 50}, {"key": "bold", "weight": 50, "payload": map[string]any{"text": "Start now"}}}})
	ab := owner.must(201, "POST", base+"/ab-tests", map[string]any{"flag": "cta", "hypothesis": "A bolder button lifts sign-ups.",
		"goals": []map[string]any{{"kind": "event", "name": "signup_started"}}})
	id := ab["id"].(string)
	if ab["registration"] != nil {
		t.Fatalf("a draft has a registration: %v", ab["registration"])
	}
	owner.must(200, "PATCH", base+"/ab-tests/"+id, map[string]any{"hypothesis": "A bolder button lifts sign-ups by a tenth."})

	started := owner.must(200, "POST", base+"/ab-tests/"+id+"/start", nil)
	reg, _ := started["registration"].(map[string]any)
	if reg == nil || reg["hypothesis"] != "A bolder button lifts sign-ups by a tenth." || reg["control"] != "control" || reg["rollout"] != 100.0 || reg["plan"] == nil {
		t.Fatalf("registration: %v", started["registration"])
	}
	versions := reg["versions"].([]any)
	if len(versions) != 2 || versions[1].(map[string]any)["weight"] != 50.0 || versions[1].(map[string]any)["content_hash"] == "" {
		t.Fatalf("registered versions: %v", versions)
	}

	// The hypothesis is fixed; the name, or the same hypothesis, still go.
	owner.must(409, "PATCH", base+"/ab-tests/"+id, map[string]any{"hypothesis": "Something else."})
	owner.must(200, "PATCH", base+"/ab-tests/"+id, map[string]any{"name": "Bold CTA", "hypothesis": "A bolder button lifts sign-ups by a tenth."})

	// Visitors split 50/50 as registered.
	if _, err := e.pool.Exec(t.Context(), `
		INSERT INTO customer_events (workspace_id, kind, name, anonymous_id, properties, context, occurred_at)
		SELECT $1::uuid, 'track', 'experiment_viewed', 'r-' || i, jsonb_build_object('flag', 'cta', 'variant', CASE WHEN i % 2 = 0 THEN 'control' ELSE 'bold' END), '{}'::jsonb, now()
		FROM generate_series(1, 400) i`, ws); err != nil {
		t.Fatal(err)
	}
	result := func() map[string]any {
		t.Helper()
		return owner.must(200, "GET", base+"/ab-tests/"+id+"/results", nil)["result"].(map[string]any)
	}
	if r := result(); r["flag_changed"] != nil || r["sample_ratio_mismatch"] != false {
		t.Fatalf("unchanged flag: %v %v", r["flag_changed"], r["sample_ratio_mismatch"])
	}

	// Changing the running flag's split, content and rollout, and adding a
	// version, all show; the split is still checked against 50/50.
	owner.must(200, "PATCH", base+"/flags/cta", map[string]any{"rollout": 80, "variants": []map[string]any{
		{"key": "control", "weight": 90}, {"key": "bold", "weight": 10, "payload": map[string]any{"text": "Start today"}}, {"key": "loud", "weight": 0}}})
	r := result()
	changed := strings.Join(toStrings(r["flag_changed"]), " | ")
	for _, want := range []string{"rollout changed from 100% to 80%", "control's share changed from 50 to 90", "bold's share changed from 50 to 10", "bold's content changed", "loud was added"} {
		if !strings.Contains(changed, want) {
			t.Fatalf("flag_changed lacks %q: %s", want, changed)
		}
	}
	if r["sample_ratio_mismatch"] != false {
		t.Fatalf("checked the 50/50 split against today's weights: %v", r["sample_ratio_p"])
	}
}
