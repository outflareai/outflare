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
	reporters := got["reporting_devices"].([]any)
	if len(reporters) != 1 {
		t.Fatalf("reporting devices: %v", reporters)
	}
	reporter := reporters[0].(map[string]any)
	person := cfg["user"].(map[string]any)
	if reporter["id"] != cfg["device_id"] || reporter["name"] != "Ana's MacBook" ||
		reporter["user_name"] != person["name"] || reporter["user_email"] != person["email"] {
		t.Fatalf("reading must identify the device's linked person: %v", reporter)
	}
	chatgpt := owner.must(200, "GET", base+"/ai-accounts/"+accs[1].(map[string]any)["account_id"].(string), nil)
	if chatgpt["name"] != "ChatGPT Plus" || chatgpt["resets_available"] != 2.0 || chatgpt["members"].([]any)[0].(map[string]any)["role"] != "owner" {
		t.Fatalf("added by the report: %v", chatgpt)
	}
	pc.must(400, "POST", "/v1/desktop/reports", map[string]any{"accounts": []any{map[string]any{"provider": "gemini", "source": "official"}}})
	d := owner.must(200, "GET", base+"/ai-devices", nil)["data"].([]any)[0].(map[string]any)
	if d["user_email"] != person["email"] || d["user_name"] != person["name"] {
		t.Fatalf("device owner: %v", d)
	}
	if d["status"] != "attention" || d["last_error_code"] != "not_signed_in" {
		t.Fatalf("device after a report with an error: %v", d)
	}

	// Switching Claude logins adds a second account, and repeated reports
	// reuse it without changing the first account's usage.
	second := map[string]any{"accounts": []any{map[string]any{
		"provider": "claude", "account_email": "other@acme.example", "plan": "Pro", "source": "extended",
		"windows": []any{map[string]any{"kind": "weekly", "used_percent": 15}},
	}}}
	switched := pc.must(200, "POST", "/v1/desktop/reports", second)["accounts"].([]any)[0].(map[string]any)
	if switched["created"] != true || switched["account_id"] == claude["id"] {
		t.Fatalf("switched account: %v", switched)
	}
	repeated := pc.must(200, "POST", "/v1/desktop/reports", second)["accounts"].([]any)[0].(map[string]any)
	if repeated["created"] == true || repeated["account_id"] != switched["account_id"] {
		t.Fatalf("repeated account: %v", repeated)
	}
	original := owner.must(200, "GET", base+"/ai-accounts/"+claude["id"].(string), nil)
	if original["account_email"] != "ana@acme.example" || original["usage"].([]any)[0].(map[string]any)["used_percent"] != 42.5 {
		t.Fatalf("original account after switching: %v", original)
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

// Identical provider logins in two workspaces must remain independent. Exercise
// the real HTTP authorization and report paths, including their owner joins.
func TestAIDesktopWorkspaceIsolation(t *testing.T) {
	e := freshEnv(t)
	owner, first := setupOwner(t, e.h)
	second := owner.must(201, "POST", "/v1/workspaces", map[string]string{"name": "Northpeak"})["id"].(string)
	alice, aliceID := joinAs(t, owner, first, "alice@acme.example", map[string]any{"role": "operator"})
	bob, bobID := joinAs(t, owner, second, "bob@northpeak.example", map[string]any{"role": "operator"})
	for _, ws := range []string{first, second} {
		if err := pgx.BeginFunc(t.Context(), e.pool, func(tx pgx.Tx) error {
			_, err := features.Set(t.Context(), tx, ws, features.AIAccounts, true, "")
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	type linked struct {
		pc              *client
		device, account string
	}
	link := func(person *client, ws, name string, usage float64) linked {
		t.Helper()
		app := &client{t: t, h: e.h}
		l := app.must(201, "POST", "/v1/desktop/links", map[string]any{"name": name, "platform": "macos", "app_version": "0.1.0"})
		person.must(201, "POST", "/v1/workspaces/"+ws+"/ai-devices", map[string]any{"user_code": l["user_code"]})
		c := app.must(200, "POST", "/v1/desktop/token", map[string]any{"device_code": l["device_code"]})
		if c["workspace"].(map[string]any)["id"] != ws {
			t.Fatal("wrong link workspace")
		}
		pc := &client{t: t, h: e.h, bearer: c["token"].(string)}
		r := pc.must(200, "POST", "/v1/desktop/reports", map[string]any{"accounts": []any{map[string]any{
			"provider": "claude", "account_email": "shared@acme.example", "source": "extended", "plan": "Pro",
			"windows": []any{map[string]any{"kind": "weekly", "used_percent": usage}},
		}}})
		return linked{pc, c["device_id"].(string), r["accounts"].([]any)[0].(map[string]any)["account_id"].(string)}
	}
	a := link(alice, first, "Acme computer", 12)
	b := link(bob, second, "Northpeak computer", 87)
	if a.account == b.account || a.device == b.device {
		t.Fatal("workspaces shared identifiers")
	}
	owner.must(400, "POST", "/v1/workspaces/"+first+"/ai-accounts", map[string]any{
		"provider": "chatgpt", "name": "Account", "members": []map[string]any{{"user_id": bobID}},
	})
	owner.must(400, "PATCH", "/v1/workspaces/"+first+"/ai-accounts/"+a.account, map[string]any{
		"members": []map[string]any{{"user_id": bobID}},
	})
	for _, tc := range []struct {
		person                *client
		ws, user, email, name string
		own, foreign          linked
		used                  float64
	}{
		{alice, first, aliceID, "alice@acme.example", "Acme computer", a, b, 12},
		{bob, second, bobID, "bob@northpeak.example", "Northpeak computer", b, a, 87},
	} {
		base := "/v1/workspaces/" + tc.ws
		cfg := tc.own.pc.must(200, "GET", "/v1/desktop/config", nil)
		if cfg["workspace"].(map[string]any)["id"] != tc.ws || cfg["user"].(map[string]any)["email"] != tc.email {
			t.Fatal("wrong desktop configuration identity")
		}
		for _, reader := range []*client{tc.person, owner} {
			accounts := reader.must(200, "GET", base+"/ai-accounts", nil)["data"].([]any)
			if len(accounts) != 1 || accounts[0].(map[string]any)["id"] != tc.own.account {
				t.Fatal("account list crossed workspaces")
			}
			account := reader.must(200, "GET", base+"/ai-accounts/"+tc.own.account, nil)
			usage := account["usage"].([]any)
			if len(usage) != 1 || usage[0].(map[string]any)["used_percent"] != tc.used {
				t.Fatal("usage crossed workspaces")
			}
			reporters := account["reporting_devices"].([]any)
			members := account["members"].([]any)
			if len(reporters) != 1 || len(members) != 1 {
				t.Fatal("foreign owner or reporter appeared")
			}
			reporter := reporters[0].(map[string]any)
			if reporter["id"] != tc.own.device || reporter["user_id"] != tc.user || reporter["user_email"] != tc.email || reporter["name"] != tc.name || members[0].(map[string]any)["user_id"] != tc.user {
				t.Fatal("reporting identity crossed workspaces")
			}
			devices := reader.must(200, "GET", base+"/ai-devices?everyone=true", nil)["data"].([]any)
			if len(devices) != 1 || devices[0].(map[string]any)["id"] != tc.own.device || devices[0].(map[string]any)["user_email"] != tc.email {
				t.Fatal("device list crossed workspaces")
			}
			reader.must(404, "GET", base+"/ai-accounts/"+tc.foreign.account, nil)
			reader.must(404, "PATCH", base+"/ai-accounts/"+tc.foreign.account, map[string]any{"plan": "Max"})
			reader.must(404, "DELETE", base+"/ai-accounts/"+tc.foreign.account, nil)
			reader.must(404, "DELETE", base+"/ai-devices/"+tc.foreign.device, nil)
		}
		// Device credentials authorize reports, never dashboard reads or selecting
		// a different workspace in a report payload.
		tc.own.pc.must(401, "GET", base+"/ai-accounts", nil)
		tc.own.pc.must(400, "POST", "/v1/desktop/reports", map[string]any{"workspace_id": second, "accounts": []any{}})
	}
	alice.must(404, "GET", "/v1/workspaces/"+second+"/ai-accounts", nil)
	alice.must(404, "GET", "/v1/workspaces/"+second+"/ai-devices?everyone=true", nil)
	bob.must(404, "GET", "/v1/workspaces/"+first+"/ai-accounts", nil)
	// A token remains scoped even when its creator can access both workspaces.
	tok := owner.must(201, "POST", "/v1/workspaces/"+first+"/api-tokens", map[string]any{"name": "Workspace reader", "scopes": []string{"ai_accounts.read"}})
	scoped := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	scoped.must(404, "GET", "/v1/workspaces/"+second+"/ai-accounts", nil)
	owner.must(204, "DELETE", "/v1/workspaces/"+second+"/members/"+bobID, nil)
	b.pc.must(401, "GET", "/v1/desktop/config", nil)
	b.pc.must(401, "POST", "/v1/desktop/reports", map[string]any{})
	// Revoking another organization's membership does not affect this link.
	a.pc.must(200, "GET", "/v1/desktop/config", nil)
}
