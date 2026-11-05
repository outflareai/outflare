package api_test

import "testing"

// Brands own their data (plans/brands/README.md, step 2): conversion
// definitions and attribution models live in one brand, and another
// brand's routes neither list nor reach them.
func TestBrandConversionsAndModels(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	alpha := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Alpha", "handle": "alpha"})["id"].(string)
	bravo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Bravo", "handle": "bravo"})["id"].(string)
	inAlpha, inBravo := "/v1/brands/"+alpha, "/v1/brands/"+bravo

	owner.must(201, "POST", inBravo+"/conversion-definitions", map[string]any{"key": "demo", "name": "Demo booked", "event": "demo.booked"})
	owner.must(201, "POST", inBravo+"/attribution/models", map[string]any{"key": "paid-7-day", "name": "Paid 7 days",
		"spec": map[string]any{"base": "last_non_direct", "channel_lookback_days": map[string]any{"paid_ads": 7}}})

	keys := func(path string) []string {
		t.Helper()
		var out []string
		for _, d := range owner.must(200, "GET", path, nil)["data"].([]any) {
			out = append(out, d.(map[string]any)["key"].(string))
		}
		return out
	}
	if got := keys(inBravo + "/conversion-definitions"); len(got) != 1 || got[0] != "demo" {
		t.Fatalf("bravo's definitions = %v", got)
	}
	if got := keys(inAlpha + "/conversion-definitions"); len(got) != 0 {
		t.Fatalf("alpha sees bravo's definitions: %v", got)
	}
	if got := keys(base + "/conversion-definitions?brand=alpha"); len(got) != 0 {
		t.Fatalf("?brand=alpha sees bravo's definitions: %v", got)
	}
	if got := keys(inBravo + "/attribution/models"); len(got) != 1 || got[0] != "paid-7-day" {
		t.Fatalf("bravo's models = %v", got)
	}
	if got := keys(inAlpha + "/attribution/models"); len(got) != 0 {
		t.Fatalf("alpha sees bravo's models: %v", got)
	}

	// Alpha's routes can't read, change or archive them.
	owner.must(404, "GET", inAlpha+"/conversion-definitions/demo", nil)
	owner.must(404, "PATCH", inAlpha+"/conversion-definitions/demo", map[string]any{"name": "Taken"})
	owner.must(404, "DELETE", inAlpha+"/conversion-definitions/demo", nil)
	owner.must(404, "GET", inAlpha+"/attribution/models/paid-7-day", nil)
	owner.must(404, "PATCH", inAlpha+"/attribution/models/paid-7-day", map[string]any{"name": "Taken"})
	owner.must(404, "DELETE", inAlpha+"/attribution/models/paid-7-day", nil)
	if d := owner.must(200, "GET", inBravo+"/conversion-definitions/demo", nil); d["name"] != "Demo booked" {
		t.Fatalf("bravo's definition = %v", d)
	}

	// A preset added on Alpha's route lands in Alpha.
	owner.must(200, "POST", inAlpha+"/conversion-definitions/presets/saas", nil)
	if got := keys(inAlpha + "/conversion-definitions"); len(got) == 0 {
		t.Fatal("alpha's preset added nothing to alpha")
	}
	if got := keys(inBravo + "/conversion-definitions"); len(got) != 1 {
		t.Fatalf("alpha's preset reached bravo: %v", got)
	}
}

