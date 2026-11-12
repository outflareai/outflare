package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// Candidates come only from stored link-gap snapshots. Adding a domain
// to the catalog does not call a paid provider, and the status follows
// not_in_catalog, in_catalog, then ordered.
func TestLinkOrderCandidatesFromSavedGapsOnly(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "handle": "acme"})["id"].(string)
	root := "/v1/brands/" + brand + "/link-orders"

	exec := func(q string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, q+` RETURNING id::text`, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	site := exec(`INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, 'northpeak.example', 'https://northpeak.example/', 'tok')`, ws, brand)
	_ = exec(`INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, status) VALUES ($1, $2, $3, 'rival.example', 'Rival', 'tracked')`, ws, site, brand)
	seo := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})["id"].(string)
	if _, err := e.pool.Exec(ctx, `DELETE FROM jobs WHERE kind = 'seo.sync'`); err != nil {
		t.Fatal(err)
	}
	snap := func(host, report, key, metrics string) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `INSERT INTO seo_snapshots (workspace_id, integration_id, connector, host, report, day, key, url, metrics)
			VALUES ($1, $2, 'dataforseo', $3, $4, current_date, $5, '', $6::jsonb)`, ws, seo, host, report, key, metrics); err != nil {
			t.Fatal(err)
		}
	}
	snap("rival.example", "backlinks", "videos.example", `{"domain_rating": 90, "backlinks": 12}`)
	snap("rival.example", "backlinks", "blog.example", `{"domain_rating": 30, "backlinks": 1}`)
	snap("rival.example", "backlinks", "www.already.example", `{"domain_rating": 40}`)
	if _, err := e.pool.Exec(ctx, `INSERT INTO backlinks (workspace_id, site_id, brand_id, source_url, target_url, source_domain, status)
		VALUES ($1, $2, $3, 'https://already.example/post', 'https://northpeak.example/', 'already.example', 'live')`, ws, site, brand); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "provider must not be called", http.StatusTeapot)
	}))
	t.Cleanup(fake.Close)
	if e.integrations.Client.BaseURLs == nil {
		e.integrations.Client.BaseURLs = map[string]string{}
	}
	for _, id := range []string{"dataforseo", "ahrefs", "se_ranking", "google_search_console"} {
		e.integrations.Client.BaseURLs[id] = fake.URL
	}

	before := calls.Load()
	got := owner.must(200, "GET", root+"/candidates?site_id="+site, nil)
	if calls.Load() != before {
		t.Fatalf("candidates called a provider %d times", calls.Load()-before)
	}
	rows := got["data"].([]any)
	byDomain := map[string]map[string]any{}
	for _, raw := range rows {
		m := raw.(map[string]any)
		byDomain[m["domain"].(string)] = m
	}
	if _, ok := byDomain["videos.example"]; !ok {
		t.Fatalf("missing videos.example: %v", got)
	}
	if byDomain["videos.example"]["status"] != "not_in_catalog" || byDomain["videos.example"]["traffic"] != nil {
		t.Fatalf("videos: %v", byDomain["videos.example"])
	}
	if _, ok := byDomain["already.example"]; ok {
		t.Fatalf("live backlink should not be a gap: %v", got)
	}
	if _, ok := byDomain["rival.example"]; ok {
		t.Fatalf("competitor host should not be a gap: %v", got)
	}
	owner.must(404, "GET", "/v1/brands/"+other+"/link-orders/candidates?site_id="+site, nil)
	owner.must(404, "POST", root+"/candidates/unknown.example/add-to-catalog", nil)

	added := owner.must(201, "POST", root+"/candidates/videos.example/add-to-catalog", nil)
	if added["domain"] != "videos.example" || added["source"] != "competitor_gap" {
		t.Fatalf("added: %v", added)
	}
	if calls.Load() != before {
		t.Fatalf("add-to-catalog called a provider")
	}
	again := owner.must(200, "POST", root+"/candidates/videos.example/add-to-catalog", nil)
	if again["id"] != added["id"] {
		t.Fatalf("add-to-catalog should be find-or-create: %v", again)
	}
	listed := owner.must(200, "GET", root+"/candidates", nil)
	for _, raw := range listed["data"].([]any) {
		m := raw.(map[string]any)
		if m["domain"] == "videos.example" && m["status"] != "in_catalog" {
			t.Fatalf("after add: %v", m)
		}
	}

	owner.must(201, "POST", root, map[string]any{
		"donor_profile_id": added["id"], "target_url": "https://northpeak.example/page",
	})
	listed = owner.must(200, "GET", root+"/candidates", nil)
	for _, raw := range listed["data"].([]any) {
		m := raw.(map[string]any)
		if m["domain"] == "videos.example" && m["status"] != "ordered" {
			t.Fatalf("after order: %v", m)
		}
	}
	if calls.Load() != before {
		t.Fatalf("provider was called %d times", calls.Load()-before)
	}
}

