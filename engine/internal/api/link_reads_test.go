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

func TestLinkReadsReportsAndSettings(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	events := track.Capture(t)
	ctx := context.Background()
	t.Cleanup(func() { setDFSLinks() })

	// Without the workspace's own SEO connection, nothing is read.
	if code := owner.must(409, "POST", base+"/link-reads", map[string]any{"target": "https://rival.example/guide"})["error"].(map[string]any)["code"]; code != "connector_needed" {
		t.Fatalf("no connection: %v", code)
	}
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})
	siteID := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.example.com/"})["id"].(string)
	rival := owner.must(201, "POST", base+"/seo-competitors", map[string]any{"site_id": siteID, "host": "rival.example"})["id"].(string)

	// Settings start with the defaults.
	set := owner.must(200, "GET", base+"/link-building/settings", nil)
	if set["pitch_cooldown_days"].(float64) != 60 || set["weekly_reads"] != true || set["weekly_budget_percent"].(float64) != 10 {
		t.Fatalf("defaults: %v", set)
	}
	owner.must(400, "PATCH", base+"/link-building/settings", map[string]any{"pitch_cooldown_days": 400})
	owner.must(400, "PATCH", base+"/link-building/settings", map[string]any{"weekly_budget_percent": 0})

	// A page's links: the estimate first, then the read.
	est := owner.must(200, "POST", base+"/link-reads", map[string]any{"target": "https://rival.example/old-guide", "estimate_only": true})
	if est["scope"] != "page" || est["estimate"].(float64) <= 0 || est["connector"] != "dataforseo" {
		t.Fatalf("estimate: %v", est)
	}
	now := time.Now()
	setDFSLinks(
		dfsLink("https://blog.example.org/tools", "https://rival.example/old-guide", "blog.example.org", 55, now.AddDate(0, -3, 0), nil),
		dfsLink("https://news.example.net/a", "https://rival.example/old-guide", "news.example.net", 70, now.AddDate(0, -1, 0), nil),
	)
	read := owner.must(201, "POST", base+"/link-reads", map[string]any{"target": "https://rival.example/old-guide", "site_id": siteID})
	if read["scope"] != "page" || read["status"] != "done" || read["links"].(float64) != 2 || read["referring_domains"].(float64) != 2 ||
		read["reason"] != "requested" || read["site_id"] != siteID || len(read["rows"].([]any)) != 2 ||
		read["rows"].([]any)[0].(map[string]any)["source_domain"] != "news.example.net" {
		t.Fatalf("read: %v", read)
	}
	owner.must(400, "POST", base+"/link-reads", map[string]any{"target": "not a site"})
	if got := owner.must(200, "GET", base+"/link-reads/"+read["id"].(string), nil); len(got["rows"].([]any)) != 2 {
		t.Fatalf("get: %v", got)
	}

	// Dead pages: competitors' pages gone from their sitemaps, with who
	// links to them.
	if _, err := e.pool.Exec(ctx, `INSERT INTO competitor_pages (competitor_id, workspace_id, url, gone_at) VALUES
		($1, $2, 'https://rival.example/old-guide', now() - interval '2 days'), ($1, $2, 'https://rival.example/gone-unread', now() - interval '1 day')`,
		rival, ws); err != nil {
		t.Fatal(err)
	}
	dead := owner.must(200, "GET", base+"/link-reads/dead-pages?site_id="+siteID, nil)["data"].([]any)
	if len(dead) != 2 || dead[0].(map[string]any)["url"] != "https://rival.example/old-guide" || dead[0].(map[string]any)["referring_domains"].(float64) != 2 ||
		dead[0].(map[string]any)["best_rating"].(float64) != 70 || dead[1].(map[string]any)["read_id"] != nil {
		t.Fatalf("dead pages: %v", dead)
	}

	// Top pages: a domain's pages by the domains linking to them.
	setDFSLinks(
		dfsLink("https://a.example.org/", "https://rival.example/guide", "a.example.org", 40, now, nil),
		dfsLink("https://b.example.org/", "https://rival.example/guide", "b.example.org", 50, now, nil),
		dfsLink("https://c.example.org/", "https://rival.example/pricing", "c.example.org", 80, now, nil),
	)
	if d := owner.must(201, "POST", base+"/link-reads", map[string]any{"target": "https://www.rival.example/"}); d["scope"] != "domain" || d["target"] != "rival.example" {
		t.Fatalf("domain read: %v", d)
	}
	top := owner.must(200, "GET", base+"/link-reads/top-pages?target=rival.example", nil)
	pages := top["data"].([]any)
	if len(pages) != 2 || pages[0].(map[string]any)["url"] != "https://rival.example/guide" || pages[0].(map[string]any)["referring_domains"].(float64) != 2 {
		t.Fatalf("top pages: %v", top)
	}
	if l := owner.must(200, "GET", base+"/link-reads?reason=requested", nil)["data"].([]any); len(l) != 2 {
		t.Fatalf("list: %v", l)
	}

	// The weekly snapshot reads the dead page nobody read yet.
	setDFSLinks(dfsLink("https://d.example.org/", "https://rival.example/gone-unread", "d.example.org", 30, now, nil))
	id := owner.must(200, "GET", base+"/seo", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	run := owner.must(200, "POST", base+"/seo/"+id+"/sync", nil)["dead_pages"].([]any)
	if len(run) != 1 || run[0].(map[string]any)["read"].(float64) != 1 || run[0].(map[string]any)["capped"] != false {
		t.Fatalf("weekly: %v", run)
	}
	if l := owner.must(200, "GET", base+"/link-reads?reason=dead_page", nil)["data"].([]any); len(l) != 1 ||
		l[0].(map[string]any)["competitor_id"] != rival || l[0].(map[string]any)["target"] != "https://rival.example/gone-unread" {
		t.Fatalf("weekly reads: %v", l)
	}
	// Had that read failed, it is read again soon by a job of its own,
	// not at next week's snapshot.
	if _, err := e.pool.Exec(ctx, `UPDATE link_reads SET status = 'failed', error_code = 'provider_error', created_at = now() - interval '20 minutes'
		WHERE workspace_id = $1 AND reason = 'dead_page'`, ws); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	seo := &seosync.Syncer{Pool: e.pool, Store: e.integrations, Log: log}
	if err := seo.QueueDue(ctx); err != nil {
		t.Fatal(err)
	}
	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	seo.Register(q)
	qctx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(qctx, 1); close(done) }() //nolint:errcheck
	deadline := time.Now().Add(10 * time.Second)
	for {
		var read int
		e.pool.QueryRow(ctx, `SELECT count(*) FROM link_reads WHERE workspace_id = $1 AND reason = 'dead_page' AND status = 'done'`, ws).Scan(&read)
		if read == 1 {
			break
		}
		if time.Now().After(deadline) {
			stop()
			<-done
			t.Fatal("the failed dead page read was not tried again")
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()
	<-done

	// Turned off, it reads nothing.
	owner.must(200, "PATCH", base+"/link-building/settings", map[string]any{"weekly_reads": false})
	if _, err := e.pool.Exec(ctx, `INSERT INTO competitor_pages (competitor_id, workspace_id, url, gone_at) VALUES ($1, $2, 'https://rival.example/third', now())`,
		rival, ws); err != nil {
		t.Fatal(err)
	}
	if run := owner.must(200, "POST", base+"/seo/"+id+"/sync?again=1", nil)["dead_pages"].([]any); run[0].(map[string]any)["read"].(float64) != 0 {
		t.Fatalf("off: %v", run)
	}

	// The cool-down follows the setting: 0 lets a declined site be pitched
	// again at once.
	owner.must(200, "PATCH", base+"/link-building/settings", map[string]any{"pitch_cooldown_days": 0})
	res := owner.must(200, "POST", base+"/link-prospects", map[string]any{"site_id": siteID, "prospects": []any{
		map[string]any{"page_url": "https://blog.example.org/tools"}, map[string]any{"page_url": "https://blog.example.org/more"}}})["data"].([]any)
	one, two := res[0].(map[string]any)["id"].(string), res[1].(map[string]any)["id"].(string)
	owner.must(200, "PATCH", base+"/link-prospects/"+one, map[string]any{"stage": "pitched"})
	owner.must(409, "PATCH", base+"/link-prospects/"+two, map[string]any{"stage": "pitched"})
	owner.must(200, "PATCH", base+"/link-prospects/"+one, map[string]any{"stage": "declined"})
	owner.must(200, "PATCH", base+"/link-prospects/"+two, map[string]any{"stage": "pitched"})

	got := map[string]bool{}
	for _, ev := range events() {
		got[ev.Name] = true
		if ev.Name == "link_read.done" && ev.Props["target_type"] != "link_read" {
			t.Errorf("link_read.done target: %v", ev.Props)
		}
	}
	for _, name := range []string{"link_read.done", "link_reads.weekly_done", "link_building.settings_updated"} {
		if !got[name] {
			t.Errorf("no %s event", name)
		}
	}
}
