package api_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/seosync"
)

// One competitor list per brand: no limit on how many are tracked, new
// ones suggested weekly with the snapshot (from the SEO connection and, at
// no cost, from the sites ranking beside the brand), AI visibility cohorts
// add theirs to it, and Reddit posts name competitors the way it does.
func TestCompetitorsOneList(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	t.Cleanup(func() { setDFSRivals() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "website": "acme.example"})
	site := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.acme.example/", "brand_id": "acme"})["id"].(string)
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT brand_id::text FROM seo_sites WHERE id = $1`, site).Scan(&brand); err != nil {
		t.Fatal(err)
	}

	// No limit: more than the old 20.
	for i := range 25 {
		owner.must(201, "POST", base+"/seo-competitors", map[string]any{"site_id": site, "host": fmt.Sprintf("rival%d.example", i)})
	}
	if l := owner.must(200, "GET", base+"/seo-competitors?status=tracked&brand=acme&site_id="+site, nil); l["total"] != 25.0 {
		t.Fatalf("tracked: %v", l["total"])
	}

	// A competitor named on an AI visibility cohort joins the list.
	owner.must(201, "POST", base+"/ai-cohorts", map[string]any{"name": "Buyers", "brand_id": "acme",
		"competitors": []map[string]any{{"name": "Summit Gear", "domains": []string{"www.summit.example"}, "aliases": []string{"Summit"}}, {"name": "No site"}}})
	var source, name string
	var status string
	if err := e.pool.QueryRow(ctx, `SELECT status, source, name FROM seo_competitors WHERE site_id = $1 AND host = 'summit.example'`, site).Scan(&status, &source, &name); err != nil {
		t.Fatal(err)
	}
	if status != "tracked" || source != "ai_visibility" || name != "Summit Gear" {
		t.Fatalf("from the cohort: %s %s %s", status, source, name)
	}

	// Reddit posts use the list's names.
	got, err := e.srv.CompetitorNamesFor(ctx, ws, &brand, []string{"summit", "SUMMIT.EXAMPLE", "Someone else"})
	if err != nil || len(got) != 2 || got[0] != "Summit Gear" || got[1] != "Someone else" {
		t.Fatalf("reddit names: %v, %v", got, err)
	}

	// Sites ranking beside the brand for two of its keywords are suggested,
	// never the brand's own site or a platform, and a dismissed one stays
	// dismissed.
	owner.must(201, "POST", base+"/seo-competitors", map[string]any{"site_id": site, "host": "gone.example"})
	if _, err := e.pool.Exec(ctx, `UPDATE seo_competitors SET status = 'dismissed' WHERE site_id = $1 AND host = 'gone.example'`, site); err != nil {
		t.Fatal(err)
	}
	for _, kw := range []string{"trail shoes", "running shoes"} {
		if _, err := e.pool.Exec(ctx, `WITH k AS (INSERT INTO tracked_keywords (workspace_id, site_id, keyword) VALUES ($1, $2, $3) RETURNING id)
			INSERT INTO rank_observations (workspace_id, tracked_id, day, depth, engine, top) SELECT $1, k.id, current_date, 10, 'google',
			'[{"position":1,"domain":"www.beside.example"},{"position":2,"domain":"youtube.com"},{"position":3,"domain":"acme.example"},{"position":4,"domain":"gone.example"}]' FROM k`,
			ws, site, kw); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO tracked_keywords (workspace_id, site_id, keyword) VALUES ($1, $2, 'one off')`, ws, site); err != nil {
		t.Fatal(err)
	}
	if err := seosync.SuggestFromRankings(ctx, e.pool, log); err != nil {
		t.Fatal(err)
	}
	sugg := owner.must(200, "GET", base+"/seo-competitors?status=suggested&brand=acme&site_id="+site, nil)["data"].([]any)
	if len(sugg) != 1 || sugg[0].(map[string]any)["host"] != "beside.example" || sugg[0].(map[string]any)["source"] != "rankings" || sugg[0].(map[string]any)["common_keywords"] != 2.0 {
		t.Fatalf("from rankings: %v", sugg)
	}
	if err := e.pool.QueryRow(ctx, `SELECT status FROM seo_competitors WHERE site_id = $1 AND host = 'gone.example'`, site).Scan(&status); err != nil || status != "dismissed" {
		t.Fatalf("dismissed: %s %v", status, err)
	}

	// Once a week the SEO connection is asked as part of the snapshot,
	// not a second time via a separate suggest job in the same cycle.
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})
	setDFSRivals(map[string]any{"domain": "daily.example", "intersections": 30})
	seo := &seosync.Syncer{Pool: e.pool, Store: e.integrations, Log: log}
	if _, err := e.pool.Exec(ctx, `DELETE FROM jobs WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := seo.QueueDue(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var queued, suggests int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE workspace_id = $1 AND kind = 'seo.sync'`, ws).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE workspace_id = $1 AND kind = 'seo.sync' AND payload->>'suggest' = 'true'`, ws).Scan(&suggests); err != nil {
		t.Fatal(err)
	}
	if suggests != 0 {
		t.Fatalf("separate suggest job queued %d, want none in the same weekly cycle", suggests)
	}
	if queued < 1 {
		t.Fatalf("weekly snapshot queued %d", queued)
	}
	q := jobs.New(e.pool, log)
	q.PollInterval = 20 * time.Millisecond
	seo.Register(q)
	qctx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(qctx, 1); close(done) }() //nolint:errcheck
	defer func() { stop(); <-done }()
	for deadline := time.Now().Add(20 * time.Second); ; {
		var n int
		e.pool.QueryRow(ctx, `SELECT count(*) FROM seo_competitors WHERE site_id = $1 AND host = 'daily.example' AND status = 'suggested'`, site).Scan(&n)
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no weekly snapshot suggestion for daily.example: %d", n)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
