package api_test

import (
	"context"
	"fmt"
	"net/url"
	"testing"
)

// Every paged list answers next_cursor, and passing it back as cursor
// gives the next page, whatever the list pages by underneath.
func TestCursorPagesEveryList(t *testing.T) {
	e := freshEnv(t)
	h := e.h
	owner, ws := setupOwner(t, h)
	for i := 0; i < 5; i++ {
		owner.must(201, "POST", "/v1/workspaces/"+ws+"/leads", map[string]any{"email": fmt.Sprintf("p%d@acme-com.example", i)})
		if _, err := e.pool.Exec(context.Background(), `INSERT INTO email_verifications (workspace_id, email, status, provider) VALUES ($1, $2, 'deliverable', 'builtin')`,
			ws, fmt.Sprintf("p%d@acme-com.example", i)); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{
		"/v1/workspaces/" + ws + "/leads?limit=2",         // after an id
		"/v1/workspaces/" + ws + "/audit-events?limit=2",  // before an id
		"/v1/workspaces/" + ws + "/events?limit=2",        // the events feed
		"/v1/workspaces/" + ws + "/verifications?limit=2", // an offset
	} {
		seen := map[string]bool{}
		next, pages := path, 0
		for ; pages < 20; pages++ {
			page := owner.must(200, "GET", next, nil)
			for _, d := range page["data"].([]any) {
				item := d.(map[string]any)
				id := fmt.Sprint(item["id"])
				if item["id"] == nil {
					id = fmt.Sprint(item["email"]) // verifications are keyed by address
				}
				if seen[id] {
					t.Fatalf("%s: %s came back twice", path, id)
				}
				seen[id] = true
			}
			c, _ := page["next_cursor"].(string)
			if c == "" || page["has_more"] == false {
				break
			}
			next = path + "&cursor=" + url.QueryEscape(c)
		}
		if pages < 2 || len(seen) < 5 {
			t.Fatalf("%s: %d pages, %d items", path, pages, len(seen))
		}
	}
	body := owner.must(400, "GET", "/v1/workspaces/"+ws+"/leads?cursor=not-a-cursor", nil)
	if err := body["error"].(map[string]any); err["code"] != "invalid_cursor" || err["param"] != "cursor" {
		t.Fatalf("bad cursor: %v", err)
	}
}
