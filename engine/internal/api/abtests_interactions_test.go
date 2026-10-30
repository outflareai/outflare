package api_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Flags in an exclusion group never put a visitor in two of their tests;
// a test of a flag outside the group is warned about before it starts,
// and checked for interactions once both run.
func TestABTestExclusionAndInteractions(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	split := []map[string]any{{"key": "control", "weight": 50}, {"key": "new", "weight": 50}}
	flag := func(key string, extra map[string]any) map[string]any {
		body := map[string]any{"key": key, "name": key, "status": "on", "variants": split}
		for k, v := range extra {
			body[k] = v
		}
		return owner.must(201, "POST", base+"/flags", body)
	}

	// Slices: hero takes 30%, cta the next 50% by default, and a third
	// flag asking for 30% doesn't fit.
	hero := flag("hero", map[string]any{"exclusion_group": "homepage", "group_share": 30})
	cta := flag("cta", map[string]any{"exclusion_group": "homepage"})
	if hero["group_start"] != 0.0 || hero["group_share"] != 30.0 || cta["group_start"] != 30.0 || cta["group_share"] != 50.0 {
		t.Fatalf("slices: hero %v+%v, cta %v+%v", hero["group_start"], hero["group_share"], cta["group_start"], cta["group_share"])
	}
	owner.must(409, "POST", base+"/flags", map[string]any{"key": "footer", "variants": split, "exclusion_group": "homepage", "group_share": 30})
	owner.must(400, "PATCH", base+"/flags/cta", map[string]any{"exclusion_group": "Home Page"})

	// No visitor gets a version of both.
	both, inGroup := 0, 0
	for i := range 400 {
		d := owner.must(200, "POST", base+"/flags/evaluate", map[string]any{"anonymous_id": fmt.Sprintf("visitor-%04d", i)})
		f, r := d["flags"].(map[string]any), d["reasons"].(map[string]any)
		if f["hero"] != false && f["cta"] != false {
			both++
		}
		if r["hero"] == "group" {
			inGroup++
		}
	}
	if both != 0 || inGroup < 200 {
		t.Fatalf("in both: %d, left out of hero by the group: %d of 400", both, inGroup)
	}

	// A running test's slice is fixed.
	goal := []map[string]any{{"kind": "event", "name": "signup_started"}}
	heroTest := owner.must(201, "POST", base+"/ab-tests", map[string]any{"flag": "hero", "name": "Hero", "goals": goal})["id"].(string)
	owner.must(200, "POST", base+"/ab-tests/"+heroTest+"/start", nil)
	owner.must(409, "PATCH", base+"/flags/hero", map[string]any{"group_share": 20})
	owner.must(409, "PATCH", base+"/flags/hero", map[string]any{"exclusion_group": ""})

	// Another test in the group isn't warned about; one outside it is.
	overlap := func(id string) string {
		t.Helper()
		for _, c := range owner.must(200, "GET", base+"/ab-tests/"+id+"/checklist", nil)["checks"].([]any) {
			if c := c.(map[string]any); c["id"] == "overlap" {
				return c["text"].(string)
			}
		}
		return ""
	}
	ctaTest := owner.must(201, "POST", base+"/ab-tests", map[string]any{"flag": "cta", "goals": goal})["id"].(string)
	if o := overlap(ctaTest); o != "" {
		t.Fatalf("warned about a test in the same group: %s", o)
	}
	flag("banner", nil)
	bannerTest := owner.must(201, "POST", base+"/ab-tests", map[string]any{"flag": "banner", "name": "Banner", "goals": goal})["id"].(string)
	if o := overlap(bannerTest); !strings.Contains(o, `"Hero"`) {
		t.Fatalf("overlap: %q", o)
	}
	owner.must(200, "POST", base+"/ab-tests/"+bannerTest+"/start", nil)

	// 2,000 visitors saw both. Hero's new version lifts sign-ups only with
	// banner's new version: an interaction.
	if _, err := e.pool.Exec(t.Context(), `
		WITH v AS (SELECT i, CASE WHEN i % 2 = 0 THEN 'control' ELSE 'new' END AS h, CASE WHEN (i / 2) % 2 = 0 THEN 'control' ELSE 'new' END AS b
			FROM generate_series(1, 2000) i)
		INSERT INTO customer_events (workspace_id, kind, name, anonymous_id, properties, context, occurred_at)
		SELECT $1::uuid, 'track', 'experiment_viewed', 'x-' || i, jsonb_build_object('flag', 'hero', 'variant', h), '{}'::jsonb, now() FROM v
		UNION ALL
		SELECT $1::uuid, 'track', 'experiment_viewed', 'x-' || i, jsonb_build_object('flag', 'banner', 'variant', b), '{}'::jsonb, now() FROM v
		UNION ALL
		SELECT $1::uuid, 'track', 'signup_started', 'x-' || i, '{}'::jsonb, '{}'::jsonb, now() + interval '1 second' FROM v
		WHERE (h = 'new' AND b = 'new' AND i % 10 < 6) OR i % 10 = 9`, ws); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	owner.must(409, "GET", base+"/ab-tests/"+ctaTest+"/interactions", nil)
	got := owner.must(200, "GET", base+"/ab-tests/"+heroTest+"/interactions", nil)["interactions"].([]any)
	if len(got) != 1 {
		t.Fatalf("interactions: %v", got)
	}
	in := got[0].(map[string]any)
	v := in["versions"].([]any)[0].(map[string]any)
	if in["flag_key"] != "banner" || in["overlap"] != 2000.0 || v["variant"] != "new" || v["interacts"] != true || len(v["effects"].([]any)) != 2 {
		t.Fatalf("interaction: %v", in)
	}
}
