package api_test

import (
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/features"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestAIAccounts(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws + "/ai-accounts"
	member, memberID := joinAs(t, owner, ws, "ana@acme.example", map[string]any{"role": "operator"})

	// Off until the workspace has the feature.
	owner.must(404, "GET", base, nil)
	if err := pgx.BeginFunc(t.Context(), e.pool, func(tx pgx.Tx) error {
		_, err := features.Set(t.Context(), tx, ws, features.AIAccounts, true, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// Bad input is refused with a reason.
	for _, body := range []map[string]any{
		{"name": "No provider"},
		{"provider": "gemini", "name": "Unknown tool"},
		{"provider": "claude", "name": ""},
		{"provider": "claude", "name": "x", "account_email": "not an email"},
		{"provider": "claude", "name": "x", "renews_on": "14/11/2026"},
		{"provider": "claude", "name": "x", "paid_by": "nobody"},
		{"provider": "claude", "name": "x", "price_cents": -1},
	} {
		owner.must(400, "POST", base, body)
	}

	// The owner adds their own Claude Max; they are its owner.
	mine := owner.must(201, "POST", base, map[string]any{"provider": "claude", "name": "Claude Max — Design",
		"account_email": " Owner@Acme.Example ", "plan": "Max 20x", "billing_cycle": "monthly", "renews_on": "2026-11-14", "price_cents": 20000})
	mineID := mine["id"].(string)
	if mine["account_email"] != "owner@acme.example" || mine["paid_by"] != "company" || mine["renews_on"] != "2026-11-14" ||
		mine["price_cents"] != 20000.0 || len(mine["members"].([]any)) != 1 || mine["owner_left"] != false {
		t.Fatalf("created: %v", mine)
	}

	// A member adds their own personal ChatGPT Plus, but not one only for
	// someone else.
	theirs := member.must(201, "POST", base, map[string]any{"provider": "chatgpt", "name": "ChatGPT Plus", "paid_by": "person", "price_cents": 2000, "resets_available": 2})
	theirsID := theirs["id"].(string)
	member.must(403, "POST", base, map[string]any{"provider": "cursor", "name": "Cursor", "members": []map[string]any{{"user_id": mine["members"].([]any)[0].(map[string]any)["user_id"]}}})

	// Members see only their own accounts; the owner sees everyone's.
	if list := member.must(200, "GET", base, nil)["data"].([]any); len(list) != 1 || list[0].(map[string]any)["id"] != theirsID {
		t.Fatalf("member's list: %v", list)
	}
	member.must(404, "GET", base+"/"+mineID, nil)
	member.must(404, "PATCH", base+"/"+mineID, map[string]any{"plan": "Pro"})
	member.must(404, "DELETE", base+"/"+mineID, nil)
	all := owner.must(200, "GET", base, nil)["data"].([]any)
	if len(all) != 2 || all[0].(map[string]any)["provider"] != "claude" || all[1].(map[string]any)["provider"] != "chatgpt" {
		t.Fatalf("owner's list (claude before chatgpt): %v", all)
	}
	if got := owner.must(200, "GET", base+"?mine=true", nil)["data"].([]any); len(got) != 1 {
		t.Fatalf("mine=true: %v", got)
	}
	if got := owner.must(200, "GET", base+"?provider=chatgpt", nil)["data"].([]any); len(got) != 1 {
		t.Fatalf("provider filter: %v", got)
	}

	// A member changes their own account, and can't take themselves off it.
	if got := member.must(200, "PATCH", base+"/"+theirsID, map[string]any{"plan": "Plus", "resets_available": 1}); got["plan"] != "Plus" || got["resets_available"] != 1.0 {
		t.Fatalf("member update: %v", got)
	}
	member.must(403, "PATCH", base+"/"+theirsID, map[string]any{"members": []map[string]any{}})
	// Clearing optional fields.
	if got := member.must(200, "PATCH", base+"/"+theirsID, map[string]any{"plan": ""}); got["plan"] != nil {
		t.Fatalf("plan should clear: %v", got)
	}

	// An API token with only my_ai_accounts.read sees its person's
	// accounts; one with ai_accounts.read sees everyone's but not the price
	// of an account its person isn't on.
	tok := owner.must(201, "POST", "/v1/workspaces/"+ws+"/api-tokens", map[string]any{"name": "Mine", "scopes": []string{"my_ai_accounts.read"}})
	bot := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	if got := bot.must(200, "GET", base, nil)["data"].([]any); len(got) != 1 {
		t.Fatalf("own-read token: %v", got)
	}
	bot.must(403, "POST", base, map[string]any{"provider": "claude", "name": "x"})
	tok = owner.must(201, "POST", "/v1/workspaces/"+ws+"/api-tokens", map[string]any{"name": "All", "scopes": []string{"ai_accounts.read"}})
	reader := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	for _, a := range reader.must(200, "GET", base, nil)["data"].([]any) {
		a := a.(map[string]any)
		if a["id"] == theirsID && (a["price_cents"] != nil || a["price_hidden"] != true) {
			t.Fatalf("someone else's price should be hidden: %v", a)
		}
		if a["id"] == mineID && a["price_cents"] != 20000.0 {
			t.Fatalf("own price should show: %v", a)
		}
	}

	// The owner hands their account to the member, who then leaves: it
	// shows owner left, and stays.
	owner.must(400, "PATCH", base+"/"+mineID, map[string]any{"members": []map[string]any{{"user_id": "00000000-0000-0000-0000-000000000000"}}})
	got := owner.must(200, "PATCH", base+"/"+mineID, map[string]any{"members": []map[string]any{{"user_id": memberID}}})
	if m := got["members"].([]any); len(m) != 1 || m[0].(map[string]any)["user_id"] != memberID {
		t.Fatalf("handed over: %v", got)
	}
	owner.must(204, "DELETE", "/v1/workspaces/"+ws+"/members/"+memberID, nil)
	got = owner.must(200, "GET", base+"/"+mineID, nil)
	if got["owner_left"] != true || got["members"].([]any)[0].(map[string]any)["left"] != true {
		t.Fatalf("owner left: %v", got)
	}
	// Someone who left can stay on it while others are changed.
	owner.must(200, "PATCH", base+"/"+mineID, map[string]any{"members": []map[string]any{{"user_id": memberID, "role": "user"}}})

	// What Outflare's own AI spent this month, for people who see spend.
	if got := owner.must(200, "GET", base+"/outflare", nil); got["calls"] != 0.0 || got["cost_cents"] != 0.0 || len(got["providers"].([]any)) != 0 {
		t.Fatalf("outflare: %v", got)
	}
	reader.must(403, "GET", base+"/outflare", nil)

	owner.must(204, "DELETE", base+"/"+theirsID, nil)
	owner.must(404, "GET", base+"/"+theirsID, nil)

	var names []string
	for _, ev := range events() {
		names = append(names, ev.Name)
	}
	for _, want := range []string{"ai_account.created", "ai_account.updated", "ai_account.deleted"} {
		if !slices.Contains(names, want) {
			t.Errorf("no %s event in %v", want, names)
		}
	}
}
