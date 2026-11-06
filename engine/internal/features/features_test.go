package features

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/db"
	"github.com/outflareai/outflare/engine/internal/track"
)

func testPool(t *testing.T) *pgxpool.Pool {
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
	return pool
}

func newWS(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var ws string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO workspaces (name, slug) VALUES ('Features', 'ft-' || substr(gen_random_uuid()::text, 1, 8)) RETURNING id::text`).
		Scan(&ws); err != nil {
		t.Fatal(err)
	}
	// Acme is made first, so it is the workspace's first brand.
	if _, err := pool.Exec(context.Background(), `INSERT INTO brands (workspace_id, name, handle, created_at) VALUES ($1, 'Acme', 'acme', now() - interval '1 minute'), ($1, 'Northpeak', 'northpeak', now())`, ws); err != nil {
		t.Fatal(err)
	}
	return ws
}

func brandOf(t *testing.T, pool *pgxpool.Pool, ws, handle string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `SELECT id::text FROM brands WHERE workspace_id = $1 AND handle = $2`, ws, handle).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// Brands are fully separate: each has its own switches, a workspace switch
// sets every brand's, and a brand added later starts with the first's.
func TestSwitchPerBrand(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ws := newWS(t, pool)
	acme, north := brandOf(t, pool, ws, "acme"), brandOf(t, pool, ws, "northpeak")
	switchBrand := func(brand string, on bool) bool {
		t.Helper()
		var changed bool
		if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			var err error
			changed, err = SetBrand(ctx, tx, ws, brand, SupportAutoBlocked, on, "")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return changed
	}
	if !switchBrand(north, true) || switchBrand(north, true) {
		t.Fatal("switching one brand on")
	}
	if on, _ := BrandEnabled(ctx, pool, north, SupportAutoBlocked); !on {
		t.Error("should be on for Northpeak")
	}
	if on, _ := BrandEnabled(ctx, pool, acme, SupportAutoBlocked); on {
		t.Error("on for Acme too")
	}
	if l, _ := BrandList(ctx, pool, acme); len(l) != 0 {
		t.Errorf("Acme's list = %v", l)
	}
	if on, _ := Enabled(ctx, pool, ws, SupportAutoBlocked); !on {
		t.Error("the workspace has it for a brand")
	}
	var audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND brand_id::text = $2 AND action = 'brand.feature_enabled'`, ws, north).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("brand audits = %d %v", audits, err)
	}
	// Someone in another workspace can't switch this one's brand.
	other := newWS(t, pool)
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		changed, err := SetBrand(ctx, tx, other, acme, SupportAutoBlocked, true, "")
		if changed {
			t.Error("switched another workspace's brand")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}

	set(t, pool, ws, AIAccounts, true)
	for _, b := range []string{acme, north} {
		if on, _ := BrandEnabled(ctx, pool, b, AIAccounts); !on {
			t.Errorf("workspace switch didn't reach brand %s", b)
		}
	}
	var later string
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO brands (workspace_id, name, handle) VALUES ($1, 'Later', 'later') RETURNING id::text`, ws).Scan(&later); err != nil {
			return err
		}
		return CopyToBrand(ctx, tx, ws, later)
	}); err != nil {
		t.Fatal(err)
	}
	if l, _ := BrandList(ctx, pool, later); !slices.Equal(l, []string{AIAccounts}) {
		t.Errorf("a later brand starts with %v, want the first brand's", l)
	}
}

func set(t *testing.T, pool *pgxpool.Pool, ws, feature string, on bool) bool {
	t.Helper()
	var changed bool
	err := pgx.BeginFunc(context.Background(), pool, func(tx pgx.Tx) error {
		var err error
		changed, err = Set(context.Background(), tx, ws, feature, on, "")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return changed
}

func TestCatalog(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range Catalog {
		if f.Name == "" || f.Description == "" || seen[f.Name] {
			t.Errorf("bad or repeated catalog entry %+v", f)
		}
		seen[f.Name] = true
	}
	if !Known(AIAccounts) || Known("nope") {
		t.Error("Known")
	}
}

func TestSwitch(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ws, other := newWS(t, pool), newWS(t, pool)
	events := track.Capture(t)

	if on, err := Enabled(ctx, pool, ws, AIAccounts); err != nil || on {
		t.Fatalf("off by default: %v %v", on, err)
	}
	if !set(t, pool, ws, AIAccounts, true) {
		t.Error("switching on should change it")
	}
	if set(t, pool, ws, AIAccounts, true) {
		t.Error("switching on again should change nothing")
	}
	if on, _ := Enabled(ctx, pool, ws, AIAccounts); !on {
		t.Error("should be on")
	}
	// Other workspaces don't get it.
	if on, _ := Enabled(ctx, pool, other, AIAccounts); on {
		t.Error("on in another workspace")
	}
	if list, err := List(ctx, pool, ws); err != nil || !slices.Equal(list, []string{AIAccounts}) {
		t.Errorf("List = %v %v", list, err)
	}
	if list, _ := List(ctx, pool, other); list == nil || len(list) != 0 {
		t.Errorf("List for other = %#v, want empty", list)
	}

	if !set(t, pool, ws, AIAccounts, false) || set(t, pool, ws, AIAccounts, false) {
		t.Error("switching off")
	}
	if on, _ := Enabled(ctx, pool, ws, AIAccounts); on {
		t.Error("should be off")
	}

	var audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action LIKE 'workspace.feature_%'`, ws).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 2 {
		t.Errorf("%d audit events, want 2 (one on, one off)", audits)
	}
	var got []string
	for _, e := range events() {
		got = append(got, e.Name)
	}
	if !slices.Contains(got, "workspace.feature_enabled") || !slices.Contains(got, "workspace.feature_disabled") {
		t.Errorf("events = %v", got)
	}
}

