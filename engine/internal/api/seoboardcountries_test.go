package api_test

import (
	"context"
	"testing"
	"time"
)

func TestSEOBoardCountryMoney(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "website": "northpeak.example"})["id"].(string)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "website": "acme.example"})["id"].(string)
	board := "/v1/brands/" + brand + "/seo/board"
	today, err := time.Parse(time.DateOnly, owner.must(200, "GET", board, nil)["today"].(string))
	if err != nil {
		t.Fatal(err)
	}
	yesterday := today.AddDate(0, 0, -1).Format(time.DateOnly)
	integ := func(name string) string {
		return owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "name": name, "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})["id"].(string)
	}
	ph, sc := integ("PostHog"), integ("Search Console")
	seg := `INSERT INTO search_segments (workspace_id, brand_id, integration_id, site, host, day, page, country, device, clicks, impressions, position)
		VALUES ($1, $2, $3, 'sc-domain:northpeak.example', 'northpeak.example', $4, 'https://northpeak.example/', $5, 'DESKTOP', $6, $7, 3)`
	exec(seg, ws, brand, sc, yesterday, "usa", 60, 600)
	exec(seg, ws, brand, sc, yesterday, "deu", 40, 400)
	cc := `INSERT INTO organic_countries (workspace_id, brand_id, integration_id, site, day, host, country, organic_sessions, session_seconds, signups, payments, revenue_usd)
		VALUES ($1, $2, $3, '1', $4, $5, $6, $7, $8, $9, $10, $11)`
	exec(cc, ws, brand, ph, yesterday, "northpeak.example", "US", 8, 480, 2, 1, 50)
	exec(cc, ws, brand, ph, yesterday, "www.northpeak.example", "US", 2, 120, 0, 0, 0)
	exec(cc, ws, brand, ph, yesterday, "northpeak.example", "IN", 5, 100, 1, 2, 30)
	exec(cc, ws, brand, ph, yesterday, "northpeak.example", "", 9, 900, 9, 9, 900)
	exec(cc, ws, other, ph, yesterday, "acme.example", "DE", 1, 10, 1, 1, 999)

	res := owner.must(200, "GET", board+"?period=yesterday", nil)
	got := map[string]map[string]any{}
	var order []string
	for _, x := range res["countries"].([]any) {
		m := x.(map[string]any)
		got[m["country"].(string)] = m
		order = append(order, m["country"].(string))
	}
	if len(order) != 3 || order[2] != "ind" {
		t.Fatalf("countries: %v", order)
	}
	us := got["usa"]
	if us["clicks"] != 60.0 || us["organic_sessions"] != 10.0 || us["avg_session_seconds"] != 60.0 || us["signups"] != 2.0 || us["payments"] != 1.0 || us["revenue_usd"] != 50.0 {
		t.Fatalf("usa: %v", us)
	}
	if de := got["deu"]; de["payments"] != 0.0 || de["revenue_usd"] != 0.0 || de["avg_session_seconds"] != nil {
		t.Fatalf("deu, another brand's money stays out: %v", de)
	}
	if in := got["ind"]; in["clicks"] != 0.0 || in["payments"] != 2.0 || in["revenue_usd"] != 30.0 || in["avg_session_seconds"] != 20.0 {
		t.Fatalf("ind, paid without clicks: %v", in)
	}
}
