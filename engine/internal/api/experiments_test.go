package api_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/deploys"
	"github.com/outflareai/outflare/engine/internal/experiments"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestExperiments(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	svc := &experiments.Service{Pool: e.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	var site, bare, gsc string
	if err := e.pool.QueryRow(ctx, `INSERT INTO seo_sites (workspace_id, host, start_url, verify_token) VALUES ($1, 'www.acme.example', 'https://www.acme.example/', 'tok')
		RETURNING id::text`, ws).Scan(&site); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO seo_sites (workspace_id, host, start_url, verify_token) VALUES ($1, 'blog.northpeak.example', 'https://blog.northpeak.example/', 'tok')
		RETURNING id::text`, ws).Scan(&bare); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO integrations (workspace_id, connector, name, credentials) VALUES ($1, 'google_search_console', 'Search Console', '\x00')
		RETURNING id::text`, ws).Scan(&gsc); err != nil {
		t.Fatal(err)
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	newest := today.AddDate(0, 0, -1)
	if _, err := e.pool.Exec(ctx, `INSERT INTO search_syncs (integration_id, site, workspace_id, host, oldest_day, newest_day)
		VALUES ($1, 'sc-domain:acme.example', $2, 'acme.example', $3, $4)`, gsc, ws, today.AddDate(0, 0, -120), newest); err != nil {
		t.Fatal(err)
	}
	// 20 blog posts and the about page, the same every day for now.
	var rows [][]any
	page := func(i int) string { return fmt.Sprintf("https://www.acme.example/blog/post-%02d", i) }
	for d := newest; !d.Before(today.AddDate(0, 0, -120)); d = d.AddDate(0, 0, -1) {
		for i := range 20 {
			rows = append(rows, []any{ws, gsc, "sc-domain:acme.example", "acme.example", d, page(i), "q", 10 + i*5, 100 + i*20, 0.1, 8.0})
		}
		rows = append(rows, []any{ws, gsc, "sc-domain:acme.example", "acme.example", d, "https://www.acme.example/about", "q", 3, 50, 0.06, 12.0})
	}
	if _, err := e.pool.CopyFrom(ctx, pgx.Identifier{"search_daily"}, []string{"workspace_id", "integration_id", "site", "host", "day", "page", "query", "clicks", "impressions", "ctr", "position"},
		pgx.CopyFromRows(rows)); err != nil {
		t.Fatal(err)
	}

	// Bad input, and a site with no Search Console data.
	owner.must(400, "POST", base+"/experiments", map[string]any{"site_id": site, "name": "x", "pattern": "blog/*"})
	owner.must(400, "POST", base+"/experiments", map[string]any{"site_id": site, "name": "x", "metric": "revenue"})
	owner.must(400, "POST", base+"/experiments", map[string]any{"site_id": site, "name": "x", "holdout_percent": 95})
	if code, out := owner.do("POST", base+"/experiments", map[string]any{"site_id": bare, "name": "x"}); code != 409 || out["error"].(map[string]any)["code"] != "connector_needed" {
		t.Fatalf("no search data = %d %v", code, out)
	}
	if code, out := owner.do("POST", base+"/experiments", map[string]any{"site_id": site, "name": "x", "pattern": "/shop/*"}); code != 409 {
		t.Fatalf("no matching pages = %d %v", code, out)
	}

	// A holdout over the blog: matched groups of 10.
	ex := owner.must(201, "POST", base+"/experiments", map[string]any{"site_id": site, "name": "Shorter titles", "pattern": "/blog/*",
		"hypothesis": "Shorter titles get more clicks.", "start_on": "manual"})
	id := ex["id"].(string)
	if ex["status"] != "draft" || ex["treatment_pages"].(float64) != 10 || ex["control_pages"].(float64) != 10 || ex["site_host"] != "www.acme.example" || ex["result"] != nil {
		t.Fatalf("experiment = %v", ex)
	}
	pages := owner.must(200, "GET", base+"/experiments/"+id+"/pages?group=treatment", nil)["data"].([]any)
	if len(pages) != 10 {
		t.Fatalf("treatment = %v", pages)
	}
	treated := map[string]bool{}
	sum := map[string]float64{}
	for _, p := range owner.must(200, "GET", base+"/experiments/"+id+"/pages", nil)["data"].([]any) {
		p := p.(map[string]any)
		if p["group"] == "treatment" {
			treated[p["url"].(string)] = true
		}
		sum[p["group"].(string)] += p["baseline"].(float64)
	}
	// Matched: the groups' baselines are within a tenth of each other.
	if math.Abs(sum["treatment"]-sum["control"])/sum["control"] > 0.1 {
		t.Fatalf("groups are not matched: %v", sum)
	}

	// The change goes live 35 days ago on the treatment pages: clicks up
	// half. A Google update cuts every page by a tenth at the same time,
	// which the control group shows.
	changed := today.AddDate(0, 0, -35)
	for i := range 20 {
		f := 0.9
		if treated[page(i)] {
			f = 0.9 * 1.5 * (1 + 0.05*float64(i%3-1))
		}
		if _, err := e.pool.Exec(ctx, `UPDATE search_daily SET clicks = round(clicks * $3::float8) WHERE workspace_id = $1 AND page = $2 AND day > $4::date`,
			ws, page(i), f, changed); err != nil {
			t.Fatal(err)
		}
	}
	owner.must(400, "POST", base+"/experiments/"+id+"/start", map[string]any{"changed_at": today.AddDate(0, 0, -200)})
	ex = owner.must(200, "POST", base+"/experiments/"+id+"/start", map[string]any{"changed_at": changed.Add(12 * time.Hour)})
	res := ex["result"].(map[string]any)
	if ex["status"] != "measuring" || res["final"] != true || res["verdict"] != "better" || res["days_measured"].(float64) != 28 || res["p_value"] == nil {
		t.Fatalf("measuring = %v", ex)
	}
	if lift := res["lift"].(float64); lift < 0.4 || lift > 0.6 {
		t.Fatalf("lift = %v", lift)
	}
	if c := res["control"].(map[string]any)["change"].(float64); c > -0.05 || c < -0.15 {
		t.Fatalf("control change = %v", c)
	}
	owner.must(409, "POST", base+"/experiments/"+id+"/start", nil)

	// The hourly check ends it with its result.
	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	ex = owner.must(200, "GET", base+"/experiments/"+id, nil)
	if ex["status"] != "done" || ex["concluded_at"] == nil || ex["result"].(map[string]any)["verdict"] != "better" {
		t.Fatalf("done = %v", ex)
	}
	if list := owner.must(200, "GET", base+"/experiments?status=done", nil)["data"].([]any); len(list) != 1 || list[0].(map[string]any)["result"] == nil {
		t.Fatalf("list = %v", list)
	}

	// A before and after experiment that starts on the site's next deploy.
	ba := owner.must(201, "POST", base+"/experiments", map[string]any{"site_id": site, "name": "New about page", "design": "before_after", "pattern": "/about"})
	baID := ba["id"].(string)
	if ba["treatment_pages"].(float64) != 1 || ba["control_pages"].(float64) != 0 {
		t.Fatalf("before and after = %v", ba)
	}
	if ba = owner.must(200, "POST", base+"/experiments/"+baID+"/start", nil); ba["status"] != "waiting" {
		t.Fatalf("waiting = %v", ba)
	}
	var target, dep string
	if err := e.pool.QueryRow(ctx, `INSERT INTO deploy_targets (workspace_id, integration_id, target, name, site_id) VALUES ($1, $2, 'acme/website', 'acme/website', $3)
		RETURNING id::text`, ws, gsc, site).Scan(&target); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour)
	if err := e.pool.QueryRow(ctx, `INSERT INTO deploys (workspace_id, target_id, site_id, external_id, kind, state, deployed_at) VALUES ($1, $2, $3, 'd1', 'deployment', 'ready', $4)
		RETURNING id::text`, ws, target, site, at).Scan(&dep); err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		return svc.OnDeploy(ctx, tx, deploys.Deploy{ID: dep, WorkspaceID: ws, SiteID: &site, DeployedAt: at})
	}); err != nil {
		t.Fatal(err)
	}
	ba = owner.must(200, "GET", base+"/experiments/"+baID, nil)
	if ba["status"] != "measuring" || ba["deploy_id"] != dep || ba["result"].(map[string]any)["verdict"] != "too_early" {
		t.Fatalf("started by the deploy = %v", ba)
	}
	if ba = owner.must(200, "POST", base+"/experiments/"+baID+"/stop", nil); ba["status"] != "done" {
		t.Fatalf("stopped early = %v", ba)
	}
	owner.must(409, "POST", base+"/experiments/"+baID+"/stop", nil)

	// A draft is stopped, renamed and deleted.
	dr := owner.must(201, "POST", base+"/experiments", map[string]any{"site_id": site, "name": "Draft"})
	if dr["treatment_pages"].(float64)+dr["control_pages"].(float64) != 21 {
		t.Fatalf("every page = %v", dr)
	}
	if dr = owner.must(200, "PATCH", base+"/experiments/"+dr["id"].(string), map[string]any{"name": "FAQ schema"}); dr["name"] != "FAQ schema" {
		t.Fatalf("renamed = %v", dr)
	}
	if dr = owner.must(200, "POST", base+"/experiments/"+dr["id"].(string)+"/stop", nil); dr["status"] != "stopped" {
		t.Fatalf("stopped = %v", dr)
	}
	owner.must(204, "DELETE", base+"/experiments/"+dr["id"].(string), nil)
	owner.must(404, "GET", base+"/experiments/"+dr["id"].(string), nil)

	seen := map[string]bool{}
	for _, ev := range got() {
		seen[ev.Name] = true
	}
	for _, name := range []string{"experiment.created", "experiment.started", "experiment.waiting", "experiment.concluded", "experiment.updated",
		"experiment.stopped", "experiment.deleted"} {
		if !seen[name] {
			t.Errorf("no %s event", name)
		}
	}
}
