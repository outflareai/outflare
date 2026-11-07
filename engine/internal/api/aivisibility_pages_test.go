package api_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestAIVisibilityPages(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	one := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}

	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "website": "acme.example"})
	site := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://acme.example/", "brand_id": "acme"})["id"].(string)
	brand := one(`SELECT brand_id::text FROM seo_sites WHERE id = $1`, site)

	// Nothing crawled or asked yet.
	empty := owner.must(200, "GET", "/v1/brands/"+brand+"/ai-visibility/pages", nil)
	if len(empty["sites"].([]any)) != 0 || len(empty["pages"].([]any)) != 0 || empty["days"] != 28.0 {
		t.Fatalf("empty: %v", empty)
	}

	run := one(`INSERT INTO crawl_runs (workspace_id, site_id, host, trigger, max_pages, delay_ms, obey_robots, status, finished_at, health_score, llms_txt_status)
		VALUES ($1, $2, 'acme.example', 'manual', 100, 0, true, 'done', now(), 90, 404) RETURNING id::text`, ws, site)
	page := func(url, signals string) string {
		return one(`INSERT INTO crawl_pages (workspace_id, run_id, url, depth, source, status, http_status, content_type, title, ai_signals)
			VALUES ($1, $2, $3, 0, 'link', 'fetched', 200, 'text/html; charset=utf-8', 'Page', $4) RETURNING id::text`, ws, run, url, signals)
	}
	home := page("https://acme.example/", `{}`)
	guide := page("https://acme.example/guide", `{"nosnippet":true,"unanswered":["Can I cancel?"]}`)
	page("https://acme.example/clean", `{}`)
	exec(`INSERT INTO crawl_issues (workspace_id, run_id, page_id, rule, severity, detail) VALUES
		($1, $2, $3, 'ai_snippet_blocked', 'warning', ''), ($1, $2, $3, 'ai_question_unanswered', 'notice', 'Can I cancel?'),
		($1, $2, $3, 'title_too_short', 'notice', ''), ($1, $2, $4, 'llms_txt_missing', 'notice', '')`, ws, run, guide, home)
	exec(`INSERT INTO crawl_ai_bots (workspace_id, run_id, token, purpose, root_allowed, pages, blocked_pages) VALUES
		($1, $2, 'OAI-SearchBot', 'search', true, 3, 1), ($1, $2, 'GPTBot', 'training', false, 3, 3), ($1, $2, 'ClaudeBot', 'training', true, 3, 0)`, ws, run)
	exec(`INSERT INTO crawl_robots (run_id, host, status, body) VALUES ($1, 'acme.example', 200, 'User-agent: *
Content-Signal: search=yes, ai-train=no
Allow: /')`, run)

	// Answers cite the home page twice and a page the crawl didn't read.
	cohort := owner.must(201, "POST", base+"/ai-cohorts", map[string]any{"name": "Buyers", "brand_id": "acme"})["id"].(string)
	owner.must(201, "POST", base+"/ai-cohorts/"+cohort+"/prompts", map[string]any{"prompts": []map[string]any{{"text": "Which proxies are best?"}}})
	prompt := one(`SELECT id::text FROM ai_prompts WHERE cohort_id = $1`, cohort)
	airun := one(`INSERT INTO ai_runs (workspace_id, cohort_id, trigger) VALUES ($1, $2, 'manual') RETURNING id::text`, ws, cohort)
	for i, url := range []string{"https://acme.example/", "https://acme.example/"} {
		sample := one(`INSERT INTO ai_samples (workspace_id, run_id, cohort_id, prompt_id, brand_id, provider, model, status, attempt)
			VALUES ($1, $2, $3, $4, $5, 'openrouter', 'm', 'ok', $6) RETURNING id::text`, ws, airun, cohort, prompt, brand, i+1)
		exec(`INSERT INTO ai_citations (sample_id, workspace_id, cohort_id, url, host, position, owner, sampled_at) VALUES
			($1, $2, $3, $4, 'acme.example', 1, 'brand', now())`, sample, ws, cohort, url)
		if i == 0 {
			exec(`INSERT INTO ai_citations (sample_id, workspace_id, cohort_id, url, host, position, owner, sampled_at) VALUES
				($1, $2, $3, 'https://acme.example/pricing', 'acme.example', 2, 'brand', now()),
				($1, $2, $3, 'https://rival.example/guide', 'rival.example', 3, 'competitor', now())`, sample, ws, cohort)
		}
	}

	got := owner.must(200, "GET", "/v1/brands/"+brand+"/ai-visibility/pages?days=7", nil)
	sites := got["sites"].([]any)
	if len(sites) != 1 {
		t.Fatalf("sites: %v", sites)
	}
	s := sites[0].(map[string]any)
	if s["llms_txt"] != false || s["pages"] != 3.0 || !slices.Equal(toStrings(s["blocked_bots"]), []string{"OAI-SearchBot"}) ||
		!slices.Equal(toStrings(s["usage_signals"]), []string{"Content-Signal: search=yes, ai-train=no"}) {
		t.Fatalf("site: %v", s)
	}
	if got["pages_checked"] != 3.0 || got["pages_with_issues"] != 2.0 {
		t.Fatalf("counts: %v", got)
	}
	pages := got["pages"].([]any)
	if len(pages) != 2 {
		t.Fatalf("pages: %v", pages)
	}
	// The cited page first, then the one with the most AI problems; a
	// non-AI rule like title_too_short is left out.
	first, second := pages[0].(map[string]any), pages[1].(map[string]any)
	if first["url"] != "https://acme.example/" || first["answers"] != 2.0 || !slices.Equal(toStrings(first["issues"]), []string{"llms_txt_missing"}) {
		t.Fatalf("first: %v", first)
	}
	if second["url"] != "https://acme.example/guide" || !slices.Equal(toStrings(second["issues"]), []string{"ai_question_unanswered", "ai_snippet_blocked"}) ||
		second["signals"].(map[string]any)["nosnippet"] != true {
		t.Fatalf("second: %v", second)
	}
	rules := map[string]float64{}
	for _, r := range got["rules"].([]any) {
		r := r.(map[string]any)
		rules[r["id"].(string)] = r["pages"].(float64)
	}
	if len(rules) != 3 || rules["ai_snippet_blocked"] != 1 || rules["llms_txt_missing"] != 1 {
		t.Fatalf("rules: %v", rules)
	}
	elsewhere := got["cited_not_crawled"].([]any)
	if len(elsewhere) != 1 || elsewhere[0].(map[string]any)["url"] != "https://acme.example/pricing" {
		t.Fatalf("cited not crawled: %v", elsewhere)
	}
	theirs := got["competitor_pages"].([]any)
	if len(theirs) != 1 || theirs[0].(map[string]any)["url"] != "https://rival.example/guide" || theirs[0].(map[string]any)["answers"] != 1.0 || theirs[0].(map[string]any)["named_you"] != 0.0 {
		t.Fatalf("competitor pages: %v", theirs)
	}
	owner.must(400, "GET", "/v1/brands/"+brand+"/ai-visibility/pages?days=0", nil)
}

