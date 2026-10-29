package api_test

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequestLogs(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	rp := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "website": "repocket.com"})["id"].(string)
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode", "website": "geonode.com"})
	owner.must(201, "POST", base+"/leads", map[string]any{"email": "ada@example.com"})
	owner.must(200, "GET", base+"/leads?q=ada@example.com", nil)
	owner.must(404, "GET", base+"/leads/00000000-0000-0000-0000-000000000000", nil)
	owner.must(200, "GET", base+"/sequences?brand=repocket", nil)

	// An agent through MCP.
	agent := agentToken(t, owner, base, "Agent", "leads.read", "audit.read")
	req := httptest.NewRequest("GET", base+"/leads", nil)
	req.Header.Set("Authorization", "Bearer "+agent.bearer)
	req.Header.Set("Outflare-Client", "mcp/0.1.0")
	req.Header.Set("Outflare-Tool", "list_leads")
	req.Header.Set("Outflare-Agent", "claude-ai")
	e.h.ServeHTTP(httptest.NewRecorder(), req)

	all := owner.must(200, "GET", base+"/logs", nil)
	logs := all["data"].([]any)
	if len(logs) < 6 {
		t.Fatalf("logs: %v", logs)
	}
	newest := logs[0].(map[string]any)
	if newest["surface"] != "mcp" || newest["mcp_tool"] != "list_leads" || newest["agent"] != "claude-ai" ||
		newest["actor_type"] != "api_token" || newest["actor_name"] != "Agent" || newest["operation"] != "listLeads" || newest["level"] != "info" {
		t.Fatalf("mcp call: %v", newest)
	}
	for _, l := range logs {
		l := l.(map[string]any)
		// Query values (here an email address) are never stored, only names.
		if strings.Contains(l["path"].(string), "ada@") || strings.Contains(strings.Join(anyStrings(l["query_keys"]), ","), "ada") {
			t.Fatalf("stored a query value: %v", l)
		}
		// Reading the logs is not itself logged.
		if strings.HasSuffix(l["path"].(string), "/logs") {
			t.Fatalf("logged a logs read: %v", l)
		}
	}

	// Filters.
	failed := owner.must(200, "GET", base+"/logs?level=warning", nil)["data"].([]any)
	if len(failed) != 1 || failed[0].(map[string]any)["status"] != 404.0 || failed[0].(map[string]any)["error_code"] != "not_found" {
		t.Fatalf("warnings: %v", failed)
	}
	if n := len(owner.must(200, "GET", base+"/logs?status=4xx,500&surface=web", nil)["data"].([]any)); n != 1 {
		t.Fatalf("4xx from web: %d", n)
	}
	if n := len(owner.must(200, "GET", base+"/logs?surface=mcp&mcp_tool=list_leads", nil)["data"].([]any)); n != 1 {
		t.Fatalf("mcp: %d", n)
	}
	if n := len(owner.must(200, "GET", base+"/logs?operation=upsertLead&method=post", nil)["data"].([]any)); n != 1 {
		t.Fatalf("operation: %d", n)
	}
	byBrand := owner.must(200, "GET", base+"/logs?brand=repocket", nil)["data"].([]any)
	if len(byBrand) != 1 || byBrand[0].(map[string]any)["brand_id"] != rp {
		t.Fatalf("by brand: %v", byBrand)
	}
	if n := len(owner.must(200, "GET", base+"/logs?q=leads&since=1h", nil)["data"].([]any)); n < 4 {
		t.Fatalf("search: %d", n)
	}
	if n := len(owner.must(200, "GET", base+"/logs?until=1h", nil)["data"].([]any)); n != 0 {
		t.Fatalf("until an hour ago: %d", n)
	}
	owner.must(400, "GET", base+"/logs?level=loud", nil)
	owner.must(400, "GET", base+"/logs?status=9xx", nil)
	owner.must(400, "GET", base+"/logs?since=yesterday", nil)
	owner.must(400, "GET", base+"/logs?actor_id=nope", nil)
	owner.must(404, "GET", base+"/logs?brand=nope", nil)

	// Paging back and following.
	page := owner.must(200, "GET", base+"/logs?limit=2", nil)
	if len(page["data"].([]any)) != 2 || page["next_before"] == nil || page["next_cursor"] == nil {
		t.Fatalf("page: %v", page)
	}
	older := owner.must(200, "GET", base+"/logs?limit=2&cursor="+page["next_cursor"].(string), nil)["data"].([]any)
	if older[0].(map[string]any)["id"] == page["data"].([]any)[0].(map[string]any)["id"] {
		t.Fatal("cursor did not page back")
	}
	tail := owner.must(200, "GET", base+"/logs?after=latest", nil)
	if len(tail["data"].([]any)) != 0 || tail["next_after"] == nil {
		t.Fatalf("tail start: %v", tail)
	}
	owner.must(200, "GET", base+"/members", nil)
	owner.must(200, "GET", base+"/leads", nil)
	more := owner.must(200, "GET", base+"/logs?after="+tail["next_after"].(string), nil)
	got := more["data"].([]any)
	if len(got) != 2 || !strings.HasSuffix(got[0].(map[string]any)["path"].(string), "/members") || more["next_after"] == tail["next_after"] {
		t.Fatalf("tail: %v", more)
	}
	again := owner.must(200, "GET", base+"/logs?after="+more["next_after"].(string), nil)
	if len(again["data"].([]any)) != 0 || again["next_after"] != more["next_after"] {
		t.Fatalf("tail again: %v", again)
	}

	// One call, by log id or request id, with what it changed.
	created := owner.must(200, "GET", base+"/logs?operation=upsertLead", nil)["data"].([]any)[0].(map[string]any)
	one := owner.must(200, "GET", base+"/logs/"+created["id"].(string), nil)
	if one["log"].(map[string]any)["request_id"] != created["request_id"] {
		t.Fatalf("log: %v", one)
	}
	audits := one["audit_events"].([]any)
	if len(audits) != 1 || audits[0].(map[string]any)["action"] != "lead.created" {
		t.Fatalf("audit events: %v", audits)
	}
	if byReq := owner.must(200, "GET", base+"/logs/"+created["request_id"].(string), nil); byReq["log"].(map[string]any)["id"] != created["id"] {
		t.Fatalf("by request id: %v", byReq)
	}
	owner.must(404, "GET", base+"/logs/log_999999999", nil)

	// Export and the chart.
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", base+"/logs/export?level=warning", nil)
	r.AddCookie(owner.cookie)
	e.h.ServeHTTP(rec, r)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") || strings.Count(rec.Body.String(), "\n") != 2 {
		t.Fatalf("export: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	r = httptest.NewRequest("GET", base+"/logs/export?format=ndjson", nil)
	r.AddCookie(owner.cookie)
	e.h.ServeHTTP(rec, r)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/x-ndjson" || !strings.Contains(rec.Body.String(), `"surface":"mcp"`) {
		t.Fatalf("ndjson: %d %s", rec.Code, rec.Body.String())
	}
	hist := owner.must(200, "GET", base+"/logs/histogram?since=1h", nil)
	if hist["bucket_seconds"] != 60.0 || len(hist["data"].([]any)) == 0 {
		t.Fatalf("histogram: %v", hist)
	}

	// A refused call keeps the field it named, never the value.
	owner.must(400, "POST", base+"/leads", map[string]any{"email": "grace@example.com", "nickname": "Grace"})
	bad := owner.must(200, "GET", base+"/logs?status=400", nil)["data"].([]any)
	if len(bad) != 1 || bad[0].(map[string]any)["error_code"] != "invalid_request" || bad[0].(map[string]any)["error_param"] != "nickname" {
		t.Fatalf("invalid request: %v", bad)
	}

	// The token's maker can read logs with audit.read; others can't.
	agent.must(200, "GET", base+"/logs", nil)
	reader := agentToken(t, owner, base, "Reader", "leads.read")
	reader.must(403, "GET", base+"/logs", nil)
	// A missing scope is kept, so the page can say what to grant.
	reader.must(403, "POST", base+"/leads", map[string]any{"email": "grace@example.com"})
	refused := owner.must(200, "GET", base+"/logs?error_code=forbidden", nil)["data"].([]any)
	if len(refused) != 1 || refused[0].(map[string]any)["error_param"] != "leads.manage" {
		t.Fatalf("missing scope: %v", refused)
	}

	// A brand token sees only calls about its brand.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Repocket agent", "scopes": []string{"audit.read", "sequences.read"}, "brand": "repocket"})
	rpAgent := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	rpAgent.must(200, "GET", base+"/sequences", nil)
	seen := rpAgent.must(200, "GET", base+"/logs", nil)["data"].([]any)
	if len(seen) != 2 {
		t.Fatalf("brand token sees: %v", seen)
	}
	for _, l := range seen {
		if l.(map[string]any)["brand_id"] != rp {
			t.Fatalf("brand token saw another brand's call: %v", l)
		}
	}
	rpAgent.must(404, "GET", base+"/logs/"+created["id"].(string), nil)
	rpAgent.must(404, "GET", base+"/logs?brand=geonode", nil)

	// Retention.
	if _, err := e.pool.Exec(t.Context(), `UPDATE request_logs SET created_at = now() - interval '40 days' WHERE workspace_id = $1 AND surface = 'mcp'`, ws); err != nil {
		t.Fatal(err)
	}
	if n, err := e.srv.PruneRequestLogs(t.Context(), 30*24*time.Hour); err != nil || n != 1 {
		t.Fatalf("pruned %d: %v", n, err)
	}
}

func anyStrings(v any) []string {
	var out []string
	for _, s := range v.([]any) {
		out = append(out, s.(string))
	}
	return out
}
