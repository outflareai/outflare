// Package db owns the Postgres connection pool and schema migrations.
package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationLockID is an arbitrary constant used as a Postgres advisory lock so
// that only one process runs migrations at a time.
const migrationLockID = 7_302_215_001

// Connect opens a pool and verifies the database is reachable.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// Migrate applies every embedded migration that has not run yet, in filename
// order, each in its own transaction. It returns the names it applied.
func Migrate(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return nil, fmt.Errorf("take migration lock: %w", err)
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationLockID) //nolint:errcheck

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name       text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}

	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)

	var applied []string
	for _, path := range names {
		name := strings.TrimPrefix(path, "migrations/")
		var exists bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`, name).Scan(&exists); err != nil {
			return applied, err
		}
		if exists {
			continue
		}
		sql, err := migrationFS.ReadFile(path)
		if err != nil {
			return applied, err
		}
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, name)
			return err
		})
		if err != nil {
			return applied, fmt.Errorf("apply %s: %w", name, err)
		}
		applied = append(applied, name)
	}
	return applied, nil
}

var madeTable = regexp.MustCompile(`(?i)(?:CREATE\s+(?:OR\s+REPLACE\s+)?(?:UNLOGGED\s+|MATERIALIZED\s+)?(?:TABLE|VIEW)\s+(?:IF\s+NOT\s+EXISTS\s+)?|RENAME\s+TO\s+)"?(\w+)`)

// KnownTables are the tables and views this version's own migrations create
// or rename to. A test that walks the live schema uses it to leave alone the
// tables a newer version added: CI also runs the previous version's tests
// on the new schema (the rollback check), and that version's lists and
// seeders can't know a table made after it. The newer version's own run
// checks it.
func KnownTables() (map[string]bool, error) {
	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, name := range names {
		b, err := migrationFS.ReadFile(name)
		if err != nil {
			return nil, err
		}
		for _, m := range madeTable.FindAllStringSubmatch(string(b), -1) {
			known[strings.ToLower(m[1])] = true
		}
	}
	return known, nil
}
