package api_test

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Custom columns and saved views of the drill-down report (plans/
// ad-tracker.md, B5b): a formula over the metrics on every row, and views
// that keep the report's settings, private until shared.
func TestReportViews(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	one := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	at := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	for i, c := range []struct{ anon, channel, country string }{{"a1", "paid_ads", "US"}, {"a2", "paid_ads", "US"}, {"a3", "seo", "DE"}} {
		sess := one(`INSERT INTO site_sessions (workspace_id, brand_id, anonymous_id, started_at, last_seen_at, landing_path, channel, country)
			VALUES ($1, $2, $3, $4, $4, '/', $5, $6) RETURNING id::text`, ws, brand, c.anon, at, c.channel, c.country)
		if i == 0 || i == 2 {
			ev := one(`INSERT INTO customer_events (workspace_id, brand_id, kind, name, anonymous_id, properties, context, occurred_at)
				VALUES ($1, $2, 'track', 'purchase', 'x', '{}', '{}', $3) RETURNING id::text`, ws, brand, at.Add(time.Hour))
			one(`INSERT INTO attribution_conversions (event_id, workspace_id, brand_id, name, occurred_at, value_minor, currency)
				VALUES ($1, $2, $3, 'purchase', $4, $5, 'USD') RETURNING event_id::text`, ev, ws, brand, at.Add(time.Hour), int64(1000*(i+1)))
			one(`INSERT INTO attribution_credits (event_id, workspace_id, brand_id, model, position, session_id, channel, weight)
				VALUES ($1, $2, $3, 'last_non_direct', 0, $4, $5, 1) RETURNING event_id::text`, ev, ws, brand, sess, c.channel)
		}
	}
	dates := "from=2026-10-01&to=2026-10-10"

	// A custom column: revenue per visit. US: 10 / 2 = 5; DE: 30 / 1.
	r := owner.must(200, "GET", base+"/drilldown?"+dates+"&group_by=country&column=rpv:revenue/visits&column=half:(revenue-0)/2&sort=-rpv", nil)
	rows := r["rows"].([]any)
	custom := func(row any, name string) any {
		return row.(map[string]any)["metrics"].(map[string]any)["custom"].(map[string]any)[name]
	}
	total := r["totals"].(map[string]any)["custom"].(map[string]any)["rpv"]
	if rows[0].(map[string]any)["keys"].([]any)[0] != "DE" || custom(rows[0], "rpv") != float64(30) || custom(rows[1], "rpv") != float64(5) ||
		custom(rows[1], "half") != float64(5) || total != 13.3333 {
		t.Fatalf("custom columns: %v, totals %v", rows, total)
	}
	// A division by zero is null, and the CSV carries the columns.
	if v := custom(owner.must(200, "GET", base+"/drilldown?"+dates+"&group_by=country&column=x:revenue/0", nil)["rows"].([]any)[0], "x"); v != nil {
		t.Fatalf("x/0: %v", v)
	}
	req := httptest.NewRequest("GET", base+"/drilldown?"+dates+"&group_by=country&column=rpv:revenue/visits&format=csv", nil)
	req.AddCookie(owner.cookie)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Body)
	if !strings.HasSuffix(strings.SplitN(string(body), "\n", 2)[0], ",cpa,rpv") {
		t.Fatalf("csv header: %q", body)
	}
	for _, bad := range []string{"column=visits:revenue", "column=rpv", "column=rpv:revenue/luck", "column=rpv:(revenue", "column=a:visits&column=a:visits", "sort=-rpv"} {
		owner.must(400, "GET", base+"/drilldown?"+bad, nil)
	}

	// A saved view, private to its maker.
	v := owner.must(201, "POST", base+"/report-views", map[string]any{"name": " By country ", "spec": map[string]any{
		"group_by": []string{"country"}, "columns": []any{map[string]any{"name": "rpv", "formula": "revenue / visits", "format": "money"}},
		"sort": "-rpv", "date_range": map[string]any{"from": "2026-10-01", "to": "2026-10-10"}, "display": map[string]any{"density": "compact"}}})
	id := v["id"].(string)
	if v["name"] != "By country" || v["shared"] != false || v["mine"] != true || v["spec"].(map[string]any)["display"].(map[string]any)["density"] != "compact" {
		t.Fatalf("view: %v", v)
	}
	// The report reads it; the request's own settings win.
	byView := owner.must(200, "GET", base+"/drilldown?view="+id, nil)
	if byView["group_by"].([]any)[0] != "country" || byView["from"] != "2026-10-01" || custom(byView["rows"].([]any)[0], "rpv") != float64(30) {
		t.Fatalf("report from the view: %v", byView)
	}
	if g := owner.must(200, "GET", base+"/drilldown?view="+id+"&group_by=channel", nil)["group_by"].([]any)[0]; g != "channel" {
		t.Fatalf("override: %v", g)
	}
	owner.must(404, "GET", base+"/drilldown?view=00000000-0000-0000-0000-000000000000", nil)

	// Another member's token can't see it until it's shared.
	tok := owner.must(201, "POST", "/v1/workspaces/"+ws+"/api-tokens", map[string]any{"name": "Analyst", "scopes": []string{"analytics.read", "analytics.manage"}})
	bot := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	if n := len(bot.must(200, "GET", base+"/report-views", nil)["data"].([]any)); n != 0 {
		t.Fatalf("the bot sees %d views", n)
	}
	bot.must(404, "GET", base+"/report-views/"+id, nil)
	owner.must(200, "PATCH", base+"/report-views/"+id, map[string]any{"shared": true, "name": "Countries"})
	if got := bot.must(200, "GET", base+"/report-views/"+id, nil); got["mine"] != false || got["name"] != "Countries" {
		t.Fatalf("shared view: %v", got)
	}
	bot.must(403, "PATCH", base+"/report-views/"+id, map[string]any{"shared": false})
	bot.must(200, "PATCH", base+"/report-views/"+id, map[string]any{"spec": map[string]any{"group_by": []string{"channel"}}})

	// Bad specs are refused as the report would refuse them.
	for _, spec := range []map[string]any{
		{"group_by": []string{"planet"}},
		{"columns": []any{map[string]any{"name": "x", "formula": "luck"}}},
		{"columns": []any{map[string]any{"name": "x", "formula": "visits", "format": "stars"}}},
		{"timezone": "Nowhere/Land"},
		{"date_range": map[string]any{"preset": "someday"}},
	} {
		owner.must(400, "POST", base+"/report-views", map[string]any{"name": "Bad", "spec": spec})
	}
	owner.must(400, "POST", base+"/report-views", map[string]any{"spec": map[string]any{}})
	owner.must(204, "DELETE", base+"/report-views/"+id, nil)
	owner.must(404, "GET", base+"/report-views/"+id, nil)
}