// Two brands of one workspace can use the same keys and names, and each
// has its own settings: changing one brand's never changes another's.
func TestBrandsReuseNamesAndKeepTheirOwnSettings(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	alpha := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Alpha", "handle": "alpha"})["id"].(string)
	bravo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Bravo", "handle": "bravo"})["id"].(string)
	split := []map[string]any{{"key": "a", "weight": 50}, {"key": "b", "weight": 50}}

	for _, b := range []string{alpha, bravo} {
		in := "/v1/brands/" + b
		owner.must(201, "POST", in+"/conversion-definitions", map[string]any{"key": "demo", "name": "Demo booked", "event": "demo.booked"})
		owner.must(201, "POST", in+"/attribution/models", map[string]any{"key": "paid-7-day", "name": "Paid 7 days",
			"spec": map[string]any{"base": "last_non_direct", "channel_lookback_days": map[string]any{"paid_ads": 7}}})
		owner.must(201, "POST", in+"/flags", map[string]any{"key": "hero", "variants": split, "exclusion_group": "homepage"})
		owner.must(200, "PUT", in+"/flag-groups/homepage", map[string]any{"holdout": 10})
	}
	inAlpha, inBravo := "/v1/brands/"+alpha, "/v1/brands/"+bravo

	// Each brand's copy is its own.
	owner.must(200, "PATCH", inAlpha+"/conversion-definitions/demo", map[string]any{"name": "Alpha demo"})
	if d := owner.must(200, "GET", inBravo+"/conversion-definitions/demo", nil); d["name"] != "Demo booked" {
		t.Fatalf("alpha's rename reached bravo: %v", d["name"])
	}
	owner.must(200, "PATCH", inAlpha+"/attribution/models/paid-7-day", map[string]any{"name": "Alpha paid"})
	if v := owner.must(200, "GET", inBravo+"/attribution/models/paid-7-day", nil)["data"].([]any); len(v) != 1 || v[0].(map[string]any)["name"] != "Paid 7 days" {
		t.Fatalf("alpha's rename reached bravo: %v", v)
	}
	owner.must(200, "PUT", inAlpha+"/flag-groups/homepage", map[string]any{"holdout": 20})
	if g := owner.must(200, "GET", inBravo+"/flag-groups", nil)["data"].([]any); len(g) != 1 || g[0].(map[string]any)["holdout"] != float64(10) {
		t.Fatalf("alpha's holdout reached bravo: %v", g)
	}
	owner.must(204, "DELETE", inAlpha+"/flags/hero", nil)
	owner.must(200, "GET", inBravo+"/flags/hero", nil)

	// Settings.
	owner.must(200, "PUT", inAlpha+"/ads/settings", map[string]any{"consent_mode": "opt_in"})
	if s := owner.must(200, "GET", inBravo+"/ads/settings", nil); s["consent_mode"] != "opt_out" {
		t.Fatalf("bravo's consent mode = %v", s["consent_mode"])
	}
	owner.must(200, "PATCH", inAlpha+"/attribution/settings", map[string]any{"lookback_days": 7})
	if s := owner.must(200, "GET", inBravo+"/attribution/settings", nil); s["lookback_days"] == float64(7) {
		t.Fatalf("alpha's lookback reached bravo: %v", s)
	}
	if s := owner.must(200, "GET", inAlpha+"/attribution/settings", nil); s["lookback_days"] != float64(7) {
		t.Fatalf("alpha's lookback = %v", s["lookback_days"])
	}
	owner.must(200, "PUT", inAlpha+"/visitors/settings", map[string]any{"hot_score": 77})
	if s := owner.must(200, "GET", inBravo+"/visitors/settings", nil); s["hot_score"] == float64(77) {
		t.Fatalf("alpha's hot score reached bravo: %v", s)
	}
	if s := owner.must(200, "GET", inAlpha+"/visitors/settings", nil); s["hot_score"] != float64(77) {
		t.Fatalf("alpha's hot score = %v", s["hot_score"])
	}
	owner.must(200, "PUT", inAlpha+"/ai-visibility/settings", map[string]any{"monthly_limit_cents": 500, "samples_per_prompt": 3})
	if s := owner.must(200, "GET", inBravo+"/ai-visibility/settings", nil); s["monthly_limit_cents"] == float64(500) || s["samples_per_prompt"] == float64(3) {
		t.Fatalf("alpha's AI visibility settings reached bravo: %v", s)
	}
	if s := owner.must(200, "GET", inAlpha+"/ai-visibility/settings", nil); s["monthly_limit_cents"] != float64(500) {
		t.Fatalf("alpha's AI visibility limit = %v", s["monthly_limit_cents"])
	}
	owner.must(200, "PATCH", inBravo+"/link-building/settings", map[string]any{"pitch_cooldown_days": 9, "weekly_reads": false})
	if s := owner.must(200, "GET", inAlpha+"/link-building/settings", nil); s["pitch_cooldown_days"] == float64(9) || s["weekly_reads"] == false {
		t.Fatalf("bravo's link building settings reached alpha: %v", s)
	}
	if s := owner.must(200, "GET", inBravo+"/link-building/settings", nil); s["pitch_cooldown_days"] != float64(9) {
		t.Fatalf("bravo's cool-down = %v", s["pitch_cooldown_days"])
	}
	// The workspace route acts on ?brand=.
	if s := owner.must(200, "GET", base+"/link-building/settings?brand=bravo", nil); s["pitch_cooldown_days"] != float64(9) {
		t.Fatalf("the workspace route with ?brand=bravo = %v", s)
	}
}
