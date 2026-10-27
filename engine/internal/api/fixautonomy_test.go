package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/deploys"
	"github.com/outflareai/outflare/engine/internal/fixauto"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestFixAutonomy(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	var site string
	if err := e.pool.QueryRow(ctx, `INSERT INTO seo_sites (workspace_id, host, start_url, verify_token) VALUES ($1, 'acme.example', 'https://acme.example/', 'tok')
		RETURNING id::text`, ws).Scan(&site); err != nil {
		t.Fatal(err)
	}
	fix := func(rule, sev string) string {
		var id string
		if err := e.pool.QueryRow(ctx, `INSERT INTO fixes (workspace_id, source, subject_type, subject_id, rule, severity, group_key, count, unit)
			VALUES ($1, 'seo_audit', 'seo_site', $2, $3, $4, '/blog/*', 12, 'pages') RETURNING id::text`, ws, site, rule, sev).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	title, canonical := fix("title_missing", "error"), fix("canonical_missing", "notice")

	// The defaults: copilot, deploys re-check fixes.
	set := owner.must(200, "GET", base+"/fix-autonomy", nil)
	if set["mode"] != "copilot" || set["verify_on_deploy"] != true || set["updated_at"] != nil || len(set["always_person"].([]any)) == 0 {
		t.Fatalf("defaults = %v", set)
	}

	// An agent in an autonomous mission.
	agent := agentToken(t, owner, base, "Fixer", "missions.read", "missions.run", "seo.read", "seo.manage")
	m := agent.must(201, "POST", base+"/missions", map[string]any{"name": "Fix the blog", "goal": "Clear the audit"})
	mission := m["id"].(string)
	owner.must(200, "POST", base+"/missions/"+mission+"/approve", map[string]any{"mode": "autonomous"})
	held := func(fixID string) bool {
		t.Helper()
		code, out := agent.inMission(mission, "POST", base+"/fixes/"+fixID+"/start", nil)
		switch code {
		case 202:
			return out["approval_required"] == true
		case 200:
			return false
		}
		t.Fatalf("start = %d %v", code, out)
		return false
	}

	// Copilot holds every fix, even in an autonomous mission.
	if !held(title) {
		t.Fatal("copilot ran a fix")
	}

	// Bad settings.
	owner.must(400, "PUT", base+"/fix-autonomy", map[string]any{"mode": "yolo"})
	owner.must(400, "PUT", base+"/fix-autonomy", map[string]any{"mode": "rules", "allowed": []string{"canonical_missing"}})
	owner.must(400, "PUT", base+"/fix-autonomy", map[string]any{"mode": "rules", "allowed": []string{"Not a rule"}})

	// Rules mode lets metadata fixes through; a canonical always waits.
	set = owner.must(200, "PUT", base+"/fix-autonomy", map[string]any{"mode": "rules", "allowed": []string{"metadata"}})
	if set["mode"] != "rules" || set["updated_at"] == nil {
		t.Fatalf("rules = %v", set)
	}
	code, out := agent.inMission(mission, "POST", base+"/fixes/"+title+"/start", nil)
	if code != 200 || out["autonomy"].(map[string]any)["reason"] != "allowed" || out["fix"].(map[string]any)["status"] != "in_progress" || out["prompt"].(map[string]any)["prompt"] == "" {
		t.Fatalf("allowed start = %d %v", code, out)
	}
	if !held(canonical) {
		t.Fatal("a canonical fix ran on its own")
	}
	owner.must(200, "PUT", base+"/fix-autonomy", map[string]any{"mode": "autonomous"})
	if !held(canonical) {
		t.Fatal("autonomous ran a canonical fix")
	}

	// The agent can't widen its own room.
	if code, out := agent.inMission(mission, "PUT", base+"/fix-autonomy", map[string]any{"mode": "autonomous"}); code != 202 || out["approval_required"] != true {
		t.Fatalf("agent changed autonomy = %d %v", code, out)
	}

	// A site's own setting wins until it is reset.
	set = owner.must(200, "PUT", base+"/seo-sites/"+site+"/fix-autonomy", map[string]any{"mode": "copilot", "verify_on_deploy": false})
	if set["inherited"] != false || set["mode"] != "copilot" || set["site_id"] != site {
		t.Fatalf("site = %v", set)
	}
	if ws := owner.must(200, "GET", base+"/fix-autonomy", nil); ws["mode"] != "autonomous" {
		t.Fatalf("workspace changed by site = %v", ws)
	}
	owner.must(200, "PATCH", base+"/fixes/"+title, map[string]any{"status": "open"})
	if !held(title) {
		t.Fatal("the site's copilot ran a fix")
	}
	if set = owner.must(200, "DELETE", base+"/seo-sites/"+site+"/fix-autonomy", nil); set["inherited"] != true || set["mode"] != "autonomous" {
		t.Fatalf("reset = %v", set)
	}

	// Outside a mission a person takes any fix, and sees what agents may do.
	if out := owner.must(200, "POST", base+"/fixes/"+canonical+"/start", map[string]any{"note": "Template fix"}); out["autonomy"].(map[string]any)["reason"] != "always_person" ||
		out["fix"].(map[string]any)["note"] != "Template fix" {
		t.Fatalf("person start = %v", out)
	}
	owner.must(200, "PATCH", base+"/fixes/"+canonical, map[string]any{"status": "ignored"})
	owner.must(409, "POST", base+"/fixes/"+canonical+"/start", nil)

	// A deploy of the site re-checks its fixes with a crawl.
	svc := &fixauto.Service{Pool: e.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	owner.must(200, "PATCH", base+"/fixes/"+title, map[string]any{"status": "done"})
	deploy := func(id string) {
		if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			return svc.OnDeploy(ctx, tx, deploys.Deploy{ID: id, WorkspaceID: ws, SiteID: &site, DeployedAt: time.Now()})
		}); err != nil {
			t.Fatal(err)
		}
	}
	runJobs := func() int {
		t.Helper()
		rows, err := e.pool.Query(ctx, `UPDATE jobs SET status = 'succeeded' WHERE workspace_id = $1 AND kind = $2 AND status = 'queued'
			RETURNING id, payload, attempts + 1, max_attempts`, ws, fixauto.JobVerify)
		if err != nil {
			t.Fatal(err)
		}
		list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (jobs.Job, error) {
			j := jobs.Job{Kind: fixauto.JobVerify, WorkspaceID: &ws}
			return j, r.Scan(&j.ID, &j.Payload, &j.Attempt, &j.MaxAttempts)
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, j := range list {
			if err := svc.Verify(ctx, j); err != nil {
				t.Fatal(err)
			}
		}
		return len(list)
	}
	deploy("00000000-0000-4000-8000-000000000001")
	deploy("00000000-0000-4000-8000-000000000001") // a repeat is one job
	if n := runJobs(); n != 1 {
		t.Fatalf("jobs = %d", n)
	}
	var trigger string
	var data []byte
	if err := e.pool.QueryRow(ctx, `SELECT data->>'trigger', data FROM audit_events WHERE workspace_id = $1 AND action = 'crawl.started' ORDER BY created_at DESC LIMIT 1`, ws).
		Scan(&trigger, &data); err != nil || trigger != "deploy" {
		t.Fatalf("crawl.started = %q %s %v", trigger, data, err)
	}
	var by string
	if err := e.pool.QueryRow(ctx, `SELECT data->>'by' FROM audit_events WHERE workspace_id = $1 AND action = 'fix.verify_requested' AND target_id = $2`, ws, title).Scan(&by); err != nil || by != "deploy" {
		t.Fatalf("fix.verify_requested by = %q %v", by, err)
	}

	// A crawl already running: the re-check waits for it.
	deploy("00000000-0000-4000-8000-000000000002")
	runJobs()
	var p map[string]any
	var raw []byte
	if err := e.pool.QueryRow(ctx, `SELECT payload FROM jobs WHERE workspace_id = $1 AND kind = $2 AND status = 'queued'`, ws, fixauto.JobVerify).Scan(&raw); err != nil {
		t.Fatalf("no retry: %v", err)
	}
	json.Unmarshal(raw, &p)
	if p["attempt"] != 1.0 || p["deploy_id"] != "00000000-0000-4000-8000-000000000002" {
		t.Fatalf("retry = %v", p)
	}

	// Re-checks off: a deploy queues nothing.
	owner.must(200, "PUT", base+"/fix-autonomy", map[string]any{"verify_on_deploy": false})
	if _, err := e.pool.Exec(ctx, `DELETE FROM jobs WHERE workspace_id = $1 AND kind = $2`, ws, fixauto.JobVerify); err != nil {
		t.Fatal(err)
	}
	deploy("00000000-0000-4000-8000-000000000003")
	if n := runJobs(); n != 0 {
		t.Fatalf("off queued %d", n)
	}

	seen := map[string]bool{}
	for _, ev := range got() {
		seen[ev.Name] = true
	}
	for _, name := range []string{"fix_autonomy.updated", "fix_autonomy.reset", "fix.started", "mission.action_held", "crawl.started", "fix.verify_requested"} {
		if !seen[name] {
			t.Errorf("no %s event", name)
		}
	}
}
