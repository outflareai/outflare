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

// testLookups finds a profile by name (billed in money) and reads a
// company page (billed in records).
type testLookups struct{}

func (testLookups) Definition() connectors.Definition {
	return connectors.Definition{ID: "test_lookups", Name: "Test lookups", Auth: connectors.AuthAPIKey, Capabilities: []string{CapFindProfile, CapCompanyProfile}}
}
func (testLookups) Test(context.Context, *connectors.Client, connectors.Creds) (connectors.Account, error) {
	return connectors.Account{}, nil
}
func (testLookups) FindProfile(_ context.Context, _ *connectors.Client, cr connectors.Creds, q connectors.PersonQuery) (FoundProfile, error) {
	if cr["api_key"] != "test-not-a-real-key" || q.FirstName != "Ana" || q.LastName != "Diaz" || q.Domain != "northpeak.example" {
		return FoundProfile{}, errors.New("wrong credential or query")
	}
	return FoundProfile{Found: true, LinkedInURL: "https://www.linkedin.com/in/ana-diaz-example", Name: "Ana Diaz", CompanyDomain: "northpeak.example", Cost: Cost{USD: 0.01}}, nil
}
func (testLookups) CompanyProfile(_ context.Context, _ *connectors.Client, cr connectors.Creds, page string) (CompanyLinkedInProfile, error) {
	if cr["api_key"] != "test-not-a-real-key" {
		return CompanyLinkedInProfile{}, errors.New("wrong connection credential")
	}
	return CompanyLinkedInProfile{Found: true, LinkedInURL: page, Size: "51-200", Employees: 143, Credits: 1}, nil
}

// An extension finds a person's profile by name and reads a company page's
// size through the workspace's own provider, by capability, and each call
// is booked against its reference: money for the one, records for the
// other.
func TestConnectionLooksUpProfilesAndCompanies(t *testing.T) {
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
	if _, err = pool.Exec(ctx, `INSERT INTO integrations(id,workspace_id,connector,name,credentials) VALUES($1,$2,'test_lookups','Test',$3)`, id, ws, box.Seal([]byte(`{"api_key":"test-not-a-real-key"}`), []byte(id))); err != nil {
		t.Fatal(err)
	}
	h := &Host{Pool: pool, store: &connectors.Store{Pool: pool, Box: box, Registry: connectors.NewRegistry([]connectors.Connector{testLookups{}}, nil), Client: &connectors.Client{}}}
	c, err := h.Connections().Open(ctx, ws, nil, CapFindProfile)
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.FindProfile(ctx, PersonQuery{FirstName: "Ana", LastName: "Diaz", Domain: "northpeak.example"}, "find-ref")
	if err != nil || !p.Found || p.LinkedInURL != "https://www.linkedin.com/in/ana-diaz-example" {
		t.Fatalf("profile %+v: %v", p, err)
	}
	co, err := c.CompanyProfile(ctx, "https://www.linkedin.com/company/northpeak-example", "company-ref")
	if err != nil || !co.Found || co.Employees != 143 {
		t.Fatalf("company %+v: %v", co, err)
	}
	var cents int64
	var units float64
	if err := pool.QueryRow(ctx, `SELECT coalesce(sum(amount_cents),0) FROM spend_entries WHERE workspace_id=$1 AND integration_id=$2 AND ref='find-ref'`, ws, id).Scan(&cents); err != nil || cents != 1 {
		t.Fatalf("profile lookup booked %d cents: %v", cents, err)
	}
	if err := pool.QueryRow(ctx, `SELECT coalesce(sum(units),0)::float8 FROM spend_entries WHERE workspace_id=$1 AND integration_id=$2 AND ref='company-ref' AND unit_name='credit'`, ws, id).Scan(&units); err != nil || units != 1 {
		t.Fatalf("company lookup booked %v credits: %v", units, err)
	}
	if _, err = h.Connections().Open(ctx, uuid.NewString(), nil, CapCompanyProfile); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("another workspace: %v", err)
	}
}
