package api_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/seosync"
	"github.com/outflareai/outflare/engine/internal/track"
)

// A competitor's page shows everything stored about its search (keywords
// beside yours, top pages, linking sites, newest sitemap pages), "Read
// their search data" reads what isn't fresh on the brand's own SEO
// connection after showing the cost, and a link gap opens the exact pages
// on that site linking to each competitor.
func TestCompetitorSearchAndLinkGapPages(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	exec := func(q string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, q+` RETURNING id::text`, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	site := exec(`INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, 'northpeak.example', 'https://northpeak.example/', 'tok')`, ws, brand)
	rival := exec(`INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, status) VALUES ($1, $2, $3, 'rival.example', 'Rival', 'tracked')`, ws, site, brand)
	maybe := exec(`INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, status) VALUES ($1, $2, $3, 'maybe.example', 'Maybe', 'suggested')`, ws, site, brand)
	root := "/v1/brands/" + brand + "/competition/"

	// Nothing read and no SEO connection.
	got := owner.must(200, "GET", root+rival+"/search", nil)
	if got["host"] != "rival.example" || got["keywords"].(map[string]any)["read_on"] != nil || len(got["keywords"].(map[string]any)["rows"].([]any)) != 0 || got["seo_integration_id"] != nil {
		t.Fatalf("empty: %v", got)
	}
	if got := owner.must(409, "POST", root+rival+"/read", nil); got["error"].(map[string]any)["code"] != "connector_needed" {
		t.Fatalf("no SEO: %v", got)
	}
	owner.must(409, "POST", root+maybe+"/read", nil)
	owner.must(404, "GET", root+"00000000-0000-0000-0000-000000000000/search", nil)

	seo := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})["id"].(string)
	if _, err := e.pool.Exec(ctx, `DELETE FROM jobs WHERE kind = 'seo.sync'`); err != nil {
		t.Fatal(err)
	}
	// The weekly read stored their keywords and linking sites, and yours.
	snap := func(host, report, key, url, metrics string) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `INSERT INTO seo_snapshots (workspace_id, integration_id, connector, host, report, day, key, url, metrics)
			VALUES ($1, $2, 'dataforseo', $3, $4, current_date, $5, $6, $7::jsonb)`, ws, seo, host, report, key, url, metrics); err != nil {
			t.Fatal(err)
		}
	}
	snap("rival.example", "keywords", "proxy pricing", "https://rival.example/pricing", `{"position": 2, "volume": 900, "traffic": 300}`)
	snap("rival.example", "keywords", "proxy guide", "https://rival.example/guide", `{"position": 7, "volume": 400, "traffic": 20}`)
	snap("northpeak.example", "keywords", "proxy pricing", "https://northpeak.example/pricing", `{"position": 11}`)
	snap("rival.example", "backlinks", "videos.example", "", `{"domain_rating": 90, "backlinks": 12}`)
	snap("rival.example", "backlinks", "blog.example", "", `{"domain_rating": 30, "backlinks": 1}`)
	if _, err := e.pool.Exec(ctx, `INSERT INTO competitor_pages (workspace_id, competitor_id, url, lastmod) VALUES ($1, $2, 'https://rival.example/new', '2026-11-01')`, ws, rival); err != nil {
		t.Fatal(err)
	}
	got = owner.must(200, "GET", root+rival+"/search", nil)
	kws := got["keywords"].(map[string]any)["rows"].([]any)
	first := kws[0].(map[string]any)
	if len(kws) != 2 || first["keyword"] != "proxy pricing" || first["position"] != 2.0 || first["your_position"] != 11.0 || kws[1].(map[string]any)["your_position"] != nil {
		t.Fatalf("keywords: %v", kws)
	}
	sites := got["linking_sites"].(map[string]any)["rows"].([]any)
	if len(sites) != 2 || sites[0].(map[string]any)["domain"] != "videos.example" || got["pages"].(map[string]any)["read_on"] != nil ||
		got["sitemap"].(map[string]any)["total"] != 1.0 || got["seo_integration_id"] != seo {
		t.Fatalf("search: %v", got)
	}

	// The cost first: keywords and linking sites are fresh, so only the
	// numbers and top pages are read.
	est := owner.must(200, "POST", root+rival+"/read", map[string]any{"estimate_only": true})
	if est["queued"] != false || len(est["reports"].([]any)) != 2 || len(est["fresh"].([]any)) != 2 || est["estimate"].(float64) <= 0 || est["unit"] != "USD" {
		t.Fatalf("estimate: %v", est)
	}
	if got := owner.must(202, "POST", root+rival+"/read", nil); got["queued"] != true {
		t.Fatalf("read: %v", got)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	(&seosync.Syncer{Pool: e.pool, Store: e.integrations, Log: log}).Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 1); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(15 * time.Second)
	for {
		var left int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE workspace_id = $1 AND kind = 'seo.sync' AND status IN ('queued', 'running')`, ws).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("read still waiting")
		}
		time.Sleep(25 * time.Millisecond)
	}
	got = owner.must(200, "GET", root+rival+"/search", nil)
	pages := got["pages"].(map[string]any)["rows"].([]any)
	if len(pages) != 1 || pages[0].(map[string]any)["url"] != "https://rival.example/pricing" || pages[0].(map[string]any)["traffic"] != 900.0 {
		t.Fatalf("pages after read: %v", got["pages"])
	}
	// Everything is fresh now: asking again reads nothing.
	if est := owner.must(200, "POST", root+rival+"/read", map[string]any{"estimate_only": true}); len(est["reports"].([]any)) != 0 || est["estimate"] != 0.0 {
		t.Fatalf("fresh: %v", est)
	}

	// A link gap: videos.example links to the rival. Its exact pages
	// aren't read yet; reading them costs, then they're kept.
	gap := base + "/seo-competitors/gaps/links"
	got = owner.must(200, "GET", gap+"?site_id="+site+"&domain=www.videos.example", nil)
	comps := got["competitors"].([]any)
	if got["domain"] != "videos.example" || len(comps) != 1 || comps[0].(map[string]any)["host"] != "rival.example" || comps[0].(map[string]any)["read_at"] != nil {
		t.Fatalf("unread gap: %v", got)
	}
	owner.must(400, "GET", gap+"?site_id="+site, nil)
	owner.must(404, "POST", gap, map[string]any{"site_id": site, "domain": "nobody.example"})
	if est := owner.must(200, "POST", gap, map[string]any{"site_id": site, "domain": "videos.example", "estimate_only": true}); est["competitors"] != 1.0 || est["estimate"].(float64) <= 0 {
		t.Fatalf("gap estimate: %v", est)
	}
	setDFSLinks(dfsLink("https://www.videos.example/watch?v=1", "https://rival.example/pricing", "www.videos.example", 90, time.Now().AddDate(0, -2, 0), nil))
	t.Cleanup(func() { setDFSLinks() })
	got = owner.must(200, "POST", gap, map[string]any{"site_id": site, "domain": "videos.example"})
	c := got["competitors"].([]any)[0].(map[string]any)
	ps := c["pages"].([]any)
	if c["read_at"] == nil || len(ps) != 1 || ps[0].(map[string]any)["source_url"] != "https://www.videos.example/watch?v=1" || ps[0].(map[string]any)["target_url"] != "https://rival.example/pricing" {
		t.Fatalf("gap pages: %v", got)
	}
	if again := owner.must(200, "GET", gap+"?site_id="+site+"&domain=videos.example", nil)["competitors"].([]any)[0].(map[string]any); len(again["pages"].([]any)) != 1 || again["read_at"] == nil {
		t.Fatalf("kept: %v", again)
	}

	seen := map[string]bool{}
	for _, ev := range events() {
		seen[ev.Name] = true
	}
	for _, name := range []string{"seo_competitor.read_requested", "seo_competitor.read", "link_read.done"} {
		if !seen[name] {
			t.Fatalf("no %s event: %v", name, seen)
		}
	}
}
