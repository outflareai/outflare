package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// What only the browser sees (an error shown, a crash, a page opened) is
// recorded by the server like its own events.
func TestTheWebAppsOwnEventsAreRecorded(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	h := e.h
	owner, ws := setupOwner(t, h)

	// The web app sends its events only to a server that keeps events.
	if m := owner.must(200, "GET", "/v1/meta", nil); m["ui_events"] != nil {
		t.Fatalf("meta: %v", m["ui_events"])
	}
	if _, err := e.srv.ProductEventSink(context.Background(), ws, ""); err != nil {
		t.Fatal(err)
	}
	if m := owner.must(200, "GET", "/v1/meta", nil); m["ui_events"] != true {
		t.Fatalf("meta: %v", m["ui_events"])
	}
	userID := named(got(), "user.signed_up")[0].UserID
	calls := len(named(got(), "api.called"))

	owner.must(204, "POST", "/v1/ui-events", map[string]any{
		"workspace_id": ws,
		"events": []map[string]any{
			{"name": "ui.error_shown", "properties": map[string]any{"error_code": "forbidden", "page": "people", "status": 403, "request_id": "req_0123456789abcdef"}},
			{"name": "ui.crashed", "properties": map[string]any{"outcome": "error", "error_code": "crashed", "where": "window", "message": strings.Repeat("x", 500), "surface": "api"}},
			{"name": "ui.page_viewed", "properties": map[string]any{"page": "people"}},
		},
	})
	ev := got()
	shown := named(ev, "ui.error_shown")
	if len(shown) != 1 || shown[0].UserID != userID || shown[0].WorkspaceID != ws || shown[0].Surface != "web" ||
		shown[0].Outcome != track.Failure || shown[0].ErrorCode != "forbidden" || shown[0].Props["page"] != "people" || shown[0].RequestID != "req_0123456789abcdef" {
		t.Fatalf("ui.error_shown: %+v", shown)
	}
	crashed := named(ev, "ui.crashed")
	if len(crashed) != 1 || crashed[0].Outcome != track.Error || crashed[0].Surface != "web" || len(crashed[0].Props["message"].(string)) != 200 {
		t.Fatalf("ui.crashed: %+v", crashed)
	}
	if _, ok := crashed[0].Props["surface"]; ok {
		t.Fatal("the browser set the surface")
	}
	if v := named(ev, "ui.page_viewed"); len(v) != 1 || v[0].Outcome != track.Success {
		t.Fatalf("ui.page_viewed: %+v", v)
	}
	// Posting events is not an event of its own.
	if n := len(named(ev, "api.called")); n != calls {
		t.Fatalf("api.called went from %d to %d", calls, n)
	}

	// A workspace the person isn't in is not written on their events.
	owner.must(204, "POST", "/v1/ui-events", map[string]any{
		"workspace_id": "00000000-0000-4000-8000-000000000000",
		"events":       []map[string]any{{"name": "ui.dialog_opened"}},
	})
	if d := named(got(), "ui.dialog_opened"); len(d) != 1 || d[0].WorkspaceID != "" {
		t.Fatalf("ui.dialog_opened: %+v", d)
	}

	// Only ui. events, only plain values.
	for _, bad := range []map[string]any{
		{"events": []map[string]any{{"name": "member.removed"}}},
		{"events": []map[string]any{{"name": "ui.x", "properties": map[string]any{"list": []string{"a"}}}}},
		{"events": []map[string]any{{"name": "ui.x", "properties": map[string]any{"Bad Name": 1}}}},
		{"events": []map[string]any{}},
	} {
		if status, body := owner.do("POST", "/v1/ui-events", bad); status != 400 {
			t.Fatalf("%v: %d %v", bad, status, body)
		}
	}
	if len(named(got(), "member.removed")) != 0 {
		t.Fatal("a browser recorded an event that isn't its own")
	}

	// Signed-in people only.
	if status, _ := (&client{t: t, h: h}).do("POST", "/v1/ui-events", map[string]any{"events": []map[string]any{{"name": "ui.x"}}}); status != 401 {
		t.Fatalf("signed out: %d", status)
	}
}