func TestAIPageReviews(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/guide":
			io.WriteString(w, `<html><head><title>Proxy guide</title></head><body><h1>Proxies</h1><p>A residential proxy sends your requests through a home connection. The fastest proxies cost $49 a month.</p></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer site.Close()
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "website": "acme.example"})
	sid := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": site.URL + "/", "brand_id": "acme"})["id"].(string)
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT brand_id::text FROM seo_sites WHERE id = $1`, sid).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	review := "/v1/brands/" + brand + "/ai-visibility/pages/review"

	// Only the brand's own pages, and only on the workspace's own AI.
	owner.must(400, "POST", review, map[string]any{"urls": []string{"https://elsewhere.example/"}})
	owner.must(400, "POST", review, map[string]any{"urls": []string{}})
	if c := owner.must(409, "POST", review, map[string]any{"urls": []string{site.URL + "/guide"}})["error"].(map[string]any)["code"]; c != "connector_needed" {
		t.Fatalf("no AI: %v", c)
	}
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})
	if q := owner.must(202, "POST", review, map[string]any{"urls": []string{site.URL + "/guide", site.URL + "/guide#top", site.URL + "/gone"}}); q["pages"] != 2.0 {
		t.Fatalf("queued: %v", q)
	}
	e.runServerJobs(t)

	reviews := map[string]map[string]any{}
	done := waitFor(t, owner, "/v1/brands/"+brand+"/ai-visibility/pages", func(m map[string]any) bool {
		for _, r := range m["reviews"].([]any) {
			if st := r.(map[string]any)["status"]; st == "queued" || st == "reading" {
				return false
			}
		}
		return true
	})
	for _, r := range done["reviews"].([]any) {
		r := r.(map[string]any)
		reviews[r["url"].(string)] = r
	}
	ok := reviews[site.URL+"/guide"]
	if ok == nil || ok["status"] != "ready" {
		t.Fatalf("reviews: %v", reviews)
	}
	res := ok["result"].(map[string]any)
	if res["score"] != 0.0 || res["answers_up_front"] != false || len(res["questions"].([]any)) != 1 || len(res["changes"].([]any)) != 1 {
		t.Fatalf("result: %v", res)
	}
	if gone := reviews[site.URL+"/gone"]; gone == nil || gone["status"] != "failed" || gone["error"] == "" {
		t.Fatalf("missing page: %v", gone)
	}
	// A low score puts the page on the fixes list, with what to change.
	var fix map[string]any
	for _, f := range owner.must(200, "GET", "/v1/brands/"+brand+"/fixes?source=ai_visibility", nil)["data"].([]any) {
		if f := f.(map[string]any); f["rule"].(map[string]any)["id"] == "ai_page_unclear" {
			fix = f
		}
	}
	if fix == nil || fix["group"] != site.URL+"/guide" || fix["count"] != 1.0 || fix["subject"].(map[string]any)["id"] != sid {
		t.Fatalf("fix: %v", fix)
	}
	p := owner.must(200, "GET", "/v1/brands/"+brand+"/fixes/"+fix["id"].(string)+"/prompt", nil)["prompt"].(string)
	if !strings.Contains(p, "Say the price in the first paragraph.") || !strings.Contains(p, "/ai-visibility/pages/review") {
		t.Fatalf("prompt: %s", p)
	}
	owner.must(409, "POST", "/v1/brands/"+brand+"/fixes/"+fix["id"].(string)+"/verify", nil)
	if !slices.ContainsFunc(got(), func(ev track.Event) bool { return ev.Name == "ai_page.reviewed" }) ||
		!slices.ContainsFunc(got(), func(ev track.Event) bool {
			return ev.Name == "ai_page.review_failed" && ev.ErrorCode == "page_unreadable"
		}) {
		t.Fatalf("events missing")
	}
}
