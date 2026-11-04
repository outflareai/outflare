package api_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// An approved item reaches the brand's own site through the
// content.approved webhook, with everything the site needs to put it in
// place, and the site reports the live address back.
func TestContentDeliveredToTheSite(t *testing.T) {
	e, owner, ws, _ := webhookEnv(t)
	ctx := context.Background()
	rc := newReceiver(t)
	base := "/v1/workspaces/" + ws
	brand := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	path := "/v1/brands/" + brand + "/content"
	owner.must(201, "POST", base+"/webhooks", map[string]any{"url": rc.URL + "/content", "events": []string{"content.*"}})

	// Bad delivery fields are refused.
	for _, bad := range []map[string]any{
		{"language": "English"},
		{"placement": map[string]any{"type": "novel"}},
		{"placement": map[string]any{"path": "//evil.example/x"}},
		{"seo": map[string]any{"intent": "curious"}},
		{"seo": map[string]any{"schema": []any{map[string]any{"name": "no type"}}}},
		{"links": map[string]any{"internal": []any{map[string]any{"anchor": "x", "href": "javascript:alert(1)"}}}},
		{"links": map[string]any{"external": []any{map[string]any{"anchor": "x", "href": "/relative"}}}},
		{"media": []any{map[string]any{"url": "data:image/png;base64,AAAA"}}},
		{"brief": map[string]any{"goal": "fame"}},
		{"brief": map[string]any{"redirects": []any{map[string]any{"from": "/a", "to": "/a"}}}},
		{"placement": map[string]any{"type": "post", "surprise": true}},
	} {
		bad["title"] = "Bad"
		owner.must(400, "POST", path, bad)
	}

	item := owner.must(201, "POST", path, map[string]any{
		"title": "Northpeak vs Ridgeline", "slug": "northpeak-vs-ridgeline", "body": "## Pricing\n\nNorthpeak bills **per seat**.",
		"excerpt": "A side-by-side look.", "tags": []string{"comparison"}, "canonical_url": "https://northpeak.example/blog/northpeak-vs-ridgeline",
		"language":  "en-AU",
		"placement": map[string]any{"type": "comparison", "collection": "blog", "path": "/blog/northpeak-vs-ridgeline", "parent": "/compare", "template_hint": "/blog/northpeak-vs-summitly", "nav_label": "vs Ridgeline"},
		"seo": map[string]any{"meta_title": "Northpeak vs Ridgeline", "meta_description": "Compare pricing.", "primary_keyword": "northpeak vs ridgeline",
			"secondary_keywords": []string{"ridgeline alternative"}, "intent": "commercial", "robots": "index,follow",
			"schema": []any{map[string]any{"@context": "https://schema.org", "@type": "Article", "headline": "Northpeak vs Ridgeline"}}},
		"links": map[string]any{
			"internal":       []any{map[string]any{"anchor": "pricing", "href": "/pricing", "why": "money page"}},
			"external":       []any{map[string]any{"anchor": "Ridgeline's pricing", "href": "https://ridgeline.example/pricing", "rel": "nofollow"}},
			"add_links_from": []any{map[string]any{"page": "/blog/choosing-a-crm", "after_text": "teams of this size often", "anchor": "compare Northpeak and Ridgeline"}}},
		"media": []any{map[string]any{"role": "featured", "url": "https://cdn.northpeak.example/vs.webp", "alt": "Two dashboards", "width": 1600, "height": 900}},
		"brief": map[string]any{"goal": "search", "opportunity": "Ranks 14th.", "check_before_live": []string{"Ridgeline's price"},
			"overlaps": []any{map[string]any{"page": "/blog/ridgeline-alternatives", "advice": "link them"}}},
	})
	id := item["id"].(string)
	if item["language"] != "en-AU" || item["revision"].(float64) != 0 || item["placement"].(map[string]any)["path"] != "/blog/northpeak-vs-ridgeline" || len(item["media"].([]any)) != 1 {
		t.Fatalf("item: %v", item)
	}
	// A draft isn't sent, and can't be reported live.
	owner.must(409, "POST", path+"/"+id+"/delivered", map[string]any{"url": "https://northpeak.example/blog/northpeak-vs-ridgeline"})
	time.Sleep(150 * time.Millisecond)
	if rc.count() != 0 {
		t.Fatalf("a draft was sent: %d", rc.count())
	}

	// Approval sends the whole item.
	approved := owner.must(200, "PATCH", path+"/"+id, map[string]any{"status": "approved"})
	if approved["revision"].(float64) != 1 || approved["approved_at"] == nil || approved["approved_by"] == nil {
		t.Fatalf("approved: %v", approved)
	}
	ev := waitDelivery(t, rc, 1)
	if ev["type"] != "content.approved" || ev["brand_id"] != brand {
		t.Fatalf("event: %v", ev)
	}
	d := ev["data"].(map[string]any)
	c := d["content"].(map[string]any)
	if d["content_id"] != id || d["revision"].(float64) != 1 || d["language"] != "en-AU" || d["slug"] != "northpeak-vs-ridgeline" ||
		!strings.Contains(c["html"].(string), "<strong>per seat</strong>") || !strings.Contains(c["markdown"].(string), "**per seat**") || c["word_count"].(float64) != 5 {
		t.Fatalf("delivery: %v", d)
	}
	seo := d["seo"].(map[string]any)
	if seo["canonical"] != "https://northpeak.example/blog/northpeak-vs-ridgeline" || seo["intent"] != "commercial" || len(seo["schema"].([]any)) != 1 {
		t.Fatalf("seo: %v", seo)
	}
	if d["placement"].(map[string]any)["template_hint"] == nil || len(d["links"].(map[string]any)["add_links_from"].([]any)) != 1 ||
		d["media"].([]any)[0].(map[string]any)["alt"] != "Two dashboards" || d["brief"].(map[string]any)["goal"] != "search" {
		t.Fatalf("delivery fields: %v", d)
	}
	if !strings.HasSuffix(d["callback"].(map[string]any)["path"].(string), "/content/"+id+"/delivered") || d["review"].(map[string]any)["approved_at"] == nil {
		t.Fatalf("callback and review: %v %v", d["callback"], d["review"])
	}
	// The audit log keeps ids, never the body.
	var stored string
	if err := e.pool.QueryRow(ctx, `SELECT data::text FROM audit_events WHERE workspace_id = $1 AND action = 'content.approved' AND target_id = $2`, ws, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "per seat") || !strings.Contains(stored, `"revision": 1`) {
		t.Fatalf("audit data: %s", stored)
	}

	// Approved again after an edit: the revision counts up, and the site
	// can't report the older one live.
	owner.must(200, "PATCH", path+"/"+id, map[string]any{"body": "## Pricing\n\nUpdated.", "status": "draft"})
	owner.must(200, "PATCH", path+"/"+id, map[string]any{"status": "approved"})
	if ev := waitDelivery(t, rc, 2); ev["data"].(map[string]any)["revision"].(float64) != 2 {
		t.Fatalf("second approval: %v", ev)
	}
	owner.must(400, "POST", path+"/"+id+"/delivered", map[string]any{"url": "ftp://northpeak.example/x"})
	owner.must(409, "POST", path+"/"+id+"/delivered", map[string]any{"url": "https://northpeak.example/blog/northpeak-vs-ridgeline", "revision": 1})
	live := owner.must(200, "POST", path+"/"+id+"/delivered", map[string]any{"url": "https://northpeak.example/blog/northpeak-vs-ridgeline", "revision": 2})
	if live["status"] != "published" || live["url"] != "https://northpeak.example/blog/northpeak-vs-ridgeline" || live["published_at"] == nil {
		t.Fatalf("delivered: %v", live)
	}
	if ev := waitDelivery(t, rc, 3); ev["type"] != "content.delivered" || ev["data"].(map[string]any)["revision"].(float64) != 2 {
		t.Fatalf("delivered event: %v", ev)
	}

	// Created approved: sent straight away.
	owner.must(201, "POST", path, map[string]any{"title": "Glossary: seat", "body": "A seat is one person.", "status": "approved", "placement": map[string]any{"type": "glossary"}})
	if ev := waitDelivery(t, rc, 4); ev["type"] != "content.approved" || ev["data"].(map[string]any)["placement"].(map[string]any)["type"] != "glossary" {
		t.Fatalf("created approved: %v", ev)
	}

	// Kept to its brand.
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "handle": "repocket"})["id"].(string)
	owner.must(404, "POST", "/v1/brands/"+other+"/content/"+id+"/delivered", map[string]any{"url": "https://northpeak.example/x"})
}

// waitDelivery waits for the receiver's nth delivery and returns it.
func waitDelivery(t *testing.T, rc *receiver, n int) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for rc.count() < n && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if rc.count() < n {
		t.Fatalf("receiver got %d deliveries, want %d", rc.count(), n)
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	var ev map[string]any
	if err := json.Unmarshal(rc.bodies[n-1], &ev); err != nil {
		t.Fatal(err)
	}
	return ev
}
