package api_test

import "testing"

// A person's saved table columns follow them: saved, listed, replaced and
// dropped, each person their own, and nothing odd gets stored.
func TestTableViews(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	if l := owner.must(200, "GET", "/v1/me/table-views", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("new person: %v", l)
	}
	got := owner.must(200, "PUT", "/v1/me/table-views/list-people", map[string]any{"hidden": []string{"email", "email", "signed_up"}})
	if hid := got["hidden"].([]any); got["view"] != "list-people" || len(hid) != 2 {
		t.Fatalf("saved: %v", got)
	}
	owner.must(200, "PUT", "/v1/me/table-views/list-people", map[string]any{"hidden": []string{}})
	l := owner.must(200, "GET", "/v1/me/table-views", nil)["data"].([]any)
	if len(l) != 1 || len(l[0].(map[string]any)["hidden"].([]any)) != 0 {
		t.Fatalf("replaced: %v", l)
	}
	owner.must(400, "PUT", "/v1/me/table-views/Not%20a%20name", map[string]any{"hidden": []string{}})
	owner.must(400, "PUT", "/v1/me/table-views/list-people", map[string]any{"hidden": []string{"a column"}})
	// Someone else has their own.
	other, _ := joinAs(t, owner, ws, "ana@acme.example", map[string]any{"role": "operator"})
	if l := other.must(200, "GET", "/v1/me/table-views", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("another person sees them: %v", l)
	}
	owner.must(204, "DELETE", "/v1/me/table-views/list-people", nil)
	owner.must(204, "DELETE", "/v1/me/table-views/list-people", nil)
	if l := owner.must(200, "GET", "/v1/me/table-views", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("dropped: %v", l)
	}
}
