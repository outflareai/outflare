package api_test

import (
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// A brand's saved replies: add, rename, count a use, delete; each brand
// has its own.
func TestSavedReplies(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	bb := "/v1/brands/" + brandOf(t, e, `SELECT outflare_first_brand($1)::text`, ws) + "/saved-replies"

	if l := owner.must(200, "GET", bb, nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("a new brand has saved replies: %v", l)
	}
	owner.must(400, "POST", bb, map[string]any{"name": " ", "body": "x"})
	owner.must(400, "POST", bb, map[string]any{"name": "Pricing", "body": ""})
	p := owner.must(201, "POST", bb, map[string]any{"name": "Pricing", "body": "Hi {first_name}, our plans start at $49 a month.\n\n{my_name}"})
	owner.must(409, "POST", bb, map[string]any{"name": "pricing", "body": "again"})
	d := owner.must(201, "POST", bb, map[string]any{"name": "Demo", "body": "Happy to show you around."})
	id, demo := p["id"].(string), d["id"].(string)

	owner.must(400, "PATCH", bb+"/"+id, map[string]any{})
	owner.must(404, "PATCH", bb+"/nope", map[string]any{"name": "x"})
	owner.must(409, "PATCH", bb+"/"+demo, map[string]any{"name": "PRICING"})
	if v := owner.must(200, "PATCH", bb+"/"+demo, map[string]any{"name": "Book a demo"}); v["name"] != "Book a demo" || v["body"] != "Happy to show you around." {
		t.Fatalf("renamed: %v", v)
	}

	// The most used leads the list.
	owner.must(404, "POST", bb+"/00000000-0000-0000-0000-000000000000/used", nil)
	if v := owner.must(200, "POST", bb+"/"+id+"/used", nil); v["uses"] != 1.0 {
		t.Fatalf("used: %v", v)
	}
	if l := owner.must(200, "GET", bb, nil)["data"].([]any); len(l) != 2 || l[0].(map[string]any)["id"] != id {
		t.Fatalf("list: %v", l)
	}

	// Another brand has none of them, and can't change them.
	north := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	nb := "/v1/brands/" + north + "/saved-replies"
	if l := owner.must(200, "GET", nb, nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("another brand sees them: %v", l)
	}
	owner.must(404, "PATCH", nb+"/"+id, map[string]any{"name": "x"})
	owner.must(404, "DELETE", nb+"/"+id, nil)
	owner.must(201, "POST", nb, map[string]any{"name": "Pricing", "body": "Its own."})

	owner.must(204, "DELETE", bb+"/"+demo, nil)
	owner.must(404, "DELETE", bb+"/"+demo, nil)
	for name, want := range map[string]int{"saved_reply.created": 3, "saved_reply.updated": 1, "saved_reply.deleted": 1} {
		if g := len(named(got(), name)); g != want {
			t.Errorf("%d %s events, want %d", g, name, want)
		}
	}
}
