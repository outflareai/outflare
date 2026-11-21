package verify

import (
	"context"
	"encoding/base64"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/db"
	"github.com/outflareai/outflare/engine/internal/secrets"
	"github.com/outflareai/outflare/engine/internal/track"
)

// gateway is a chosen verifier whose first check is always unavailable: it
// asks the fallback it is given and passes on what that says, or answers
// unknown without one (connectors.FallbackVerifier).
type gateway struct {
	mu            sync.Mutex
	plain, withFb int
}

func (*gateway) Definition() connectors.Definition {
	return connectors.Definition{ID: "testgateway", Name: "Test gateway", Category: connectors.CategoryVerification,
		Auth: connectors.AuthAPIKey, Capabilities: []string{connectors.CapVerify}}
}
func (*gateway) Test(context.Context, *connectors.Client, connectors.Creds) (connectors.Account, error) {
	return connectors.Account{}, nil
}
func (g *gateway) Verify(ctx context.Context, cl *connectors.Client, cr connectors.Creds, email string) (connectors.Verification, error) {
	g.mu.Lock()
	g.plain++
	g.mu.Unlock()
	return connectors.Verification{Email: email, Status: connectors.Unknown, Reason: "first check unavailable"}, nil
}
func (g *gateway) VerifyWith(ctx context.Context, cl *connectors.Client, cr connectors.Creds, email string, fallback func(context.Context, string) (connectors.Verification, error)) (connectors.Verification, error) {
	g.mu.Lock()
	g.withFb++
	g.mu.Unlock()
	if fb, err := fallback(ctx, email); err == nil {
		fb.Reason = "second verifier: " + fb.Status
		return fb, nil
	}
	return connectors.Verification{Email: email, Status: connectors.Unknown, Reason: "nobody answered"}, nil
}

// other is another connected verifier. It records the key each call
// carried.
type other struct {
	id   string
	mu   sync.Mutex
	keys []string
	err  error
}

func (o *other) Definition() connectors.Definition {
	return connectors.Definition{ID: o.id, Name: "Other verifier", Category: connectors.CategoryVerification,
		Auth: connectors.AuthAPIKey, Capabilities: []string{connectors.CapVerify}}
}
func (*other) Test(context.Context, *connectors.Client, connectors.Creds) (connectors.Account, error) {
	return connectors.Account{}, nil
}
func (o *other) Verify(_ context.Context, _ *connectors.Client, cr connectors.Creds, email string) (connectors.Verification, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.keys = append(o.keys, cr["api_key"])
	if o.err != nil {
		return connectors.Verification{}, o.err
	}
	return connectors.Verification{Email: email, Status: connectors.Deliverable, NotCatchAll: true}, nil
}
func (o *other) calls() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.keys...)
}

type fallbackEnv struct {
	t     *testing.T
	pool  *pgxpool.Pool
	ws    string
	store *connectors.Store
	s     *Service
}