func TestLinkOrderEquivalents(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	root := "/v1/brands/" + brand + "/link-orders"
	ownerID := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	owner.must(200, "PATCH", root+"/settings", map[string]any{"approver_user_id": ownerID})

	src := owner.must(201, "POST", root+"/catalog", map[string]any{
		"domain": "source.example", "niches": []string{"hosting", "cdn"}, "domain_rating": 55,
		"prices": []map[string]any{{"currency": "USD", "amount_minor": 10000}},
	})
	good := owner.must(201, "POST", root+"/catalog", map[string]any{
		"domain": "good.example", "niches": []string{"Hosting"}, "domain_rating": 51,
		"prices": []map[string]any{{"currency": "USD", "amount_minor": 8000}},
	})
	pbn := owner.must(201, "POST", root+"/catalog", map[string]any{
		"domain": "pbn.example", "pbn": true, "niches": []string{"hosting"}, "domain_rating": 52,
		"prices": []map[string]any{{"currency": "USD", "amount_minor": 4000}},
	})
	owner.must(201, "POST", root+"/catalog", map[string]any{
		"domain": "other-niche.example", "niches": []string{"casino"}, "domain_rating": 55,
		"prices": []map[string]any{{"currency": "USD", "amount_minor": 10000}},
	})
	owner.must(201, "POST", root+"/catalog", map[string]any{
		"domain": "other-dr.example", "niches": []string{"hosting"}, "domain_rating": 70,
		"prices": []map[string]any{{"currency": "USD", "amount_minor": 10000}},
	})
	owner.must(201, "POST", root+"/catalog", map[string]any{
		"domain": "eur-only.example", "niches": []string{"hosting"}, "domain_rating": 53,
		"prices": []map[string]any{{"currency": "EUR", "amount_minor": 10000}},
	})
	archived := owner.must(201, "POST", root+"/catalog", map[string]any{
		"domain": "archived.example", "niches": []string{"hosting"}, "domain_rating": 54,
		"prices": []map[string]any{{"currency": "USD", "amount_minor": 10000}},
	})
	owner.must(200, "PATCH", root+"/catalog/items/"+archived["id"].(string), map[string]any{"archived": true})
	busy := owner.must(201, "POST", root+"/catalog", map[string]any{
		"domain": "busy.example", "niches": []string{"hosting"}, "domain_rating": 56,
		"prices": []map[string]any{{"currency": "USD", "amount_minor": 10000}},
	})

	order := owner.must(201, "POST", root, map[string]any{
		"donor_profile_id": src["id"], "target_url": "https://northpeak.example/page",
	})
	owner.must(201, "POST", root, map[string]any{
		"donor_profile_id": busy["id"], "target_url": "https://northpeak.example/page",
	})

	got := owner.must(200, "GET", root+"/"+order["id"].(string)+"/equivalents", nil)
	domains := []string{}
	sameFirst := false
	for i, raw := range got["data"].([]any) {
		m := raw.(map[string]any)
		d := m["donor"].(map[string]any)
		domains = append(domains, d["domain"].(string))
		if i == 0 && m["same_donor"] == true && d["id"] == src["id"] {
			sameFirst = true
		}
		if m["dr_band_start"] != 50.0 || m["dr_band_end"] != 59.0 {
			t.Fatalf("band: %v", m)
		}
	}
	if !sameFirst {
		t.Fatalf("same donor should be first: %v", domains)
	}
	if !containsAll(domains, "source.example", "good.example", "pbn.example") {
		t.Fatalf("missing expected donors: %v", domains)
	}
	for _, no := range []string{"other-niche.example", "other-dr.example", "eur-only.example", "archived.example", "busy.example"} {
		if containsAll(domains, no) {
			t.Fatalf("%s should be excluded: %v", no, domains)
		}
	}
	// Non-PBN matches are preferred over PBN when the source is not PBN.
	if idx := indexOf(domains, "good.example"); idx < 0 || indexOf(domains, "pbn.example") < idx {
		t.Fatalf("PBN should sort after non-PBN: %v", domains)
	}
	_ = pbn
	_ = good
}

