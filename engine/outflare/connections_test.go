package outflare

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/db"
	"github.com/outflareai/outflare/engine/internal/emailfind"
	"github.com/outflareai/outflare/engine/internal/secrets"
)

// testPeople is a people-data provider: it finds a company's page,
// searches people (from the page when pages is set), reads a profile for
// one record and can be asked for emails one step at a time.
type testPeople struct {
	id    string
	pages bool
	got   *[]string
}

func (p testPeople) Definition() connectors.Definition {
	return connectors.Definition{ID: p.id, Name: "Test people", Category: connectors.CategoryData, Auth: connectors.AuthAPIKey,
		Capabilities: []string{CapSearchPeople, CapCompanyPage, CapReadProfile, connectors.CapEnrichPerson, connectors.CapFindEmail}}
}
func (testPeople) Test(context.Context, *connectors.Client, connectors.Creds) (connectors.Account, error) {
	return connectors.Account{}, nil
}
func (p testPeople) log(s string) { *p.got = append(*p.got, p.id+" "+s) }
func (p testPeople) CompanyPage(_ context.Context, _ *connectors.Client, cr connectors.Creds, domain string) (CompanyLinkedInPage, error) {
	if cr["api_key"] != "test-not-a-real-key" {
		return CompanyLinkedInPage{}, errors.New("wrong connection credential")
	}
	p.log("page " + domain)
	if domain == "nopage.example" {
		return CompanyLinkedInPage{Domain: domain}, nil
	}
	return CompanyLinkedInPage{Found: true, LinkedInURL: "https://www.linkedin.com/company/northpeak-example", Name: "Northpeak", Domain: domain, Credits: 1}, nil
}
func (testPeople) SearchFilters() []string { return []string{connectors.FilterCompanyDomains} }
func (p testPeople) SearchPeople(_ context.Context, _ *connectors.Client, _ connectors.Creds, f PeopleFilter, _ string, _ int) (PeoplePage, error) {
	p.log("search " + f.CompanyDomains[0])
	return PeoplePage{People: []FoundPerson{{FirstName: "Ana"}}, Credits: 3}, nil
}
func (p testPeople) ReadProfile(context.Context, *connectors.Client, connectors.Creds, string) (PersonProfile, error) {
	p.log("profile")
	return PersonProfile{Found: true, Cost: Cost{Units: 1}}, nil
}
func (testPeople) EnrichPerson(context.Context, *connectors.Client, connectors.Creds, connectors.PersonQuery) (connectors.Enriched, error) {
	return connectors.Enriched{}, nil
}
func (testPeople) ProfileOnly() bool { return true }
func (testPeople) FindSteps() []connectors.FindStep {
	return []connectors.FindStep{{ID: "byprofile", NeedsProfile: true}}
}
func (p testPeople) FindStep(_ context.Context, _ *connectors.Client, _ connectors.Creds, step string, q connectors.PersonQuery) (connectors.StepAnswer, error) {
	p.log("step " + step + " " + q.LinkedInURL)
	return connectors.StepAnswer{Addresses: []string{"ana@old.example", "ana@northpeak.example"}, Credits: 1}, nil
}

// testPageSearcher is a testPeople that can search a known page.
type testPageSearcher struct{ testPeople }

func (p testPageSearcher) SearchCompanyPage(_ context.Context, _ *connectors.Client, _ connectors.Creds, page CompanyLinkedInPage, _ PeopleFilter, size int) (PeoplePage, error) {
	p.log("search page " + page.LinkedInURL)
	return PeoplePage{People: []FoundPerson{{FirstName: "Ana"}, {FirstName: "Ben"}}, Credits: 2}, nil
}

// testPeopleResearch finds people through research; testSequencer knows
// who its account has emailed.
type testPeopleResearch struct{ got *[]PeopleResearchQuery }

