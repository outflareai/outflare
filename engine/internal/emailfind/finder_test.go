package emailfind

import (
	"context"
	"encoding/base64"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/verifiers"
	"github.com/outflareai/outflare/engine/internal/secrets"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/verify"
)

// hasMX is a resolver for which every domain takes mail, so the built-in
// check can't rule an address out (no real DNS).
type hasMX struct{}

func (hasMX) LookupMX(context.Context, string) ([]*net.MX, error) {
	return []*net.MX{{Host: "mx.acme.example."}}, nil
}
func (hasMX) LookupHost(context.Context, string) ([]string, error) { return []string{"192.0.2.1"}, nil }

// fakeFinder is a connected email finder. It records which connection's key
// each call carried and answers with answer.
type fakeFinder struct {
	id     string
	mu     sync.Mutex
	keys   []string
	answer func(key string, q connectors.PersonQuery) (connectors.Enriched, error)
}

func (f *fakeFinder) Definition() connectors.Definition {
	return connectors.Definition{ID: f.id, Name: "Fake finder", Category: connectors.CategoryData, Auth: connectors.AuthAPIKey,
		Capabilities: []string{connectors.CapEnrichPerson, connectors.CapFindEmail}}
}

func (f *fakeFinder) Test(context.Context, *connectors.Client, connectors.Creds) (connectors.Account, error) {
	return connectors.Account{}, nil
}

func (f *fakeFinder) EnrichPerson(_ context.Context, _ *connectors.Client, cr connectors.Creds, q connectors.PersonQuery) (connectors.Enriched, error) {
	f.mu.Lock()
	f.keys = append(f.keys, cr["api_key"])
	f.mu.Unlock()
	return f.answer(cr["api_key"], q)
}

func (f *fakeFinder) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.keys...)
}

// hit answers every lookup with email, unchecked, for one credit.
func hit(email string) func(string, connectors.PersonQuery) (connectors.Enriched, error) {
	return func(_ string, q connectors.PersonQuery) (connectors.Enriched, error) {
		return connectors.Enriched{Found: true, Credits: 1, Person: connectors.Person{
			FirstName: q.FirstName, LastName: q.LastName, Email: email, EmailStatus: connectors.EmailGuessed}}, nil
	}
}

func miss(string, connectors.PersonQuery) (connectors.Enriched, error) {
	return connectors.Enriched{}, nil
}

// fakeVerifier is a connected paid verifier with one verdict for every
// address.
type fakeVerifier struct {
	status string
	mu     sync.Mutex
	asked  []string
}

func (f *fakeVerifier) Definition() connectors.Definition {
	return connectors.Definition{ID: "fakeverifier", Name: "Fake verifier", Category: connectors.CategoryVerification,
		Auth: connectors.AuthAPIKey, Capabilities: []string{connectors.CapVerify}}
}

func (f *fakeVerifier) Test(context.Context, *connectors.Client, connectors.Creds) (connectors.Account, error) {
	return connectors.Account{}, nil
}

func (f *fakeVerifier) Verify(_ context.Context, _ *connectors.Client, _ connectors.Creds, email string) (connectors.Verification, error) {
	f.mu.Lock()
	f.asked = append(f.asked, email)
	f.mu.Unlock()
	score := 97
	return connectors.Verification{Email: email, Status: f.status, Score: &score}, nil
}

// finderEnv is a workspace with a finder service over fake connectors.
type finderEnv struct {
	t     *testing.T
	pool  *pgxpool.Pool
	ws    string
	s     *Service
	store *connectors.Store
}

