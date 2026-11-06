package api_test

import (
	"context"
	"testing"
)

// Finds that came in as Google redirect links are replaced by the pages
// they lead to, a duplicate goes, and the report counts everything found.
func TestPresenceGoogleRedirects(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	brand := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	root := "/v1/brands/" + brand + "/coverage"
	if _, err := e.pool.Exec(ctx, `INSERT INTO coverage_candidates(workspace_id,brand_id,url,title,source,source_id) VALUES
		($1,$2,'https://www.google.com/url?q=https://reviews.example/northpeak&sa=U','Northpeak review','web_search','a'),
		($1,$2,'https://google.com/url?q=https://blog.example/northpeak','Northpeak on a blog','web_search','b'),
		($1,$2,'https://blog.example/northpeak','Northpeak on a blog','backlinks','c')`, ws, brand); err != nil {
		t.Fatal(err)
	}
	fixed, removed, err := e.srv.ResolvePresenceRedirects(ctx, ws, brand)
	if err != nil || fixed != 1 || removed != 1 {
		t.Fatalf("fixed %d removed %d: %v", fixed, removed, err)
	}
	urls := map[string]bool{}
	for _, c := range owner.must(200, "GET", root+"/candidates", nil)["data"].([]any) {
		urls[c.(map[string]any)["url"].(string)] = true
	}
	if len(urls) != 2 || !urls["https://reviews.example/northpeak"] || !urls["https://blog.example/northpeak"] {
		t.Fatalf("finds: %v", urls)
	}
	found := owner.must(200, "GET", root+"/reports", nil)["found"].(map[string]any)
	if found["pages"] != 2.0 || found["sites"] != 2.0 || found["unreviewed"] != 2.0 {
		t.Fatalf("found: %v", found)
	}
}
