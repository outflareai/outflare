package api_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/ranks"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Find more competitors: the results pages of the keyword gaps are read on
// the workspace's own SERP connection and kept, a gap keyword opens its
// results (who ranks, competitor or not), and the domains that rank for
// several gaps are suggested to say Yes or No to.
func TestDiscoverCompetitors(t *testing.T) {
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
	site := exec(`INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, 'www.northpeak.example', 'https://www.northpeak.example/', 'tok')`, ws, brand)
	root := "/v1/brands/" + brand + "/competition/discover"
	results := base + "/seo-competitors/results?site_id=" + site + "&keyword="

	// Nothing tracked yet.
	owner.must(409, "POST", root, nil)
	exec(`INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, status) VALUES ($1, $2, $3, 'rival.example', 'Rival', 'tracked')`, ws, site, brand)
	// No SERP connection: it says which to add.
	if got := owner.must(409, "POST", root, nil); got["error"].(map[string]any)["code"] != "connector_needed" {
		t.Fatalf("no SERP: %v", got)
	}
	seo := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})["id"].(string)
	if _, err := e.pool.Exec(ctx, `DELETE FROM jobs WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	// The rival's keywords from the weekly snapshot: three in its top 20, one not.
	for _, k := range []struct {
		key string
		pos float64
	}{{"gap alpha", 2}, {"gap beta", 5}, {"gap gamma", 9}, {"gap deep", 45}} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO seo_snapshots (workspace_id, integration_id, connector, host, report, day, key, metrics)
			VALUES ($1, $2, 'dataforseo', 'rival.example', 'keywords', current_date, $3, jsonb_build_object('position', $4::float, 'volume', 100))`, ws, seo, k.key, k.pos); err != nil {
			t.Fatal(err)
		}
	}

	// Not read yet: an empty page.
	if got := owner.must(200, "GET", results+"gap+alpha", nil); got["read_at"] != nil || len(got["items"].([]any)) != 0 {
		t.Fatalf("unread: %v", got)
	}
	owner.must(400, "GET", base+"/seo-competitors/results?site_id="+site, nil)
	owner.must(400, "POST", root, map[string]any{"keywords": make([]string, 26)})

	got := owner.must(202, "POST", root, nil)
	if got["sites"] != 1.0 || got["serp_integration_id"] != seo {
		t.Fatalf("discover: %v", got)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	svc := &ranks.Service{Pool: e.pool, Store: e.integrations, Log: log}
	svc.Register(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 1); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })
	wait := func() {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			var left int
			if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE workspace_id = $1 AND kind = $2 AND status IN ('queued', 'running')`, ws, ranks.JobGapSERPs).Scan(&left); err != nil {
				t.Fatal(err)
			}
			if left == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("gap read still waiting")
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	wait()

	var read int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM keyword_serps WHERE workspace_id = $1 AND site_id = $2`, ws, site).Scan(&read); err != nil || read != 3 {
		t.Fatalf("read %d gap keywords (the three in the top 20), %v", read, err)
	}
	// newrival.example ranks for all three: suggested. once.example for one,
	// github.com is a platform, the site itself and the tracked rival are not.
	rows, err := e.pool.Query(ctx, `SELECT host, status, source, COALESCE(common_keywords, 0) FROM seo_competitors WHERE workspace_id = $1 AND site_id = $2 ORDER BY host`, ws, site)
	if err != nil {
		t.Fatal(err)
	}
	type comp struct {
		host, status, source string
		n                    float64
	}
	var list []comp
	for rows.Next() {
		var c comp
		if err := rows.Scan(&c.host, &c.status, &c.source, &c.n); err != nil {
			t.Fatal(err)
		}
		list = append(list, c)
	}
	rows.Close()
	// redirected.example ranks behind a Google redirect link, which is
	// read as its page; google.com itself is never suggested.
	if len(list) != 3 || list[0].host != "newrival.example" || list[0].status != "suggested" || list[0].source != "keyword_gaps" || list[0].n != 3 ||
		list[1].host != "redirected.example" || list[1].n != 3 || list[2].host != "rival.example" {
		t.Fatalf("competitors: %v", list)
	}

	// A gap keyword opens its results, with who is who.
	page := owner.must(200, "GET", results+"Gap+Beta", nil)
	items := page["items"].([]any)
	if page["source"] != "keyword_gaps" || page["read_at"] == nil || page["engine"] != "google" || len(items) != 5 || page["stale"] != false {
		t.Fatalf("results: %v", page)
	}
	first, second, last := items[0].(map[string]any), items[1].(map[string]any), items[4].(map[string]any)
	if r := items[3].(map[string]any); r["url"] != "https://redirected.example/gap-beta" || r["domain"] != "redirected.example" {
		t.Fatalf("redirect read as its page: %v", r)
	}
	if first["domain"] != "rival.example" || first["competitor"].(map[string]any)["status"] != "tracked" || first["you"] != false {
		t.Fatalf("rival: %v", first)
	}
	if second["competitor"].(map[string]any)["status"] != "suggested" || items[2].(map[string]any)["competitor"] != nil || items[3].(map[string]any)["competitor"].(map[string]any)["status"] != "suggested" {
		t.Fatalf("suggested and other: %v", items)
	}
	if last["you"] != true || last["position"] != 4.0 {
		t.Fatalf("you: %v", last)
	}

	// Again: nothing is read twice in a week, unless a keyword is asked for.
	calls := dataForSEOCalls.Load()
	owner.must(202, "POST", root, nil)
	wait()
	if dataForSEOCalls.Load() != calls {
		t.Fatalf("read again: %d calls", dataForSEOCalls.Load()-calls)
	}
	owner.must(202, "POST", root, map[string]any{"keywords": []string{"gap alpha"}})
	wait()
	if dataForSEOCalls.Load() != calls+1 {
		t.Fatalf("asked for one: %d calls", dataForSEOCalls.Load()-calls)
	}

	// The daily schedule queues it by itself.
	if n, err := svc.QueueGapReads(ctx); err != nil || n < 1 {
		t.Fatalf("schedule: %d %v", n, err)
	}
	wait()

	var requested, readEv bool
	for _, ev := range events() {
		switch ev.Name {
		case "competition.discover_requested":
			requested = true
		case "seo_competitors.gap_results_read":
			readEv = readEv || ev.Props["read"] == 3
		}
	}
	if !requested || !readEv {
		t.Fatalf("events: requested %v, read %v", requested, readEv)
	}
}
