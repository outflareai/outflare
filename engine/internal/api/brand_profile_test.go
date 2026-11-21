package api_test

import (
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// The brand profile keeps what a brand's agents know about the company as
// separate facts and personas. Only a person confirms: what an API token
// adds is a suggestion and is never used with customers. Nothing is
// erased, every change is in the history, the profile is kept to its
// brand, and the facts that mirror the brand's older context fields
// change together with them.
func TestBrandProfile(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	path := "/v1/brands/" + brand + "/profile"

	// An empty profile lists its twelve sections.
	p := owner.must(200, "GET", path, nil)
	if n := len(p["sections"].([]any)); n != 12 || len(p["facts"].([]any)) != 0 || p["counts"].(map[string]any)["sections_total"] != float64(12) {
		t.Fatalf("empty profile: %d sections, %v", n, p["counts"])
	}

	// A person's fact is confirmed; it may be said to customers only when
	// its visibility says so.
	owner.must(400, "POST", path+"/facts", map[string]any{"section": "personas", "text": "x"})
	owner.must(400, "POST", path+"/facts", map[string]any{"section": "offer"})
	fact := owner.must(201, "POST", path+"/facts", map[string]any{
		"section": "offer", "title": "What we sell", "text": "Trail running shoes for wet, rocky ground.", "visibility": "customers",
		"evidence": []map[string]any{{"note": "The home page headline", "ref": "https://northpeak.example/"}},
	})
	id := fact["id"].(string)
	if fact["status"] != "confirmed" || fact["source"] != "person" || fact["say_to_customers"] != true || fact["confirmed_by"] == nil {
		t.Fatalf("a person's fact: %v", fact)
	}
	if c := named(got(), "profile_fact.created"); len(c) != 1 || c[0].Props["section"] != "offer" || c[0].Props["text"] != nil {
		t.Fatalf("profile_fact.created: %+v", c)
	}
	internal := owner.must(201, "POST", path+"/facts", map[string]any{"section": "goals", "text": "Double wholesale orders this year."})
	if internal["visibility"] != "internal" || internal["say_to_customers"] != false {
		t.Fatalf("default visibility: %v", internal)
	}

	// An API token's fact is a suggestion: not for customers, and the
	// token can neither confirm it nor change what a person confirmed.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "agent", "scopes": []string{"workspace.read", "context.manage"}})
	bot := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	bot.must(400, "POST", path+"/facts", map[string]any{"section": "proof", "text": "Rated 4.8 by 2,000 runners.", "status": "confirmed"})
	sug := bot.must(201, "POST", path+"/facts", map[string]any{"section": "proof", "text": "Rated 4.8 by 2,000 runners.", "visibility": "customers"})
	if sug["status"] != "suggested" || sug["source"] != "agent" || sug["say_to_customers"] != false {
		t.Fatalf("a token's fact: %v", sug)
	}
	bot.must(400, "PATCH", path+"/facts/"+sug["id"].(string), map[string]any{"status": "confirmed"})
	bot.must(400, "PATCH", path+"/facts/"+id, map[string]any{"text": "Road shoes."})
	bot.must(200, "PATCH", path+"/facts/"+sug["id"].(string), map[string]any{"text": "Rated 4.8 out of 5 by 2,000 runners."})
	done := owner.must(200, "PATCH", path+"/facts/"+sug["id"].(string), map[string]any{"status": "confirmed"})
	if done["status"] != "confirmed" || done["say_to_customers"] != true {
		t.Fatalf("confirmed: %v", done)
	}
	if c := named(got(), "profile_fact.confirmed"); len(c) != 1 {
		t.Fatalf("profile_fact.confirmed: %+v", c)
	}

	// A suggestion that replaces a fact is written into it when confirmed.
	rep := bot.must(201, "POST", path+"/facts", map[string]any{"section": "offer", "text": "Trail and fell running shoes.", "replaces_id": id})
	owner.must(200, "PATCH", path+"/facts/"+rep["id"].(string), map[string]any{"status": "confirmed"})
	if f := owner.must(200, "GET", path+"/facts/"+id, nil); f["text"] != "Trail and fell running shoes." || f["title"] != "What we sell" || f["status"] != "confirmed" {
		t.Fatalf("replaced fact: %v", f)
	}
	if f := owner.must(200, "GET", path+"/facts/"+rep["id"].(string), nil); f["status"] != "retired" {
		t.Fatalf("the suggestion should be retired: %v", f)
	}
	if h := owner.must(200, "GET", path+"/changes?fact_id="+id, nil)["data"].([]any); len(h) != 2 ||
		h[0].(map[string]any)["action"] != "updated" || h[0].(map[string]any)["before"].(map[string]any)["text"] != "Trail running shoes for wet, rocky ground." {
		t.Fatalf("history: %v", h)
	}

	// Retired, not deleted: gone from the profile, still listed with status all.
	owner.must(200, "PATCH", path+"/facts/"+internal["id"].(string), map[string]any{"status": "retired"})
	if l := owner.must(200, "GET", path+"/facts", nil)["data"].([]any); len(l) != 2 {
		t.Fatalf("live facts: %d", len(l))
	}
	if l := owner.must(200, "GET", path+"/facts?status=all", nil)["data"].([]any); len(l) != 4 {
		t.Fatalf("all facts: %d", len(l))
	}
	if l := owner.must(200, "GET", path+"/facts?section=proof", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("by section: %d", len(l))
	}

	// The context field and its fact are one truth, whichever is changed.
	owner.must(200, "PATCH", base+"/brands/"+brand+"/context", map[string]any{"about": "Northpeak makes trail running shoes.", "voice": "Plain and warm."})
	facts := owner.must(200, "GET", path+"/facts?section=company", nil)["data"].([]any)
	if len(facts) != 1 || facts[0].(map[string]any)["context_field"] != "about" || facts[0].(map[string]any)["text"] != "Northpeak makes trail running shoes." ||
		facts[0].(map[string]any)["status"] != "confirmed" {
		t.Fatalf("mirror fact: %v", facts)
	}
	about := facts[0].(map[string]any)["id"].(string)
	owner.must(200, "PATCH", path+"/facts/"+about, map[string]any{"text": "Northpeak makes shoes for running in the hills."})
	ctx := owner.must(200, "GET", base+"/brands/"+brand+"/context", nil)
	if ctx["about"] != "Northpeak makes shoes for running in the hills." {
		t.Fatalf("the context should follow its fact: %v", ctx["about"])
	}
	owner.must(400, "PATCH", path+"/facts/"+about, map[string]any{"section": "offer"})
	owner.must(200, "PATCH", base+"/brands/"+brand+"/context", map[string]any{"about": ""})
	if f := owner.must(200, "GET", path+"/facts/"+about, nil); f["status"] != "retired" {
		t.Fatalf("emptying the field retires its fact: %v", f)
	}
	owner.must(200, "PATCH", path+"/facts/"+about, map[string]any{"status": "confirmed"})
	if ctx = owner.must(200, "GET", base+"/brands/"+brand+"/context", nil); ctx["about"] != "Northpeak makes shoes for running in the hills." {
		t.Fatalf("restoring the fact restores the field: %v", ctx["about"])
	}

	// The brand's context carries the confirmed facts that mirror nothing.
	brief := ctx["profile"].([]any)
	if len(brief) != 2 || brief[0].(map[string]any)["section"] != "offer" || brief[0].(map[string]any)["say_to_customers"] != true {
		t.Fatalf("context profile: %v", brief)
	}

	// Personas follow the same rules.
	personas := "/v1/brands/" + brand + "/personas"
	owner.must(400, "POST", personas, map[string]any{"role": "Runs the shop"})
	per := owner.must(201, "POST", personas, map[string]any{
		"name": "Independent running shop owner", "role": "Owns one or two shops",
		"pains": []string{"Big brands undercut on price", " ", "Big brands undercut on price"}, "goals": []string{"Stock shoes customers come back for"},
	})
	if per["status"] != "confirmed" || len(per["pains"].([]any)) != 1 || len(per["triggers"].([]any)) != 0 {
		t.Fatalf("persona: %v", per)
	}
	owner.must(400, "POST", personas, map[string]any{"name": "independent running shop owner"})
	sp := bot.must(201, "POST", personas, map[string]any{"name": "Club coach"})
	if sp["status"] != "suggested" {
		t.Fatalf("a token's persona: %v", sp)
	}
	bot.must(400, "PATCH", personas+"/"+per["id"].(string), map[string]any{"role": "x"})
	owner.must(200, "PATCH", personas+"/"+sp["id"].(string), map[string]any{"status": "confirmed", "channels": []string{"Running clubs"}})
	owner.must(200, "PATCH", personas+"/"+sp["id"].(string), map[string]any{"status": "archived"})
	if l := owner.must(200, "GET", personas, nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("live personas: %d", len(l))
	}
	if l := owner.must(200, "GET", personas+"?status=all", nil)["data"].([]any); len(l) != 2 {
		t.Fatalf("all personas: %d", len(l))
	}
	if h := owner.must(200, "GET", path+"/changes?persona_id="+sp["id"].(string), nil)["data"].([]any); len(h) != 3 {
		t.Fatalf("persona history: %v", h)
	}
	for _, name := range []string{"persona.created", "persona.confirmed", "persona.archived", "profile_fact.retired", "profile_fact.restored", "profile_fact.updated"} {
		if len(named(got(), name)) == 0 {
			t.Fatalf("no %s event", name)
		}
	}

	// The overview counts what is there.
	p = owner.must(200, "GET", path, nil)
	counts := p["counts"].(map[string]any)
	if counts["confirmed"] != float64(5) || counts["suggested"] != float64(0) || counts["sections_filled"] != float64(5) {
		t.Fatalf("counts: %v", counts)
	}
	if ctx = owner.must(200, "GET", base+"/brands/"+brand+"/context", nil); len(ctx["personas"].([]any)) != 1 {
		t.Fatalf("context personas: %v", ctx["personas"])
	}

	// Kept to its brand.
	owner.must(404, "GET", "/v1/brands/"+other+"/profile/facts/"+id, nil)
	owner.must(404, "PATCH", "/v1/brands/"+other+"/profile/facts/"+id, map[string]any{"text": "x"})
	owner.must(404, "GET", "/v1/brands/"+other+"/personas/"+per["id"].(string), nil)
	if p := owner.must(200, "GET", "/v1/brands/"+other+"/profile", nil); len(p["facts"].([]any)) != 0 || len(p["personas"].([]any)) != 0 {
		t.Fatalf("another brand's profile should be empty: %v", p["counts"])
	}
	if h := owner.must(200, "GET", "/v1/brands/"+other+"/profile/changes", nil)["data"].([]any); len(h) != 0 {
		t.Fatalf("another brand's history should be empty: %v", h)
	}
}
