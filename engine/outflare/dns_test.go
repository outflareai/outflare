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

type testDNSHost struct{ got *[]DNSRecord }

func (testDNSHost) Definition() connectors.Definition {
	return connectors.Definition{ID: "test_dns", Name: "Test DNS", Auth: connectors.AuthAPIKey, Capabilities: []string{CapDNSWrite}}
}
func (testDNSHost) Test(context.Context, *connectors.Client, connectors.Creds) (connectors.Account, error) {
	return connectors.Account{}, nil
}
func (h testDNSHost) EnsureRecords(_ context.Context, _ *connectors.Client, cr connectors.Creds, records []DNSRecord) (DNSResult, error) {
	if cr["api_token"] != "test-only-not-a-token" {
		return DNSResult{}, errors.New("wrong connection credential")
	}
	*h.got = append(*h.got, records...)
	return DNSResult{Zone: "northpeak.example", Added: len(records)}, nil
}

// An extension adds records through the workspace's own DNS host, by
// capability, without seeing the credentials.
func TestConnectionEnsuresDNSRecords(t *testing.T) {
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
	ws, brand, id := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err = pool.Exec(ctx, `INSERT INTO workspaces(id,name,slug) VALUES($1,'Northpeak',$2)`, ws, ws); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO brands(id,workspace_id,name,handle) VALUES($1,$2,'Northpeak','northpeak')`, brand, ws); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.NewBox(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO integrations(id,workspace_id,brand_id,connector,name,credentials) VALUES($1,$2,$3,'test_dns','Test',$4)`, id, ws, brand, box.Seal([]byte(`{"api_token":"test-only-not-a-token"}`), []byte(id))); err != nil {
		t.Fatal(err)
	}
	var got []DNSRecord
	h := &Host{Pool: pool, store: &connectors.Store{Pool: pool, Box: box, Registry: connectors.NewRegistry([]connectors.Connector{testDNSHost{got: &got}}, nil), Client: &connectors.Client{}}}
	c, err := h.Connections().Open(ctx, ws, &brand, CapDNSWrite)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.EnsureDNSRecords(ctx, []DNSRecord{{Type: "MX", Name: "northpeak.example", Content: "smtp.google.com", Priority: 1}})
	if err != nil || res.Added != 1 || len(got) != 1 || got[0].Content != "smtp.google.com" {
		t.Fatalf("records %+v %+v %v", res, got, err)
	}
	other := uuid.NewString()
	if _, err = h.Connections().Open(ctx, other, nil, CapDNSWrite); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("another workspace: %v", err)
	}
}
