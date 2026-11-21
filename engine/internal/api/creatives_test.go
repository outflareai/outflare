package api_test

import (
	"bytes"
	"context"
	"image"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/track"
)

// A skill keeps every version of its prompts, shares new creatives
// between the prompts of a split test, and a creative says what made it
// and waits for a person's yes.
func TestCreatives(t *testing.T) {
	got := track.Capture(t)
	bucket := &memBlobs{m: map[string][]byte{}}
	e := freshEnvWith(t, func(o *api.Options) { o.Files = bucket })
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	fake := &fakeMedia{t: t, started: map[string]int{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	e.integrations.Client.BaseURLs["openai"] = srv.URL
	skills, creatives := "/v1/brands/"+brand+"/creative-skills", "/v1/brands/"+brand+"/creatives"
	code := func(b map[string]any) any { return b["error"].(map[string]any)["code"] }

	// What isn't a skill.
	recipe := map[string]any{"name": "Static ad", "description": "One headline over one picture", "kind": "image", "model": "img-model-test",
		"sizes": []string{"1:1", "4:5", "1:1"}, "rules": "Never: extra text in the picture. Accent: {Brand colour}.",
		"variants": []map[string]any{{"name": "Problem first", "picture": "A {persona} stuck on {pain}, for {brand}.", "copy": "Name {pain} as a question."}}}
	without := func(k string, v any) map[string]any {
		m := map[string]any{}
		for key, val := range recipe {
			m[key] = val
		}
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
		return m
	}
	owner.must(400, "POST", skills, without("name", " "))
	owner.must(400, "POST", skills, without("kind", "voice"))
	owner.must(400, "POST", skills, without("model", nil))
	owner.must(400, "POST", skills, without("variants", []map[string]any{}))
	owner.must(400, "POST", skills, without("sizes", []string{"square"}))
	owner.must(400, "POST", skills, without("provider", "elevenlabs"))

	k := owner.must(201, "POST", skills, recipe)
	skill := k["id"].(string)
	cur := k["current"].(map[string]any)
	first := cur["variants"].([]any)[0].(map[string]any)
	if k["version"] != float64(1) || k["split_test"] != false || k["enabled"] != true || len(cur["sizes"].([]any)) != 2 ||
		first["key"] != "A" || first["share"] != float64(100) || strings.Join(strs(first["fill_ins"]), ",") != "persona,pain,brand,brand_colour" {
		t.Fatalf("new skill: %v", k)
	}
	if c := code(owner.must(409, "POST", skills, recipe)); c != "conflict" {
		t.Fatalf("same name twice: %v", c)
	}

	// No account, no creative; and a prompt with a hole is never sent.
	ask := map[string]any{"skill_id": skill, "persona": "Scraping engineer", "pain": "getting blocked", "headline": "Blocked again?", "cta": "Start free",
		"tags": []string{"Proof", "q4"}, "fill_ins": map[string]string{"Brand colour": "green"}}
	if c := code(owner.must(409, "POST", creatives, ask)); c != "connector_needed" {
		t.Fatalf("without a connection: %v", c)
	}
	owner.must(201, "POST", "/v1/workspaces/"+ws+"/integrations", map[string]any{"connector": "openai", "credentials": map[string]any{"api_key": openAIMediaKey}})
	hole := owner.must(400, "POST", creatives, map[string]any{"skill_id": skill, "persona": "Scraping engineer", "pain": "getting blocked"})
	if !strings.Contains(hole["error"].(map[string]any)["message"].(string), "{brand_colour}") {
		t.Fatalf("hole: %v", hole)
	}
	owner.must(400, "POST", creatives, map[string]any{"skill_id": brand})

	// A creative: the prompt filled in, made on the brand's account, then
	// waiting for a person.
	c := owner.must(201, "POST", creatives, ask)
	id := c["id"].(string)
	if c["status"] != "making" || c["code"] != "CR-0001" || c["name"] != "Blocked again?" || c["variant"] != "A" || c["skill_version"] != float64(1) ||
		c["skill_name"] != "Static ad" || c["provider"] != "openai" || strings.Join(strs(c["tags"]), ",") != "proof,q4" || fake.started["pictures"] != 0 {
		t.Fatalf("making: %v", c)
	}
	if c := code(owner.must(409, "POST", creatives+"/"+id+"/approve", nil)); c != "conflict" {
		t.Fatalf("approving before it is made: %v", c)
	}
	if status, _, _ := owner.download(creatives + "/" + id + "/ad"); status != 409 {
		t.Fatalf("ad before it is made: %d", status)
	}
	if err := e.srv.RunGeneration(ctx, ws, c["generation_id"].(string)); err != nil {
		t.Fatal(err)
	}
	want := "A Scraping engineer stuck on getting blocked, for " + brandName(t, e, brand) + ".\nNever: extra text in the picture. Accent: green."
	if fake.prompt != want {
		t.Fatalf("prompt sent: %q, want %q", fake.prompt, want)
	}
	if c = owner.must(200, "GET", creatives+"/"+id, nil); c["status"] != "needs_review" || c["asset_id"] == nil || c["prompt"] != want {
		t.Fatalf("made: %v", c)
	}

	// Words and tags change without a new picture; a stale version is refused.
	c = owner.must(200, "PATCH", creatives+"/"+id, map[string]any{"name": "Blocked again, dashboard", "headline": "Still blocked?", "version": c["version"]})
	if c["name"] != "Blocked again, dashboard" || c["headline"] != "Still blocked?" || c["persona"] != "Scraping engineer" || fake.started["pictures"] != 1 {
		t.Fatalf("renamed: %v", c)
	}
	owner.must(409, "PATCH", creatives+"/"+id, map[string]any{"name": "x", "version": 1})
	owner.must(400, "PATCH", creatives+"/"+id, map[string]any{"name": " "})

	// The ad: the words laid over the picture, in any size, drawn from
	// the creative as it is now.
	adURL := creatives + "/" + id + "/ad"
	status, hdr, body := owner.download(adURL + "?size=4:5&width=540")
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(body)); status != 200 || err != nil || cfg.Width != 540 || cfg.Height != 675 || hdr.Get("Content-Type") != "image/jpeg" || hdr.Get("ETag") == "" {
		t.Fatalf("ad: %d %v %v %v", status, cfg, err, hdr)
	}
	if status, _, body = owner.download(adURL + "?format=png"); status != 200 {
		t.Fatalf("ad as png: %d", status)
	} else if cfg, f, err := image.DecodeConfig(bytes.NewReader(body)); err != nil || f != "png" || cfg.Width != 1080 || cfg.Height != 540 {
		t.Fatalf("ad as png: %v %s %v", cfg, f, err)
	}
	for _, q := range []string{"?size=wide", "?width=50", "?format=gif"} {
		if status, _, _ = owner.download(adURL + q); status != 400 {
			t.Fatalf("ad%s: %d", q, status)
		}
	}
	// New words give a new ad without a new picture.
	owner.must(200, "PATCH", creatives+"/"+id, map[string]any{"headline": "Blocked again?"})
	if _, again, _ := owner.download(adURL + "?size=4:5&width=540"); again.Get("ETag") == hdr.Get("ETag") || fake.started["pictures"] != 1 {
		t.Fatalf("ad after new words: %v, pictures %d", again.Get("ETag"), fake.started["pictures"])
	}

	// A person's yes and no.
	if c = owner.must(200, "POST", creatives+"/"+id+"/reject", map[string]any{"reason": "Looks made by AI"}); c["status"] != "rejected" || c["review_reason"] != "Looks made by AI" || c["reviewed_at"] == nil {
		t.Fatalf("rejected: %v", c)
	}
	if c = owner.must(200, "POST", creatives+"/"+id+"/approve", nil); c["status"] != "approved" || c["review_reason"] != "" {
		t.Fatalf("approved: %v", c)
	}

	// Liking is separate from approving, takes a reason, and can be taken back.
	owner.must(400, "POST", creatives+"/"+id+"/rating", map[string]any{"rating": "great"})
	if c = owner.must(200, "POST", creatives+"/"+id+"/rating", map[string]any{"rating": "liked", "reason": "The green accent works"}); c["rating"] != "liked" ||
		c["rating_reason"] != "The green accent works" || c["rated_at"] == nil || c["status"] != "approved" {
		t.Fatalf("liked: %v", c)
	}
	// One history: made, changed, rejected with why, approved, liked.
	var kinds []string
	for _, h := range owner.must(200, "GET", creatives+"/"+id+"/history", nil)["data"].([]any) {
		line := h.(map[string]any)
		kinds = append(kinds, line["kind"].(string))
		if line["kind"] == "rejected" && line["said"] != "Looks made by AI" {
			t.Fatalf("rejected line: %v", line)
		}
	}
	if got := strings.Join(kinds, " "); !strings.HasPrefix(got, "liked approved rejected") || !strings.HasSuffix(got, "made") {
		t.Fatalf("history: %v", kinds)
	}
	// A rule the brand taught goes into the next picture's prompt.
	owner.must(201, "POST", "/v1/brands/"+brand+"/creative-lessons", map[string]any{"kind": "rule", "rule": "No stock-photo smiles", "applies_to": "image"})

	// A second prompt is a new version and a split test: the first
	// version stays as it was, and new creatives are shared out.
	k = owner.must(200, "PATCH", skills+"/"+skill, map[string]any{"version": 1, "note": "Added prompt B", "rules": "",
		"variants": []map[string]any{
			{"name": "Problem first", "picture": "A {persona} stuck on {pain}.", "share": 50},
			{"name": "One number", "picture": "One picture that shows {proof}.", "share": 50}}})
	if k["version"] != float64(2) || k["split_test"] != true || k["creatives"] != float64(1) || k["current"].(map[string]any)["creatives"] != float64(0) {
		t.Fatalf("version 2: %v", k)
	}
	owner.must(409, "PATCH", skills+"/"+skill, map[string]any{"version": 1, "rules": "x"})
	made := map[string]int{}
	for range 4 {
		c := owner.must(201, "POST", creatives, map[string]any{"skill_id": skill, "persona": "Agency owner", "pain": "unclear pricing", "fill_ins": map[string]string{"proof": "99.9% uptime"}})
		made[c["variant"].(string)]++
		if c["skill_version"] != float64(2) {
			t.Fatalf("made by: %v", c)
		}
	}
	if made["A"] != 2 || made["B"] != 2 {
		t.Fatalf("shares: %v", made)
	}
	if c := owner.must(201, "POST", creatives, map[string]any{"skill_id": skill, "variant": "b", "fill_ins": map[string]string{"proof": "190 countries"}}); c["variant"] != "B" || c["code"] != "CR-0006" || !strings.Contains(c["prompt"].(string), "Always:\n- No stock-photo smiles") {
		t.Fatalf("named variant: %v", c)
	}
	owner.must(400, "POST", creatives, map[string]any{"skill_id": skill, "variant": "C"})

	// Versions are kept, and going back makes a new one.
	vs := owner.must(200, "GET", skills+"/"+skill+"/versions", nil)["data"].([]any)
	if len(vs) != 2 || vs[0].(map[string]any)["note"] != "Added prompt B" || vs[1].(map[string]any)["creatives"] != float64(1) || len(vs[1].(map[string]any)["variants"].([]any)) != 1 {
		t.Fatalf("versions: %v", vs)
	}
	owner.must(400, "PATCH", skills+"/"+skill, map[string]any{"restore_version": 9})
	if k = owner.must(200, "PATCH", skills+"/"+skill, map[string]any{"restore_version": 1}); k["version"] != float64(3) || k["split_test"] != false ||
		k["current"].(map[string]any)["note"] != "Went back to version 1" {
		t.Fatalf("went back: %v", k)
	}

	// The switch changes no version, and an off skill makes nothing.
	if k = owner.must(200, "PATCH", skills+"/"+skill, map[string]any{"enabled": false, "name": "Static"}); k["version"] != float64(3) || k["enabled"] != false || k["name"] != "Static" {
		t.Fatalf("off: %v", k)
	}
	owner.must(409, "POST", creatives, ask)

	// A creative whose picture the provider refuses has nothing to answer.
	owner.must(200, "PATCH", skills+"/"+skill, map[string]any{"enabled": true})
	fake.refuse = true
	bad := owner.must(201, "POST", creatives, ask)
	if err := e.srv.RunGeneration(ctx, ws, bad["generation_id"].(string)); err != nil {
		t.Fatal(err)
	}
	if c = owner.must(200, "GET", creatives+"/"+bad["id"].(string), nil); c["status"] != "failed" || c["error_code"] != "connector_invalid" {
		t.Fatalf("failed: %v", c)
	}
	owner.must(409, "POST", creatives+"/"+bad["id"].(string)+"/approve", nil)

	// Lists and filters; another brand sees none of it.
	for q, n := range map[string]int{"": 7, "?status=approved": 1, "?status=making": 5, "?status=failed": 1, "?persona=agency+owner": 4, "?tag=proof": 2, "?q=dashboard": 1, "?skill_id=" + skill: 7} {
		if list := owner.must(200, "GET", creatives+q, nil)["data"].([]any); len(list) != n {
			t.Fatalf("list %q: %d, want %d", q, len(list), n)
		}
	}
	owner.must(400, "GET", creatives+"?status=live", nil)
	other := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Summitly"})["id"].(string)
	if list := owner.must(200, "GET", "/v1/brands/"+other+"/creatives", nil)["data"].([]any); len(list) != 0 {
		t.Fatalf("other brand's creatives: %v", list)
	}
	if list := owner.must(200, "GET", "/v1/brands/"+other+"/creative-skills", nil)["data"].([]any); len(list) != 0 {
		t.Fatalf("other brand's skills: %v", list)
	}
	owner.must(404, "GET", "/v1/brands/"+other+"/creatives/"+id, nil)
	owner.must(404, "GET", "/v1/brands/"+other+"/creative-skills/"+skill, nil)
	owner.must(400, "POST", "/v1/brands/"+other+"/creatives", map[string]any{"skill_id": skill})

	// Archived skills leave the list.
	owner.must(200, "PATCH", skills+"/"+skill, map[string]any{"archived": true})
	if list := owner.must(200, "GET", skills, nil)["data"].([]any); len(list) != 0 {
		t.Fatalf("after archiving: %v", list)
	}
	if list := owner.must(200, "GET", skills+"?archived=true", nil)["data"].([]any); len(list) != 1 {
		t.Fatalf("archived: %v", list)
	}

	wantEvents := map[string]int{"creative_skill.created": 1, "creative_skill.version_saved": 2, "creative_skill.updated": 3,
		"creative.requested": 7, "creative.updated": 2, "creative.rejected": 1, "creative.approved": 1, "creative.rated": 1}
	seen := map[string]int{}
	for _, ev := range got() {
		if _, ok := wantEvents[ev.Name]; ok {
			seen[ev.Name]++
			for key := range ev.Props {
				if key == "prompt" || key == "name" || key == "headline" || key == "persona" || key == "reason" {
					t.Fatalf("%s carries %s", ev.Name, key)
				}
			}
		}
	}
	for name, n := range wantEvents {
		if seen[name] != n {
			t.Fatalf("%s events: %d, want %d (all: %v)", name, seen[name], n, seen)
		}
	}
}

func strs(v any) []string {
	out := []string{}
	list, _ := v.([]any)
	for _, x := range list {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

func brandName(t *testing.T, e *env, brand string) string {
	t.Helper()
	var name string
	if err := e.pool.QueryRow(context.Background(), `SELECT name FROM brands WHERE id = $1`, brand).Scan(&name); err != nil {
		t.Fatal(err)
	}
	return name
}
