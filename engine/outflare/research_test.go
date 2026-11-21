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

type testResearcher struct{ got *[]CompanyResearchQuery }

func (testResearcher) Definition() connectors.Definition {
	return connectors.Definition{ID: "test_research", Name: "Test research", Auth: connectors.AuthAPIKey, Capabilities: []string{CapResearchCompany}}
}
func (testResearcher) Test(context.Context, *connectors.Client, connectors.Creds) (connectors.Account, error) {
	return connectors.Account{}, nil
}
func (testResearcher) EstimateCompanyResearch(CompanyResearchQuery) Cost {
	return Cost{USD: 0.025, Estimated: true}
}
func (r testResearcher) ResearchCompany(_ context.Context, _ *connectors.Client, cr connectors.Creds, q CompanyResearchQuery) (CompanyResearch, error) {
	if cr["api_key"] != "test-not-a-real-key" {
		return CompanyResearch{}, errors.New("wrong connection credential")
	}
	*r.got = append(*r.got, q)
	if q.RunID == "" {
		// Still going when the wait ran out: nothing to book yet.
		return CompanyResearch{RunID: "run_1"}, connectors.TimedOutf("still researching")
	}
	return CompanyResearch{RunID: q.RunID, Cost: Cost{USD: 0.025, Estimated: true}, Sections: []ResearchSection{
		{Title: "Summary", Text: "Northpeak sells to agencies.", Sources: []ResearchSource{{Title: "Northpeak", URL: "https://northpeak.example/"}}},
	}}, nil
}

// An extension researches a company through the workspace's own research
// provider, by capability, without seeing the credentials, and the cost is
// booked once, against its reference, when the findings arrive.
func TestConnectionResearchesACompany(t *testing.T) {
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
	if _, err = pool.Exec(ctx, `INSERT INTO integrations(id,workspace_id,brand_id,connector,name,credentials) VALUES($1,$2,$3,'test_research','Test',$4)`, id, ws, brand, box.Seal([]byte(`{"api_key":"test-not-a-real-key"}`), []byte(id))); err != nil {
		t.Fatal(err)
	}
	var got []CompanyResearchQuery
	h := &Host{Pool: pool, store: &connectors.Store{Pool: pool, Box: box, Registry: connectors.NewRegistry([]connectors.Connector{testResearcher{got: &got}}, nil), Client: &connectors.Client{}}}
	c, err := h.Connections().Open(ctx, ws, &brand, CapResearchCompany)
	if err != nil {
		t.Fatal(err)
	}
	booked := func() (n int, cents int64) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT count(*), coalesce(sum(amount_cents),0) FROM spend_entries WHERE workspace_id=$1 AND integration_id=$2 AND ref='run-ref'`, ws, id).Scan(&n, &cents); err != nil {
			t.Fatal(err)
		}
		return n, cents
	}
	q := CompanyResearchQuery{Domain: "northpeak.example", Brief: "Do they sell to agencies?"}
	res, err := c.Research(ctx, q, "run-ref")
	if err == nil || ConnectorErrorKind(err) != ConnectorUnavailable || res.RunID != "run_1" {
		t.Fatalf("still going: %+v %v", res, err)
	}
	if n, _ := booked(); n != 0 {
		t.Fatalf("booked %d entries before the findings arrived", n)
	}
	q.RunID = res.RunID
	res, err = c.Research(ctx, q, "run-ref")
	if err != nil || len(res.Sections) != 1 || res.Sections[0].Sources[0].URL != "https://northpeak.example/" || len(got) != 2 {
		t.Fatalf("findings %+v after %d calls: %v", res, len(got), err)
	}
	if n, cents := booked(); n != 1 || cents != 3 {
		t.Fatalf("booked %d entries, %d cents", n, cents)
	}
	other := uuid.NewString()
	if _, err = h.Connections().Open(ctx, other, nil, CapResearchCompany); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("another workspace: %v", err)
	}
}
