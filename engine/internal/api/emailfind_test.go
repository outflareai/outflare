package api_test

import "testing"

// Exercises the three /email-finder endpoints so the contract check validates
// their request and response shapes against api/openapi.yaml, and confirms the
// finder answers per-workspace without a connected verifier.
func TestEmailFinderEndpoints(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws + "/email-finder"

	// A domain this workspace has never seen: the pattern is built on first
	// use and comes back with a code saying how confident it is.
	pat := owner.must(200, "GET", base+"/pattern?domain=acme.example", nil)
	data, _ := pat["data"].(map[string]any)
	if data == nil || data["domain"] != "acme.example" {
		t.Fatalf("pattern data = %v", pat["data"])
	}
	if _, ok := pat["code"].(string); !ok {
		t.Fatalf("pattern code missing: %v", pat)
	}

	// Find proposes an address for a person and echoes the caller's ref.
	find := owner.must(200, "POST", base+"/find", map[string]any{
		"items": []map[string]any{
			{"ref": "a1", "first_name": "Alice", "last_name": "Nguyen", "domain": "acme.example"},
		},
	})
	items, _ := find["data"].(map[string]any)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("find items = %v", find["data"])
	}
	if it, _ := items[0].(map[string]any); it["ref"] != "a1" {
		t.Fatalf("find ref = %v", items[0])
	}

	// Verify runs the waterfall; with no verifier connected it still answers
	// from the free built-in checks.
	ver := owner.must(200, "POST", base+"/verify", map[string]any{
		"items": []map[string]any{{"ref": "v1", "email": "alice.nguyen@acme.example"}},
	})
	vitems, _ := ver["data"].(map[string]any)["items"].([]any)
	if len(vitems) != 1 {
		t.Fatalf("verify items = %v", ver["data"])
	}
	if it, _ := vitems[0].(map[string]any); it["ref"] != "v1" {
		t.Fatalf("verify ref = %v", vitems[0])
	}

	// The pattern lookup needs a domain.
	owner.must(400, "GET", base+"/pattern", nil)

	// Over the batch cap is refused.
	big := make([]map[string]any, 51)
	for i := range big {
		big[i] = map[string]any{"ref": "r", "first_name": "A", "last_name": "B", "domain": "acme.example"}
	}
	owner.must(400, "POST", base+"/find", map[string]any{"items": big})
}
