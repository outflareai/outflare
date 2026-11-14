package outflare

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/db"
	"github.com/outflareai/outflare/engine/internal/secrets"
)

type testWorkspaceInspector struct{}

func (testWorkspaceInspector) Definition() connectors.Definition {
	return connectors.Definition{ID: "test_workspace", Name: "Test workspace", Auth: connectors.AuthAPIKey, Capabilities: []string{CapWorkspaceInspect, CapWorkspaceManageUsers}}
}
func (testWorkspaceInspector) UpdateWorkspaceUser(_ context.Context, _ *connectors.Client, cr connectors.Creds, u WorkspaceUserUpdate) (WorkspaceUserResult, error) {
	if cr["key"] != "test-only-not-a-key" {
		return WorkspaceUserResult{}, errors.New("wrong connection credential")
	}
	return WorkspaceUserResult{Name: "updated"}, nil
}
func (testWorkspaceInspector) Test(context.Context, *connectors.Client, connectors.Creds) (connectors.Account, error) {
	return connectors.Account{}, nil
}
func (testWorkspaceInspector) InspectWorkspaceDomain(_ context.Context, _ *connectors.Client, cr connectors.Creds, customer, domain string) (WorkspaceDomain, error) {
	if cr["key"] != "test-only-not-a-key" {
		return WorkspaceDomain{}, errors.New("wrong connection credential")
	}
	return WorkspaceDomain{CustomerID: customer, Domain: domain, Verified: true}, nil
}
func TestExactWorkspaceConnectionIsolation(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("disposable database required")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err = db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	ws, otherWS, brand, otherBrand, id := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, w := range []string{ws, otherWS} {
		if _, err = pool.Exec(ctx, `INSERT INTO workspaces(id,name,slug) VALUES($1,'Northpeak',$2)`, w, w); err != nil {
			t.Fatal(err)
		}
	}
	for i, b := range []string{brand, otherBrand} {
		name := "one"
		if i == 1 {
			name = "two"
		}
		if _, err = pool.Exec(ctx, `INSERT INTO brands(id,workspace_id,name,handle) VALUES($1,$2,$3,$3)`, b, ws, name); err != nil {
			t.Fatal(err)
		}
	}
	box, err := secrets.NewBox(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	sealed := box.Seal([]byte(`{"key":"test-only-not-a-key"}`), []byte(id))
	if _, err = pool.Exec(ctx, `INSERT INTO integrations(id,workspace_id,brand_id,connector,name,credentials) VALUES($1,$2,$3,'test_workspace','Test', $4)`, id, ws, brand, sealed); err != nil {
		t.Fatal(err)
	}
	h := &Host{Pool: pool, store: &connectors.Store{Pool: pool, Box: box, Registry: connectors.NewRegistry([]connectors.Connector{testWorkspaceInspector{}}, nil), Client: &connectors.Client{}}}
	cs := h.Connections()
	c, err := cs.OpenID(ctx, ws, &brand, id, CapWorkspaceInspect)
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.InspectWorkspaceDomain(ctx, "Ctest", "northpeak.example")
	if err != nil || !result.Verified {
		t.Fatalf("inspection: %+v %v", result, err)
	}
	m, err := cs.OpenID(ctx, ws, &brand, id, CapWorkspaceManageUsers)
	if err != nil {
		t.Fatal(err)
	}
	given := "Ana"
	if res, err := m.UpdateWorkspaceUser(ctx, WorkspaceUserUpdate{Email: "ana@northpeak.example", GivenName: &given}); err != nil || res.Name != "updated" {
		t.Fatalf("profile change: %+v %v", res, err)
	}
	for _, tc := range []struct{ name, w, b, id, cap string }{
		{"workspace", otherWS, brand, id, CapWorkspaceInspect}, {"brand", ws, otherBrand, id, CapWorkspaceInspect}, {"missing", ws, brand, uuid.NewString(), CapWorkspaceInspect}, {"capability", ws, brand, id, "test.unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := cs.OpenID(ctx, tc.w, &tc.b, tc.id, tc.cap); !errors.Is(err, ErrNotConnected) {
				t.Fatalf("foreign/missing connection opened: %v", err)
			}
		})
	}
	// Explicit sharing and a brand-owned override use the same serving
	// rules as the normal connection UI; an exact ID never bypasses them.
	if _, err = pool.Exec(ctx, `UPDATE integrations SET brand_id=NULL,brand_ids=ARRAY[$2::uuid] WHERE id=$1`, id, brand); err != nil {
		t.Fatal(err)
	}
	if _, err = cs.OpenID(ctx, ws, &otherBrand, id, CapWorkspaceInspect); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("unshared connection opened: %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE integrations SET brand_ids='{}' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = cs.OpenID(ctx, ws, &otherBrand, id, CapWorkspaceInspect); err != nil {
		t.Fatal("shared connection unavailable", err)
	}
	own := uuid.NewString()
	ownSealed := box.Seal([]byte(`{"key":"test-only-not-a-key"}`), []byte(own))
	if _, err = pool.Exec(ctx, `INSERT INTO integrations(id,workspace_id,brand_id,connector,name,credentials) VALUES($1,$2,$3,'test_workspace','Brand owned',$4)`, own, ws, otherBrand, ownSealed); err != nil {
		t.Fatal(err)
	}
	if _, err = cs.OpenID(ctx, ws, &otherBrand, id, CapWorkspaceInspect); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("brand override bypassed: %v", err)
	}
	if _, err = cs.OpenID(ctx, ws, &otherBrand, own, CapWorkspaceInspect); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE integrations SET status='error' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = cs.OpenID(ctx, ws, &brand, id, CapWorkspaceInspect); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("disconnected account opened: %v", err)
	}
}