func (testPeopleResearch) Definition() connectors.Definition {
	return connectors.Definition{ID: "test_people_research", Name: "Test research", Auth: connectors.AuthAPIKey, Capabilities: []string{CapResearchPeople}}
}
func (testPeopleResearch) Test(context.Context, *connectors.Client, connectors.Creds) (connectors.Account, error) {
	return connectors.Account{}, nil
}
func (r testPeopleResearch) ResearchPeople(_ context.Context, _ *connectors.Client, _ connectors.Creds, q PeopleResearchQuery) (PeopleResearch, error) {
	*r.got = append(*r.got, q)
	if q.RunID == "" {
		return PeopleResearch{RunID: "run_1"}, connectors.TimedOutf("still researching")
	}
	return PeopleResearch{People: []ResearchedPerson{{Name: "Ana Diaz", Title: "CEO"}}, RunID: q.RunID, Cost: Cost{USD: 0.01, Estimated: true}}, nil
}

type testSequencer struct{}

func (testSequencer) Definition() connectors.Definition {
	return connectors.Definition{ID: "test_sequencer", Name: "Test sequencer", Auth: connectors.AuthAPIKey, Capabilities: []string{CapContactHistory}}
}
func (testSequencer) Test(context.Context, *connectors.Client, connectors.Creds) (connectors.Account, error) {
	return connectors.Account{}, nil
}
func (testSequencer) ContactSent(_ context.Context, _ *connectors.Client, _ connectors.Creds, email string) (connectors.SentBefore, error) {
	switch email {
	case "ana@northpeak.example":
		return connectors.SentBefore{Found: true, EmailsSent: 2}, nil
	case "down@northpeak.example":
		return connectors.SentBefore{}, connectors.Errf(connectors.ErrUnavailable, "down")
	}
	return connectors.SentBefore{}, nil
}

type connEnv struct {
	t    *testing.T
	pool *pgxpool.Pool
	box  *secrets.Box
	ws   string
	h    *Host
}

func newConnEnv(t *testing.T, cs ...connectors.Connector) *connEnv {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("disposable database required")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	ws := uuid.NewString()
	if _, err = pool.Exec(ctx, `INSERT INTO workspaces(id,name,slug) VALUES($1,'Northpeak',$2)`, ws, ws); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.NewBox(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	store := &connectors.Store{Pool: pool, Box: box, Registry: connectors.NewRegistry(cs, nil), Client: &connectors.Client{}}
	return &connEnv{t: t, pool: pool, box: box, ws: ws, h: &Host{Pool: pool, store: store}}
}

// connect adds a connection of the workspace's own, newer than the ones
// before it. brand is the brand that owns it, or "".
func (e *connEnv) connect(connector, brand string) string {
	e.t.Helper()
	id := uuid.NewString()
	var b *string
	if brand != "" {
		b = &brand
	}
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO integrations(id,workspace_id,brand_id,connector,name,credentials,created_at)
		VALUES($1,$2,$3,$4,$6,$5,clock_timestamp())`, id, e.ws, b, connector,
		e.box.Seal([]byte(`{"api_key":"test-not-a-real-key"}`), []byte(id)), "Test "+id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// booked is what was booked against ref on a connection: entries, credits
// and cents.
func (e *connEnv) booked(id, ref string) (n int, credits float64, cents int64) {
	e.t.Helper()
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*), coalesce(sum(units) FILTER (WHERE unit_name = 'credit'),0)::float8, coalesce(sum(amount_cents),0)
		FROM spend_entries WHERE workspace_id=$1 AND integration_id=$2 AND ref=$3`, e.ws, id, ref).Scan(&n, &credits, &cents); err != nil {
		e.t.Fatal(err)
	}
	return n, credits, cents
}

func ids(cs []*Connection) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

