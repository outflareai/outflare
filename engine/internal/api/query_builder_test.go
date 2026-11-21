package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func testBuilderCatalog() builderCatalog {
	return builderCatalog{
		Tables: map[string]builderTable{
			"people":            {Columns: map[string]string{"id": "uuid", "name": "text", "email": "text", "created_at": "timestamp with time zone", "attributes": "jsonb"}, RecordType: "person"},
			"transactions":      {Columns: map[string]string{"id": "uuid", "record_id": "uuid", "kind": "text", "brand_amount_minor": "bigint", "occurred_at": "timestamp with time zone"}},
			"record_identities": {Columns: map[string]string{"record_id": "uuid", "namespace": "text", "value": "text"}},
			"deals":             {Columns: map[string]string{"id": "uuid", "name": "text", "amount_cents": "bigint", "state": "text"}},
		},
		Fields: map[string]map[string]fieldView{"person": {"plan": {Key: "plan", ValueType: "select"}, "seats": {Key: "seats", ValueType: "number"}}},
	}
}

func mustBuilder(t *testing.T, s string) queryBuilder {
	t.Helper()
	b, err := parseBuilder(json.RawMessage(s))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The example from the request that started this feature: customers who
// spent over $500, with their email, user id, sign-up date and lifetime
// spend, biggest first.
func TestCompileBuilderLifetimeSpend(t *testing.T) {
	b := mustBuilder(t, `{"from": "people", "columns": [
		{"label": "Email", "source": {"column": "email"}},
		{"label": "User id", "source": {"identity": "user_id"}},
		{"label": "Signed up", "source": {"column": "created_at"}},
		{"label": "Lifetime spend", "source": {"related": {"table": "transactions", "link": "record_id", "agg": "sum", "column": "brand_amount_minor", "filters": [{"column": "kind", "op": "eq", "value": "payment"}]}}, "divide_by": 100}
	], "filters": [{"column": "lifetime spend", "op": "gt", "value": 500}], "sort": [{"column": "Lifetime spend", "dir": "desc"}]}`)
	sql, err := compileBuilder(b, testBuilderCatalog())
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT t.id AS record_id,
  t."email" AS "Email",
  (SELECT i.value FROM record_identities i WHERE i.record_id = t.id AND i.namespace = 'user_id' AND i.valid_to IS NULL ORDER BY i.verified DESC, i.created_at LIMIT 1) AS "User id",
  t."created_at" AS "Signed up",
  round((COALESCE((SELECT sum(x."brand_amount_minor") FROM "transactions" x WHERE x."record_id" = t."id" AND x."kind" = 'payment'), 0))::numeric / 100, 2) AS "Lifetime spend"
FROM "people" t
WHERE round((COALESCE((SELECT sum(x."brand_amount_minor") FROM "transactions" x WHERE x."record_id" = t."id" AND x."kind" = 'payment'), 0))::numeric / 100, 2) > 500
ORDER BY "Lifetime spend" DESC NULLS LAST`
	if sql != want {
		t.Fatalf("sql:\n%s\nwant:\n%s", sql, want)
	}
}

func TestCompileBuilderOps(t *testing.T) {
	cat := testBuilderCatalog()
	b := mustBuilder(t, `{"from": "people", "columns": [{"label": "Name", "source": {"column": "name"}}, {"label": "Plan", "source": {"field": "plan"}}],
		"match": "any", "limit": 10,
		"filters": [
			{"column": "Plan", "op": "in", "value": ["pro", "it's"]},
			{"source": {"field": "seats"}, "op": "gte", "value": 5},
			{"source": {"column": "created_at"}, "op": "within_days", "value": 30},
			{"column": "Name", "op": "contains", "value": "50%_off"},
			{"source": {"column": "email"}, "op": "not_set"}
		]}`)
	sql, err := compileBuilder(b, cat)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`t.attributes->>'plan' IN ('pro', 'it''s')`,
		`(r.attributes->>'seats')::numeric END >= 5`,
		`t."created_at" >= now() - interval '30 days'`,
		`(t."name")::text ILIKE '%50\%\_off%'`,
		`t."email" IS NULL`,
		"\n  OR ",
		"LIMIT 10",
	} {
		want = strings.Replace(want, "r.attributes", "t.attributes", 1)
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	// Not a person table: no record_id.
	sql, err = compileBuilder(mustBuilder(t, `{"from": "deals", "columns": [{"label": "Deal", "source": {"column": "name"}}]}`), cat)
	if err != nil || strings.Contains(sql, "record_id") {
		t.Fatalf("deals: %v\n%s", err, sql)
	}
}

func TestCompileBuilderRefuses(t *testing.T) {
	cat := testBuilderCatalog()
	for name, tc := range map[string]struct{ builder, want string }{
		"unknown table":     {`{"from": "secrets", "columns": [{"label": "A", "source": {"column": "id"}}]}`, "not a table you can read"},
		"unknown column":    {`{"from": "people", "columns": [{"label": "A", "source": {"column": "password"}}]}`, `no column "password"`},
		"no columns":        {`{"from": "people", "columns": []}`, "pick 1 to"},
		"two sources":       {`{"from": "people", "columns": [{"label": "A", "source": {"column": "id", "field": "plan"}}]}`, "exactly one"},
		"same label":        {`{"from": "people", "columns": [{"label": "A", "source": {"column": "id"}}, {"label": "a", "source": {"column": "name"}}]}`, "two columns"},
		"record_id label":   {`{"from": "people", "columns": [{"label": "record_id", "source": {"column": "id"}}]}`, "added by itself"},
		"bad op":            {`{"from": "people", "columns": [{"label": "A", "source": {"column": "id"}}], "filters": [{"column": "A", "op": "like", "value": "x"}]}`, "op is one of"},
		"bad identity":      {`{"from": "people", "columns": [{"label": "A", "source": {"identity": "x' OR 1=1"}}]}`, "identity is a kind"},
		"identity on deals": {`{"from": "deals", "columns": [{"label": "A", "source": {"identity": "email"}}]}`, "only people"},
		"sum no column":     {`{"from": "people", "columns": [{"label": "A", "source": {"related": {"table": "transactions", "link": "record_id", "agg": "sum"}}}]}`, "needs the column"},
		"bad agg":           {`{"from": "people", "columns": [{"label": "A", "source": {"related": {"table": "transactions", "link": "record_id", "agg": "median", "column": "id"}}}]}`, "agg is one of"},
		"sort unknown":      {`{"from": "people", "columns": [{"label": "A", "source": {"column": "id"}}], "sort": [{"column": "B"}]}`, "no column called"},
		"limit":             {`{"from": "people", "columns": [{"label": "A", "source": {"column": "id"}}], "limit": 0}`, "limit is 1"},
		"bad divide":        {`{"from": "people", "columns": [{"label": "A", "source": {"column": "id"}, "divide_by": 0}]}`, "divide_by"},
		"days not number":   {`{"from": "people", "columns": [{"label": "A", "source": {"column": "id"}}], "filters": [{"column": "A", "op": "within_days", "value": "7; DROP"}]}`, "whole number"},
	} {
		b, err := parseBuilder(json.RawMessage(tc.builder))
		if err == nil {
			_, err = compileBuilder(b, cat)
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", name, err, tc.want)
		}
	}
	if _, err := parseBuilder(json.RawMessage(`{"from": "people", "colums": []}`)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("unknown part: %v", err)
	}
}
