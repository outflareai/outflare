package api

import (
	"encoding/json"
	"testing"
)

func TestDescribeEndpointSQL(t *testing.T) {
	params := []endpointParam{{Name: "country", Type: "text", Required: true}, {Name: "since", Type: "date"}}
	cases := []struct {
		sql    string
		params []endpointParam
		rowCap int
		want   string
	}{
		{
			"SELECT name, email, country, lifetime_spend_minor AS spend FROM query.people p WHERE p.country = $1 AND created_at >= $2::date ORDER BY spend DESC LIMIT 500",
			params, 1000,
			"People where country is the country you send and created is on or after the since you send, highest spend first. Returns name, email, country and spend, up to 500 rows a call. Callers send country (text) and since (date, optional).",
		},
		{
			"-- weekly\nselect * from customer_events where name = 'Signed Up' and occurred_at >= now() - interval '30 days' order by occurred_at desc",
			nil, 200,
			"Product events where name is Signed Up and when it happened is within the last 30 days, newest when it happened first. Returns every column, up to 200 rows a call.",
		},
		{
			"SELECT plan, count(*) AS signups FROM subscriptions WHERE status IN ('active', 'trialing') AND canceled_at IS NULL AND lower(plan) <> 'free' GROUP BY plan ORDER BY 2 DESC",
			nil, 1000,
			"Subscriptions where status is one of active or trialing and canceled is empty, plus 1 more condition, grouped by plan, highest signups first. Returns plan and signups, up to 1,000 rows a call.",
		},
		{
			"SELECT email FROM people WHERE email ILIKE '%@acme.example' LIMIT $1",
			[]endpointParam{{Name: "how_many", Type: "integer", Required: true}}, 1000,
			"People where email ends with @acme.example. Returns email, as many rows as how_many says, up to 1,000. Callers send how_many (integer).",
		},
	}
	for _, c := range cases {
		if got := describeEndpoint(c.sql, nil, c.params, c.rowCap, nil); got != c.want {
			t.Errorf("%s\n got: %s\nwant: %s", c.sql, got, c.want)
		}
	}
}

func TestDescribeEndpointBuilder(t *testing.T) {
	var b queryBuilder
	if err := json.Unmarshal([]byte(`{"from": "people", "match": "all",
		"columns": [{"label": "Name", "source": {"column": "name"}}, {"label": "Email", "source": {"column": "email"}},
			{"label": "Lifetime spend", "source": {"related": {"table": "transactions", "link": "record_id", "agg": "sum", "column": "brand_amount_minor"}}}],
		"filters": [{"source": {"field": "plan"}, "op": "in", "value": ["Pro", "Team"]}, {"column": "Lifetime spend", "op": "gt", "value": 500},
			{"source": {"column": "created_at"}, "op": "within_days", "value": 90}],
		"sort": [{"column": "Lifetime spend", "dir": "desc"}], "limit": 100}`), &b); err != nil {
		t.Fatal(err)
	}
	got := describeEndpoint("", &b, nil, 1000, map[string]string{"plan": "Plan"})
	want := "People where plan is one of Pro or Team, lifetime spend is more than 500 and created is within the last 90 days, highest lifetime spend first. Returns name, email and lifetime spend, up to 100 rows a call."
	if got != want {
		t.Errorf("\n got: %s\nwant: %s", got, want)
	}
}