func newFallbackEnv(t *testing.T, cs ...connectors.Connector) *fallbackEnv {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var ws string
	if err := pool.QueryRow(ctx, `INSERT INTO workspaces (name, slug) VALUES ('Verify', 'vf-' || substr(gen_random_uuid()::text, 1, 8)) RETURNING id::text`).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.NewBox(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	store := &connectors.Store{Pool: pool, Box: box, Registry: connectors.NewRegistry(cs, nil), Client: &connectors.Client{}}
	return &fallbackEnv{t: t, pool: pool, ws: ws, store: store, s: &Service{Pool: pool, Store: store, Concurrency: 1}}
}

func (e *fallbackEnv) connect(connector, key string) string {
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
	if _, err := e.pool.Exec(context.Background(), `UPDATE integrations SET created_at = clock_timestamp() WHERE id = $1`, id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *fallbackEnv) choose(id string) {
	e.t.Helper()
	err := pgx.BeginFunc(context.Background(), e.pool, func(tx pgx.Tx) error {
		set := Defaults
		set.IntegrationID = &id
		return SaveSettings(context.Background(), tx, e.ws, set)
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *fallbackEnv) booked(integration string) float64 {
	e.t.Helper()
	var units float64
	if err := e.pool.QueryRow(context.Background(), `SELECT coalesce(sum(units), 0)::float8 FROM spend_entries
		WHERE workspace_id = $1 AND integration_id = $2 AND category = 'verification'`, e.ws, integration).Scan(&units); err != nil {
		e.t.Fatal(err)
	}
	return units
}

// A chosen verifier whose first check can be unavailable is given the
// workspace's other connected verifier: the oldest one that is not the
// chosen one, on its own key, booked to its own connection.
func TestVerifyGivesTheChosenVerifierASecondOne(t *testing.T) {
	g, second, third := &gateway{}, &other{id: "othera"}, &other{id: "otherb"}
	e := newFallbackEnv(t, g, second, third)
	chosen := e.connect("testgateway", "test_not_a_real_key_g")
	secondID := e.connect("othera", "test_not_a_real_key_a")
	thirdID := e.connect("otherb", "test_not_a_real_key_b")
	e.choose(chosen)
	events := track.Capture(t)

	res, err := e.s.Verify(context.Background(), e.ws, []string{"Ana@acme.example", "ben@acme.example"}, false)
	if err != nil || len(res) != 2 {
		t.Fatalf("results %+v %v", res, err)
	}
	for _, r := range res {
		// The result is the chosen verifier's, whoever it asked.
		if r.Status != connectors.Deliverable || r.Reason != "second verifier: deliverable" || r.Provider != "testgateway" || r.IntegrationID == nil || *r.IntegrationID != chosen {
			t.Fatalf("result %+v", r)
		}
	}
	if g.withFb != 2 || g.plain != 0 {
		t.Fatalf("the chosen verifier was asked %d times with a fallback and %d without", g.withFb, g.plain)
	}
	if c := second.calls(); len(c) != 2 || c[0] != "test_not_a_real_key_a" || len(third.calls()) != 0 {
		t.Fatalf("the second verifier got %v, the third %v", c, third.calls())
	}
	// One credit an address on each connection that answered.
	if e.booked(chosen) != 2 || e.booked(secondID) != 2 || e.booked(thirdID) != 0 {
		t.Fatalf("booked %v, %v and %v", e.booked(chosen), e.booked(secondID), e.booked(thirdID))
	}
	var asked int
	for _, ev := range events() {
		if ev.Name == "email.fallback_verifier_asked" {
			asked++
			if ev.WorkspaceID != e.ws || ev.Props["integration_id"] != secondID || ev.Props["connector"] != "othera" || ev.Props["status"] != connectors.Deliverable {
				t.Fatalf("event %+v", ev)
			}
		}
	}
	if asked != 2 {
		t.Fatalf("%d email.fallback_verifier_asked events, want 2", asked)
	}
}

// A second verifier that refuses its key is not asked again in the batch,
// shows the error on its own connection, and books nothing; the chosen
// verifier carries on without it.
func TestVerifySecondVerifierRefuses(t *testing.T) {
	g, second := &gateway{}, &other{id: "othera", err: connectors.HTTPError("Other verifier", 401, "bad key")}
	e := newFallbackEnv(t, g, second)
	secondID := e.connect("othera", "test_not_a_real_key_a") // older than the chosen one
	chosen := e.connect("testgateway", "test_not_a_real_key_g")
	e.choose(chosen)
	events := track.Capture(t)

	res, err := e.s.Verify(context.Background(), e.ws, []string{"ana@acme.example", "ben@acme.example", "cy@acme.example"}, false)
	if err != nil || len(res) != 3 {
		t.Fatalf("results %+v %v", res, err)
	}
	for _, r := range res {
		if r.Status != connectors.Unknown || r.Reason != "nobody answered" || r.Provider != "testgateway" {
			t.Fatalf("result %+v", r)
		}
	}
	if len(second.calls()) != 1 || g.withFb != 3 {
		t.Fatalf("the second verifier was asked %d times, the chosen one %d", len(second.calls()), g.withFb)
	}
	if e.booked(secondID) != 0 || e.booked(chosen) != 3 {
		t.Fatalf("booked %v and %v", e.booked(secondID), e.booked(chosen))
	}
	var status string
	if err := e.pool.QueryRow(context.Background(), `SELECT status FROM integrations WHERE id = $1`, secondID).Scan(&status); err != nil || status != connectors.StatusError {
		t.Fatalf("second verifier's status = %q, %v", status, err)
	}
	if err := e.pool.QueryRow(context.Background(), `SELECT status FROM integrations WHERE id = $1`, chosen).Scan(&status); err != nil || status != connectors.StatusConnected {
		t.Fatalf("chosen verifier's status = %q, %v", status, err)
	}
	var failed int
	for _, ev := range events() {
		if ev.Name == "email.fallback_verifier_failed" {
			failed++
			if ev.ErrorCode != "connector_auth" || ev.Outcome != track.Failure || ev.Props["integration_id"] != secondID {
				t.Fatalf("event %+v", ev)
			}
		}
	}
	if failed != 1 {
		t.Fatalf("%d email.fallback_verifier_failed events, want 1", failed)
	}
	// It is left out from then on: the chosen verifier checks alone.
	if _, err := e.s.Verify(context.Background(), e.ws, []string{"dee@acme.example"}, false); err != nil || g.plain != 1 || len(second.calls()) != 1 {
		t.Fatalf("after the refusal: %v, %d plain checks, %d calls to the second", err, g.plain, len(second.calls()))
	}
}

// With no other verifier connected, or a chosen one that takes no
// fallback, verification is as it was.
func TestVerifyWithoutASecondVerifier(t *testing.T) {
	g, plain := &gateway{}, &other{id: "othera"}
	e := newFallbackEnv(t, g, plain)
	chosen := e.connect("testgateway", "test_not_a_real_key_g")
	e.choose(chosen)
	res, err := e.s.Verify(context.Background(), e.ws, []string{"ana@acme.example"}, false)
	if err != nil || len(res) != 1 || res[0].Reason != "first check unavailable" || g.plain != 1 || g.withFb != 0 {
		t.Fatalf("no other verifier: %+v %v (%d plain, %d with a fallback)", res, err, g.plain, g.withFb)
	}

	// The chosen one takes no fallback: the gateway is never its second.
	plainID := e.connect("othera", "test_not_a_real_key_a")
	e.choose(plainID)
	res, err = e.s.Verify(context.Background(), e.ws, []string{"ben@acme.example"}, false)
	if err != nil || len(res) != 1 || res[0].Status != connectors.Deliverable || res[0].Provider != "othera" || g.plain != 1 || g.withFb != 0 {
		t.Fatalf("a plain verifier: %+v %v", res, err)
	}
	if e.booked(plainID) != 1 || e.booked(chosen) != 1 {
		t.Fatalf("booked %v and %v", e.booked(plainID), e.booked(chosen))
	}

	// Another workspace's verifier is never the second one.
	e2 := newFallbackEnv(t, g, plain)
	chosen2 := e2.connect("testgateway", "test_not_a_real_key_g2")
	e2.choose(chosen2)
	before := len(plain.calls())
	if _, err := e2.s.Verify(context.Background(), e2.ws, []string{"cy@acme.example"}, false); err != nil || len(plain.calls()) != before || g.withFb != 0 {
		t.Fatalf("another workspace: %v, %d calls", err, len(plain.calls())-before)
	}
}

// costly is a verifier that says what each check cost, as Treg does.
type costly struct{}

func (costly) Definition() connectors.Definition {
	return connectors.Definition{ID: "testcostly", Name: "Test costly", Category: connectors.CategoryVerification,
		Auth: connectors.AuthAPIKey, Capabilities: []string{connectors.CapVerify}}
}
func (costly) Test(context.Context, *connectors.Client, connectors.Creds) (connectors.Account, error) {
	return connectors.Account{}, nil
}
func (costly) Verify(_ context.Context, _ *connectors.Client, _ connectors.Creds, email string) (connectors.Verification, error) {
	return connectors.Verification{Email: email, Status: connectors.Deliverable, CostMicros: 5250}, nil
}

// Checks whose verifier said what they cost are booked as that money, to
// the millionth, beside one credit an address.
func TestVerifyBooksTheReportedCost(t *testing.T) {
	e := newFallbackEnv(t, costly{})
	id := e.connect("testcostly", "test_not_a_real_key_c")
	e.choose(id)
	res, err := e.s.Verify(context.Background(), e.ws, []string{"ana@acme.example", "ben@acme.example"}, false)
	if err != nil || len(res) != 2 || res[0].CostMicros != 5250 {
		t.Fatalf("verify: %+v %v", res, err)
	}
	var units float64
	var cents, micros int64
	if err := e.pool.QueryRow(context.Background(), `SELECT units::float8, amount_cents, amount_micros FROM spend_entries
		WHERE workspace_id = $1 AND integration_id = $2`, e.ws, id).Scan(&units, &cents, &micros); err != nil {
		t.Fatal(err)
	}
	if units != 2 || micros != 10500 || cents != 2 {
		t.Fatalf("booked %v credits, %d cents, %d micros; want 2, 2 and 10500", units, cents, micros)
	}
}
