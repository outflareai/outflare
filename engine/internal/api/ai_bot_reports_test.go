package api_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A site that isn't behind a CDN reports crawler requests from its own
// server: only AI crawlers Outflare knows are counted, per host and day,
// and a connection's logs win where they cover the same host and day.
func TestAIBotReports(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	geo := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Northpeak", "website": "https://northpeak.example"})["id"].(string)
	g := "/v1/brands/" + geo + "/ai-bot-hits"
	read := "/v1/workspaces/" + ws + "/ai-bot-hits"
	gpt := "Mozilla/5.0 (compatible; GPTBot/1.2; +https://openai.com/gptbot)"
	today := time.Now().UTC().Format(time.DateOnly)
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly)

	// What a report must be.
	for _, bad := range []map[string]any{
		{},
		{"hits": []any{}},
		{"hits": []any{map[string]any{"user_agent": gpt}}},
		{"hits": []any{map[string]any{"user_agent": gpt, "host": "not a host"}}},
		{"hits": []any{map[string]any{"user_agent": gpt, "host": "localhost"}}},
		{"hits": []any{map[string]any{"user_agent": gpt, "host": "northpeak.example", "requests": 0}}},
		{"hits": []any{map[string]any{"user_agent": gpt, "host": "northpeak.example", "day": "2020-01-01"}}},
		{"hits": []any{map[string]any{"user_agent": gpt, "host": "northpeak.example", "day": "tomorrow"}}},
	} {
		owner.must(400, "POST", g, bad)
	}

	out := owner.must(200, "POST", g, map[string]any{"hits": []any{
		map[string]any{"user_agent": gpt, "host": "Northpeak.EXAMPLE:443", "requests": 5},
		map[string]any{"user_agent": gpt, "host": "northpeak.example"},
		map[string]any{"user_agent": "Mozilla/5.0 (compatible; ClaudeBot/1.0)", "host": "northpeak.example", "day": yesterday, "requests": 3},
		map[string]any{"user_agent": "Mozilla/5.0 ChatGPT-User/1.0", "host": "docs.example.com"},
		map[string]any{"user_agent": "Mozilla/5.0 (Macintosh) Chrome/130 Safari/537.36", "host": "northpeak.example", "requests": 900},
		map[string]any{"user_agent": "curl/8.0", "host": "northpeak.example"},
	}})
	if out["counted"] != 10.0 || out["ignored"] != 2.0 {
		t.Fatalf("report: %v", out)
	}
	// A second report adds to the day's count.
	owner.must(200, "POST", g, map[string]any{"hits": []any{map[string]any{"user_agent": gpt, "host": "northpeak.example", "requests": 4}}})

	// Only counts are kept: no user agent anywhere.
	var rows, total int64
	if err := e.pool.QueryRow(ctx, `SELECT count(*), sum(requests) FROM ai_bot_reports WHERE brand_id = $1`, geo).Scan(&rows, &total); err != nil || rows != 3 || total != 14 {
		t.Fatalf("stored: %d rows, %d requests, %v", rows, total, err)
	}

	host := func(name string) map[string]any {
		t.Helper()
		for _, h := range owner.must(200, "GET", read, nil)["data"].([]any) {
			if h.(map[string]any)["host"] == name {
				return h.(map[string]any)
			}
		}
		t.Fatalf("no host %s", name)
		return nil
	}
	www := host("northpeak.example")
	if www["requests"] != 13.0 || www["training"] != 13.0 || www["source"] != "server" || len(www["bots"].([]any)) != 2 || len(www["days"].([]any)) != 2 {
		t.Fatalf("www: %v", www)
	}
	if d := host("docs.example.com"); d["requests"] != 1.0 || d["user"] != 1.0 || d["source"] != "server" {
		t.Fatalf("docs: %v", d)
	}

	// A connection's logs for the same host and day win, so nothing counts
	// twice; the server's count for the other day stays.
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"result": []}`) }))
	t.Cleanup(cf.Close)
	e.integrations.Client.BaseURLs["cloudflare_web_analytics"] = cf.URL
	i := owner.must(201, "POST", "/v1/workspaces/"+ws+"/integrations", map[string]any{"connector": "cloudflare_web_analytics",
		"credentials": map[string]string{"api_token": "cf_test_not_a_real_token", "account_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}})
	if _, err := e.pool.Exec(ctx, `INSERT INTO ai_bot_hits (workspace_id, integration_id, brand_id, host, day, token, requests) VALUES ($1, $2, $4, 'northpeak.example', $3::date, 'OAI-SearchBot', 7)`,
		ws, i["id"], today, geo); err != nil {
		t.Fatal(err)
	}
	www = host("northpeak.example")
	if www["requests"] != 10.0 || www["search"] != 7.0 || www["training"] != 3.0 || www["source"] != "both" {
		t.Fatalf("with logs: %v", www)
	}

	// Each brand keeps its own counts.
	other := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Summitly", "website": "https://summitly.example"})["id"].(string)
	owner.must(200, "POST", "/v1/brands/"+other+"/ai-bot-hits", map[string]any{"hits": []any{map[string]any{"user_agent": gpt, "host": "summitly.example", "requests": 2}}})
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM ai_bot_reports WHERE brand_id = $1`, other).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("other brand: %d %v", rows, err)
	}
}
