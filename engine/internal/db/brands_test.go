package db_test

import (
	"bufio"
	"context"
	"embed"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/db"
)

//go:embed brandtables/*.txt
var brandTables embed.FS

func tableList(t *testing.T, name string) []string {
	t.Helper()
	f, err := brandTables.Open("brandtables/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

// Brands own their data (plans/brands/README.md): every table a workspace
// owns carries brand_id, unless it is the team, the account or machinery
// (brandtables/workspace.txt), or brand data not moved yet
// (brandtables/pending.txt, which only shrinks).
func TestEveryWorkspaceTableHasABrand(t *testing.T) {
	pool := testPool(t)
	workspace, pending := tableList(t, "workspace.txt"), tableList(t, "pending.txt")
	for _, name := range workspace {
		if slices.Contains(pending, name) {
			t.Errorf("%s is in both workspace.txt and pending.txt", name)
		}
	}
	rows, err := pool.Query(context.Background(), `
		SELECT c.table_name, EXISTS (SELECT 1 FROM information_schema.columns b
			WHERE b.table_schema = c.table_schema AND b.table_name = c.table_name AND b.column_name = 'brand_id')
		FROM information_schema.columns c
		WHERE c.table_schema = 'public' AND c.column_name = 'workspace_id' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var branded bool
		if err := rows.Scan(&name, &branded); err != nil {
			t.Fatal(err)
		}
		// A pending table that has brand_id now is fine: this test also runs
		// the previous version against a newer schema (CI's migrations job),
		// where the next step has moved some. The step that moves a table
		// takes it off pending.txt.
		if !branded && !slices.Contains(workspace, name) && !slices.Contains(pending, name) {
			t.Errorf("%s has workspace_id but no brand_id: brand data carries brand_id (plans/brands/README.md); "+
				"only the team, the account and machinery are workspace-level (engine/internal/db/brandtables/workspace.txt)", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

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
