package db_test

import (
	"bufio"
	"context"
	"embed"
	"slices"
	"strings"
	"testing"
)

//go:embed querytables/*.txt
var queryTableLists embed.FS

func queryTableList(t *testing.T, name string) []string {
	t.Helper()
	f, err := queryTableLists.Open("querytables/" + name)
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

// Agents can see all of a brand's data (AGENTS.md, "Data: agents can see
// all of it"): every table that holds brand data is read by a view in the
// query schema, unless querytables/private.txt says why it never is, or it
// is still in querytables/pending.txt, which only shrinks.
func TestEveryBrandTableIsQueryable(t *testing.T) {
	pool := testPool(t)
	private, pending := queryTableList(t, "private.txt"), queryTableList(t, "pending.txt")
	known := knownTables(t)
	ctx := context.Background()
	read := map[string]bool{}
	rows, err := pool.Query(ctx, `SELECT DISTINCT table_name FROM information_schema.view_table_usage
		WHERE view_schema = 'query' AND table_schema = 'public'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		read[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, name := range private {
		if slices.Contains(pending, name) {
			t.Errorf("%s is in both querytables/private.txt and pending.txt", name)
		}
		if read[name] {
			t.Errorf("%s is in querytables/private.txt, but a query view reads it", name)
		}
	}
	// A pending table a view reads is not an error: this test also runs the
	// previous version against a newer schema, where the next step has
	// added views. The step that adds a view takes the table off the list.
	branded, err := pool.Query(ctx, `SELECT DISTINCT c.table_name FROM information_schema.columns c
		JOIN information_schema.tables x ON x.table_schema = c.table_schema AND x.table_name = c.table_name AND x.table_type = 'BASE TABLE'
		WHERE c.table_schema = 'public' AND c.column_name = 'brand_id' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer branded.Close()
	for branded.Next() {
		var name string
		if err := branded.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if !known[name] {
			continue // made by a newer version (see knownTables)
		}
		if !read[name] && !slices.Contains(private, name) && !slices.Contains(pending, name) {
			t.Errorf("%s holds brand data that agents can't read: add a view in the query schema with its catalog entry "+
				"(engine/internal/api/query_catalog.go), or list it in engine/internal/db/querytables/private.txt with the reason", name)
		}
	}
	if err := branded.Err(); err != nil {
		t.Fatal(err)
	}
}

// pending.txt only names tables that exist, so it can only shrink.
func TestQueryTablePendingIsReal(t *testing.T) {
	known := knownTables(t)
	for _, name := range append(queryTableList(t, "pending.txt"), queryTableList(t, "private.txt")...) {
		if !known[name] {
			t.Errorf("%s is listed in querytables/ but no migration makes it", name)
		}
	}
}

// A query for one brand reads that brand alone (AGENTS.md, "Brands own
// their data"): every query view keeps to the brand in the query's
// query_scopes row. A view without that filter shows a brand its sister
// brands' rows.
func TestEveryQueryViewKeepsToOneBrand(t *testing.T) {
	pool := testPool(t)
	rows, err := pool.Query(context.Background(), `SELECT viewname FROM pg_views
		WHERE schemaname = 'query' AND definition !~ 's\.brand IS NULL' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		t.Errorf("query.%s doesn't keep to the query's brand: add AND (s.brand IS NULL OR <row>.brand_id = s.brand) to its WHERE", name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// Query views leave out secrets (AGENTS.md, "Data: agents can see all of
// it"): nothing sealed, no hash of a secret and no binary column. The one
// name allowed is crawl_pages.content_hash, the fingerprint of a page's
// public text.
func TestNoQueryViewShowsSealedColumns(t *testing.T) {
	pool := testPool(t)
	rows, err := pool.Query(context.Background(), `SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = 'query'
		  AND (data_type = 'bytea' OR column_name ~ '(_sealed|_hash|_secret|password|_token)$' OR column_name ~ '^(secret|password|token_hash)')
		  AND (table_name, column_name) NOT IN (('crawl_pages', 'content_hash'))
		ORDER BY 1, 2`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatal(err)
		}
		t.Errorf("query.%s shows %s, which looks sealed or secret: leave it out of the view", table, column)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
