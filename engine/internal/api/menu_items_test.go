package api_test

import "testing"

// A person's pins and recents follow them: pins are replaced as a list and
// keep their order, recents list newest first and can be cleared, each
// person has their own, and nothing odd gets stored.
func TestMenuItems(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	menu := func(c *client) (pins, recents []any) {
		t.Helper()
		m := c.must(200, "GET", "/v1/me/menu", nil)
		return m["pins"].([]any), m["recents"].([]any)
	}
	if pins, recents := menu(owner); len(pins) != 0 || len(recents) != 0 {
		t.Fatalf("new person: %v %v", pins, recents)
	}

	item := "/" + ws + "/-/funnels/checkout"
	got := owner.must(200, "PUT", "/v1/me/menu/pins", map[string]any{"pins": []string{"sequences", item, "sequences"}})["pins"].([]any)
	if len(got) != 2 || got[0] != "sequences" || got[1] != item {
		t.Fatalf("saved pins: %v", got)
	}
	owner.must(200, "PUT", "/v1/me/menu/pins", map[string]any{"pins": []string{item, "people"}})
	if pins, _ := menu(owner); len(pins) != 2 || pins[0] != item || pins[1] != "people" {
		t.Fatalf("replaced pins: %v", pins)
	}
	owner.must(400, "PUT", "/v1/me/menu/pins", map[string]any{"pins": []string{"not a ref"}})
	many := make([]string, 101)
	for i := range many {
		many[i] = "p" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	owner.must(400, "PUT", "/v1/me/menu/pins", map[string]any{"pins": many})

	owner.must(204, "POST", "/v1/me/menu/recents", map[string]any{"ref": "/a/-/funnels/one"})
	owner.must(204, "POST", "/v1/me/menu/recents", map[string]any{"ref": "/a/-/funnels/two"})
	owner.must(204, "POST", "/v1/me/menu/recents", map[string]any{"ref": "/a/-/funnels/one"})
	owner.must(400, "POST", "/v1/me/menu/recents", map[string]any{"ref": "has a space"})
	_, recents := menu(owner)
	if len(recents) != 2 || recents[0].(map[string]any)["ref"] != "/a/-/funnels/one" {
		t.Fatalf("recents, newest first: %v", recents)
	}
	// Only the newest are kept.
	for i := range 60 {
		owner.must(204, "POST", "/v1/me/menu/recents", map[string]any{"ref": "/a/-/trends/" + string(rune('a'+i%26)) + string(rune('a'+i/26))})
	}
	if _, recents := menu(owner); len(recents) != 50 {
		t.Fatalf("kept %d recents, want 50", len(recents))
	}

	other, _ := joinAs(t, owner, ws, "ana@acme.example", map[string]any{"role": "operator"})
	if pins, recents := menu(other); len(pins) != 0 || len(recents) != 0 {
		t.Fatalf("someone else's menu: %v %v", pins, recents)
	}
	owner.must(204, "DELETE", "/v1/me/menu/recents", nil)
	owner.must(204, "DELETE", "/v1/me/menu/recents", nil)
	if pins, recents := menu(owner); len(pins) != 2 || len(recents) != 0 {
		t.Fatalf("after clearing recents: %v %v", pins, recents)
	}
}
