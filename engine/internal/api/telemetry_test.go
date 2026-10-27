package api_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestTelemetrySetupChoiceOwnerOnlyAndPreview(t *testing.T) {
	e := freshEnv(t)
	if _, err := e.pool.Exec(context.Background(), `UPDATE telemetry_state SET enabled = true, changed_by = NULL, changed_at = NULL`); err != nil {
		t.Fatal(err)
	}
	anon := &client{t: t, h: e.h}
	st := anon.must(200, "GET", "/v1/setup", nil)
	// This test build has no endpoint, so it says so before setup.
	if tel := st["telemetry"].(map[string]any); tel["enabled"] != false || tel["disabled_by"] != "no_endpoint" {
		t.Fatalf("setup telemetry: %v", st)
	}

	// The owner turns it off on the first-run form.
	owner := &client{t: t, h: e.h}
	out := owner.must(201, "POST", "/v1/setup", map[string]any{
		"email": "jack@example.com", "name": "Jack", "password": "long enough password", "workspace_name": "Acme Outbound", "telemetry": false,
	})
	ws := out["workspace_id"].(string)
	got := owner.must(200, "GET", "/v1/instance/telemetry", nil)
	if got["disabled_by"] != "setting" || got["can_change"] != true || got["instance_id"] == "" {
		t.Fatalf("after setup: %v", got)
	}

	// Someone else on the server can see it but not change it.
	inv := owner.must(201, "POST", "/v1/workspaces/"+ws+"/invites", map[string]string{"email": "sam@example.com", "role": "admin"})
	sam := &client{t: t, h: e.h}
	sam.must(200, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "name": "Sam", "password": "sams long password"})
	if got := sam.must(200, "GET", "/v1/instance/telemetry", nil); got["can_change"] != false {
		t.Fatalf("sam: %v", got)
	}
	sam.must(403, "PATCH", "/v1/instance/telemetry", map[string]any{"enabled": true})
	owner.must(400, "PATCH", "/v1/instance/telemetry", map[string]any{})
	if got := owner.must(200, "PATCH", "/v1/instance/telemetry", map[string]any{"enabled": true}); got["disabled_by"] != "no_endpoint" {
		t.Fatalf("turned on: %v", got)
	}

	// API tokens are machines, not the server's people.
	tok := owner.must(201, "POST", "/v1/workspaces/"+ws+"/api-tokens", map[string]any{"name": "bot", "scopes": []string{"workspace.read"}})
	(&client{t: t, h: e.h, bearer: tok["token"].(string)}).must(403, "GET", "/v1/instance/telemetry", nil)

	// The preview is exactly what would be sent: ranges and ids, nothing personal.
	prev := owner.must(200, "GET", "/v1/instance/telemetry/preview", nil)
	counts := prev["counts"].(map[string]any)
	if counts["users"] != "2-5" || counts["workspaces"] != "1" || prev["schema"] != float64(1) {
		t.Fatalf("preview: %v", prev)
	}
	raw, _ := json.Marshal(prev)
	for _, secret := range []string{"jack@example.com", "sam@example.com", "Acme", "Jack", ws} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("preview leaks %q: %s", secret, raw)
		}
	}
}
