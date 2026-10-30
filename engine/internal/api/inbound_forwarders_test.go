package api_test

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/features"
	"github.com/outflareai/outflare/engine/internal/inboundemail"
	"github.com/outflareai/outflare/engine/internal/track"
)

// A member links a personal address to forward AI receipts from, sees it,
// and unlinks it; admins see everyone's.
func TestInboundForwarders(t *testing.T) {
	got := track.Capture(t)
	inbound := &inboundemail.Service{}
	inbound.Enable("in.example.test", inboundSecret)
	e := freshEnvWith(t, func(o *api.Options) { o.InboundEmail = inbound })
	inbound.Pool = e.pool
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	if err := pgx.BeginFunc(t.Context(), e.pool, func(tx pgx.Tx) error {
		_, err := features.Set(t.Context(), tx, ws, features.AIAccounts, true, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	owner.must(400, "POST", base+"/inbound-forwarders", map[string]any{"email": "not an email"})
	if list := owner.must(201, "POST", base+"/inbound-forwarders", map[string]any{"email": "ana.personal@gmail.example"})["data"].([]any); len(list) != 1 {
		t.Fatalf("linked: %v", list)
	}
	if list := owner.must(200, "GET", base+"/inbound-forwarders", nil)["data"].([]any); len(list) != 1 || list[0].(map[string]any)["confirmation_url"] != nil {
		t.Fatalf("forwarders: %v", list)
	}
	if list := owner.must(200, "GET", base+"/inbound-forwarders?everyone=true", nil)["data"].([]any); len(list) != 1 {
		t.Fatalf("everyone's forwarders: %v", list)
	}
	owner.must(204, "DELETE", base+"/inbound-forwarders/ana.personal@gmail.example", nil)
	owner.must(404, "DELETE", base+"/inbound-forwarders/ana.personal@gmail.example", nil)

	seen := map[string]bool{}
	for _, ev := range got() {
		seen[ev.Name] = true
	}
	for _, want := range []string{"inbound_forwarder.linked", "inbound_forwarder.unlinked"} {
		if !seen[want] {
			t.Errorf("no %s event", want)
		}
	}
}
