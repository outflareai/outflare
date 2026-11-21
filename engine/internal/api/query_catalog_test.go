package api

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/db"
)

// Every query table and column is explained in the data catalog
// (AGENTS.md, "Data: agents can see all of it"): a new view or column comes
// with its description, by the naming conventions or in words.
func TestEveryQueryColumnIsDocumented(t *testing.T) {
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
	s := &Server{pool: pool, log: slog.New(slog.DiscardHandler)}
	cols, err := s.queryColumns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The database can be newer than this code: the previous version's tests
	// run on the next version's schema (the rollback check), where a later
	// migration may have added a column to a view. Only that later version
	// can describe it, so such a column isn't counted against this one.
	newer, err := schemaIsNewer(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	for _, qt := range queryTables {
		c, ok := queryCatalog[qt.Name]
		switch {
		case !ok:
			t.Errorf("query table %s has no entry in queryCatalog (query_catalog.go)", qt.Name)
			continue
		case !slices.Contains(dataCatalogAreas, c.Area):
			t.Errorf("query table %s: area %q is not one of dataCatalogAreas", qt.Name, c.Area)
		case c.Source == "":
			t.Errorf("query table %s: say where its rows come from (Source)", qt.Name)
		}
		if c.Time != "" && !slices.ContainsFunc(cols[qt.Name], func(x queryColumn) bool { return x.Name == c.Time }) {
			t.Errorf("query table %s: its time column %s is not one of its columns", qt.Name, c.Time)
		}
		for _, col := range cols[qt.Name] {
			if describeDataColumn(qt.Name, col).Description == "" {
				if newer {
					t.Logf("query table %s: column %s has no description here; a newer migration than this code made the schema", qt.Name, col.Name)
					continue
				}
				t.Errorf("query table %s: column %s has no description: add it to the table's Columns in query_catalog.go", qt.Name, col.Name)
			}
		}
		for name := range c.Columns {
			if !slices.ContainsFunc(cols[qt.Name], func(x queryColumn) bool { return x.Name == name }) {
				t.Errorf("query table %s: queryCatalog describes %s, which it doesn't have", qt.Name, name)
			}
		}
	}
	for name := range queryCatalog {
		if !slices.ContainsFunc(queryTables, func(x queryTable) bool { return x.Name == name }) {
			t.Errorf("queryCatalog describes %s, which is not a query table", name)
		}
	}
}

// schemaIsNewer reports whether the database has a migration applied that
// this version of the code doesn't have.
func schemaIsNewer(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	files, err := filepath.Glob("../db/migrations/*.sql")
	if err != nil {
		return false, err
	}
	ours := make([]string, len(files))
	for i, f := range files {
		ours[i] = filepath.Base(f)
	}
	var newer bool
	err = pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE NOT (name = ANY($1)))`, ours).Scan(&newer)
	return newer, err
}