// The live coverage.check job writes last_check_*; a passing result is
// what opens pay. One conclusive failure is not enough to mark the order
// lost; two in a row are, and a replacement task is opened.
func TestLinkOrderLiveCheckJobWritesLastCheckAndTwoFailuresLose(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	root := "/v1/brands/" + brand + "/link-orders"
	ownerID := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	owner.must(200, "PATCH", root+"/settings", map[string]any{"approver_user_id": ownerID})
	owner.must(201, "POST", "/v1/brands/"+brand+"/coverage/budgets", map[string]any{
		"title": "Link orders USD", "currency": "USD", "amount_minor": 50000,
		"starts_on": "2000-01-01", "ends_on": "2099-12-31", "tags": "link_orders",
	})
	donor := owner.must(201, "POST", root+"/catalog", map[string]any{
		"domain": "donor.example",
		"prices": []map[string]any{{"currency": "USD", "amount_minor": 15000}},
	})
	order := owner.must(201, "POST", root, map[string]any{
		"donor_profile_id": donor["id"], "target_url": "https://northpeak.example/page",
	})
	path := root + "/" + order["id"].(string)
	owner.must(200, "POST", path+"/quote", map[string]any{"amount_minor": 15000, "currency": "USD"})
	owner.must(200, "POST", path+"/approve", nil)

	page := &linkOrderPage{}
	page.status.Store(200)
	page.href.Store("https://northpeak.example/page")
	e.crawl.Fetcher = page
	e.crawl.Wait = func(context.Context, time.Duration) error { return nil }
	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 10 * time.Millisecond
	e.srv.RegisterJobs(q)
	run, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = q.Run(run, 1); close(done) }()
	defer func() { cancel(); <-done }()

	pub := owner.must(200, "POST", path+"/publish", map[string]any{"published_url": "https://donor.example/post"})
	if pub["placement_id"] == nil {
		t.Fatalf("published: %v", pub)
	}
	waitOrder(t, owner, path, func(o map[string]any) bool {
		return o["last_check_live"] == true && o["last_check_target_match"] == true
	})
	checked := owner.must(200, "GET", path, nil)
	if checked["last_check_rel"] == nil || checked["last_check_anchor"] == "" {
		t.Fatalf("check fields: %v", checked)
	}
	paid := owner.must(200, "POST", path+"/pay", nil)
	if paid["status"] != "paid" {
		t.Fatalf("pay after job: %v", paid)
	}

	page.noLink.Store(true)
	owner.must(202, "POST", path+"/check", nil)
	waitOrder(t, owner, path, func(o map[string]any) bool {
		return o["last_check_live"] == false && o["status"] == "paid"
	})
	// One failure is not enough.
	if got := owner.must(200, "GET", path, nil); got["status"] != "paid" {
		t.Fatalf("one failure lost the order: %v", got)
	}
	owner.must(202, "POST", path+"/check", nil)
	waitOrder(t, owner, path, func(o map[string]any) bool { return o["status"] == "lost" })

	var evidence string
	if err := e.pool.QueryRow(ctx, `SELECT evidence FROM coverage_tasks WHERE workspace_id=$1 AND evidence=$2 AND status='open'`,
		ws, "link_order_replace:"+order["id"].(string)).Scan(&evidence); err != nil || evidence == "" {
		t.Fatalf("replacement task: %v %s", err, evidence)
	}
	eq := owner.must(200, "GET", path+"/equivalents", nil)["data"].([]any)
	if len(eq) == 0 || eq[0].(map[string]any)["same_donor"] != true {
		t.Fatalf("same donor should lead replacement suggestions: %v", eq)
	}
}

