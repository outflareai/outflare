package api_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// TestDashboardSharesAlertsAndDigest covers a dashboard's share link (open
// to anyone with it, then behind a password, then taken back), alerts on a
// saved trend's number (raised once per crossing, checked by the hourly
// job) and a dashboard's trends in the digest.
func TestDashboardSharesAlertsAndDigest(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	day := 24 * time.Hour
	today := time.Now().UTC().Truncate(day)
	d := func(n int) time.Time { return today.Add(-time.Duration(n)*day + time.Hour) }
	// Three visits three days ago, two ten days ago.
	for i, at := range []time.Time{d(3), d(3), d(3), d(10), d(10)} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO site_sessions (workspace_id, brand_id, anonymous_id, started_at, last_seen_at, landing_path)
			VALUES ($1, $2, $3, $4, $4, '/')`, ws, brand, "s"+itoa(i), at); err != nil {
			t.Fatal(err)
		}
	}
	trend := owner.must(201, "POST", base+"/insights", map[string]any{"kind": "trend", "name": "Visits",
		"spec": map[string]any{"date_range": map[string]any{"preset": "last_7_days"}, "series": []map[string]any{{"kind": "visit"}}, "display": "number"}})["id"].(string)
	funnel := owner.must(201, "POST", base+"/insights", map[string]any{"kind": "funnel", "name": "Visit to visit",
		"spec": map[string]any{"date_range": map[string]any{"preset": "last_7_days"}, "steps": []map[string]any{{"kind": "visit"}, {"kind": "payment"}}}})["id"].(string)
	dash := owner.must(201, "POST", base+"/dashboards", map[string]any{"name": "Weekly numbers", "settings": map[string]any{"date_range": map[string]any{"preset": "last_30_days"}, "digest": true}})
	if dash["share"] != nil {
		t.Fatalf("a new dashboard has no link: %v", dash["share"])
	}
	did := dash["id"].(string)
	withTile := owner.must(201, "POST", base+"/dashboards/"+did+"/tiles", map[string]any{"insight_id": trend})
	tile := withTile["tile_list"].([]any)[0].(map[string]any)["id"].(string)
	owner.must(201, "POST", base+"/dashboards/"+did+"/tiles", map[string]any{"note": "Look at visits first."})

	// A link that opens on its own.
	anon := &client{t: t, h: e.h}
	owner.must(400, "POST", base+"/dashboards/"+did+"/share", map[string]any{"password": "short"})
	owner.must(400, "POST", base+"/dashboards/"+did+"/share", map[string]any{"expires_in_days": 1000})
	owner.must(404, "POST", base+"/dashboards/00000000-0000-0000-0000-000000000000/share", map[string]any{})
	link := owner.must(201, "POST", base+"/dashboards/"+did+"/share", map[string]any{"expires_in_days": 30})
	token := link["token"].(string)
	if !strings.HasPrefix(token, "ofd_") || !strings.HasSuffix(link["url"].(string), "/share/"+token) || link["share"].(map[string]any)["has_password"] != false || link["share"].(map[string]any)["expires_at"] == nil {
		t.Fatalf("link: %v", link)
	}
	anon.must(404, "POST", "/v1/shared/dashboards/ofd_not_a_real_token", map[string]any{})
	open := anon.must(200, "POST", "/v1/shared/dashboards/"+token, map[string]any{})
	tiles := open["tile_list"].([]any)
	if open["name"] != "Weekly numbers" || open["key"] != "" || len(tiles) != 2 || tiles[0].(map[string]any)["kind"] != "trend" || tiles[1].(map[string]any)["note"] != "Look at visits first." {
		t.Fatalf("open: %v", open)
	}
	if _, has := tiles[0].(map[string]any)["spec"]; has {
		t.Fatal("a shared tile carries no spec")
	}
	// The tile runs with the dashboard's dates (30 days: all five visits),
	// or the viewer's (7 days: three).
	res := anon.must(200, "POST", "/v1/shared/dashboards/"+token+"/tiles/"+tile, map[string]any{})
	if total := res["series"].([]any)[0].(map[string]any)["total"]; total != float64(5) {
		t.Fatalf("shared tile total = %v", total)
	}
	res = anon.must(200, "POST", "/v1/shared/dashboards/"+token+"/tiles/"+tile, map[string]any{"date_range": map[string]any{"preset": "last_7_days"}})
	if total := res["series"].([]any)[0].(map[string]any)["total"]; total != float64(3) {
		t.Fatalf("shared tile total over 7 days = %v", total)
	}
	anon.must(404, "POST", "/v1/shared/dashboards/"+token+"/tiles/00000000-0000-0000-0000-000000000000", map[string]any{})
	share := owner.must(200, "GET", base+"/dashboards/"+did, nil)["share"].(map[string]any)
	if share["views"] != float64(1) || share["has_password"] != false || share["last_viewed_at"] == nil {
		t.Fatalf("share after a view: %v", share)
	}

	// A new link with a password replaces it: the old one is dead.
	link = owner.must(201, "POST", base+"/dashboards/"+did+"/share", map[string]any{"password": "northpeak numbers"})
	anon.must(404, "POST", "/v1/shared/dashboards/"+token, map[string]any{})
	token = link["token"].(string)
	if link["share"].(map[string]any)["has_password"] != true {
		t.Fatalf("link: %v", link)
	}
	if code, out := anon.do("POST", "/v1/shared/dashboards/"+token, map[string]any{}); code != 401 || out["error"].(map[string]any)["code"] != "password_required" {
		t.Fatalf("without the password: %d %v", code, out)
	}
	if code, out := anon.do("POST", "/v1/shared/dashboards/"+token, map[string]any{"password": "wrong one"}); code != 401 || out["error"].(map[string]any)["code"] != "wrong_password" {
		t.Fatalf("wrong password: %d %v", code, out)
	}
	anon.must(401, "POST", "/v1/shared/dashboards/"+token+"/tiles/"+tile, map[string]any{})
	open = anon.must(200, "POST", "/v1/shared/dashboards/"+token, map[string]any{"password": "northpeak numbers"})
	key := open["key"].(string)
	if key == "" {
		t.Fatal("a password-protected link gives a key")
	}
	anon.must(200, "POST", "/v1/shared/dashboards/"+token+"/tiles/"+tile, map[string]any{"key": key})
	anon.must(401, "POST", "/v1/shared/dashboards/"+token+"/tiles/"+tile, map[string]any{"key": "deadbeef"})
	anon.must(200, "POST", "/v1/shared/dashboards/"+token, map[string]any{"key": key})
	// Taken back: nothing opens any more.
	owner.must(204, "DELETE", base+"/dashboards/"+did+"/share", nil)
	owner.must(404, "DELETE", base+"/dashboards/"+did+"/share", nil)
	anon.must(404, "POST", "/v1/shared/dashboards/"+token, map[string]any{"key": key})
	if owner.must(200, "GET", base+"/dashboards/"+did, nil)["share"] != nil {
		t.Fatal("the link is gone")
	}
	var viewed, failed int
	for _, ev := range got() {
		switch ev.Name {
		case "dashboard_share.viewed":
			viewed++
		case "dashboard_share.password_failed":
			failed++
		}
	}
	if viewed != 2 || failed != 1 {
		t.Fatalf("events: %d viewed, %d failed", viewed, failed)
	}

	// Alerts on a number: the trend's total (3 over the last 7 days).
	owner.must(400, "POST", base+"/insight-alerts", map[string]any{"insight_id": funnel, "condition": "above", "threshold": 1})
	owner.must(400, "POST", base+"/insight-alerts", map[string]any{"insight_id": trend, "condition": "over", "threshold": 1})
	owner.must(400, "POST", base+"/insight-alerts", map[string]any{"insight_id": trend, "condition": "rises_by", "threshold": -5})
	owner.must(400, "POST", base+"/insight-alerts", map[string]any{"insight_id": "00000000-0000-0000-0000-000000000000", "condition": "above", "threshold": 1})
	rule := owner.must(201, "POST", base+"/insight-alerts", map[string]any{"insight_id": trend, "condition": "above", "threshold": 2})
	rid := rule["id"].(string)
	if rule["state"] != "unknown" || rule["insight_name"] != "Visits" || rule["last_value"] != nil {
		t.Fatalf("rule: %v", rule)
	}
	calm := owner.must(201, "POST", base+"/insight-alerts", map[string]any{"insight_id": trend, "condition": "below", "threshold": 1})["id"].(string)
	if list := owner.must(200, "GET", base+"/insight-alerts?insight_id="+trend, nil)["data"].([]any); len(list) != 2 {
		t.Fatalf("list: %v", list)
	}
	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 20 * time.Millisecond
	e.srv.RegisterJobs(q)
	qctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(qctx, 2); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })
	check := func(wantState string, id string) map[string]any {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `UPDATE insight_alerts SET next_check_at = now() WHERE workspace_id = $1`, ws); err != nil {
			t.Fatal(err)
		}
		if n, err := e.srv.QueueDueInsightAlertChecks(ctx); err != nil || n != 2 {
			t.Fatalf("queued %d, %v", n, err)
		}
		for range 500 {
			for _, r := range owner.must(200, "GET", base+"/insight-alerts", nil)["data"].([]any) {
				if r := r.(map[string]any); r["id"] == id && r["state"] == wantState && r["last_checked_at"] != nil {
					return r
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("the alert did not reach %s", wantState)
		return nil
	}
	r := check("firing", rid)
	if r["last_value"] != float64(3) || r["fired_at"] == nil {
		t.Fatalf("fired: %v", r)
	}
	if r := check("ok", calm); r["fired_at"] != nil {
		t.Fatalf("calm rule: %v", r)
	}
	alertsOf := func() []any {
		var out []any
		for _, a := range owner.must(200, "GET", base+"/alerts", nil)["data"].([]any) {
			if a.(map[string]any)["kind"] == "insight.threshold" {
				out = append(out, a)
			}
		}
		return out
	}
	raised := alertsOf()
	if len(raised) != 1 || raised[0].(map[string]any)["title"] != "Visits is above 2" || raised[0].(map[string]any)["path"] != "trends/"+trend {
		t.Fatalf("raised: %v", raised)
	}
	// Still over the line next hour: nothing new.
	check("firing", rid)
	if len(alertsOf()) != 1 {
		t.Fatal("raised again while still over the line")
	}
	// A higher line: checked afresh, and the number is under it.
	owner.must(400, "PATCH", base+"/insight-alerts/"+rid, map[string]any{"insight_id": funnel})
	if r := owner.must(200, "PATCH", base+"/insight-alerts/"+rid, map[string]any{"threshold": 10}); r["state"] != "unknown" || r["fired_at"] != nil {
		t.Fatalf("after the change: %v", r)
	}
	check("ok", rid)
	// Back under the line, then over it again: raised once more.
	owner.must(200, "PATCH", base+"/insight-alerts/"+rid, map[string]any{"threshold": 2})
	check("firing", rid)
	if len(alertsOf()) != 2 {
		t.Fatalf("raised %d times", len(alertsOf()))
	}
	owner.must(204, "DELETE", base+"/insight-alerts/"+rid, nil)
	owner.must(404, "DELETE", base+"/insight-alerts/"+rid, nil)
	owner.must(204, "DELETE", base+"/insight-alerts/"+calm, nil)

	// The digest: the dashboard's trend over the week, against the week
	// before.
	p := owner.must(200, "GET", "/v1/workspaces/"+ws+"/digests/preview", nil)
	var found bool
	for _, sec := range p["sections"].([]any) {
		sec := sec.(map[string]any)
		if sec["id"] != "dashboards" {
			continue
		}
		found = true
		ms := sec["metrics"].([]any)
		if len(ms) != 1 {
			t.Fatalf("digest metrics: %v", ms)
		}
		m := ms[0].(map[string]any)
		if m["label"] != "Weekly numbers · Visits" || m["value"] != float64(3) || m["previous"] != float64(2) {
			t.Fatalf("digest metric: %v", m)
		}
	}
	if !found {
		t.Fatalf("no dashboards section: %v", p["sections"])
	}
	owner.must(200, "PATCH", base+"/dashboards/"+did, map[string]any{"settings": map[string]any{"digest": false}})
	for _, sec := range owner.must(200, "GET", "/v1/workspaces/"+ws+"/digests/preview", nil)["sections"].([]any) {
		if sec.(map[string]any)["id"] == "dashboards" {
			t.Fatal("a dashboard out of the digest still shows")
		}
	}
}
