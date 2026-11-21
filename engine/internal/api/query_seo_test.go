package api_test

import (
	"context"
	"strings"
	"testing"
)

// Search, SEO and AI visibility in Query: for callers with seo.read, one
// brand at a time, a row without its own brand following its site's, and
// no verification token.
func TestQuerySEO(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	exec := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	np := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	acme := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "handle": "acme"})["id"].(string)
	for _, b := range []struct{ id, host string }{{np, "northpeak.example"}, {acme, "acme.example"}} {
		site := exec(`INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, $3, 'https://'||$3||'/', 'tok') RETURNING id::text`, ws, b.id, b.host)
		// No brand of its own: it follows its site's.
		exec(`INSERT INTO seo_regressions (workspace_id, site_id, kind, subject, before, after) VALUES ($1, $2, 'clicks', '/pricing', 120, 40) RETURNING 'x'`, ws, site)
	}

	out := owner.must(200, "POST", "/v1/brands/"+np+"/query", map[string]any{"sql": "select host from seo_sites"})
	if rows := queryRows(t, out); len(rows) != 1 || rows[0][0] != "northpeak.example" {
		t.Fatalf("northpeak sites: %v", out)
	}
	out = owner.must(200, "POST", "/v1/brands/"+acme+"/query", map[string]any{"sql": "select r.subject, s.host from seo_regressions r join seo_sites s on s.id = r.site_id"})
	if rows := queryRows(t, out); len(rows) != 1 || rows[0][1] != "acme.example" {
		t.Fatalf("acme regressions: %v", out)
	}
	code, got := owner.do("POST", "/v1/brands/"+np+"/query", map[string]any{"sql": "select verify_token from seo_sites"})
	if code != 400 || !strings.Contains(strings.ToLower(got["error"].(map[string]any)["message"].(string)), "does not exist") {
		t.Fatalf("verify_token: %d %v", code, got)
	}
	cat := owner.must(200, "GET", "/v1/brands/"+np+"/data/catalog/seo_sites", nil)
	if cat["table"].(map[string]any)["area"] != "seo" || cat["table"].(map[string]any)["rows"] != 1.0 {
		t.Fatalf("catalog: %v", cat["table"])
	}

	// Without seo.read the tables aren't readable.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "CRM agent", "scopes": []string{"customers.read"}, "brand": "northpeak"})
	agent := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	if code, _ := agent.do("POST", "/v1/brands/"+np+"/query", map[string]any{"sql": "select count(*) from ai_samples"}); code == 200 {
		t.Fatalf("ai_samples read without seo.read")
	}
}
