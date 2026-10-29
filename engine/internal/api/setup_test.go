package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// runServerJobs works the API server's own jobs until the test ends.
func (e *env) runServerJobs(t *testing.T) {
	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 20 * time.Millisecond
	e.srv.RegisterJobs(q)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { q.Run(ctx, 2); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })
}

func setupSteps(o map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, s := range o["steps"].([]any) {
		st := s.(map[string]any)
		out[st["id"].(string)] = st
	}
	return out
}

func TestBrandSetupChecklist(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	b := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "website": "northpeak.example"})
	setup := base + "/brands/" + b["handle"].(string) + "/setup"

	// A new brand has every step to do, what it does first.
	o := owner.must(200, "GET", setup, nil)
	order := []string{}
	for _, s := range o["steps"].([]any) {
		order = append(order, s.(map[string]any)["id"].(string))
	}
	if strings.Join(order, ",") != "business_model,site_script,verify_site,analytics,search,email,payments,ads,social,crm" || o["next_step"] != "business_model" || o["done"] != 0.0 || o["complete"] != false {
		t.Fatalf("new brand: %v", o)
	}

	// Saying what the brand does: a model from the list, changeable any
	// time, and each change is recorded for the report packs.
	models := owner.must(200, "GET", "/v1/business-models", nil)
	if len(models["models"].([]any)) < 30 || len(models["groups"].([]any)) != 8 || len(models["revenue_models"].([]any)) != 7 {
		t.Fatalf("business models: %v", models)
	}
	brandURL := base + "/brands/" + b["handle"].(string)
	owner.must(400, "PATCH", brandURL, map[string]any{"business_model": "pyramid_scheme"})
	owner.must(400, "PATCH", brandURL, map[string]any{"revenue_models": []string{"barter"}})
	nb := owner.must(200, "PATCH", brandURL, map[string]any{"business_model": "b2b_saas_self_serve", "revenue_models": []string{"subscription", "usage", "subscription"}})
	if nb["business_model"] != "b2b_saas_self_serve" || len(nb["revenue_models"].([]any)) != 2 {
		t.Fatalf("brand after picking a model: %v", nb)
	}
	owner.must(200, "PATCH", brandURL, map[string]any{"business_model": "marketplace"})
	if ev := named(got(), "brand.business_model_changed"); len(ev) != 2 {
		t.Fatalf("model change events: %v", ev)
	}
	var change string
	if err := e.pool.QueryRow(context.Background(), `SELECT (data->>'from') || '>' || (data->>'to') FROM audit_events
		WHERE action = 'brand.business_model_changed' ORDER BY created_at DESC LIMIT 1`).Scan(&change); err != nil || change != "b2b_saas_self_serve>marketplace" {
		t.Fatalf("model change audit: %q %v", change, err)
	}
	o = owner.must(200, "GET", setup, nil)
	if o["business_model"] != "marketplace" || setupSteps(o)["business_model"]["done"] != true || o["next_step"] != "site_script" || o["done"] != 1.0 {
		t.Fatalf("after picking a model: %v", o)
	}

	st := setupSteps(o)
	in := st["site_script"]["install"].(map[string]any)
	if in["site_key_id"] != nil || in["snippet"] != nil || in["connector"] != "google_tag_manager" || !strings.Contains(in["ai_prompt"].(string), "<head>") {
		t.Fatalf("site script before prepare: %v", in)
	}
	if st["analytics"]["install"] != nil || st["analytics"]["connectors"].([]any)[0] != "google_analytics" || st["analytics"]["status"] != "todo" {
		t.Fatalf("analytics step: %v", st["analytics"])
	}

	// Preparing makes the brand's site key once; its snippet goes in every
	// way to install it.
	owner.must(400, "POST", setup+"/prepare", map[string]any{"step": "analytics"})
	o = owner.must(200, "POST", setup+"/prepare", map[string]any{"step": "site_script"})
	in = setupSteps(o)["site_script"]["install"].(map[string]any)
	key := regexp.MustCompile(`ofk_[A-Za-z0-9_-]+`).FindString(in["snippet"].(string))
	if key == "" || !strings.Contains(in["ai_prompt"].(string), key) || !strings.Contains(in["developer"].(map[string]any)["body"].(string), key) {
		t.Fatalf("prepared: %v", in)
	}
	again := owner.must(200, "POST", setup+"/prepare", map[string]any{"step": "site_script"})
	if setupSteps(again)["site_script"]["install"].(map[string]any)["site_key_id"] != in["site_key_id"] {
		t.Fatal("preparing twice made a second key")
	}
	if keys := owner.must(200, "GET", base+"/site-keys", nil)["data"].([]any); len(keys) != 1 || keys[0].(map[string]any)["brand_id"] != b["id"] {
		t.Fatalf("site keys: %v", keys)
	}

	// The first visit ticks it off, and the script's version is kept.
	current := siteScriptVersion(t)
	b2, _ := json.Marshal(map[string]any{"key": key, "v": current, "batch": []any{map[string]any{"type": "page", "anonymousId": "a-1", "messageId": "m-1",
		"context": map[string]any{"page": map[string]any{"url": "https://northpeak.example/", "path": "/"}}}}})
	req := httptest.NewRequest("POST", "/v1/site/events", strings.NewReader(string(b2)))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("User-Agent", desktopUA)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("site event: %d %s", rec.Code, rec.Body)
	}
	var version string
	if err := e.pool.QueryRow(context.Background(), `SELECT script_version FROM site_keys WHERE key = $1`, key).Scan(&version); err != nil || version != current {
		t.Fatalf("script version %q %v", version, err)
	}
	o = owner.must(200, "GET", setup, nil)
	if setupSteps(o)["site_script"]["status"] != "done" || o["next_step"] != "verify_site" || o["done"] != 2.0 {
		t.Fatalf("after a visit: %v", o)
	}

	// Verifying: prepare adds the website for site audits; checking looks
	// for the record now and keeps looking.
	o = owner.must(200, "POST", setup+"/prepare", map[string]any{"step": "verify_site"})
	vin := setupSteps(o)["verify_site"]["install"].(map[string]any)
	rec2 := vin["record"].(map[string]any)
	if rec2["type"] != "TXT" || rec2["name"] != "northpeak.example" || !strings.HasPrefix(rec2["content"].(string), "outflare-site-verification=") ||
		!strings.Contains(vin["ai_prompt"].(string), rec2["content"].(string)) {
		t.Fatalf("verify install: %v", vin)
	}
	o = owner.must(200, "POST", setup+"/check", map[string]any{"step": "verify_site"})
	if s := setupSteps(o)["verify_site"]; s["status"] != "waiting" || s["done"] != false {
		t.Fatalf("waiting for DNS: %v", s)
	}
	e.dns.setTXT("northpeak.example", []string{rec2["content"].(string)})
	if _, err := e.pool.Exec(context.Background(), `UPDATE seo_sites SET verify_checked_at = now() - interval '2 minutes'`); err != nil {
		t.Fatal(err)
	}
	e.srv.CheckInstalls(context.Background()) // the minute check finds it
	o = owner.must(200, "GET", setup, nil)
	if setupSteps(o)["verify_site"]["status"] != "done" || o["next_step"] != "analytics" {
		t.Fatalf("after the record appeared: %v", o)
	}

	// Connecting a tool anywhere ticks its step; Search Console is the
	// search step, not analytics.
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})
	if o = owner.must(200, "GET", setup, nil); setupSteps(o)["analytics"]["done"] != true || setupSteps(o)["search"]["done"] != false {
		t.Fatalf("after analytics: %v", o)
	}

	// Skips, closing and the events they record.
	owner.must(400, "PATCH", setup, map[string]any{"skip": []string{"billing"}})
	o = owner.must(200, "PATCH", setup, map[string]any{"skip": []string{"search", "search"}, "dismissed": true})
	if setupSteps(o)["search"]["status"] != "skipped" || o["next_step"] != "email" || o["dismissed"] != true {
		t.Fatalf("after skipping: %v", o)
	}
	o = owner.must(200, "PATCH", setup, map[string]any{"unskip": []string{"search"}, "dismissed": false})
	if o["next_step"] != "search" || o["dismissed"] != false {
		t.Fatalf("after unskipping: %v", o)
	}
	for _, name := range []string{"brand_setup.step_skipped", "brand_setup.dismissed", "brand_setup.step_unskipped", "brand_setup.reopened", "seo_site.verified"} {
		if len(named(got(), name)) == 0 {
			t.Errorf("no %s event", name)
		}
	}

	// Install health: nothing wrong yet, with what was looked at.
	h := owner.must(200, "GET", base+"/install-health?brand="+b["handle"].(string), nil)
	if len(h["checks"].([]any)) != 0 || h["checked"].(map[string]any)["site_keys"] != 1.0 || h["latest"].(map[string]any)["site_script"] != current {
		t.Fatalf("health: %v", h)
	}
}

