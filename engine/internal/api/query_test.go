package api_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/track"
)

func queryRows(t *testing.T, out map[string]any) [][]any {
	t.Helper()
	var rows [][]any
	for _, r := range out["rows"].([]any) {
		rows = append(rows, r.([]any))
	}
	return rows
}

func queryCode(out map[string]any) string {
	if e, ok := out["error"].(map[string]any); ok {
		return e["code"].(string)
	}
	return ""
}

// Query sees exactly what the caller sees through the API: its workspace,
// its brands and its scopes, and it can never change anything.
func TestQuery(t *testing.T) {
	got := track.Capture(t)
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws
	rp := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "website": "repocket.com"})["id"].(string)
	gn := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode", "website": "geonode.com"})["id"].(string)
	owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ada", "brand_id": rp,
		"identities": []map[string]any{{"namespace": "email", "value": "ada@acme.example"}}})
	owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Grace", "brand_id": gn})
	owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Shared"})
	owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme"})

	// Another workspace's records are never seen.
	other := owner.must(201, "POST", "/v1/workspaces", map[string]string{"name": "Other"})["id"].(string)
	owner.must(201, "POST", "/v1/workspaces/"+other+"/records", map[string]any{"type": "person", "name": "Elsewhere"})

	out := owner.must(200, "POST", base+"/query", map[string]any{"sql": "select name, email from people order by name;"})
	rows := queryRows(t, out)
	if len(rows) != 3 || rows[0][0] != "Ada" || rows[0][1] != "ada@acme.example" || rows[2][0] != "Shared" {
		t.Fatalf("people: %v", out)
	}
	if cols := out["columns"].([]any); cols[0].(map[string]any)["name"] != "name" || cols[0].(map[string]any)["type"] != "text" {
		t.Fatalf("columns: %v", cols)
	}
	out = owner.must(200, "POST", base+"/query", map[string]any{"sql": "select record_type, count(*) from records group by 1 order by 1"})
	if rows := queryRows(t, out); len(rows) != 2 || rows[0][0] != "account" || rows[1][1] != float64(3) {
		t.Fatalf("counts: %v", out)
	}

	// Limits.
	out = owner.must(200, "POST", base+"/query", map[string]any{"sql": "select * from people", "limit": 2})
	if out["row_count"] != float64(2) || out["truncated"] != true {
		t.Fatalf("limit: %v", out)
	}

	// A token kept to one brand sees only that brand's people: one made
	// without a brand belongs to the workspace's first brand.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Repocket agent", "scopes": []string{"customers.read", "leads.read"}, "brand": "repocket"})
	agent := &client{t: t, h: h, bearer: tok["token"].(string), header: map[string]string{"Outflare-Client": "mcp/0.1.0", "Outflare-Tool": "run_query"}}
	out = agent.must(200, "POST", base+"/query", map[string]any{"sql": "select name from people order by name"})
	if rows := queryRows(t, out); len(rows) != 1 || rows[0][0] != "Ada" {
		t.Fatalf("brand token: %v", out)
	}
	if code, out := agent.do("POST", base+"/query", map[string]any{"sql": "select count(*) from leads"}); code != 403 || !strings.Contains(out["error"].(map[string]any)["message"].(string), "no brand") {
		t.Fatalf("leads for a brand token: %d %v", code, out)
	}
	// Scopes: a table needs its own read scope.
	if code, out := agent.do("POST", base+"/query", map[string]any{"sql": "select * from deals"}); code != 403 || !strings.Contains(out["error"].(map[string]any)["message"].(string), "outcomes.read") {
		t.Fatalf("deals without outcomes.read: %d %v", code, out)
	}
	tables := agent.must(200, "GET", base+"/query/tables", nil)["data"].([]any)
	avail := map[string]bool{}
	for _, tb := range tables {
		m := tb.(map[string]any)
		avail[m["name"].(string)] = m["available"].(bool)
		if m["name"] == "people" && len(m["columns"].([]any)) == 0 {
			t.Fatalf("people has no columns: %v", m)
		}
	}
	if !avail["people"] || avail["deals"] || avail["leads"] {
		t.Fatalf("tables: %v", avail)
	}

	// Nothing else is reachable, and nothing can change.
	for _, tc := range []struct{ sql, want string }{
		{"select * from public.records", "permission denied"},
		{"select * from public.query_scopes", "permission denied"},
		{"select * from public.api_tokens", "permission denied"},
		{"select 1; delete from records", "multiple commands"},
		{"delete from records", "syntax error"},
		{"with d as (delete from records returning 1) select * from d", ""},
		{"select set_config('role', 'postgres', false)", "set_config"},
		{"select pg_sleep(30)", "pg_sleep"},
		{`select U&"\0073et_config"('role', 'postgres', false)`, "u&"},
		{"select pg_advisory_lock(1)", "pg_advisory_lock"},
		{"select nope from people", "does not exist"},
		{"select query from pg_stat_activity", "pg_stat_activity"},
		{"select query_to_xml('select 1', true, true, '')", "query_to_xml"},
		{"select * from people for update", "permission denied"},
	} {
		code, out := owner.do("POST", base+"/query", map[string]any{"sql": tc.sql})
		if code != 400 || queryCode(out) != "query_invalid" || !strings.Contains(strings.ToLower(out["error"].(map[string]any)["message"].(string)), strings.ToLower(tc.want)) {
			t.Fatalf("%s: %d %v", tc.sql, code, out)
		}
	}
	// The scope rows are gone once each query ends.
	pool, err := pgx.Connect(context.Background(), os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close(context.Background())
	var left int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM query_scopes`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("scopes left: %d %v", left, err)
	}
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM records`).Scan(&n); err != nil || n != 5 {
		t.Fatalf("records after the queries: %d %v", n, err)
	}

	var ran []track.Event
	for _, e := range got() {
		if e.Name == "query.ran" {
			ran = append(ran, e)
		}
	}
	if len(ran) < 4 || !slices.Contains(ran[0].Props["tables"].([]string), "people") {
		t.Fatalf("query.ran: %v", ran)
	}
	for _, e := range ran {
		if _, ok := e.Props["sql"]; ok {
			t.Fatalf("an event carries the SQL: %v", e)
		}
	}
	if ran[len(ran)-1].Surface == "" {
		t.Fatalf("no surface: %v", ran[len(ran)-1])
	}
}

