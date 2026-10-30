package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/aidesktop"
	"github.com/outflareai/outflare/engine/internal/features"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Outflare for desktop end to end: a computer links through a code the
// person approves, reports usage with its token, and stops working once
// unlinked.
func TestAIDesktop(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	app := &client{t: t, h: e.h}
	// Sources switched off are server-wide; start with none.
	if _, err := e.pool.Exec(t.Context(), `DELETE FROM ai_desktop_sources_off`); err != nil {
		t.Fatal(err)
	}
	events := track.Capture(t)

	// Off: approving is a 404 like the rest of AI Accounts.
	link := app.must(201, "POST", "/v1/desktop/links", map[string]any{"name": "Ana's MacBook", "platform": "macos", "app_version": "0.1.0"})
	code, device := link["user_code"].(string), link["device_code"].(string)
	owner.must(404, "POST", base+"/ai-devices", map[string]any{"user_code": code})
	if err := pgx.BeginFunc(t.Context(), e.pool, func(tx pgx.Tx) error {
		_, err := features.Set(t.Context(), tx, ws, features.AIAccounts, true, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	app.must(400, "POST", "/v1/desktop/links", map[string]any{"name": "x", "platform": "beos", "app_version": "0.1.0"})
	if got := app.must(202, "POST", "/v1/desktop/token", map[string]any{"device_code": device}); got["status"] != "pending" {
		t.Fatalf("before approval: %v", got)
	}
	owner.must(404, "POST", base+"/ai-devices", map[string]any{"user_code": "ZZZZ-ZZZZ"})
	// Typed in lower case without the dash still matches.
	approved := owner.must(201, "POST", base+"/ai-devices", map[string]any{"user_code": " " + code[:4] + code[5:] + " "})
	if approved["name"] != "Ana's MacBook" || approved["platform"] != "macos" {
		t.Fatalf("approved: %v", approved)
	}
	owner.must(410, "POST", base+"/ai-devices", map[string]any{"user_code": code})
	got := app.must(200, "POST", "/v1/desktop/token", map[string]any{"device_code": device})
	token := got["token"].(string)
	if got["workspace"].(map[string]any)["id"] != ws || len(token) < 20 || token[:4] != "ofd_" {
		t.Fatalf("token: %v", got)
	}
	app.must(410, "POST", "/v1/desktop/token", map[string]any{"device_code": device})

	pc := &client{t: t, h: e.h, bearer: token}
	cfg := pc.must(200, "GET", "/v1/desktop/config", nil)
	if cfg["report_every_seconds"] != 600.0 || cfg["workspace"].(map[string]any)["id"] != ws {
		t.Fatalf("config: %v", cfg)
	}
	(&client{t: t, h: e.h, bearer: "ofd_not_a_real_token"}).must(401, "GET", "/v1/desktop/config", nil)

	// The person's Claude account, already added; the ChatGPT account isn't.
	claude := owner.must(201, "POST", base+"/ai-accounts", map[string]any{"provider": "claude", "name": "Claude", "account_email": "ana@acme.example"})
	resets := time.Now().Add(3 * time.Hour).UTC().Truncate(time.Second)
	rep := pc.must(200, "POST", "/v1/desktop/reports", map[string]any{
		"app_version": "0.1.1", "status": "attention",
		"errors": []any{map[string]any{"provider": "cursor", "code": "not_signed_in"}},
		"accounts": []any{
			map[string]any{"provider": "claude", "account_email": "Ana@Acme.example", "plan": "Max 20x", "source": "official",
				"windows": []any{
					map[string]any{"kind": "five_hour", "used_percent": 42.5, "resets_at": resets.Format(time.RFC3339)},
					map[string]any{"kind": "weekly", "used_percent": 74},
				}},
			map[string]any{"provider": "chatgpt", "account_email": "ana@acme.example", "plan": "Plus", "resets_available": 2, "source": "extended",
				"windows": []any{map[string]any{"kind": "weekly", "used_percent": 10}}},
		},
	})
	accs := rep["accounts"].([]any)
	if len(accs) != 2 || accs[0].(map[string]any)["account_id"] != claude["id"] || accs[1].(map[string]any)["created"] != true {
		t.Fatalf("report: %v", rep)
	}
	got = owner.must(200, "GET", base+"/ai-accounts/"+claude["id"].(string), nil)
	usage := got["usage"].([]any)
	if got["plan"] != "Max 20x" || len(usage) != 2 || usage[0].(map[string]any)["kind"] != "five_hour" ||
		usage[0].(map[string]any)["used_percent"] != 42.5 || usage[0].(map[string]any)["resets_at"] != resets.Format(time.RFC3339) {
		t.Fatalf("account with usage: %v", got)
	}
	chatgpt := owner.must(200, "GET", base+"/ai-accounts/"+accs[1].(map[string]any)["account_id"].(string), nil)
	if chatgpt["name"] != "ChatGPT Plus" || chatgpt["resets_available"] != 2.0 || chatgpt["members"].([]any)[0].(map[string]any)["role"] != "owner" {
		t.Fatalf("added by the report: %v", chatgpt)
	}
	pc.must(400, "POST", "/v1/desktop/reports", map[string]any{"accounts": []any{map[string]any{"provider": "gemini", "source": "official"}}})
	d := owner.must(200, "GET", base+"/ai-devices", nil)["data"].([]any)[0].(map[string]any)
	if d["status"] != "attention" || d["last_error_code"] != "not_signed_in" {
		t.Fatalf("device after a report with an error: %v", d)
	}

	// A source switched off for everyone is left out.
	if err := aidesktop.SetSource(t.Context(), e.pool, "claude", "official", false, "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = aidesktop.SetSource(context.Background(), e.pool, "claude", "official", true, "") })
	rep = pc.must(200, "POST", "/v1/desktop/reports", map[string]any{"accounts": []any{map[string]any{"provider": "claude", "source": "official",
		"account_email": "ana@acme.example", "windows": []any{map[string]any{"kind": "weekly", "used_percent": 99}}}}})
	if rep["accounts"].([]any)[0].(map[string]any)["skipped"] != "source_off" {
		t.Fatalf("source off: %v", rep)
	}
	if off := pc.must(200, "GET", "/v1/desktop/config", nil)["sources_off"].(map[string]any); len(off["claude"].([]any)) != 1 {
		t.Fatalf("config sources_off: %v", off)
	}

	if all := owner.must(200, "GET", base+"/ai-devices?everyone=true", nil)["data"].([]any); len(all) != 1 {
		t.Fatalf("everyone's computers: %v", all)
	}
	devices := owner.must(200, "GET", base+"/ai-devices", nil)["data"].([]any)
	d = devices[0].(map[string]any)
	if len(devices) != 1 || d["status"] != "ok" || d["last_error_code"] != nil || d["app_version"] != "0.1.1" || d["silent"] != false {
		t.Fatalf("devices after a clean report: %v", devices)
	}
	owner.must(204, "DELETE", base+"/ai-devices/"+d["id"].(string), nil)
	owner.must(404, "DELETE", base+"/ai-devices/"+d["id"].(string), nil)
	pc.must(401, "POST", "/v1/desktop/reports", map[string]any{})

	var names []string
	for _, ev := range events() {
		names = append(names, ev.Name)
	}
	for _, want := range []string{"ai_device.linked", "ai_device.unlinked", "ai_device.read_failed", "ai_account.created"} {
		found := false
		for _, n := range names {
			found = found || n == want
		}
		if !found {
			t.Errorf("no %s event in %v", want, names)
		}
	}
}
