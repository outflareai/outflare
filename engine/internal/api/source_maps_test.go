package api_test

import (
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/jserrors"
	"github.com/outflareai/outflare/engine/internal/track"
)

// A brand's source maps turn the stacks of its errors back into its own
// files, lines and function names, brand by brand.
func TestSourceMaps(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	geo := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Northpeak", "website": "https://northpeak.example"})["id"].(string)
	rp := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Summitly", "website": "https://summitly.example"})["id"].(string)
	g := "/v1/brands/" + geo
	k := owner.must(201, "POST", g+"/site-keys", map[string]any{"name": "northpeak.example", "origins": []string{"https://northpeak.example"}})
	key := k["key"].(string)
	owner.must(200, "PATCH", g+"/site-keys/"+k["id"].(string), map[string]any{"capture_errors": true})

	// One built line: "a" at column 1 is checkout's pay() at src/checkout.ts 12:3,
	// and column 11 is src/plans.ts 40:5 with no name.
	// Segments: [0,0,11,2,0] then [10,1,28,2].
	sm := map[string]any{
		"version": 3, "file": "app-3f9a1c.js",
		"sources":        []string{"webpack://./src/checkout.ts", "src/plans.ts"},
		"sourcesContent": []string{"export function pay() { secret() }", "export const plans = []"},
		"names":          []string{"pay"},
		"mappings":       "AAWEA,UC4BE",
	}
	stack := "TypeError: plan 42 not found\n    at a (https://northpeak.example/assets/app-3f9a1c.js:1:1)\n    at https://northpeak.example/assets/app-3f9a1c.js:1:11\n    at c (https://northpeak.example/assets/vendor.js:1:5)"
	code, out := sendSite(t, e.h, "https://northpeak.example", key, map[string]any{"type": "track", "event": "Error Caught", "anonymousId": "anon-1", "messageId": "sm-1",
		"properties": map[string]any{"type": "TypeError", "message": "plan 42 not found", "stack": stack, "fingerprint": "aaaa1111bbbb2222", "handled": false}})
	if code != 200 || out["accepted"] != 1.0 {
		t.Fatalf("send: %d %v", code, out)
	}
	fp := jserrors.Group("TypeError", "plan 42 not found", stack)
	latest := func(brand string) map[string]any {
		t.Helper()
		return owner.must(200, "GET", "/v1/brands/"+brand+"/errors/"+fp, nil)["occurrences"].([]any)[0].(map[string]any)
	}
	if o := latest(geo); o["stack_original"] != "" || o["stack"] != stack {
		t.Fatalf("before any map: %v", o)
	}

	// What an upload must be.
	for _, bad := range []map[string]any{
		{"map": sm},
		{"file": "/assets/app-3f9a1c.js"},
		{"file": "/assets/app-3f9a1c.js", "map": map[string]any{"version": 3}},
		{"file": "/assets/app-3f9a1c.js", "map": "not json"},
		{"file": "/assets/app-3f9a1c.js", "map": sm, "release": strings.Repeat("x", 101)},
	} {
		owner.must(400, "POST", g+"/source-maps", bad)
	}
	up := owner.must(201, "POST", g+"/source-maps", map[string]any{"file": "https://northpeak.example/assets/app-3f9a1c.js?v=2", "map": sm})
	if up["file"] != "/assets/app-3f9a1c.js" || up["sources"] != 2.0 || up["release"] != "" {
		t.Fatalf("uploaded: %v", up)
	}
	// The source code in the map is not kept.
	var kept int
	if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM source_maps WHERE brand_id = $1 AND position('secret'::bytea in map) > 0`, geo).Scan(&kept); err != nil || kept != 0 {
		t.Fatalf("source code kept: %d %v", kept, err)
	}
	if n := up["bytes"].(float64); n <= 0 || n > 400 {
		t.Fatalf("size as kept: %v", n)
	}

	want := "TypeError: plan 42 not found\n    at pay (src/checkout.ts:12:3)\n    at src/plans.ts:40:5\n    at c (https://northpeak.example/assets/vendor.js:1:5)"
	if o := latest(geo); o["stack_original"] != want || o["stack"] != stack {
		t.Fatalf("read through the map:\n%v\nwant\n%v", o["stack_original"], want)
	}

	// The same file again replaces the map; a release is another one; the text of a map is a map.
	owner.must(201, "POST", g+"/source-maps", map[string]any{"file": "/assets/app-3f9a1c.js", "map": `{"version":3,"sources":["src/plans.ts"],"names":[],"mappings":"AAAA"}`})
	owner.must(201, "POST", g+"/source-maps", map[string]any{"file": "vendor.js", "release": "v2", "map": sm})
	list := owner.must(200, "GET", g+"/source-maps", nil)
	if n := len(list["data"].([]any)); n != 2 || list["limit"] != 300.0 {
		t.Fatalf("maps: %v", list)
	}
	// A map uploaded under a bare file name covers that file in any folder.
	if o := latest(geo); !strings.HasSuffix(o["stack_original"].(string), "\n    at pay (src/checkout.ts:12:3)") || !strings.Contains(o["stack_original"].(string), "\n    at a (src/plans.ts:1:1)\n") {
		t.Fatalf("by file name: %v", o["stack_original"])
	}

	// Another brand has its own maps, and can't delete this one.
	if theirs := owner.must(200, "GET", "/v1/brands/"+rp+"/source-maps", nil)["data"].([]any); len(theirs) != 0 {
		t.Fatalf("the other brand's maps: %v", theirs)
	}
	id := up["id"].(string)
	owner.must(404, "DELETE", "/v1/brands/"+rp+"/source-maps/"+id, nil)
	owner.must(404, "DELETE", g+"/source-maps/nope", nil)
	owner.must(204, "DELETE", g+"/source-maps/"+id, nil)
	owner.must(404, "DELETE", g+"/source-maps/"+id, nil)

	for _, n := range []string{"source_map.uploaded", "source_map.deleted"} {
		got := named(events(), n)
		if len(got) == 0 {
			t.Fatalf("no %s event", n)
		}
		for k := range got[0].Props {
			if k == "file" || k == "release" {
				t.Fatalf("%s carries %s", n, k)
			}
		}
	}
}
