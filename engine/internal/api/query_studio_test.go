package api_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// postRaw sends a JSON POST and returns the whole answer, for CSV files.
func (c *client) postRaw(path string, body any) *httptest.ResponseRecorder {
	c.t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec
}

// Query Studio (plans/brands/README.md, "Data pages"): saved, versioned
// queries for one brand. customers.read runs them and downloads CSV;
// customers.manage saves and shares them; a private one is its owner's
// alone; and a saved query reads its own brand only, whatever its SQL says.
func TestQueryStudio(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	alpha := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Alpha", "handle": "alpha"})["id"].(string)
	bravo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Bravo", "handle": "bravo"})["id"].(string)
	a, b := "/v1/brands/"+alpha, "/v1/brands/"+bravo
	owner.must(201, "POST", a+"/records", map[string]any{"type": "person", "name": "Ana", "attributes": map[string]any{}})
	owner.must(201, "POST", a+"/records", map[string]any{"type": "person", "name": "=HYPERLINK(\"x\")"})
	owner.must(201, "POST", b+"/records", map[string]any{"type": "person", "name": "Ben"})

	op, _ := joinAs(t, owner, ws, "op@example.com", map[string]any{"role": "operator"})
	viewer, _ := joinAs(t, owner, ws, "viewer@example.com", map[string]any{"role": "viewer"})
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Bravo app", "scopes": []string{"customers.manage"}, "brand": "bravo"})
	bravoApp := &client{t: t, h: e.h, bearer: tok["token"].(string)}

	// Save: version 1, private to its owner.
	sql := "select name from people order by name"
	q := owner.must(201, "POST", a+"/queries", map[string]any{"name": "People by name", "sql": sql, "note": "first"})
	id := q["id"].(string)
	qp := a + "/queries/" + id
	if q["current_version"] != 1.0 || q["visibility"] != "private" || q["mine"] != true || q["can_edit"] != true || q["sql"] != sql || q["chart"] != nil || q["brand_id"] != alpha {
		t.Fatalf("saved: %v", q)
	}
	owner.must(400, "POST", a+"/queries", map[string]any{"name": "", "sql": sql})
	owner.must(400, "POST", a+"/queries", map[string]any{"name": "No SQL"})
	owner.must(400, "POST", a+"/queries", map[string]any{"name": "Bad chart", "sql": sql, "chart": map[string]any{"type": "pie", "x": "name", "y": []string{"n"}}})

	// Private: nobody else sees it, opens it or runs it.
	for _, c := range []*client{op, viewer} {
		if l := c.must(200, "GET", a+"/queries", nil)["data"].([]any); len(l) != 0 {
			t.Fatalf("another person sees a private query: %v", l)
		}
		c.must(404, "GET", qp, nil)
		c.must(404, "POST", qp+"/run", map[string]any{})
		c.must(404, "GET", qp+"/versions", nil)
	}
	op.must(404, "PATCH", qp, map[string]any{"name": "Mine now"})

	// Edit: a new version for SQL or chart, none for a rename.
	q = owner.must(200, "PATCH", qp, map[string]any{"name": "People", "description": "Everyone in Alpha"})
	if q["current_version"] != 1.0 || q["name"] != "People" {
		t.Fatalf("rename: %v", q)
	}
	sql2 := "select count(*) as n, (attributes->>'plan') as plan from people group by 2"
	chart := map[string]any{"type": "bar", "x": "plan", "y": []string{"n"}}
	q = owner.must(200, "PATCH", qp, map[string]any{"sql": sql2, "chart": chart, "note": "count by plan"})
	if q["current_version"] != 2.0 || q["sql"] != sql2 || q["chart"].(map[string]any)["type"] != "bar" {
		t.Fatalf("new version: %v", q)
	}
	q = owner.must(200, "PATCH", qp, map[string]any{"chart": nil})
	if q["current_version"] != 3.0 || q["chart"] != nil {
		t.Fatalf("chart removed: %v", q)
	}
	if q = owner.must(200, "PATCH", qp, map[string]any{"sql": sql2}); q["current_version"] != 3.0 {
		t.Fatalf("the same SQL made a version: %v", q)
	}
	vs := owner.must(200, "GET", qp+"/versions", nil)["data"].([]any)
	if len(vs) != 3 || vs[0].(map[string]any)["version"] != 3.0 || vs[0].(map[string]any)["current"] != true || vs[2].(map[string]any)["note"] != "first" {
		t.Fatalf("versions: %v", vs)
	}
	if v := owner.must(200, "GET", qp+"/versions/1", nil); v["sql"] != sql || v["current"] != false {
		t.Fatalf("version 1: %v", v)
	}
	owner.must(404, "GET", qp+"/versions/9", nil)
	owner.must(404, "GET", qp+"/versions/x", nil)

	// Restore: a new version from an old one.
	q = owner.must(200, "POST", qp+"/versions/1/restore", nil)
	if q["current_version"] != 4.0 || q["sql"] != sql {
		t.Fatalf("restored: %v", q)
	}
	if v := owner.must(200, "GET", qp+"/versions/4", nil); v["restored_from"] != 1.0 || v["note"] != "Restored version 1" {
		t.Fatalf("restored version: %v", v)
	}
	owner.must(409, "POST", qp+"/versions/4/restore", nil)

	// Run: the current version, or one asked for.
	run := owner.must(200, "POST", qp+"/run", nil)
	if run["version"] != 4.0 || run["saved_query_id"] != id || len(queryRows(t, run)) != 2 {
		t.Fatalf("run: %v", run)
	}
	if run = owner.must(200, "POST", qp+"/run", map[string]any{"version": 2}); run["version"] != 2.0 || run["columns"].([]any)[0].(map[string]any)["name"] != "n" {
		t.Fatalf("run version 2: %v", run)
	}
	owner.must(404, "POST", qp+"/run", map[string]any{"version": 7})
	owner.must(400, "POST", qp+"/run", map[string]any{"limit": 5000})

	// Share: only its owner shares it; then everyone who can query the
	// brand sees and runs it, and customers.manage edits it.
	op.must(404, "PATCH", qp, map[string]any{"visibility": "brand"})
	q = owner.must(200, "PATCH", qp, map[string]any{"visibility": "brand"})
	if q["visibility"] != "brand" || q["current_version"] != 4.0 {
		t.Fatalf("shared: %v", q)
	}
	if l := viewer.must(200, "GET", a+"/queries", nil)["data"].([]any); len(l) != 1 || l[0].(map[string]any)["mine"] != false || l[0].(map[string]any)["can_edit"] != false {
		t.Fatalf("viewer's list: %v", l)
	}
	viewer.must(200, "GET", qp, nil)
	viewer.must(200, "GET", qp+"/versions", nil)
	if run = viewer.must(200, "POST", qp+"/run", nil); len(queryRows(t, run)) != 2 {
		t.Fatalf("viewer's run: %v", run)
	}
	// customers.read runs and downloads, but doesn't save.
	viewer.must(403, "POST", a+"/queries", map[string]any{"name": "Mine", "sql": sql})
	viewer.must(403, "PATCH", qp, map[string]any{"name": "Viewer's"})
	viewer.must(403, "POST", qp+"/archive", nil)
	viewer.must(403, "POST", qp+"/versions/2/restore", nil)
	if op.must(200, "PATCH", qp, map[string]any{"sql": sql + " limit 1"})["current_version"] != 5.0 {
		t.Fatal("a shared query takes a new version from another editor")
	}
	op.must(403, "PATCH", qp, map[string]any{"visibility": "private"})
	if l := owner.must(200, "GET", a+"/queries?q=everyone", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("search: %v", l)
	}
	if l := owner.must(200, "GET", a+"/queries?q=nothing%25", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("search for nothing: %v", l)
	}
	if l := owner.must(200, "GET", a+"/queries?visibility=private", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("private only: %v", l)
	}

	// Another brand's caller gets 404 for all of it, and the query isn't
	// under another brand's route.
	for _, path := range []string{qp, qp + "/versions", qp + "/versions/1"} {
		bravoApp.must(404, "GET", path, nil)
		owner.must(404, "GET", strings.Replace(path, a, b, 1), nil)
	}
	bravoApp.must(404, "POST", qp+"/run", nil)
	bravoApp.must(404, "PATCH", qp, map[string]any{"name": "Bravo's"})
	bravoApp.must(404, "GET", a+"/queries", nil)
	owner.must(404, "POST", b+"/queries/"+id+"/run", nil)
	if l := owner.must(200, "GET", b+"/queries", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("bravo lists alpha's query: %v", l)
	}

	// A saved query reads its own brand only, whatever its SQL says.
	sneaky := owner.must(201, "POST", a+"/queries", map[string]any{"name": "Every brand",
		"sql": "select name from records where brand_id = '" + bravo + "' or brand_id <> '" + alpha + "' or brand_id is null", "visibility": "brand"})["id"].(string)
	if rows := queryRows(t, owner.must(200, "POST", a+"/queries/"+sneaky+"/run", nil)); len(rows) != 0 {
		t.Fatalf("a saved query read another brand: %v", rows)
	}
	// A token's private query belongs to the person who made the token.
	bq := bravoApp.must(201, "POST", b+"/queries", map[string]any{"name": "Bravo people", "sql": "select name from people"})
	if bq["mine"] != true || bq["visibility"] != "private" {
		t.Fatalf("token's query: %v", bq)
	}
	if l := owner.must(200, "GET", b+"/queries", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("the token's owner sees its private query: %v", l)
	}

	// CSV: the same rows, as a file, with formulas defused.
	rec := owner.postRaw(a+"/query/csv", map[string]any{"sql": "select name, 1.5::numeric as score, '{\"a\":1}'::jsonb as j, null as nothing from people order by length(name) desc"})
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") ||
		!strings.HasPrefix(rec.Header().Get("Content-Disposition"), `attachment; filename="outflare-query-`) ||
		rec.Header().Get("Outflare-Query-Rows") != "2" || rec.Header().Get("Outflare-Query-Truncated") != "false" {
		t.Fatalf("csv: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	recs, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(recs) != `[[name score j nothing] ['=HYPERLINK("x") 1.5 {"a":1} ] [Ana 1.5 {"a":1} ]]` {
		t.Fatalf("csv rows: %q", recs)
	}
	if rec = owner.postRaw(a+"/query/csv", map[string]any{"sql": "select name from people", "limit": 1}); rec.Header().Get("Outflare-Query-Truncated") != "true" {
		t.Fatalf("csv limit: %v", rec.Header())
	}
	if rec = owner.postRaw(a+"/query/csv", map[string]any{"sql": "select * from nowhere"}); rec.Code != 400 {
		t.Fatalf("bad csv: %d", rec.Code)
	}
	if rec = owner.postRaw(a+"/query/csv", map[string]any{"sql": "select 1", "limit": 10001}); rec.Code != 400 {
		t.Fatalf("csv over the cap: %d", rec.Code)
	}
	rec = viewer.postRaw(qp+"/csv", map[string]any{})
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), `attachment; filename="people-`) || !strings.HasPrefix(rec.Body.String(), "name\n") {
		t.Fatalf("saved csv: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	if rec = bravoApp.postRaw(a+"/query/csv", map[string]any{"sql": "select 1"}); rec.Code != 404 {
		t.Fatalf("another brand's csv: %d", rec.Code)
	}

	// Archive: out of the list, can't change or run; owners and admins
	// archive anyone's, even private.
	op.must(200, "POST", qp+"/archive", nil)
	if l := owner.must(200, "GET", a+"/queries", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("archived still listed: %v", l)
	}
	if q = owner.must(200, "GET", qp, nil); q["archived_at"] == nil || q["can_edit"] != false {
		t.Fatalf("archived: %v", q)
	}
	owner.must(409, "PATCH", qp, map[string]any{"name": "Again"})
	owner.must(409, "POST", qp+"/run", nil)
	owner.must(409, "POST", qp+"/versions/1/restore", nil)
	opQuery := op.must(201, "POST", a+"/queries", map[string]any{"name": "Operator's", "sql": "select 1"})["id"].(string)
	owner.must(404, "GET", a+"/queries/"+opQuery, nil)
	owner.must(200, "POST", a+"/queries/"+opQuery+"/archive", nil)
	ownerQuery := owner.must(201, "POST", a+"/queries", map[string]any{"name": "Owner's", "sql": "select 1"})["id"].(string)
	op.must(404, "POST", a+"/queries/"+ownerQuery+"/archive", nil)

	// Audit and events: every change is in the activity log, and no event
	// carries the SQL or the query's name.
	var actions []string
	rows, err := e.pool.Query(context.Background(), `SELECT action FROM audit_events WHERE workspace_id = $1 AND target_type IN ('saved_query', 'brand') AND action LIKE 'query.%' ORDER BY id`, ws)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		rows.Scan(&s) //nolint:errcheck
		actions = append(actions, s)
	}
	rows.Close()
	for _, want := range []string{"query.saved", "query.updated", "query.version_created", "query.restored", "query.shared", "query.exported", "query.archived"} {
		if !strings.Contains(strings.Join(actions, " "), want) {
			t.Fatalf("no %s in the audit log: %v", want, actions)
		}
	}
	var brandOf string
	if err := e.pool.QueryRow(context.Background(), `SELECT brand_id::text FROM audit_events WHERE workspace_id = $1 AND action = 'query.saved' LIMIT 1`, ws).Scan(&brandOf); err != nil || brandOf != alpha {
		t.Fatalf("query.saved brand: %q %v", brandOf, err)
	}
	var savedRan, exported int
	for _, ev := range got() {
		b, _ := json.Marshal(ev.Props)
		if strings.Contains(string(b), "select") || strings.Contains(string(b), "People by name") || strings.Contains(string(b), "HYPERLINK") {
			t.Fatalf("%s carries query text: %s", ev.Name, b)
		}
		switch ev.Name {
		case "query.ran":
			if ev.Props["saved_query_id"] != nil {
				savedRan++
			}
		case "query.exported":
			exported++
			if ev.Props["rows"] == nil {
				t.Fatalf("query.exported without its row count: %v", ev.Props)
			}
		}
	}
	if savedRan < 4 || exported < 3 {
		t.Fatalf("events: %d saved runs, %d exports", savedRan, exported)
	}
}