func TestBrandSetupOneClickInstalls(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	b := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "website": "https://www.northpeak.example"})
	setup := base + "/brands/" + b["handle"].(string) + "/setup"
	e.runServerJobs(t)

	// Cloudflare: one zone; the TXT record is added once.
	var mu sync.Mutex
	var cfRecords []map[string]any
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cf_test_not_a_real_token" {
			w.WriteHeader(403)
			io.WriteString(w, `{"success":false}`)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/zones":
			name := r.URL.Query().Get("name")
			if name == "" || name == "northpeak.example" {
				json.NewEncoder(w).Encode(map[string]any{"result": []any{map[string]any{"id": "z1", "name": "northpeak.example", "account": map[string]string{"name": "Northpeak"}}}})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"result": []any{}})
		case r.URL.Path == "/zones/z1/dns_records" && r.Method == "GET":
			out := []any{}
			for _, rec := range cfRecords {
				if rec["name"] == r.URL.Query().Get("name") && rec["type"] == r.URL.Query().Get("type") {
					out = append(out, rec)
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"result": out})
		case r.URL.Path == "/zones/z1/dns_records" && r.Method == "POST":
			var rec map[string]any
			json.NewDecoder(r.Body).Decode(&rec)
			cfRecords = append(cfRecords, rec)
			json.NewEncoder(w).Encode(map[string]any{"result": rec})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(cf.Close)
	e.integrations.Client.BaseURLs["cloudflare"] = cf.URL
	cfConn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "cloudflare", "credentials": map[string]string{"api_token": "cf_test_not_a_real_token"}})

	// A connection that can't add DNS records is refused.
	plausible := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})
	owner.must(400, "POST", setup+"/install", map[string]any{"step": "verify_site", "method": "dns", "integration_id": plausible["id"]})
	owner.must(400, "POST", setup+"/install", map[string]any{"step": "verify_site", "method": "gtm", "integration_id": cfConn["id"]})

	owner.must(202, "POST", setup+"/install", map[string]any{"step": "verify_site", "method": "dns", "integration_id": cfConn["id"]})
	o := waitFor(t, owner, setup, func(o map[string]any) bool {
		last, _ := setupSteps(o)["verify_site"]["install"].(map[string]any)["last"].(map[string]any)
		return last != nil && last["status"] != "queued"
	})
	vin := setupSteps(o)["verify_site"]["install"].(map[string]any)
	if last := vin["last"].(map[string]any); last["status"] != "done" || last["method"] != "dns" {
		t.Fatalf("dns install: %v", vin)
	}
	mu.Lock()
	if len(cfRecords) != 1 || cfRecords[0]["type"] != "TXT" || cfRecords[0]["name"] != "northpeak.example" ||
		cfRecords[0]["content"] != `"`+vin["record"].(map[string]any)["content"].(string)+`"` || cfRecords[0]["comment"] != "Added by Outflare" {
		t.Fatalf("records: %v", cfRecords)
	}
	mu.Unlock()
	if s := setupSteps(o)["verify_site"]; s["status"] != "waiting" {
		t.Fatalf("watching for the record: %v", s)
	}
	// Cloudflare's record is what the background check then finds.
	e.dns.setTXT("northpeak.example", []string{vin["record"].(map[string]any)["content"].(string)})
	e.srv.CheckInstalls(ctx)
	if s := setupSteps(owner.must(200, "GET", setup, nil))["verify_site"]; s["status"] != "done" {
		t.Fatalf("verified: %v", s)
	}

	// Tag Manager: the tag goes into a workspace of Outflare's own, then a
	// version is published.
	var calls []string
	var tagHTML string
	gtm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		j := func(v any) { json.NewEncoder(w).Encode(v) }
		switch {
		case r.URL.Path == "/v1/userinfo":
			j(map[string]string{"sub": "1", "email": "ana@northpeak.example"})
		case r.URL.Path == "/accounts":
			j(map[string]any{"account": []any{map[string]string{"path": "accounts/1", "accountId": "1", "name": "Northpeak"}}})
		case r.URL.Path == "/accounts/1/containers":
			j(map[string]any{"container": []any{
				map[string]any{"path": "accounts/1/containers/3", "name": "Blog", "publicId": "GTM-BLOG", "domainName": []string{"blog.example"}, "usageContext": []string{"web"}},
				map[string]any{"path": "accounts/1/containers/2", "name": "Main site", "publicId": "GTM-MAIN", "domainName": []string{"northpeak.example"}, "usageContext": []string{"web"}},
				map[string]any{"path": "accounts/1/containers/4", "name": "App", "publicId": "GTM-APP", "usageContext": []string{"android"}},
			}})
		case r.URL.Path == "/accounts/1/containers/2/workspaces" && r.Method == "GET":
			j(map[string]any{"workspace": []any{map[string]string{"path": "accounts/1/containers/2/workspaces/1", "name": "Default Workspace"}}})
		case r.URL.Path == "/accounts/1/containers/2/workspaces" && r.Method == "POST":
			j(map[string]string{"path": "accounts/1/containers/2/workspaces/9", "workspaceId": "9", "name": "Outflare website script"})
		case r.URL.Path == "/accounts/1/containers/2/workspaces/9/tags" && r.Method == "GET":
			j(map[string]any{"tag": []any{}})
		case r.URL.Path == "/accounts/1/containers/2/workspaces/9/tags" && r.Method == "POST":
			var tg struct {
				Parameter []struct{ Key, Value string }
			}
			json.NewDecoder(r.Body).Decode(&tg)
			for _, p := range tg.Parameter {
				if p.Key == "html" {
					tagHTML = p.Value
				}
			}
			j(map[string]string{"path": "accounts/1/containers/2/workspaces/9/tags/1"})
		case r.URL.Path == "/accounts/1/containers/2/workspaces/9:create_version":
			j(map[string]any{"containerVersion": map[string]string{"path": "accounts/1/containers/2/versions/5", "containerVersionId": "5"}})
		case r.URL.Path == "/accounts/1/containers/2/versions/5:publish":
			j(map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(gtm.Close)
	e.integrations.Client.BaseURLs["google_tag_manager"] = gtm.URL
	e.integrations.Client.BaseURLs["google.userinfo"] = gtm.URL
	var gtmID string
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		i, err := e.integrations.Insert(ctx, tx, ws, "google_tag_manager", connectors.AuthOAuth2, "Tag Manager", connectors.Creds{"access_token": "gt"}, connectors.Account{}, "")
		gtmID = i.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	cs := owner.must(200, "GET", setup+"/tag-containers?integration_id="+gtmID, nil)["data"].([]any)
	if len(cs) != 2 || cs[0].(map[string]any)["public_id"] != "GTM-MAIN" || cs[0].(map[string]any)["matches_website"] != true {
		t.Fatalf("containers, the website's first and no app containers: %v", cs)
	}
	owner.must(400, "POST", setup+"/install", map[string]any{"step": "site_script", "method": "gtm", "integration_id": gtmID})
	owner.must(202, "POST", setup+"/install", map[string]any{"step": "site_script", "method": "gtm", "integration_id": gtmID, "container": "accounts/1/containers/2"})
	o = waitFor(t, owner, setup, func(o map[string]any) bool {
		last, _ := setupSteps(o)["site_script"]["install"].(map[string]any)["last"].(map[string]any)
		return last != nil && last["status"] != "queued"
	})
	sin := setupSteps(o)["site_script"]["install"].(map[string]any)
	last := sin["last"].(map[string]any)
	if last["status"] != "done" || last["published"] != true || !strings.Contains(last["link"].(string), "/versions/5") {
		t.Fatalf("gtm install: %v", sin)
	}
	mu.Lock()
	if tagHTML != sin["snippet"] || strings.Contains(strings.Join(calls, ","), "workspaces/1/") {
		t.Fatalf("tag %q, calls %v", tagHTML, calls)
	}
	mu.Unlock()
	var actions []string
	rows, _ := e.pool.Query(ctx, `SELECT action FROM audit_events WHERE workspace_id = $1 AND action LIKE 'brand_setup.%' ORDER BY id`, ws)
	for rows.Next() {
		var a string
		rows.Scan(&a)
		actions = append(actions, a)
	}
	if strings.Join(actions, ",") != "brand_setup.install_started,brand_setup.installed,brand_setup.install_started,brand_setup.installed" {
		t.Fatalf("audit: %v", actions)
	}
}

func TestInstallHealthFindsProblems(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	// A key nobody installed, one gone quiet, and one running an old copy.
	for _, name := range []string{"never.example", "quiet.example", "old.example"} {
		owner.must(201, "POST", base+"/site-keys", map[string]any{"name": name})
	}
	for _, q := range []string{
		`UPDATE site_keys SET created_at = now() - interval '3 days' WHERE name = 'never.example'`,
		`UPDATE site_keys SET last_used_at = now() - interval '3 days', script_version = '2' WHERE name = 'quiet.example'`,
		`UPDATE site_keys SET last_used_at = now(), script_version = '' WHERE name = 'old.example'`,
	} {
		if _, err := e.pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	// An old MCP server and a deprecated route in the request logs.
	if _, err := e.pool.Exec(ctx, `
		INSERT INTO request_logs (workspace_id, request_id, method, route, operation, path, status, duration_ms, surface, client)
		VALUES ($1, 'r1', 'GET', 'x', 'listLeads', '/x', 200, 1, 'mcp', 'mcp/0.0.9'),
		       ($1, 'r2', 'GET', 'x', 'getMailboxWarmup', '/x', 200, 1, 'api', '')`, ws); err != nil {
		t.Fatal(err)
	}

	h := owner.must(200, "GET", base+"/install-health", nil)
	kinds := map[string]string{}
	for _, c := range h["checks"].([]any) {
		c := c.(map[string]any)
		kinds[c["kind"].(string)] = c["severity"].(string)
	}
	want := map[string]string{"site_script_missing": "warning", "site_script_silent": "critical", "site_script_outdated": "warning", "client_outdated": "critical", "api_deprecated": "warning"}
	for k, sev := range want {
		if kinds[k] != sev {
			t.Errorf("%s: %q, want %q (%v)", k, kinds[k], sev, h["checks"])
		}
	}
	if h["checks"].([]any)[0].(map[string]any)["severity"] != "critical" || h["checks"].([]any)[4].(map[string]any)["severity"] != "warning" {
		t.Errorf("worst first: %v", h["checks"])
	}

	// The loop opens each once, they reach /status, and fixing one closes it.
	got := track.Capture(t)
	e.srv.CheckInstalls(ctx)
	e.srv.CheckInstalls(ctx)
	if n := len(named(got(), "install.issue_opened")); n != 5 {
		t.Fatalf("opened %d issues", n)
	}
	codes := map[string]bool{}
	for _, b := range owner.must(200, "GET", base+"/status", nil)["blockers"].([]any) {
		codes[b.(map[string]any)["code"].(string)] = true
	}
	if !codes["install_broken"] || !codes["install_outdated"] {
		t.Fatalf("status: %v", codes)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE site_keys SET last_used_at = now() WHERE name = 'quiet.example'`); err != nil {
		t.Fatal(err)
	}
	e.srv.CheckInstalls(ctx)
	if n := len(named(got(), "install.issue_resolved")); n != 1 {
		t.Fatalf("resolved %d issues", n)
	}
	if since := owner.must(200, "GET", base+"/install-health", nil)["checks"].([]any)[0].(map[string]any)["since"]; since == nil {
		t.Fatal("no since on a known issue")
	}
}

// siteScriptVersion is the VERSION the served site.js sends.
func siteScriptVersion(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("site.js")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`VERSION = "([^"]+)"`).FindSubmatch(b)
	if m == nil {
		t.Fatal("site.js has no VERSION")
	}
	return string(m[1])
}
