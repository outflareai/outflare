package api_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/api"
)

func TestDeleteUser(t *testing.T) {
	e := freshEnv(t)
	acc := &api.Accounts{Pool: e.pool, PublicURL: "https://app.example.com", Mailer: e.outbox, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx := context.Background()
	signup := func(email, ws string) (string, string) {
		t.Helper()
		in := api.NewAccount{Email: email, Name: strings.Split(email, "@")[0], Password: "correct horse battery", WorkspaceName: ws}
		u, w, err := acc.CreateAccount(ctx, nil, httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil), in)
		if err != nil {
			t.Fatal(err)
		}
		return u, w
	}
	ada, solo := signup("ada@example.com", "Ada solo")
	bob, _ := signup("bob@example.com", "Bob's")
	cyd, _ := signup("cyd@example.com", "Cyd's")
	// Ada also owns a workspace shared with Bob (operator) and Cyd (admin),
	// and made a sequence there.
	var shared, adaActor string
	if err := e.pool.QueryRow(ctx, `INSERT INTO workspaces (name, slug) VALUES ('Shared', 'shared') RETURNING id`).Scan(&shared); err != nil {
		t.Fatal(err)
	}
	e.pool.QueryRow(ctx, `SELECT actor_id FROM users WHERE id = $1`, ada).Scan(&adaActor) //nolint:errcheck
	for _, m := range []struct{ user, role string }{{ada, "owner"}, {bob, "operator"}, {cyd, "admin"}} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO memberships (workspace_id, user_id, role) VALUES ($1, $2, $3)`, shared, m.user, m.role); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO sequences (workspace_id, name, created_by) VALUES ($1, 'Q4', $2)`, shared, adaActor); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO leads (workspace_id, email) VALUES ($1, 'lead@acme.test')`, solo); err != nil {
		t.Fatal(err)
	}

	var plan api.UserDeletion
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) (err error) {
		plan, err = acc.PlanUserDeletion(ctx, tx, ada)
		return err
	})
	if err != nil || len(plan.Deleted) != 1 || plan.Deleted[0].ID != solo || len(plan.Kept) != 1 || plan.Kept[0].NewOwner != "cyd@example.com" {
		t.Fatalf("plan: %+v %v", plan, err)
	}

	err = pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) (err error) {
		plan, err = acc.DeleteUser(ctx, tx, ada)
		return err
	})
	if err != nil || len(plan.Deleted) != 1 {
		t.Fatalf("delete: %+v %v", plan, err)
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM users WHERE id = $1 OR lower(email) = 'ada@example.com'`, ada); n != 0 {
		t.Fatalf("user left: %d", n)
	}
	if n := count(`SELECT count(*) FROM workspaces WHERE id = $1`, solo) + count(`SELECT count(*) FROM leads WHERE workspace_id = $1`, solo); n != 0 {
		t.Fatalf("solo workspace left: %d", n)
	}
	if n := count(`SELECT count(*) FROM sessions s WHERE NOT EXISTS (SELECT 1 FROM users u WHERE u.id = s.user_id)`); n != 0 {
		t.Fatalf("sessions left: %d", n)
	}
	if n := count(`SELECT count(*) FROM memberships WHERE workspace_id = $1 AND user_id = $2 AND role = 'owner'`, shared, cyd); n != 1 {
		t.Fatal("cyd (admin) should own the shared workspace")
	}
	if n := count(`SELECT count(*) FROM memberships WHERE workspace_id = $1`, shared); n != 2 {
		t.Fatalf("shared members: %d", n)
	}
	if n := count(`SELECT count(*) FROM sequences WHERE workspace_id = $1`, shared); n != 1 {
		t.Fatal("shared work must stay")
	}
	if n := count(`SELECT count(*) FROM actors WHERE id = $1 AND display_name <> 'Deleted user'`, adaActor); n != 0 {
		t.Fatal("actor keeps the person's name")
	}

	// Gone means gone: a second delete and a reset email both say so.
	err = pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error { _, err := acc.DeleteUser(ctx, tx, ada); return err })
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second delete: %v", err)
	}
	if _, err := acc.SendPasswordReset(ctx, ada); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("reset for deleted: %v", err)
	}
}

func TestSendPasswordResetForSomeone(t *testing.T) {
	e := freshEnv(t)
	acc := &api.Accounts{Pool: e.pool, PublicURL: "https://app.example.com/", Mailer: e.outbox, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx := context.Background()
	in := api.NewAccount{Email: "ada@example.com", Name: "Ada", Password: "correct horse battery", WorkspaceName: "Acme"}
	ada, _, err := acc.CreateAccount(ctx, nil, httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil), in)
	if err != nil {
		t.Fatal(err)
	}
	to, err := acc.SendPasswordReset(ctx, ada)
	if err != nil || to != "ada@example.com" {
		t.Fatalf("reset: %q %v", to, err)
	}
	last := e.outbox.sent[len(e.outbox.sent)-1]
	if last.To != "ada@example.com" || !strings.Contains(last.Text, "https://app.example.com/reset-password#") {
		t.Fatalf("email: %+v", last)
	}
}

// Deleting a workspace relies on every table that holds workspace data
// cascading from workspaces. A new table that doesn't would leave data
// behind after a deletion request, so this fails until it does.
func TestEveryWorkspaceTableCascades(t *testing.T) {
	e := freshEnv(t)
	rows, err := e.pool.Query(context.Background(), `
		SELECT c.table_name FROM information_schema.columns c
		JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name AND t.table_type = 'BASE TABLE'
		WHERE c.table_schema = 'public' AND c.column_name = 'workspace_id'
		AND NOT EXISTS (
			SELECT 1 FROM pg_constraint k JOIN pg_attribute a ON a.attrelid = k.conrelid AND a.attnum = ANY (k.conkey)
			WHERE k.contype = 'f' AND k.conrelid = format('public.%I', c.table_name)::regclass AND a.attname = 'workspace_id'
			  AND k.confrelid = 'workspaces'::regclass AND k.confdeltype = 'c')`)
	if err != nil {
		t.Fatal(err)
	}
	bad, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Fatalf("tables whose workspace_id doesn't cascade from workspaces: %v", bad)
	}
}