// OpenAll returns every connection with the capability that serves the
// brand: the preferred connectors first, in the order named, then the
// rest, the oldest first within each. One that can't be opened is left out.
func TestConnectionsOpenAll(t *testing.T) {
	var got []string
	e := newConnEnv(t, testPeople{id: "people_a", got: &got}, testPeople{id: "people_b", got: &got}, testPeople{id: "people_c", got: &got}, testSequencer{})
	ctx := context.Background()
	cs := e.h.Connections()
	if _, err := cs.OpenAll(ctx, e.ws, nil, CapSearchPeople); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("nothing connected: %v", err)
	}
	a1 := e.connect("people_a", "")
	b1 := e.connect("people_b", "")
	e.connect("test_sequencer", "") // another capability
	a2 := e.connect("people_a", "")
	c1 := e.connect("people_c", "")

	open := func(prefer ...string) []string {
		t.Helper()
		list, err := cs.OpenAll(ctx, e.ws, nil, CapSearchPeople, prefer...)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range list {
			if c.WorkspaceID != e.ws || c.Connector == "" || c.o == nil {
				t.Fatalf("connection %+v", c)
			}
		}
		return ids(list)
	}
	if got := open(); !reflect.DeepEqual(got, []string{a1, b1, a2, c1}) {
		t.Fatalf("oldest first: %v", got)
	}
	if got := open("people_c", "people_a"); !reflect.DeepEqual(got, []string{c1, a1, a2, b1}) {
		t.Fatalf("preferred first: %v", got)
	}
	if got := open("people_b", "not_connected"); !reflect.DeepEqual(got, []string{b1, a1, a2, c1}) {
		t.Fatalf("one preferred: %v", got)
	}

	// A connection that is not connected does not count; one whose
	// credentials can't be read is left out.
	if _, err := e.pool.Exec(ctx, `UPDATE integrations SET status = 'error' WHERE id = $1`, a1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE integrations SET credentials = $2 WHERE id = $1`, b1, []byte("not sealed")); err != nil {
		t.Fatal(err)
	}
	if got := open(); !reflect.DeepEqual(got, []string{a2, c1}) {
		t.Fatalf("broken ones left out: %v", got)
	}
	// When none can be opened, the first one's error comes back.
	if _, err := e.pool.Exec(ctx, `UPDATE integrations SET credentials = $2 WHERE id = ANY($1)`, []string{a2, c1}, []byte("not sealed")); err != nil {
		t.Fatal(err)
	}
	if list, err := cs.OpenAll(ctx, e.ws, nil, CapSearchPeople); err == nil || errors.Is(err, ErrNotConnected) || list != nil {
		t.Fatalf("none can be opened: %v %v", list, err)
	}

	// Only the connections that serve the brand, and never another
	// workspace's.
	e2 := newConnEnv(t, testPeople{id: "people_a", got: &got}, testPeople{id: "people_b", got: &got})
	brand, other := uuid.NewString(), uuid.NewString()
	for _, b := range []string{brand, other} {
		if _, err := e2.pool.Exec(ctx, `INSERT INTO brands(id,workspace_id,name,handle) VALUES($1,$2,$3,$3)`, b, e2.ws, "b-"+b[:8]); err != nil {
			t.Fatal(err)
		}
	}
	shared := e2.connect("people_a", "")
	own := e2.connect("people_b", brand)
	theirs := e2.connect("people_b", other)
	list, err := e2.h.Connections().OpenAll(ctx, e2.ws, &brand, CapSearchPeople)
	if err != nil || !reflect.DeepEqual(ids(list), []string{shared, own}) {
		t.Fatalf("for a brand: %v %v (not %s)", ids(list), err, theirs)
	}
	if list, err = e2.h.Connections().OpenAll(ctx, e2.ws, nil, CapSearchPeople); err != nil || len(list) != 3 {
		t.Fatalf("for shared work: %v %v", ids(list), err)
	}
	if _, err = e2.h.Connections().OpenAll(ctx, uuid.NewString(), nil, CapSearchPeople); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("another workspace: %v", err)
	}
}

// A company's page is found once and then searched, on any connection:
// one that can search a page does only that, another searches the domain.
func TestConnectionCompanyPageAndSearchPeopleAt(t *testing.T) {
	var got []string
	e := newConnEnv(t, testPageSearcher{testPeople{id: "people_pages", got: &got}}, testPeople{id: "people_plain", got: &got}, testSequencer{})
	ctx := context.Background()
	pagesID := e.connect("people_pages", "")
	plainID := e.connect("people_plain", "")
	seq := e.connect("test_sequencer", "")
	list, err := e.h.Connections().OpenAll(ctx, e.ws, nil, CapCompanyPage)
	if err != nil || len(list) != 2 {
		t.Fatalf("connections: %v %v", list, err)
	}
	pages, plain := list[0], list[1]

	page, err := pages.CompanyPage(ctx, "northpeak.example", "run-ref")
	if err != nil || !page.Found || page.LinkedInURL == "" || page.Domain != "northpeak.example" {
		t.Fatalf("page %+v %v", page, err)
	}
	if n, credits, _ := e.booked(pagesID, "run-ref"); n != 1 || credits != 1 {
		t.Fatalf("booked %d entries, %v credits", n, credits)
	}
	var note string
	if err := e.pool.QueryRow(ctx, `SELECT note FROM spend_entries WHERE integration_id = $1 AND ref = 'run-ref'`, pagesID).Scan(&note); err != nil || note != "Company page lookup" {
		t.Fatalf("note %q %v", note, err)
	}
	// No page is an answer, and free.
	if miss, err := pages.CompanyPage(ctx, "nopage.example", "miss-ref"); err != nil || miss.Found {
		t.Fatalf("no page: %+v %v", miss, err)
	}
	if n, _, _ := e.booked(pagesID, "miss-ref"); n != 0 {
		t.Fatalf("a miss booked %d entries", n)
	}

	got = nil
	found, err := pages.SearchPeopleAt(ctx, page, PeopleFilter{Titles: []string{"CTO"}}, 5, "search-ref")
	if err != nil || len(found.People) != 2 || !reflect.DeepEqual(got, []string{"people_pages search page " + page.LinkedInURL}) {
		t.Fatalf("page search: %+v %v after %v", found, err, got)
	}
	if n, credits, _ := e.booked(pagesID, "search-ref"); n != 1 || credits != 2 {
		t.Fatalf("booked %d entries, %v credits", n, credits)
	}
	// A connector that can't search a page searches the company's domain.
	got = nil
	found, err = plain.SearchPeopleAt(ctx, page, PeopleFilter{CompanyDomains: []string{"other.example"}}, 5, "plain-ref")
	if err != nil || len(found.People) != 1 || !reflect.DeepEqual(got, []string{"people_plain search northpeak.example"}) {
		t.Fatalf("domain search: %+v %v after %v", found, err, got)
	}
	if n, credits, _ := e.booked(plainID, "plain-ref"); n != 1 || credits != 3 {
		t.Fatalf("booked %d entries, %v credits", n, credits)
	}
	// So does one that can, when there is no page.
	got = nil
	if _, err = pages.SearchPeopleAt(ctx, CompanyLinkedInPage{Domain: "nopage.example"}, PeopleFilter{}, 5, "nopage-ref"); err != nil ||
		!reflect.DeepEqual(got, []string{"people_pages search nopage.example"}) {
		t.Fatalf("no page: %v after %v", err, got)
	}

	// A profile a provider counts in records is booked in credits.
	if p, err := pages.ReadProfile(ctx, "https://www.linkedin.com/in/ana-diaz-example", "profile-ref"); err != nil || !p.Found {
		t.Fatalf("profile %+v %v", p, err)
	}
	if n, credits, cents := e.booked(pagesID, "profile-ref"); n != 1 || credits != 1 || cents != 0 {
		t.Fatalf("booked %d entries, %v credits, %d cents", n, credits, cents)
	}

	// A connection that can't do it says so.
	c, err := e.h.Connections().Open(ctx, e.ws, nil, CapContactHistory)
	if err != nil || c.ID != seq {
		t.Fatal(err)
	}
	if _, err := c.CompanyPage(ctx, "northpeak.example", ""); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("a sequencer asked for a page: %v", err)
	}
	if _, err := c.ResearchPeople(ctx, PeopleResearchQuery{Domain: "northpeak.example"}, ""); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("a sequencer asked to research: %v", err)
	}
	if _, _, err := pages.ContactSent(ctx, "ana@northpeak.example", ""); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("a data provider asked who it emailed: %v", err)
	}

	// Has the sequencer already emailed someone.
	if found, sent, err := c.ContactSent(ctx, "ana@northpeak.example", "run-ref"); err != nil || !found || sent != 2 {
		t.Fatalf("emailed before: %v %d %v", found, sent, err)
	}
	if found, sent, err := c.ContactSent(ctx, "new@northpeak.example", "run-ref"); err != nil || found || sent != 0 {
		t.Fatalf("never seen: %v %d %v", found, sent, err)
	}
	if _, _, err := c.ContactSent(ctx, "down@northpeak.example", "run-ref"); ConnectorErrorKind(err) != ConnectorUnavailable {
		t.Fatalf("the sequencer down: %v", err)
	}
}

// People research waits like company research: a run still going is
// picked up by its id and booked once.
func TestConnectionResearchesPeople(t *testing.T) {
	var got []PeopleResearchQuery
	e := newConnEnv(t, testPeopleResearch{got: &got})
	ctx := context.Background()
	id := e.connect("test_people_research", "")
	c, err := e.h.Connections().Open(ctx, e.ws, nil, CapResearchPeople)
	if err != nil {
		t.Fatal(err)
	}
	q := PeopleResearchQuery{Domain: "northpeak.example", Exclude: []string{"Ben Ito"}}
	res, err := c.ResearchPeople(ctx, q, "run-ref")
	if err == nil || ConnectorErrorKind(err) != ConnectorUnavailable || res.RunID != "run_1" {
		t.Fatalf("still going: %+v %v", res, err)
	}
	if n, _, _ := e.booked(id, "run-ref"); n != 0 {
		t.Fatalf("booked %d entries before the answer", n)
	}
	q.RunID = res.RunID
	res, err = c.ResearchPeople(ctx, q, "run-ref")
	if err != nil || len(res.People) != 1 || res.People[0].Name != "Ana Diaz" || len(got) != 2 || got[1].Exclude[0] != "Ben Ito" {
		t.Fatalf("people %+v after %d calls: %v", res, len(got), err)
	}
	if n, _, cents := e.booked(id, "run-ref"); n != 1 || cents != 1 {
		t.Fatalf("booked %d entries, %d cents", n, cents)
	}
	var note string
	if err := e.pool.QueryRow(ctx, `SELECT note FROM spend_entries WHERE integration_id = $1`, id).Scan(&note); err != nil || note != "People research, estimated from the price list" {
		t.Fatalf("note %q %v", note, err)
	}
}

// An extension asks the workspace's finders one step at a time, for the
// brand its work is for.
func TestEmailFindAsksOneStep(t *testing.T) {
	var got []string
	e := newConnEnv(t, testPeople{id: "people_a", got: &got})
	ctx := context.Background()
	brand, other := uuid.NewString(), uuid.NewString()
	for _, b := range []string{brand, other} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO brands(id,workspace_id,name,handle) VALUES($1,$2,$3,$3)`, b, e.ws, "b-"+b[:8]); err != nil {
			t.Fatal(err)
		}
	}
	id := e.connect("people_a", brand)
	e.h.emailFinder = &emailfind.Service{Pool: e.pool, Store: e.h.store}
	find := e.h.EmailFind()

	steps, err := find.FinderSteps(WithBrand(ctx, brand), e.ws)
	want := []EmailFinderStep{{ID: "byprofile", NeedsProfile: true, Connectors: []string{"people_a"}}}
	if err != nil || !reflect.DeepEqual(steps, want) {
		t.Fatalf("steps %+v %v", steps, err)
	}
	// Another brand has no finder of its own.
	if steps, err = find.FinderSteps(WithBrand(ctx, other), e.ws); err != nil || len(steps) != 0 {
		t.Fatalf("another brand's steps %+v %v", steps, err)
	}
	it := EmailFindItem{FirstName: "Ana", LastName: "Diaz", Domain: "northpeak.example", LinkedInURL: "https://www.linkedin.com/in/ana-diaz-example"}
	res, err := find.AskFinderStep(WithBrand(ctx, other), e.ws, "byprofile", it)
	if err != nil || res.Code != EmailStop || res.StopCode != "connector_needed" || len(got) != 0 {
		t.Fatalf("another brand: %+v %v after %v", res, err, got)
	}
	var r EmailStepResult
	r, err = find.AskFinderStep(WithBrand(ctx, brand), e.ws, "byprofile", it)
	if err != nil || !r.Asked || r.Code != "" || r.Connection != id || r.Cost.Units != 1 ||
		!reflect.DeepEqual(r.Addresses, []string{"ana@old.example", "ana@northpeak.example"}) ||
		!reflect.DeepEqual(got, []string{"people_a step byprofile https://www.linkedin.com/in/ana-diaz-example"}) {
		t.Fatalf("step %+v %v after %v", r, err, got)
	}
	// The call is booked to the brand the work is for.
	var booked *string
	if err := e.pool.QueryRow(ctx, `SELECT brand_id::text FROM spend_entries WHERE workspace_id = $1 AND integration_id = $2`, e.ws, id).Scan(&booked); err != nil || booked == nil || *booked != brand {
		t.Fatalf("booked to %v, %v", booked, err)
	}
}
