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

type testProfiles struct{ got *[]string }

func (testProfiles) Definition() connectors.Definition {
	return connectors.Definition{ID: "test_profiles", Name: "Test profiles", Auth: connectors.AuthAPIKey, Capabilities: []string{CapReadProfile}}
}
func (testProfiles) Test(context.Context, *connectors.Client, connectors.Creds) (connectors.Account, error) {
	return connectors.Account{}, nil
}
func (r testProfiles) ReadProfile(_ context.Context, _ *connectors.Client, cr connectors.Creds, profileURL string) (PersonProfile, error) {
	if cr["api_key"] != "test-not-a-real-key" {
		return PersonProfile{}, errors.New("wrong connection credential")
	}
	*r.got = append(*r.got, profileURL)
	return PersonProfile{Found: true, Headline: "CTO at Northpeak", Current: []ProfileRole{{Company: "Northpeak", Title: "CTO"}}, Cost: Cost{USD: 0.02}}, nil
}

// An extension reads a person's profile through the workspace's own
// provider, by capability, without seeing the credentials, and the cost is
// booked against its reference. Another workspace has no such connection.
func TestConnectionReadsAProfile(t *testing.T) {
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
	ws, id := uuid.NewString(), uuid.NewString()
	if _, err = pool.Exec(ctx, `INSERT INTO workspaces(id,name,slug) VALUES($1,'Northpeak',$2)`, ws, ws); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.NewBox(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO integrations(id,workspace_id,connector,name,credentials) VALUES($1,$2,'test_profiles','Test',$3)`, id, ws, box.Seal([]byte(`{"api_key":"test-not-a-real-key"}`), []byte(id))); err != nil {
		t.Fatal(err)
	}
	var got []string
	h := &Host{Pool: pool, store: &connectors.Store{Pool: pool, Box: box, Registry: connectors.NewRegistry([]connectors.Connector{testProfiles{got: &got}}, nil), Client: &connectors.Client{}}}
	c, err := h.Connections().Open(ctx, ws, nil, CapReadProfile)
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.ReadProfile(ctx, "https://www.linkedin.com/in/ana-diaz-example", "check-ref")
	if err != nil || !p.Found || len(p.Current) != 1 || p.Current[0].Company != "Northpeak" || len(got) != 1 {
		t.Fatalf("profile %+v after %d calls: %v", p, len(got), err)
	}
	var n int
	var cents int64
	if err := pool.QueryRow(ctx, `SELECT count(*), coalesce(sum(amount_cents),0) FROM spend_entries WHERE workspace_id=$1 AND integration_id=$2 AND ref='check-ref'`, ws, id).Scan(&n, &cents); err != nil {
		t.Fatal(err)
	}
	if n != 1 || cents != 2 {
		t.Fatalf("booked %d entries, %d cents", n, cents)
	}
	if _, err = h.Connections().Open(ctx, uuid.NewString(), nil, CapReadProfile); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("another workspace: %v", err)
	}
}
