package api_test

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// queryAskBodies keeps what the Queries describe box sent the model.
var queryAskBodies struct {
	sync.Mutex
	all []string
}

// answerQueryAsk answers fakeClaude's describe-box calls: a builder with a
// column that doesn't exist first, then a good one once told why; SQL for
// a count; nothing for the weather.
func answerQueryAsk(w http.ResponseWriter, body string) {
	queryAskBodies.Lock()
	queryAskBodies.all = append(queryAskBodies.all, body)
	queryAskBodies.Unlock()
	switch {
	case strings.Contains(body, "weather"):
		answerJSON(w, map[string]any{"kind": "none", "name": "", "builder_json": "", "sql": "", "explanation": "There is no weather data here."})
	case strings.Contains(body, "how many people"):
		answerJSON(w, map[string]any{"kind": "sql", "name": "People count", "builder_json": "", "sql": "select count(*) as people from people", "explanation": "How many people there are."})
	case strings.Contains(body, "cannot be used"):
		answerJSON(w, map[string]any{"kind": "builder", "name": "Recent sign-ups",
			"builder_json": `{"from": "people", "columns": [{"label": "Name", "source": {"column": "name"}}, {"label": "Email", "source": {"identity": "email"}}, {"label": "Signed up", "source": {"column": "created_at"}}], "filters": [{"column": "Signed up", "op": "within_days", "value": 30}], "sort": [{"column": "Signed up", "dir": "desc"}]}`,
			"sql":          "", "explanation": "People who signed up in the last 30 days, newest first."})
	default:
		answerJSON(w, map[string]any{"kind": "builder", "name": "Recent sign-ups",
			"builder_json": `{"from": "people", "columns": [{"label": "Signed up", "source": {"column": "signup_date"}}]}`, "sql": "", "explanation": "x"})
	}
}

