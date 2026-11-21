package api_test

import (
	"testing"
)

// Searching donor sites for addresses is queued, at most once a day per
// donor, only for the brand's own rows.
func TestLinkOrderFindEmailsQueued(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	a := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	b := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Acme", "handle": "acme"})["id"].(string)
	if code, out := owner.raw("POST", "/v1/brands/"+a+"/link-orders/catalog/import", "text/csv", intermediaryCSV); code != 200 || out["created"] != 2.0 {
		t.Fatalf("import %d: %v", code, out)
	}
	list := owner.must(200, "GET", "/v1/brands/"+a+"/link-orders/catalog", nil)["data"].([]any)
	var ids []string
	for _, d := range list {
		m := d.(map[string]any)
		if m["email_search"] != nil {
			t.Fatalf("no search has run: %v", m["email_search"])
		}
		ids = append(ids, m["id"].(string))
	}
	root := "/v1/brands/" + a + "/link-orders/catalog/find-emails"
	if out := owner.must(202, "POST", root, map[string]any{"ids": ids}); out["queued"] != true || out["searches"] != 2.0 {
		t.Fatalf("queue: %v", out)
	}
	// Once a day per donor.
	if out := owner.must(202, "POST", root, map[string]any{"ids": ids}); out["searches"] != 0.0 {
		t.Fatalf("again today: %v", out)
	}
	var jobs int
	e.pool.QueryRow(t.Context(), `SELECT count(*) FROM jobs WHERE kind = 'link_orders.find_emails' AND workspace_id = $1`, ws).Scan(&jobs)
	if jobs != 2 {
		t.Fatalf("jobs: %d", jobs)
	}
	// Another brand's route never queues this brand's rows.
	if out := owner.must(202, "POST", "/v1/brands/"+b+"/link-orders/catalog/find-emails", map[string]any{"ids": ids}); out["searches"] != 0.0 {
		t.Fatalf("other brand: %v", out)
	}
	owner.must(400, "POST", root, map[string]any{"ids": []string{}})
	many := make([]string, 51)
	for i := range many {
		many[i] = ids[0]
	}
	owner.must(400, "POST", root, map[string]any{"ids": many})
}