// A person switching it is named in the audit log (by their actor) and the
// event (by their user id).
func TestSwitchByPerson(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ws := newWS(t, pool)
	var user, actor string
	if err := pool.QueryRow(ctx, `WITH a AS (INSERT INTO actors (type, display_name) VALUES ('human', 'Ana') RETURNING id)
		INSERT INTO users (actor_id, email, name, password_hash) SELECT id, 'ana-' || substr(gen_random_uuid()::text, 1, 8) || '@acme.example', 'Ana', '' FROM a
		RETURNING id::text, actor_id::text`).Scan(&user, &actor); err != nil {
		t.Fatal(err)
	}
	events := track.Capture(t)
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		_, err := Set(ctx, tx, ws, AIAccounts, true, user)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var by, actorType string
	if err := pool.QueryRow(ctx, `SELECT actor_id::text, actor_type FROM audit_events WHERE workspace_id = $1 AND action = 'workspace.feature_enabled'`, ws).
		Scan(&by, &actorType); err != nil || by != actor || actorType != "human" {
		t.Fatalf("audit actor = %q %q %v, want %q human", by, actorType, err, actor)
	}
	var enabledBy string
	_ = pool.QueryRow(ctx, `SELECT enabled_by::text FROM workspace_features WHERE workspace_id = $1`, ws).Scan(&enabledBy)
	if enabledBy != user {
		t.Errorf("enabled_by = %q, want %q", enabledBy, user)
	}
	if got := events(); len(got) != 1 || got[0].UserID != user {
		t.Errorf("events = %+v", got)
	}
}

func TestUnknown(t *testing.T) {
	pool := testPool(t)
	ws := newWS(t, pool)
	err := pgx.BeginFunc(context.Background(), pool, func(tx pgx.Tx) error {
		_, err := Set(context.Background(), tx, ws, "made_up", true, "")
		return err
	})
	if !errors.Is(err, ErrUnknown) {
		t.Errorf("err = %v, want ErrUnknown", err)
	}
}