func TestLinkOrderImpactReadsStoredMetricsOnly(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	root := "/v1/brands/" + brand + "/link-orders"
	ownerID := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	owner.must(200, "PATCH", root+"/settings", map[string]any{"approver_user_id": ownerID})
	owner.must(201, "POST", "/v1/brands/"+brand+"/coverage/budgets", map[string]any{
		"title": "USD", "currency": "USD", "amount_minor": 50000,
		"starts_on": "2000-01-01", "ends_on": "2099-12-31", "tags": "link_orders",
	})
	donor := owner.must(201, "POST", root+"/catalog", map[string]any{
		"domain": "donor.example",
		"prices": []map[string]any{{"currency": "USD", "amount_minor": 1000}},
	})
	order := owner.must(201, "POST", root, map[string]any{
		"donor_profile_id": donor["id"], "target_url": "https://northpeak.example/page",
	})
	path := root + "/" + order["id"].(string)
	owner.must(409, "GET", path+"/impact", nil)
	owner.must(200, "POST", path+"/quote", map[string]any{"amount_minor": 1000, "currency": "USD"})
	owner.must(200, "POST", path+"/approve", nil)
	pub := owner.must(200, "POST", path+"/publish", map[string]any{"published_url": "https://donor.example/post"})
	publishedAt, err := time.Parse(time.RFC3339, pub["published_at"].(string))
	if err != nil {
		t.Fatal(err)
	}

	seo := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})["id"].(string)
	if _, err := e.pool.Exec(ctx, `DELETE FROM jobs WHERE kind = 'seo.sync'`); err != nil {
		t.Fatal(err)
	}
	page := "https://northpeak.example/page"
	beforeDay := publishedAt.UTC().AddDate(0, 0, -3)
	afterDay := publishedAt.UTC().AddDate(0, 0, 2)
	ins := func(day time.Time, clicks, impressions int, pos float64) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `INSERT INTO search_daily (workspace_id, integration_id, site, host, day, page, query, clicks, impressions, ctr, position)
			VALUES ($1, $2, 'sc-domain:northpeak.example', 'northpeak.example', $3, $4, 'proxy', $5, $6, 0.1, $7)`,
			ws, seo, day, page, clicks, impressions, pos); err != nil {
			t.Fatal(err)
		}
	}
	ins(beforeDay, 10, 100, 8)
	ins(afterDay, 20, 200, 5)
	if _, err := e.pool.Exec(ctx, `INSERT INTO seo_snapshots (workspace_id, integration_id, connector, host, report, day, key, url, metrics)
		VALUES ($1, $2, 'dataforseo', 'northpeak.example', 'keywords', $3, 'proxy', $4, '{"position": 6}'::jsonb)`,
		ws, seo, afterDay, page); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "provider must not be called", http.StatusTeapot)
	}))
	t.Cleanup(fake.Close)
	if e.integrations.Client.BaseURLs == nil {
		e.integrations.Client.BaseURLs = map[string]string{}
	}
	for _, id := range []string{"dataforseo", "ahrefs", "se_ranking", "google_search_console"} {
		e.integrations.Client.BaseURLs[id] = fake.URL
	}

	before := calls.Load()
	got := owner.must(200, "GET", path+"/impact", nil)
	if calls.Load() != before {
		t.Fatalf("impact called a provider")
	}
	if got["causality"] != false || got["note"] == "" || got["target_url"] != page {
		t.Fatalf("impact: %v", got)
	}
	sc := got["search_console"].(map[string]any)
	if sc["available"] != true || sc["baseline"] == nil || sc["after"] == nil {
		t.Fatalf("search console: %v", sc)
	}
	if sc["baseline"].(map[string]any)["clicks"] != 10.0 || sc["after"].(map[string]any)["clicks"] != 20.0 {
		t.Fatalf("totals: %v", sc)
	}
	ranks := got["rankings"].(map[string]any)
	if ranks["available"] != true || len(ranks["series"].([]any)) != 1 {
		t.Fatalf("rankings: %v", ranks)
	}
}