// Queries in plain English (docs/query.md): the workspace's own model turns
// a description into a builder or SQL, which the engine checks by running
// it once as the person who asked; a builder compiles to SQL and saves
// with its versions.
func TestQueryAskAndBuilder(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	e.runServerJobs(t)
	var brand string
	if err := e.pool.QueryRow(t.Context(), `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana", "identities": []map[string]any{{"namespace": "email", "value": "ana@acme.example"}}})

	// The builder compiles to SQL, and refuses what it can't read.
	b := map[string]any{"from": "people", "columns": []any{map[string]any{"label": "Name", "source": map[string]any{"column": "name"}}}, "sort": []any{map[string]any{"column": "Name"}}}
	sql := owner.must(200, "POST", base+"/query/build", map[string]any{"builder": b})["sql"].(string)
	if !strings.Contains(sql, `t."name" AS "Name"`) || !strings.Contains(sql, "t.id AS record_id") {
		t.Fatalf("built: %s", sql)
	}
	if c := owner.must(400, "POST", base+"/query/build", map[string]any{"builder": map[string]any{"from": "secrets", "columns": []any{map[string]any{"label": "A", "source": map[string]any{"column": "id"}}}}})["error"].(map[string]any)["code"]; c != "query_builder_invalid" {
		t.Fatalf("bad table: %v", c)
	}
	run := owner.must(200, "POST", base+"/query", map[string]any{"sql": sql})
	if run["row_count"] != 1.0 {
		t.Fatalf("ran: %v", run)
	}

	// A builder saves without SQL; changing it is a new version; SQL
	// changed by hand drops it.
	q := owner.must(201, "POST", base+"/queries", map[string]any{"name": "People", "builder": b, "question": "everyone by name"})
	qp := base + "/queries/" + q["id"].(string)
	if q["sql"] != sql || q["builder"] == nil || q["question"] != "everyone by name" || q["current_version"] != 1.0 {
		t.Fatalf("saved: %v", q)
	}
	b["limit"] = 5
	q = owner.must(200, "PATCH", qp, map[string]any{"builder": b})
	if q["current_version"] != 2.0 || !strings.Contains(q["sql"].(string), "LIMIT 5") {
		t.Fatalf("builder change: %v", q)
	}
	q = owner.must(200, "PATCH", qp, map[string]any{"sql": "select name from people"})
	if q["current_version"] != 3.0 || q["builder"] != nil {
		t.Fatalf("hand-edited SQL: %v", q)
	}
	v := owner.must(200, "GET", qp+"/versions", nil)["data"].([]any)
	if len(v) != 3 || v[1].(map[string]any)["builder"] == nil {
		t.Fatalf("versions: %v", v)
	}

	// Without the workspace's own AI model, nothing is asked.
	if c := owner.must(409, "POST", base+"/query/ask", map[string]any{"question": "People who signed up this month"})["error"].(map[string]any)["code"]; c != "connector_needed" {
		t.Fatalf("no AI: %v", c)
	}
	owner.must(201, "POST", "/v1/workspaces/"+ws+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})
	owner.must(400, "POST", base+"/query/ask", map[string]any{"question": " "})
	owner.must(400, "POST", base+"/query/ask", map[string]any{"question": strings.Repeat("x", 2001)})
	owner.must(404, "GET", base+"/query/ask/00000000-0000-0000-0000-000000000000", nil)

	ask := func(question string) map[string]any {
		t.Helper()
		r := owner.must(202, "POST", base+"/query/ask", map[string]any{"question": question})
		if r["status"] != "queued" || r["question"] != question {
			t.Fatalf("asked: %v", r)
		}
		return waitFor(t, owner, base+"/query/ask/"+r["id"].(string), func(m map[string]any) bool { return m["status"] == "ready" || m["status"] == "failed" })
	}

	// The first builder names a column that doesn't exist, so the model is
	// told why and asked again; the second is checked and kept.
	a := ask("People who signed up in the last 30 days, with their email, newest first")
	if a["status"] != "ready" || a["kind"] != "builder" || a["name"] != "Recent sign-ups" || a["builder"] == nil || !strings.Contains(a["sql"].(string), "interval '30 days'") {
		t.Fatalf("answer: %v", a)
	}
	rows := owner.must(200, "POST", base+"/query", map[string]any{"sql": a["sql"]})
	if rows["row_count"] != 1.0 {
		t.Fatalf("the answer's SQL: %v", rows)
	}
	queryAskBodies.Lock()
	bodies := strings.Join(queryAskBodies.all, "\n")
	n := len(queryAskBodies.all)
	queryAskBodies.Unlock()
	if n != 2 || !strings.Contains(bodies, "signup_date") || !strings.Contains(bodies, "people") {
		t.Fatalf("the model was asked %d times, or without the tables or the engine's reason", n)
	}
	if strings.Contains(bodies, "acme.example") {
		t.Fatal("a customer's data reached the model")
	}

	// SQL when a builder can't say it, and nothing for what isn't there.
	if s := ask("how many people are there?"); s["kind"] != "sql" || s["sql"] != "select count(*) as people from people" {
		t.Fatalf("sql: %v", s)
	}
	if none := ask("What will the weather be tomorrow?"); none["status"] != "ready" || none["kind"] != "" || !strings.Contains(none["explanation"].(string), "weather") {
		t.Fatalf("none: %v", none)
	}

	// Only the person who asked reads the answer.
	op, _ := joinAs(t, owner, ws, "op@example.com", map[string]any{"role": "operator"})
	r := owner.must(202, "POST", base+"/query/ask", map[string]any{"question": "how many people?"})
	op.must(404, "GET", base+"/query/ask/"+r["id"].(string), nil)

	seen := map[string]int{}
	for _, ev := range got() {
		seen[ev.Name]++
		if strings.HasPrefix(ev.Name, "query_question.") {
			for k, v := range ev.Props {
				if s, ok := v.(string); ok && strings.Contains(s, "signed up") {
					t.Fatalf("%s carries the question in %s", ev.Name, k)
				}
			}
		}
	}
	if seen["query_question.asked"] < 3 || seen["query_question.answered"] < 3 {
		t.Fatalf("events: %v", seen)
	}
}