// The tables Query lists are the views in the query schema.
func TestQueryTablesMatchSchema(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	listed := owner.must(200, "GET", "/v1/workspaces/"+ws+"/query/tables", nil)["data"].([]any)
	pool, err := pgx.Connect(context.Background(), os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close(context.Background())
	rows, _ := pool.Query(context.Background(), `SELECT table_name FROM information_schema.views WHERE table_schema = 'query' ORDER BY 1`)
	views, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tb := range listed {
		m := tb.(map[string]any)
		names = append(names, m["name"].(string))
		if len(m["columns"].([]any)) == 0 {
			t.Errorf("%s: listed but not in the query schema", m["name"])
		}
	}
	slices.Sort(names)
	// The database can be newer than this code: the previous version runs on
	// the next version's schema during a deploy and after a rollback. Compare
	// with the views this version's own migrations create, so a view from a
	// later migration isn't counted against it.
	views = slices.DeleteFunc(views, func(v string) bool { return !ownViews[v] })
	if !slices.Equal(names, views) {
		t.Fatalf("listed %v, schema has %v", names, views)
	}
}

var ownViews = func() map[string]bool {
	files, _ := filepath.Glob("../db/migrations/*.sql")
	// In file order, so a view a later migration drops is not counted.
	re := regexp.MustCompile(`(?i)(CREATE\s+(?:OR\s+REPLACE\s+)?|DROP\s+)VIEW\s+(?:IF\s+EXISTS\s+)?query\.(\w+)`)
	views := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			panic(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			views[m[2]] = !strings.HasPrefix(strings.ToUpper(m[1]), "DROP")
		}
	}
	return views
}()