func TestLinkOrderRemindersAndScheduledChecks(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	root := "/v1/brands/" + brand + "/link-orders"
	ownerID := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	owner.must(200, "PATCH", root+"/settings", map[string]any{"approver_user_id": ownerID})
	owner.must(201, "POST", "/v1/brands/"+brand+"/coverage/budgets", map[string]any{
		"title": "USD", "currency": "USD", "amount_minor": 50000,
		"starts_on": "2000-01-01", "ends_on": "2099-12-31", "tags": "link_orders",
	})
	donor := owner.must(201, "POST", root+"/catalog", map[string]any{
		"domain": "donor.example", "tat_days": 7,
		"prices": []map[string]any{{"currency": "USD", "amount_minor": 1000}},
	})

	overdue := owner.must(201, "POST", root, map[string]any{
		"donor_profile_id": donor["id"], "target_url": "https://northpeak.example/overdue",
	})
	overduePath := root + "/" + overdue["id"].(string)
	owner.must(200, "POST", overduePath+"/quote", map[string]any{"amount_minor": 1000, "currency": "USD"})
	owner.must(200, "POST", overduePath+"/approve", nil)
	if _, err := e.pool.Exec(ctx, `UPDATE coverage_link_orders SET approved_at = now() - interval '20 days' WHERE id = $1`, overdue["id"]); err != nil {
		t.Fatal(err)
	}

	pay := owner.must(201, "POST", root, map[string]any{
		"donor_profile_id": donor["id"], "target_url": "https://northpeak.example/pay",
	})
	payPath := root + "/" + pay["id"].(string)
	owner.must(200, "POST", payPath+"/quote", map[string]any{"amount_minor": 1000, "currency": "USD"})
	owner.must(200, "POST", payPath+"/approve", nil)
	pub := owner.must(200, "POST", payPath+"/publish", map[string]any{"published_url": "https://donor.example/pay"})
	if _, err := e.pool.Exec(ctx, `UPDATE coverage_link_orders SET last_check_at = now(), last_check_live = true, last_check_target_match = true WHERE id = $1`, pay["id"]); err != nil {
		t.Fatal(err)
	}
	// Keep the placement from being selected by the regular due-check,
	// so a later enqueue comes from the link-order scheduler.
	if _, err := e.pool.Exec(ctx, `UPDATE coverage_placements SET next_check_at = now() + interval '30 days' WHERE id = $1`, pub["placement_id"]); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE coverage_link_orders SET last_check_at = now() - interval '8 days', guarantee_until = now() + interval '20 days' WHERE id = $1`, pay["id"]); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `DELETE FROM jobs WHERE kind = 'coverage.check'`); err != nil {
		t.Fatal(err)
	}

	if _, err := e.srv.QueueDueCoverage(ctx); err != nil {
		t.Fatal(err)
	}
	var payTask, overdueTask int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM coverage_tasks WHERE workspace_id=$1 AND evidence=$2 AND status='open'`,
		ws, "link_order_pay:"+pay["id"].(string)).Scan(&payTask); err != nil || payTask != 1 {
		t.Fatalf("pay reminder: %d %v", payTask, err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM coverage_tasks WHERE workspace_id=$1 AND evidence=$2 AND status='open'`,
		ws, "link_order_overdue:"+overdue["id"].(string)).Scan(&overdueTask); err != nil || overdueTask != 1 {
		t.Fatalf("overdue task: %d %v", overdueTask, err)
	}
	var queued int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind='coverage.check' AND payload->>'placement_id'=$1 AND status IN ('queued','running')`,
		pub["placement_id"]).Scan(&queued); err != nil || queued < 1 {
		t.Fatalf("scheduled check: %d %v", queued, err)
	}
	// Idempotent on a second pass.
	if _, err := e.srv.QueueDueCoverage(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM coverage_tasks WHERE workspace_id=$1 AND evidence=$2 AND status='open'`,
		ws, "link_order_pay:"+pay["id"].(string)).Scan(&payTask); err != nil || payTask != 1 {
		t.Fatalf("pay reminder duplicated: %d %v", payTask, err)
	}
}

type linkOrderPage struct {
	status atomic.Int32
	noLink atomic.Bool
	href   atomic.Value
}

func (f *linkOrderPage) Fetch(ctx context.Context, url string) (*crawl.Response, error) {
	if strings.HasSuffix(url, "/robots.txt") {
		return &crawl.Response{URL: url, Status: 200, Body: []byte("User-agent: *\nAllow: /"), Header: http.Header{"Content-Type": []string{"text/plain"}}}, nil
	}
	status := int(f.status.Load())
	if status == 0 {
		return nil, context.DeadlineExceeded
	}
	href, _ := f.href.Load().(string)
	if href == "" {
		href = "https://northpeak.example/page"
	}
	body := `<html><head><title>Review</title></head><body>Northpeak. <a href="` + href + `" rel="sponsored nofollow">Northpeak</a></body></html>`
	if f.noLink.Load() {
		body = `<html><body>No link here.</body></html>`
	}
	return &crawl.Response{URL: url, Status: status, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: []byte(body)}, nil
}

func waitOrder(t *testing.T, c *client, path string, ok func(map[string]any) bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last map[string]any
	for time.Now().Before(deadline) {
		last = c.must(200, "GET", path, nil)
		if ok(last) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("order did not reach expected state: %v", last)
}

func containsAll(have []string, want ...string) bool {
	set := map[string]bool{}
	for _, h := range have {
		set[h] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

func indexOf(have []string, want string) int {
	for i, h := range have {
		if h == want {
			return i
		}
	}
	return -1
}
