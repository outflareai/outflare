package api_test

import (
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestKnowledge(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	kb := base + "/knowledge"

	geo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode", "handle": "geonode"})["id"].(string)
	rep := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "handle": "repocket"})["id"].(string)

	// Folders, shared and a brand's, and one inside the brand's takes it.
	policies := owner.must(201, "POST", kb+"/collections", map[string]any{"name": " Policies "})
	if policies["name"] != "Policies" || policies["brand_id"] != nil || policies["item_count"] != 0.0 {
		t.Fatalf("folder = %v", policies)
	}
	product := owner.must(201, "POST", kb+"/collections", map[string]any{"name": "Product", "brand_id": "geonode"})
	pricing := owner.must(201, "POST", kb+"/collections", map[string]any{"name": "Pricing", "parent_id": product["id"]})
	if pricing["brand_id"] != geo || pricing["position"] != 0.0 {
		t.Fatalf("sub folder = %v", pricing)
	}
	owner.must(400, "POST", kb+"/collections", map[string]any{"name": "x", "parent_id": product["id"], "brand_id": "repocket"})
	owner.must(400, "PATCH", kb+"/collections/"+product["id"].(string), map[string]any{"parent_id": pricing["id"]})
	owner.must(400, "POST", kb+"/collections", map[string]any{"name": ""})

	// An article and a quick answer.
	art := owner.must(201, "POST", kb+"/items", map[string]any{
		"title": "Refund policy", "body": "# Refunds\n\nWe refund **any** plan within 30 days.", "collection_id": policies["id"],
	})
	if art["kind"] != "article" || art["visibility"] != "team" || art["status"] != "published" || art["version"] != 1.0 ||
		art["excerpt"] != "We refund any plan within 30 days." || art["owner_id"] == nil || art["body"] == nil {
		t.Fatalf("article = %v", art)
	}
	snip := owner.must(201, "POST", kb+"/items", map[string]any{
		"kind": "snippet", "title": "Do you offer a free trial?", "body": "Yes, 7 days.", "visibility": "customers_ai", "collection_id": pricing["id"],
	})
	if snip["brand_id"] != geo {
		t.Fatalf("snippet takes its folder's brand = %v", snip)
	}
	owner.must(400, "POST", kb+"/items", map[string]any{"kind": "snippet", "title": "Empty?"})
	owner.must(400, "POST", kb+"/items", map[string]any{"title": "Wrong brand", "collection_id": pricing["id"], "brand_id": "repocket"})
	owner.must(400, "POST", kb+"/items", map[string]any{"title": "x", "visibility": "everyone"})
	owner.must(400, "POST", kb+"/items", map[string]any{"title": "x", "owner_id": "00000000-0000-0000-0000-000000000000"})
	owner.must(201, "POST", kb+"/items", map[string]any{"title": "Repocket payouts", "body": "Paid monthly.", "brand_id": "repocket"})

	// Lists: no body, filters, word search with prefixes, brands.
	l := owner.must(200, "GET", kb+"/items", nil)
	if l["total"] != 3.0 || l["data"].([]any)[0].(map[string]any)["body"] != nil {
		t.Fatalf("list = %v", l)
	}
	if l := owner.must(200, "GET", kb+"/items?q=refu", nil)["data"].([]any); len(l) != 1 || l[0].(map[string]any)["id"] != art["id"] {
		t.Fatalf("search = %v", l)
	}
	if l := owner.must(200, "GET", kb+"/items?q=trial&kind=snippet&visibility=customers_ai", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("filtered = %v", l)
	}
	if l := owner.must(200, "GET", kb+"/items?brand=geonode", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("by brand = %v", l)
	}
	if l := owner.must(200, "GET", kb+"/items?collection_id=none", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("no folder = %v", l)
	}
	owner.must(400, "GET", kb+"/items?kind=page", nil)
	if c := owner.must(200, "GET", kb+"/collections", nil)["data"].([]any); len(c) != 3 || c[0].(map[string]any)["item_count"] != 1.0 {
		t.Fatalf("folders = %v", c)
	}

	// Editing the text keeps a version; other changes don't.
	id := art["id"].(string)
	up := owner.must(200, "PATCH", kb+"/items/"+id, map[string]any{"body": "We refund any plan within 14 days.", "visibility": "public"})
	if up["version"] != 2.0 || up["visibility"] != "public" {
		t.Fatalf("updated = %v", up)
	}
	if up = owner.must(200, "PATCH", kb+"/items/"+id, map[string]any{"status": "draft", "review_by": "2027-01-31"}); up["version"] != 2.0 || up["review_by"] != "2027-01-31" {
		t.Fatalf("settings = %v", up)
	}
	owner.must(400, "PATCH", kb+"/items/"+id, map[string]any{"kind": "snippet"})
	v := owner.must(200, "GET", kb+"/items/"+id+"/versions", nil)["data"].([]any)
	if len(v) != 2 || v[1].(map[string]any)["body"] != "# Refunds\n\nWe refund **any** plan within 30 days." {
		t.Fatalf("versions = %v", v)
	}
	back := owner.must(200, "POST", kb+"/items/"+id+"/versions/1/restore", nil)
	if back["version"] != 3.0 || back["body"] != "# Refunds\n\nWe refund **any** plan within 30 days." {
		t.Fatalf("restored version = %v", back)
	}
	owner.must(400, "POST", kb+"/items/"+id+"/versions/9/restore", nil)

	// The bin: hidden, restorable, then deleted for good.
	owner.must(204, "DELETE", kb+"/items/"+id, nil)
	owner.must(404, "PATCH", kb+"/items/"+id, map[string]any{"title": "x"})
	if l := owner.must(200, "GET", kb+"/items?deleted=true", nil)["data"].([]any); len(l) != 1 || l[0].(map[string]any)["deleted_at"] == nil {
		t.Fatalf("bin = %v", l)
	}
	owner.must(200, "POST", kb+"/items/"+id+"/restore", nil)
	owner.must(400, "POST", kb+"/items/"+id+"/restore", nil)
	owner.must(400, "DELETE", kb+"/items/"+id+"?permanent=true", nil)
	owner.must(204, "DELETE", kb+"/items/"+id, nil)
	owner.must(204, "DELETE", kb+"/items/"+id+"?permanent=true", nil)
	owner.must(404, "GET", kb+"/items/"+id, nil)

	// Deleting a folder moves what it holds up a level.
	owner.must(204, "DELETE", kb+"/collections/"+product["id"].(string), nil)
	if c := owner.must(200, "GET", kb+"/collections?brand=geonode", nil)["data"].([]any); len(c) != 1 || c[0].(map[string]any)["parent_id"] != nil {
		t.Fatalf("after folder delete = %v", c)
	}
	if s := owner.must(200, "GET", kb+"/items/"+snip["id"].(string), nil); s["collection_id"] != pricing["id"] {
		t.Fatalf("snippet moved = %v", s)
	}

	// A Repocket-only token sees Repocket's and shared knowledge, and can't
	// change the shared folder.
	scopes := []string{"workspace.read", "context.manage"}
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Repocket agent", "scopes": scopes, "brand": "repocket"})
	rp := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	if l := rp.must(200, "GET", kb+"/items", nil)["data"].([]any); len(l) != 1 || l[0].(map[string]any)["brand_id"] != rep {
		t.Fatalf("brand-limited list = %v", l)
	}
	rp.must(404, "GET", kb+"/items/"+snip["id"].(string), nil)
	rp.must(403, "PATCH", kb+"/collections/"+policies["id"].(string), map[string]any{"name": "Mine"})
	made := rp.must(201, "POST", kb+"/items", map[string]any{"kind": "snippet", "title": "When are payouts?", "body": "Monthly."})
	if made["brand_id"] != rep || made["owner_id"] != nil {
		t.Fatalf("brand-limited create = %v", made)
	}

	seen := map[string]bool{}
	for _, ev := range got() {
		seen[ev.Name] = true
	}
	for _, name := range []string{"knowledge.created", "knowledge.updated", "knowledge.deleted", "knowledge.restored", "knowledge.purged",
		"knowledge_collection.created", "knowledge_collection.deleted"} {
		if !seen[name] {
			t.Fatalf("no %s event", name)
		}
	}
}
