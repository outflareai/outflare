package api_test

import (
	"strings"
	"testing"
)

// ?fields= trims each item of a list, or a single object, to the fields
// asked for (id always stays), and names the fields that exist when one
// does not.
func TestFieldsSelectsWhatTheCallerReads(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	lead := owner.must(201, "POST", "/v1/workspaces/"+ws+"/leads", map[string]any{"email": "ana@acme-com.example", "first_name": "Ana"})
	owner.must(201, "POST", "/v1/workspaces/"+ws+"/leads", map[string]any{"email": "bo@acme-com.example"})

	page := owner.must(200, "GET", "/v1/workspaces/"+ws+"/leads?limit=1&fields=email", nil)
	items := page["data"].([]any)
	if len(items) != 1 {
		t.Fatalf("items: %v", items)
	}
	item := items[0].(map[string]any)
	if len(item) != 2 || item["email"] == nil || item["id"] == nil {
		t.Fatalf("item should have only id and email: %v", item)
	}
	if page["next_cursor"] == nil {
		t.Fatalf("paging fields should stay: %v", page)
	}

	one := owner.must(200, "GET", "/v1/workspaces/"+ws+"/leads/"+lead["id"].(string)+"?fields=first_name,email", nil)
	if len(one) != 3 || one["first_name"] != "Ana" {
		t.Fatalf("single object: %v", one)
	}

	body := owner.must(400, "GET", "/v1/workspaces/"+ws+"/leads?fields=emial", nil)
	err := body["error"].(map[string]any)
	if err["param"] != "fields" || !strings.Contains(err["message"].(string), "email") {
		t.Fatalf("unknown field: %v", err)
	}
	owner.must(400, "GET", "/v1/workspaces/"+ws+"/leads?fields=Email,id", nil)
}
