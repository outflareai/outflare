package api_test

import (
	"context"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/datanetwork"
	"github.com/outflareai/outflare/engine/internal/track"
)

// testNetwork is turned on for every workspace except off.
type testNetwork struct{ off string }

func (n testNetwork) Enabled(_ context.Context, ws string) bool { return ws != n.off }

func (testNetwork) Lookup(context.Context, string, datanetwork.Query) (datanetwork.Answer, error) {
	return datanetwork.Answer{}, nil
}
func (testNetwork) Contribute(context.Context, string, []datanetwork.Contribution) error { return nil }
func (testNetwork) Withdraw(context.Context, string) error                               { return nil }
func (testNetwork) Allowance(_ context.Context, _ string, member bool) (datanetwork.Allowance, error) {
	if member {
		return datanetwork.Allowance{Unlimited: true}, nil
	}
	return datanetwork.Allowance{Limit: 25, Used: 3}, nil
}

func TestDataNetworkWithoutANetwork(t *testing.T) {
	e := freshEnvWith(t, func(o *api.Options) { o.DataNetwork = &datanetwork.Service{} })
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws + "/data-network"
	st := owner.must(200, "GET", base, nil)
	if st["available"] != false || st["status"] != "not_joined" || st["allowance"] != nil {
		t.Fatalf("status = %v", st)
	}
	owner.must(409, "GET", base+"/preview", nil)
	if out := owner.must(409, "POST", base+"/join", map[string]any{"confirm": true}); errCode(out) != "data_network_unavailable" {
		t.Fatalf("join = %v", out)
	}
}

func TestDataNetworkOnlyWhereTurnedOn(t *testing.T) {
	dn := &datanetwork.Service{}
	e := freshEnvWith(t, func(o *api.Options) { o.DataNetwork = dn })
	dn.Pool = e.pool
	owner, ws := setupOwner(t, e.h)
	dn.Set(testNetwork{off: ws})
	base := "/v1/workspaces/" + ws + "/data-network"
	if st := owner.must(200, "GET", base, nil); st["available"] != false || st["allowance"] != nil {
		t.Fatalf("status = %v", st)
	}
	owner.must(409, "GET", base+"/preview", nil)
	owner.must(409, "POST", base+"/join", map[string]any{"confirm": true})
}

func TestDataNetworkJoinIsTheOwnersAndLeaveWithdraws(t *testing.T) {
	got := track.Capture(t)
	dn := &datanetwork.Service{}
	dn.Set(testNetwork{})
	e := freshEnvWith(t, func(o *api.Options) { o.DataNetwork = dn })
	dn.Pool = e.pool
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws + "/data-network"

	st := owner.must(200, "GET", base, nil)
	al, _ := st["allowance"].(map[string]any)
	if st["available"] != true || st["status"] != "not_joined" || al["limit"] != float64(25) || al["used"] != float64(3) {
		t.Fatalf("status = %v", st)
	}
	p := owner.must(200, "GET", base+"/preview", nil)
	if p["addresses"] != float64(0) || p["settle_days"] != float64(7) || p["withdraw_days"] != float64(30) {
		t.Fatalf("preview = %v", p)
	}

	// Only the owner, signed in, and only after confirming.
	admin, _ := joinAs(t, owner, ws, "admin@example.com", map[string]any{"role": "admin"})
	admin.must(403, "POST", base+"/join", map[string]any{"confirm": true})
	tok := owner.must(201, "POST", "/v1/workspaces/"+ws+"/api-tokens", map[string]any{"name": "agent", "scopes": []string{"integrations.manage", "leads.read"}})
	bot := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	bot.must(403, "POST", base+"/join", map[string]any{"confirm": true})
	owner.must(400, "POST", base+"/join", map[string]any{"confirm": false})

	st = owner.must(200, "POST", base+"/join", map[string]any{"confirm": true})
	al, _ = st["allowance"].(map[string]any)
	if st["status"] != "joined" || st["joined_at"] == nil || al["unlimited"] != true || al["limit"] != nil {
		t.Fatalf("joined = %v", st)
	}
	owner.must(200, "POST", base+"/join", map[string]any{"confirm": true}) // again: nothing changes

	// An admin can leave; what was shared is withdrawn within 30 days.
	st = admin.must(200, "POST", base+"/leave", nil)
	if st["status"] != "left" || st["withdraw_by"] == nil {
		t.Fatalf("left = %v", st)
	}
	admin.must(200, "POST", base+"/leave", nil)

	ev := got()
	if j := named(ev, "data_network.joined"); len(j) != 1 || j[0].WorkspaceID != ws {
		t.Fatalf("joined events = %+v", j)
	}
	if l := named(ev, "data_network.left"); len(l) != 1 {
		t.Fatalf("left events = %+v", l)
	}
	var emails int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE kind = 'data_network.email' AND workspace_id = $1`, ws).Scan(&emails); err != nil {
		t.Fatal(err)
	}
	var admins int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE m.workspace_id = $1 AND m.role IN ('owner', 'admin') AND u.email_verified_at IS NOT NULL`, ws).Scan(&admins); err != nil {
		t.Fatal(err)
	}
	if emails != 2*admins {
		t.Fatalf("emails queued = %d, want one per owner or admin for each change (%d)", emails, 2*admins)
	}
}
