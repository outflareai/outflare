package api_test

import (
	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/features"
	"slices"
	"testing"
)

func TestAIAccountExplicitBrands(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	if err := pgx.BeginFunc(t.Context(), e.pool, func(tx pgx.Tx) error {
		_, err := features.Set(t.Context(), tx, ws, features.AIAccounts, true, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	a := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme"})["id"].(string)
	b := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak"})["id"].(string)
	path := base + "/ai-accounts"
	// A selected browser brand is read context, never assignment consent.
	account := owner.must(201, "POST", path+"?brand="+a, map[string]any{"provider": "claude", "name": "Claude", "account_email": "shared@acme.example", "price_cents": 2000, "billing_cycle": "monthly"})
	id := account["id"].(string)
	if len(account["brand_ids"].([]any)) != 0 {
		t.Fatal("browser context assigned a brand")
	}
	account = owner.must(200, "PATCH", path+"/"+id+"?brand="+a, map[string]any{"brand_ids": []string{b}})
	if ids := account["brand_ids"].([]any); len(ids) != 1 || ids[0] != b {
		t.Fatal("explicit choice lost to browser context")
	}
	account = owner.must(200, "PATCH", path+"/"+id, map[string]any{"brand_ids": []string{a, b}})
	if len(account["brand_ids"].([]any)) != 2 {
		t.Fatal("shared assignment")
	}
	owner.must(201, "POST", path, map[string]any{"provider": "chatgpt", "name": "ChatGPT", "brand_ids": []string{b}, "price_cents": 1000, "billing_cycle": "monthly"})
	unassigned := owner.must(201, "POST", path, map[string]any{"provider": "cursor", "name": "Cursor"})["id"]
	for _, tc := range []struct {
		brand string
		count int
	}{{"all", 3}, {a, 1}, {b, 2}, {"none", 1}} {
		rows := owner.must(200, "GET", path+"?brand="+tc.brand, nil)["data"].([]any)
		if len(rows) != tc.count {
			t.Fatalf("brand %s count %d, want %d", tc.brand, len(rows), tc.count)
		}
		if tc.brand == "none" && rows[0].(map[string]any)["id"] != unassigned {
			t.Fatal("unassigned filter")
		}
	}
	rows := owner.must(200, "GET", path+"?brand=all", nil)["data"].([]any)
	var total float64
	for _, r := range rows {
		if price, ok := r.(map[string]any)["price_cents"].(float64); ok {
			total += price
		}
	}
	if total != 3000 {
		t.Fatalf("workspace double counted shared subscription: %v", total)
	}
	// Cross-workspace and duplicate assignments never alter the saved choice.
	other := owner.must(201, "POST", "/v1/workspaces", map[string]string{"name": "Other workspace"})["id"].(string)
	foreign := owner.must(200, "GET", "/v1/workspaces/"+other+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	owner.must(400, "PATCH", path+"/"+id, map[string]any{"brand_ids": []string{foreign}})
	owner.must(400, "PATCH", path+"/"+id, map[string]any{"brand_ids": []string{a, a}})
	owner.must(400, "PATCH", path+"/"+id, map[string]any{"brand_ids": []string{"not-a-uuid"}})
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO ai_account_brands(workspace_id,account_id,brand_id) VALUES($1,$2,$3)`, ws, id, foreign); err == nil {
		t.Fatal("database allowed cross-workspace allocation")
	}
	// A later desktop report updates usage without resetting brand assignments.
	app := &client{t: t, h: e.h}
	l := app.must(201, "POST", "/v1/desktop/links", map[string]any{"name": "Test computer", "platform": "macos", "app_version": "0.1.0"})
	owner.must(201, "POST", base+"/ai-devices?brand="+a, map[string]any{"user_code": l["user_code"]})
	token := app.must(200, "POST", "/v1/desktop/token", map[string]any{"device_code": l["device_code"]})["token"].(string)
	pc := &client{t: t, h: e.h, bearer: token}
	pc.must(400, "POST", "/v1/desktop/reports", map[string]any{"accounts": []any{map[string]any{
		"provider": "claude", "account_email": "shared@acme.example", "source": "official", "brand_ids": []string{a},
	}}})
	for _, email := range []string{"shared@acme.example", "discovered@acme.example"} {
		report := pc.must(200, "POST", "/v1/desktop/reports", map[string]any{"accounts": []any{map[string]any{"provider": "claude", "account_email": email, "source": "official", "windows": []any{map[string]any{"kind": "weekly", "used_percent": 42}}}}})
		got := owner.must(200, "GET", path+"/"+report["accounts"].([]any)[0].(map[string]any)["account_id"].(string), nil)
		ids := got["brand_ids"].([]any)
		if email == "shared@acme.example" {
			if len(ids) != 2 || !slices.Contains(ids, any(a)) || !slices.Contains(ids, any(b)) {
				t.Fatal("report overwrote explicit assignments")
			}
		} else if len(ids) != 0 {
			t.Fatal("discovered account inherited browser brand")
		}
	}
	owner.must(200, "PATCH", path+"/"+id, map[string]any{"brand_ids": []string{}})
	if ids := owner.must(200, "GET", path+"/"+id, nil)["brand_ids"].([]any); len(ids) != 0 {
		t.Fatal("explicit clear did not await assignment")
	}
}

func TestAIAccountBrandPermissions(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	if err := pgx.BeginFunc(t.Context(), e.pool, func(tx pgx.Tx) error {
		_, err := features.Set(t.Context(), tx, ws, features.AIAccounts, true, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	a := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme"})["id"].(string)
	b := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak"})["id"].(string)
	member, _ := joinAs(t, owner, ws, "operator@acme.example", map[string]any{"role": "operator", "brand_ids": []string{a}})
	path := base + "/ai-accounts"
	mine := member.must(201, "POST", path+"?brand="+a, map[string]any{"provider": "claude", "name": "Mine"})
	id := mine["id"].(string)
	if len(mine["brand_ids"].([]any)) != 0 {
		t.Fatal("limited caller's single brand was assigned implicitly")
	}
	member.must(403, "PATCH", path+"/"+id, map[string]any{"brand_ids": []string{b}})
	member.must(200, "PATCH", path+"/"+id, map[string]any{"brand_ids": []string{a}})
	owner.must(200, "PATCH", path+"/"+id, map[string]any{"brand_ids": []string{a, b}})
	member.must(200, "PATCH", path+"/"+id, map[string]any{"brand_ids": []string{b, a}, "plan": "Pro"})
	member.must(403, "PATCH", path+"/"+id, map[string]any{"brand_ids": []string{a}})
	member.must(403, "PATCH", path+"/"+id, map[string]any{"brand_ids": []string{}})
	member.must(404, "GET", path+"?brand="+b, nil)
	// Even a workspace-wide read token remains within its allowed brands.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Brand reader", "scopes": []string{"ai_accounts.read"}, "brand": a})
	reader := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	hidden := owner.must(201, "POST", path, map[string]any{"provider": "chatgpt", "name": "Other brand", "brand_ids": []string{b}})
	rows := reader.must(200, "GET", path+"?brand=all", nil)["data"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["id"] != id {
		t.Fatal("workspace report crossed allowed brands")
	}
	reader.must(404, "GET", path+"/"+hidden["id"].(string), nil)
}