func newFinderEnv(t *testing.T, cs ...connectors.Connector) *finderEnv {
	t.Helper()
	pool := testPool(t)
	box, err := secrets.NewBox(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	store := &connectors.Store{Pool: pool, Box: box, Registry: connectors.NewRegistry(cs, nil), Client: &connectors.Client{}}
	v := &verify.Service{Pool: pool, Store: store, Builtin: verifiers.Builtin{Resolver: hasMX{}}}
	return &finderEnv{t: t, pool: pool, ws: newWS(t, pool), store: store,
		s: &Service{Pool: pool, Verify: v, Resolver: noMX{}, Store: store}}
}

// connect adds a connection of the workspace's own to connector, with key.
func (e *finderEnv) connect(connector, key string) string {
	e.t.Helper()
	var id string
	err := pgx.BeginFunc(context.Background(), e.pool, func(tx pgx.Tx) error {
		i, err := e.store.Insert(context.Background(), tx, e.ws, connector, connectors.AuthAPIKey, key,
			connectors.Creds{"api_key": key}, connectors.Account{}, "")
		id = i.ID
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	// Connections are used in the order they were connected.
	if _, err := e.pool.Exec(context.Background(),
		`UPDATE integrations SET created_at = clock_timestamp() WHERE id = $1`, id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *finderEnv) resolve(first, last, domain string) ItemResult {
	e.t.Helper()
	res, err := e.s.Resolve(context.Background(), e.ws, FindRequest{Items: []FindItem{
		{Ref: "r1", FirstName: first, LastName: last, Domain: domain}}})
	if err != nil {
		e.t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].Ref != "r1" {
		e.t.Fatalf("items = %+v", res.Items)
	}
	return res.Items[0]
}

// booked is the credits in the workspace's spend ledger for a connection.
func (e *finderEnv) booked(integration string) float64 {
	e.t.Helper()
	var units float64
	if err := e.pool.QueryRow(context.Background(), `SELECT coalesce(sum(units), 0)::float8 FROM spend_entries
		WHERE workspace_id = $1 AND integration_id = $2`, e.ws, integration).Scan(&units); err != nil {
		e.t.Fatal(err)
	}
	return units
}

func (e *finderEnv) checks(email, checkType string) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM email_checks
		WHERE workspace_id = $1 AND email = $2 AND check_type = $3`, e.ws, email, checkType).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestResolveFinderHitBeatsLowConfidencePattern(t *testing.T) {
	f := &fakeFinder{id: "fakefinder", answer: hit("a.nguyen@acme.example")}
	e := newFinderEnv(t, f)
	id := e.connect("fakefinder", "test_not_a_real_key_a")
	// The workspace's own data gives a shape, but too thin to trust.
	addLead(t, e.pool, e.ws, "John", "Doe", "john.doe@acme.example", "")
	native, err := e.s.Find(context.Background(), e.ws, FindRequest{Items: []FindItem{
		{Ref: "r1", FirstName: "Alice", LastName: "Nguyen", Domain: "acme.example"}}})
	if err != nil {
		t.Fatal(err)
	}
	if n := native.Items[0]; n.Evidence != EvidencePattern || n.Confidence >= highConfidence || n.Email == "" {
		t.Fatalf("the test needs a low-confidence pattern guess, got %+v", n)
	}

	got := e.resolve("Alice", "Nguyen", "acme.example")
	if got.Email != "a.nguyen@acme.example" || got.Code != CodeFound || got.Confidence != finderConfidence ||
		got.Evidence != "provider:fakefinder" {
		t.Fatalf("result = %+v", got)
	}
	if got.Confidence < highConfidence {
		t.Fatalf("a provider's answer must not read as a low-confidence guess: %d", got.Confidence)
	}
	if want := (Cost{Provider: "fakefinder", Connection: id, Units: 1}); got.Cost != want {
		t.Fatalf("cost = %+v, want %+v", got.Cost, want)
	}
	if n := len(f.calls()); n != 1 {
		t.Fatalf("finder called %d times", n)
	}
	if u := e.booked(id); u != 1 {
		t.Fatalf("ledger has %v credits, want 1 booked once", u)
	}
	if e.checks("a.nguyen@acme.example", "provider:fakefinder") != 1 || e.checks("a.nguyen@acme.example", "builtin") != 1 {
		t.Fatal("the finder tier and the built-in check on its address are both logged")
	}
}

func TestResolveFinderHitWithNoPattern(t *testing.T) {
	f := &fakeFinder{id: "fakefinder", answer: hit("Bo.Chen@Unknown.example")}
	e := newFinderEnv(t, f)
	e.connect("fakefinder", "test_not_a_real_key_a")
	got := e.resolve("Bo", "Chen", "www.Unknown.example")
	if got.Email != "bo.chen@unknown.example" || got.Code != CodeFound || got.Evidence != "provider:fakefinder" || got.Cost.Units != 1 {
		t.Fatalf("result = %+v", got)
	}
	// The finder is asked with the bare company domain.
	f.answer = func(_ string, q connectors.PersonQuery) (connectors.Enriched, error) {
		if q.Domain != "other.example" || q.FirstName != "Cy" || q.LastName != "Diaz" {
			t.Errorf("query = %+v", q)
		}
		return connectors.Enriched{}, nil
	}
	e.resolve(" Cy ", "Diaz", "WWW.other.example")
}

func TestResolveFinderMissFallsBack(t *testing.T) {
	f := &fakeFinder{id: "fakefinder", answer: miss}
	e := newFinderEnv(t, f)
	id := e.connect("fakefinder", "test_not_a_real_key_a")
	got := e.resolve("Bo", "Chen", "unknown.example")
	if len(f.calls()) != 1 {
		t.Fatalf("finder called %d times", len(f.calls()))
	}
	// Exactly what a workspace with no finder gets.
	none := newFinderEnv(t)
	want := none.resolve("Bo", "Chen", "unknown.example")
	if got != want {
		t.Fatalf("miss = %+v, want the old result %+v", got, want)
	}
	if got.Code != CodeRisky || got.Email == "" || got.Cost.Units != 0 {
		t.Fatalf("result = %+v", got)
	}
	if e.booked(id) != 0 {
		t.Fatal("a free miss booked spend")
	}

	// A miss the provider charged for is still reported, on the old result.
	f.answer = func(string, connectors.PersonQuery) (connectors.Enriched, error) {
		return connectors.Enriched{Credits: 1}, nil
	}
	got = e.resolve("Di", "Evans", "unknown.example")
	if got.Code != CodeRisky || got.Evidence != EvidenceDefault || got.Cost != (Cost{Provider: "fakefinder", Connection: id, Units: 1}) {
		t.Fatalf("charged miss = %+v", got)
	}
}

func TestResolveFinderTakesTurnsAndSkipsRateLimited(t *testing.T) {
	f := &fakeFinder{id: "fakefinder", answer: hit("x@acme.example")}
	e := newFinderEnv(t, f)
	a := e.connect("fakefinder", "test_not_a_real_key_a")
	b := e.connect("fakefinder", "test_not_a_real_key_b")
	for _, first := range []string{"Ann", "Ben", "Cat", "Dan"} {
		if got := e.resolve(first, "Roe", "acme.example"); got.Code != CodeFound {
			t.Fatalf("%s: %+v", first, got)
		}
	}
	want := []string{"test_not_a_real_key_a", "test_not_a_real_key_b", "test_not_a_real_key_a", "test_not_a_real_key_b"}
	if got := f.calls(); len(got) != 4 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		t.Fatalf("calls went to %v, want turns %v", got, want)
	}
	if e.booked(a) != 2 || e.booked(b) != 2 {
		t.Fatalf("booked %v and %v, want 2 each", e.booked(a), e.booked(b))
	}

	// The first connection is rate limited: the second answers, and the
	// first is left alone while it recovers.
	events := track.Capture(t)
	f.mu.Lock()
	f.keys = nil
	f.mu.Unlock()
	f.answer = func(key string, q connectors.PersonQuery) (connectors.Enriched, error) {
		if key == "test_not_a_real_key_a" {
			return connectors.Enriched{}, connectors.HTTPError("Fake finder", 429, "slow down")
		}
		return hit("y@acme.example")(key, q)
	}
	got := e.resolve("Eve", "Roe", "acme.example") // a's turn
	if got.Email != "y@acme.example" || got.Code != CodeFound || got.Cost.Connection != b {
		t.Fatalf("after a rate limit: %+v", got)
	}
	if c := f.calls(); len(c) != 2 || c[0] != "test_not_a_real_key_a" || c[1] != "test_not_a_real_key_b" {
		t.Fatalf("calls = %v", c)
	}
	got = e.resolve("Flo", "Roe", "acme.example") // b's turn
	got2 := e.resolve("Gus", "Roe", "acme.example")
	if got.Code != CodeFound || got2.Code != CodeFound {
		t.Fatalf("results = %+v, %+v", got, got2)
	}
	for _, key := range f.calls()[2:] {
		if key != "test_not_a_real_key_b" {
			t.Fatalf("a rate limited connection was asked again at once: %v", f.calls())
		}
	}
	var failed int
	for _, ev := range events() {
		if ev.Name == "email.finder_failed" {
			failed++
			if ev.ErrorCode != "connector_quota" || ev.WorkspaceID != e.ws || ev.Props["integration_id"] != a {
				t.Fatalf("event = %+v", ev)
			}
		}
	}
	if failed != 1 {
		t.Fatalf("%d email.finder_failed events, want 1", failed)
	}
}

func TestResolveFinderEveryConnectionRefused(t *testing.T) {
	f := &fakeFinder{id: "fakefinder", answer: func(string, connectors.PersonQuery) (connectors.Enriched, error) {
		return connectors.Enriched{}, connectors.HTTPError("Fake finder", 401, "no")
	}}
	e := newFinderEnv(t, f)
	a := e.connect("fakefinder", "test_not_a_real_key_a")
	e.connect("fakefinder", "test_not_a_real_key_b")
	got := e.resolve("Bo", "Chen", "unknown.example")
	if got.Code != CodeStop || got.StopCode != "connector_needed" || len(f.calls()) != 2 {
		t.Fatalf("result = %+v after %d calls", got, len(f.calls()))
	}
	// The refused connections show as broken and are left out from then on.
	var status string
	if err := e.pool.QueryRow(context.Background(), `SELECT status FROM integrations WHERE id = $1`, a).Scan(&status); err != nil || status != connectors.StatusError {
		t.Fatalf("status = %q, %v", status, err)
	}
	if got = e.resolve("Bo", "Chen", "unknown.example"); got.Code != CodeRisky || len(f.calls()) != 2 {
		t.Fatalf("with no usable finder: %+v after %d calls", got, len(f.calls()))
	}

	// A provider that could not be reached is tried again later.
	f2 := &fakeFinder{id: "fakefinder", answer: func(string, connectors.PersonQuery) (connectors.Enriched, error) {
		return connectors.Enriched{}, connectors.HTTPError("Fake finder", 503, "down")
	}}
	e2 := newFinderEnv(t, f2)
	e2.connect("fakefinder", "test_not_a_real_key_a")
	if got = e2.resolve("Bo", "Chen", "unknown.example"); got.Code != CodeRetry {
		t.Fatalf("unreachable finder: %+v", got)
	}

	// A provider that got no answer in time for this person gave no answer
	// this time: the next tier decides, but the timeout is never
	// remembered as "no email", so a later resolve asks the provider again.
	f3 := &fakeFinder{id: "fakefinder", answer: func(string, connectors.PersonQuery) (connectors.Enriched, error) {
		return connectors.Enriched{}, connectors.TimedOutf("Fake finder did not answer in time")
	}}
	e3 := newFinderEnv(t, f3)
	e3.connect("fakefinder", "test_not_a_real_key_a")
	if got = e3.resolve("Bo", "Chen", "unknown.example"); got.Code == CodeRetry || got.Code == CodeStop || got.Code == CodePending || len(f3.calls()) != 1 {
		t.Fatalf("finder timed out: %+v after %d calls", got, len(f3.calls()))
	}
	if got = e3.resolve("Bo", "Chen", "unknown.example"); len(f3.calls()) != 2 {
		t.Fatalf("a timeout was remembered as a miss: %+v after %d calls", got, len(f3.calls()))
	}
}

func TestResolveHistoryNeverCallsFinder(t *testing.T) {
	f := &fakeFinder{id: "fakefinder", answer: hit("other@acme.example")}
	e := newFinderEnv(t, f)
	e.connect("fakefinder", "test_not_a_real_key_a")
	addLead(t, e.pool, e.ws, "Mark", "Lee", "mark.lee@acme.example", "deliverable")
	got := e.resolve("Mark", "Lee", "acme.example")
	if got.Code != CodeVerified || got.Evidence != EvidenceHistory || got.Email != "mark.lee@acme.example" || got.Cost.Units != 0 {
		t.Fatalf("result = %+v", got)
	}
	if n := len(f.calls()); n != 0 {
		t.Fatalf("a proven address called the finder %d times", n)
	}
}

func TestResolveConfidentPatternNeverCallsFinder(t *testing.T) {
	f := &fakeFinder{id: "fakefinder", answer: hit("other@acme.example")}
	e := newFinderEnv(t, f)
	e.connect("fakefinder", "test_not_a_real_key_a")
	for _, n := range [][2]string{{"John", "Doe"}, {"Jane", "Roe"}, {"Bob", "Smith"}, {"Ann", "Lee"}, {"Raj", "Patel"}, {"Li", "Wong"},
		{"Omar", "Haddad"}, {"Ines", "Costa"}, {"Kofi", "Mensah"}, {"Mia", "Berg"}, {"Noa", "Katz"}, {"Tom", "Hale"}} {
		addLead(t, e.pool, e.ws, n[0], n[1], emailOf(n[0], n[1]), "deliverable")
	}
	got := e.resolve("Alice", "Nguyen", "acme.example")
	if got.Confidence < highConfidence {
		t.Fatalf("the test needs a confident pattern, got %+v", got)
	}
	if got.Code != CodeFound || got.Evidence != EvidencePattern || got.Email != "alice.nguyen@acme.example" || len(f.calls()) != 0 {
		t.Fatalf("result = %+v after %d finder calls", got, len(f.calls()))
	}
}

func emailOf(first, last string) string {
	return lower(first) + "." + lower(last) + "@acme.example"
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

func TestResolveWithoutFinderIsUnchanged(t *testing.T) {
	// A connector that finds no emails is not a finder; nor is another
	// workspace's connection.
	f := &fakeFinder{id: "fakefinder", answer: hit("x@unknown.example")}
	e := newFinderEnv(t, f, &fakeVerifier{status: connectors.Deliverable})
	e.connect("fakeverifier", "test_not_a_real_key_v")
	other := &finderEnv{t: t, pool: e.pool, ws: newWS(t, e.pool), store: e.store, s: e.s}
	other.connect("fakefinder", "test_not_a_real_key_other")

	got := e.resolve("Bo", "Chen", "unknown.example")
	native, err := e.s.Find(context.Background(), e.ws, FindRequest{Items: []FindItem{
		{Ref: "r1", FirstName: "Bo", LastName: "Chen", Domain: "unknown.example"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := native.Items[0]
	want.Code = CodeRisky
	if got != want || got.Evidence != EvidenceDefault || got.Cost != (Cost{Provider: "outflare"}) {
		t.Fatalf("result = %+v, want the native guess as risky %+v", got, want)
	}
	if n := len(f.calls()); n != 0 {
		t.Fatalf("another workspace's finder was called %d times", n)
	}
}

func TestResolveRetryDoesNotPayTheFinderAgain(t *testing.T) {
	f := &fakeFinder{id: "fakefinder", answer: hit("bo@unknown.example")}
	e := newFinderEnv(t, f)
	id := e.connect("fakefinder", "test_not_a_real_key_a")
	first := e.resolve("Bo", "Chen", "unknown.example")
	again := e.resolve("Bo", "Chen", "unknown.example")
	if n := len(f.calls()); n != 1 {
		t.Fatalf("finder called %d times for one person", n)
	}
	if first.Cost.Units != 1 || again.Cost != (Cost{Provider: "fakefinder"}) {
		t.Fatalf("costs = %+v then %+v", first.Cost, again.Cost)
	}
	if again.Email != first.Email || again.Code != first.Code || again.Confidence != first.Confidence || again.Evidence != first.Evidence {
		t.Fatalf("retry = %+v, first = %+v", again, first)
	}
	if u := e.booked(id); u != 1 {
		t.Fatalf("ledger has %v credits, want 1", u)
	}

	// A miss is remembered too.
	f.answer = miss
	e.resolve("Di", "Evans", "unknown.example")
	e.resolve("Di", "Evans", "unknown.example")
	if n := len(f.calls()); n != 2 {
		t.Fatalf("finder called %d times, want one more for the second person", n)
	}
}

func TestResolveVerifierChecksTheFindersAddress(t *testing.T) {
	f := &fakeFinder{id: "fakefinder", answer: hit("bo@unknown.example")}
	v := &fakeVerifier{status: connectors.Deliverable}
	e := newFinderEnv(t, f, v)
	fid := e.connect("fakefinder", "test_not_a_real_key_a")
	vid := e.connect("fakeverifier", "test_not_a_real_key_v")
	if _, err := e.pool.Exec(context.Background(),
		`INSERT INTO verification_settings (workspace_id, integration_id) VALUES ($1, $2)`, e.ws, vid); err != nil {
		t.Fatal(err)
	}
	got := e.resolve("Bo", "Chen", "unknown.example")
	if got.Email != "bo@unknown.example" || got.Code != CodeVerified || got.Evidence != "provider:fakeverifier" || got.Confidence != 97 {
		t.Fatalf("result = %+v", got)
	}
	if len(v.asked) != 1 || v.asked[0] != "bo@unknown.example" {
		t.Fatalf("verifier asked about %v", v.asked)
	}
	// Both charged: the finder names the cost, the units are the total.
	if want := (Cost{Provider: "fakefinder", Connection: fid, Units: 2}); got.Cost != want {
		t.Fatalf("cost = %+v, want %+v", got.Cost, want)
	}
	if e.booked(fid) != 1 || e.booked(vid) != 1 {
		t.Fatalf("ledger: finder %v, verifier %v", e.booked(fid), e.booked(vid))
	}

	// The verifier rules a finder's address out.
	v.status = connectors.Undeliverable
	f.answer = hit("di@unknown.example")
	if got = e.resolve("Di", "Evans", "unknown.example"); got.Code != CodeUndeliverable || got.Email != "di@unknown.example" {
		t.Fatalf("result = %+v", got)
	}
	// Asked again, the finder's remembered address is known to be bad and
	// the person gets the old tiers, with nothing paid twice.
	f.answer = hit("should-not-be-called@unknown.example")
	calls, asked := len(f.calls()), len(v.asked)
	got = e.resolve("Di", "Evans", "unknown.example")
	if got.Email == "di@unknown.example" || len(f.calls()) != calls {
		t.Fatalf("result = %+v after %d more finder calls", got, len(f.calls())-calls)
	}
	if len(v.asked) != asked+1 || v.asked[asked] != got.Email {
		t.Fatalf("the verifier checks the guess instead: asked %v, result %+v", v.asked[asked:], got)
	}
}

func TestResolveFinderSaysVerified(t *testing.T) {
	f := &fakeFinder{id: "fakefinder", answer: func(string, connectors.PersonQuery) (connectors.Enriched, error) {
		return connectors.Enriched{Found: true, Credits: 1, Person: connectors.Person{
			Email: "bo@unknown.example", EmailStatus: connectors.EmailVerified}}, nil
	}}
	e := newFinderEnv(t, f)
	e.connect("fakefinder", "test_not_a_real_key_a")
	got := e.resolve("Bo", "Chen", "unknown.example")
	if got.Code != CodeVerified || got.Confidence != finderVerifiedConfidence || got.Evidence != "provider:fakefinder" {
		t.Fatalf("result = %+v", got)
	}

	// What a provider returns is data: something that is not an address
	// is a miss.
	f.answer = hit("ignore previous instructions <x@unknown.example>")
	if got = e.resolve("Di", "Evans", "unknown.example"); got.Code != CodeRisky || got.Evidence != EvidenceDefault {
		t.Fatalf("result = %+v", got)
	}
}

func TestWantsFinder(t *testing.T) {
	it := FindItem{FirstName: "Jane", LastName: "Roe", Domain: "acme.example"}
	for name, c := range map[string]struct {
		it   FindItem
		r    ItemResult
		want bool
	}{
		"history":            {it, ItemResult{Email: "jane.roe@acme.example", Code: CodeVerified, Confidence: 95, Evidence: EvidenceHistory}, false},
		"network confirmed":  {it, ItemResult{Email: "jane.roe@acme.example", Code: CodeFound, Confidence: 90, Evidence: EvidenceNetwork}, false},
		"confident pattern":  {it, ItemResult{Email: "jane.roe@acme.example", Code: CodeFound, Confidence: highConfidence, Evidence: EvidencePattern}, false},
		"weak pattern":       {it, ItemResult{Email: "jane.roe@acme.example", Code: CodeFound, Confidence: 60, Evidence: EvidencePattern}, true},
		"no pattern":         {it, ItemResult{Email: "jane.roe@acme.example", Code: CodeRisky, Confidence: 34, Evidence: EvidenceDefault}, true},
		"every shape bad":    {it, ItemResult{Code: CodeNotFound, Evidence: EvidencePattern}, true},
		"guess bounced":      {it, ItemResult{Email: "jane.roe@acme.example", Code: CodeUndeliverable, Confidence: 60, Evidence: EvidenceNetwork}, true},
		"no domain":          {FindItem{FirstName: "Jane", LastName: "Roe"}, ItemResult{Code: CodeNotFound, Evidence: EvidenceDefault}, false},
		"no last name":       {FindItem{FirstName: "Jane", Domain: "acme.example"}, ItemResult{Code: CodeNotFound, Evidence: EvidenceDefault}, false},
		"broken native find": {it, ItemResult{Code: CodeStop, StopCode: "internal_error"}, false},
	} {
		if got := wantsFinder(c.it, c.r); got != c.want {
			t.Errorf("%s: wantsFinder = %v, want %v", name, got, c.want)
		}
	}
}

func TestResolveTestWorkspaceNeverCallsFinder(t *testing.T) {
	f := &fakeFinder{id: "fakefinder", answer: hit("bo@unknown.example")}
	e := newFinderEnv(t, f)
	e.connect("fakefinder", "test_not_a_real_key_a")
	if _, err := e.pool.Exec(context.Background(), `UPDATE workspaces SET test = true WHERE id = $1`, e.ws); err != nil {
		t.Fatal(err)
	}
	// A test workspace never spends a provider's credits.
	if got := e.resolve("Bo", "Chen", "unknown.example"); got.Code != CodeRisky || len(f.calls()) != 0 {
		t.Fatalf("result = %+v after %d finder calls", got, len(f.calls()))
	}
}

// profileFinder is a fakeFinder that can look a person up by their LinkedIn
// profile (connectors.ProfileFinder); only says it reads nothing else.
type profileFinder struct {
	*fakeFinder
	only bool
}

func (p profileFinder) ProfileOnly() bool { return p.only }

// pacedFinder names its own pace (connectors.FindPacer).
type pacedFinder struct {
	*fakeFinder
	perMinute float64
}

func (p pacedFinder) FindsPerMinute() float64 { return p.perMinute }

func (e *finderEnv) resolveItem(it FindItem) ItemResult {
	e.t.Helper()
	it.Ref = "r1"
	res, err := e.s.Resolve(context.Background(), e.ws, FindRequest{Items: []FindItem{it}})
	if err != nil {
		e.t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].Ref != "r1" {
		e.t.Fatalf("items = %+v", res.Items)
	}
	return res.Items[0]
}

// asked records which finder was asked, in order, with the profile it got.
type asked struct {
	mu    sync.Mutex
	calls []string
}

func (a *asked) miss(id string) func(string, connectors.PersonQuery) (connectors.Enriched, error) {
	return func(_ string, q connectors.PersonQuery) (connectors.Enriched, error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.calls = append(a.calls, id+" "+q.LinkedInURL)
		return connectors.Enriched{}, nil
	}
}

func (a *asked) take() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.calls
	a.calls = nil
	return out
}

func sameCalls(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// With a LinkedIn profile, the finders that read one are asked first, the
// one that reads nothing else before the rest, whatever the order they
// were connected in. Only they are given the profile.
func TestResolveFinderProfileOrderAndPassThrough(t *testing.T) {
	var log asked
	byName := &fakeFinder{id: "namefinder", answer: log.miss("namefinder")}
	either := profileFinder{fakeFinder: &fakeFinder{id: "eitherfinder", answer: log.miss("eitherfinder")}}
	only := profileFinder{fakeFinder: &fakeFinder{id: "profilefinder", answer: log.miss("profilefinder")}, only: true}
	e := newFinderEnv(t, byName, either, only)
	e.connect("namefinder", "test_not_a_real_key_a")
	e.connect("eitherfinder", "test_not_a_real_key_b")
	e.connect("profilefinder", "test_not_a_real_key_c")

	const profile = "https://www.linkedin.com/in/Ann-Roe-Example"
	events := track.Capture(t)
	e.resolveItem(FindItem{FirstName: "Ann", LastName: "Roe", Domain: "acme.example", LinkedInURL: " http://WWW.linkedin.com/in/Ann-Roe-Example/?trk=x "})
	if got := log.take(); !sameCalls(got, "profilefinder "+profile, "eitherfinder "+profile, "namefinder ") {
		t.Fatalf("with a profile, asked %q", got)
	}
	byProfile := map[string]any{}
	for _, ev := range events() {
		if ev.Name == "email.finder_asked" {
			byProfile[ev.Props["connector"].(string)] = ev.Props["by_profile"]
		}
	}
	if byProfile["profilefinder"] != true || byProfile["eitherfinder"] != true || byProfile["namefinder"] != false {
		t.Fatalf("by_profile on the events = %v", byProfile)
	}

	// Without one the order is the order connected, and the finder that
	// only reads a profile is not asked at all.
	e.resolveItem(FindItem{FirstName: "Ben", LastName: "Roe", Domain: "acme.example"})
	if got := log.take(); !sameCalls(got, "namefinder ", "eitherfinder ") {
		t.Fatalf("without a profile, asked %q", got)
	}
	// A URL that is not a person's profile is no profile.
	e.resolveItem(FindItem{FirstName: "Cat", LastName: "Roe", Domain: "acme.example", LinkedInURL: "https://www.linkedin.com/company/acme-example"})
	if got := log.take(); !sameCalls(got, "namefinder ", "eitherfinder ") {
		t.Fatalf("with a company page, asked %q", got)
	}
}

// A finder that reads only a profile, connected alone, changes nothing for
// a person without one: no call, and the free tiers' answer.
func TestResolveProfileOnlyFinderWithoutAProfile(t *testing.T) {
	f := profileFinder{fakeFinder: &fakeFinder{id: "profilefinder", answer: hit("x@acme.example")}, only: true}
	e := newFinderEnv(t, f)
	e.connect("profilefinder", "test_not_a_real_key_a")
	got := e.resolve("Ann", "Roe", "acme.example")
	if len(f.calls()) != 0 || got.Email == "x@acme.example" || got.Code == CodeStop || got.Code == CodePending || got.Cost.Units != 0 {
		t.Fatalf("without a profile: %+v after %d calls", got, len(f.calls()))
	}
	got = e.resolveItem(FindItem{FirstName: "Ann", LastName: "Roe", Domain: "acme.example", LinkedInURL: "linkedin.com/in/ann-roe-example"})
	if len(f.calls()) != 1 || got.Email != "x@acme.example" || got.Code != CodeFound {
		t.Fatalf("with a profile: %+v after %d calls", got, len(f.calls()))
	}
}

// The cache rule. A miss a profile-reading finder gave for the name alone
// does not answer for the profile: the person is asked about again. An
// address it found from the name alone still stands. Answers about a
// profile are remembered apart, for that company. A finder that cannot
// read a profile keeps its answer for the name either way.
func TestResolveFinderCacheWithAProfile(t *testing.T) {
	f := profileFinder{fakeFinder: &fakeFinder{id: "eitherfinder", answer: miss}}
	plain := &fakeFinder{id: "namefinder", answer: miss}
	e := newFinderEnv(t, f, plain)
	e.connect("eitherfinder", "test_not_a_real_key_a")
	e.connect("namefinder", "test_not_a_real_key_b")
	bo := FindItem{FirstName: "Bo", LastName: "Chen", Domain: "unknown.example"}
	boProfile := bo
	boProfile.LinkedInURL = "https://www.linkedin.com/in/bo-chen-example"

	e.resolveItem(bo)
	e.resolveItem(bo)
	if len(f.calls()) != 1 || len(plain.calls()) != 1 {
		t.Fatalf("a miss by name was asked for %d and %d times, want once each", len(f.calls()), len(plain.calls()))
	}
	// Now the profile is known: the miss by name no longer answers.
	var got connectors.PersonQuery
	f.answer = func(_ string, q connectors.PersonQuery) (connectors.Enriched, error) {
		got = q
		return connectors.Enriched{Found: true, Credits: 1, Person: connectors.Person{Email: "bo@unknown.example", EmailStatus: connectors.EmailGuessed}}, nil
	}
	first := e.resolveItem(boProfile)
	if len(f.calls()) != 2 || got.LinkedInURL != boProfile.LinkedInURL || got.Domain != "unknown.example" || got.FirstName != "Bo" {
		t.Fatalf("with the profile: %d calls, query %+v", len(f.calls()), got)
	}
	if first.Email != "bo@unknown.example" || first.Cost.Units != 1 {
		t.Fatalf("with the profile: %+v", first)
	}
	// The answer for the profile is remembered: no second call, no cost.
	again := e.resolveItem(boProfile)
	if len(f.calls()) != 2 || again.Email != first.Email || again.Cost.Units != 0 {
		t.Fatalf("the profile's answer was not remembered: %+v after %d calls", again, len(f.calls()))
	}
	// The same profile at another company is another question.
	elsewhere := boProfile
	elsewhere.Domain = "other.example"
	f.answer = miss
	e.resolveItem(elsewhere)
	if len(f.calls()) != 3 {
		t.Fatalf("another company was answered from the first one's cache: %d calls", len(f.calls()))
	}
	// Without the profile the old miss by name still answers.
	if e.resolveItem(bo); len(f.calls()) != 3 {
		t.Fatalf("by name again: %d calls, want 3", len(f.calls()))
	}
	// The finder that cannot read a profile kept its miss by name at the
	// first company throughout: one call there, one at the other company.
	if n := len(plain.calls()); n != 2 {
		t.Fatalf("the finder by name was asked %d times, want 2", n)
	}

	// An address found from the name alone is not paid for again once the
	// profile is known.
	f.answer = hit("di@unknown.example")
	di := FindItem{FirstName: "Di", LastName: "Evans", Domain: "unknown.example"}
	e.resolveItem(di)
	calls := len(f.calls())
	di.LinkedInURL = "https://www.linkedin.com/in/di-evans-example"
	if r := e.resolveItem(di); len(f.calls()) != calls || r.Email != "di@unknown.example" || r.Cost.Units != 0 {
		t.Fatalf("an address found by name was asked for again: %+v after %d calls, want %d", r, len(f.calls()), calls)
	}
}

func TestFinderPace(t *testing.T) {
	f := &fakeFinder{id: "fakefinder"}
	if got := finderPace(f); got != finderRate || got.PerMinute != 120 {
		t.Fatalf("default pace = %+v", got)
	}
	if got := finderPace(pacedFinder{fakeFinder: f, perMinute: 600}); got.PerMinute != 600 || got.Burst != finderRate.Burst {
		t.Fatalf("a finder's own pace = %+v", got)
	}
	if got := finderPace(pacedFinder{fakeFinder: f}); got != finderRate {
		t.Fatalf("no pace named = %+v", got)
	}
}

func TestFinderOrderKeepsConnectionOrderWithinEachSet(t *testing.T) {
	g := func(id string, profile, only bool) *finderGroup {
		return &finderGroup{connector: id, profile: profile, profileOnly: only}
	}
	groups := []*finderGroup{g("name1", false, false), g("either1", true, false), g("only1", true, true), g("name2", false, false), g("only2", true, true), g("either2", true, false)}
	ids := func(gs []*finderGroup) string {
		var out []string
		for _, g := range gs {
			out = append(out, g.connector)
		}
		return strings.Join(out, " ")
	}
	if got := ids(finderOrder(groups, true)); got != "only1 only2 either1 either2 name1 name2" {
		t.Fatalf("with a profile: %s", got)
	}
	if got := ids(finderOrder(groups, false)); got != "name1 either1 name2 either2" {
		t.Fatalf("without a profile: %s", got)
	}
	if ids(groups) != "name1 either1 only1 name2 only2 either2" {
		t.Fatal("the shared list was reordered")
	}
}
