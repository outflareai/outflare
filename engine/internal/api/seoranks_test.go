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

// runRanks runs rank check jobs in the background until the test ends.
func (e *env) runRanks(t *testing.T) {
	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 20 * time.Millisecond
	(&ranks.Service{Pool: e.pool, Store: e.integrations, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Register(q)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { q.Run(ctx, 2); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })
}

func TestRankTracking(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	site := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.example.com/"})["id"].(string)
	sb := base + "/seo-sites/" + site

	// Nothing to check with yet.
	if l := owner.must(200, "GET", sb+"/rankings", nil); l["connection"] != nil || len(l["data"].([]any)) != 0 {
		t.Fatalf("empty: %v", l)
	}
	owner.must(409, "POST", sb+"/rankings/check", nil)

	dfs := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})["id"].(string)
	owner.must(200, "PUT", sb+"/targets", map[string]any{"url": "https://www.example.com/pricing", "keyword": "proxy pricing"})
	add := owner.must(201, "POST", sb+"/rankings", map[string]any{"keywords": []string{"Cheap  Proxies", "cheap proxies"}, "from_targets": true})
	if add["added"].(float64) != 2 {
		t.Fatalf("added: %v", add)
	}
	owner.must(201, "POST", sb+"/rankings", map[string]any{"keywords": []string{"proxy pricing"}, "device": "mobile", "frequency": "daily"})
	if again := owner.must(201, "POST", sb+"/rankings", map[string]any{"keywords": []string{"proxy pricing"}}); again["added"].(float64) != 0 || again["skipped"].(float64) != 1 {
		t.Fatalf("already tracked: %v", again)
	}
	owner.must(400, "POST", sb+"/rankings", map[string]any{"keywords": []string{"x"}, "country": "USA"})
	owner.must(400, "POST", sb+"/rankings", map[string]any{"keywords": []string{}})
	owner.must(400, "POST", sb+"/rankings", map[string]any{"keywords": []string{"x"}, "integration_id": "00000000-0000-0000-0000-000000000000"})

	// The first checks run as a job straight away.
	e.runRanks(t)
	byKey := func() map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, r := range owner.must(200, "GET", sb+"/rankings", nil)["data"].([]any) {
			r := r.(map[string]any)
			out[r["keyword"].(string)+"/"+r["device"].(string)] = r
		}
		return out
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		k := byKey()
		if k["proxy pricing/desktop"]["last_checked_at"] != nil && k["proxy pricing/mobile"]["last_checked_at"] != nil && k["cheap proxies/desktop"]["last_checked_at"] != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not checked: %v", k)
		}
		time.Sleep(50 * time.Millisecond)
	}
	k := byKey()
	pp := k["proxy pricing/desktop"]
	if pp["position"].(float64) != 3 || pp["url"] != "https://www.example.com/pricing" || pp["target_url"] != "https://www.example.com/pricing" || pp["engine"] != "google" {
		t.Fatalf("proxy pricing: %v", pp)
	}
	if k["proxy pricing/mobile"]["position"].(float64) != 2 || k["cheap proxies/desktop"]["position"] != nil || k["cheap proxies/desktop"]["last_error"] != nil {
		t.Fatalf("others: %v", k)
	}
	l := owner.must(200, "GET", sb+"/rankings", nil)
	if c := l["connection"].(map[string]any); c["integration_id"] != dfs || c["connector"] != "dataforseo" {
		t.Fatalf("connection: %v", l["connection"])
	}
	h := owner.must(200, "GET", sb+"/rankings/"+pp["id"].(string)+"/history", nil)["data"].([]any)
	if len(h) != 1 || len(h[0].(map[string]any)["top"].([]any)) != 2 {
		t.Fatalf("history: %v", h)
	}
	// Each check is spend on the connection.
	spend := owner.must(200, "GET", base+"/spend", nil)["data"].([]any)
	if len(spend) != 3 || spend[0].(map[string]any)["provider"] != "dataforseo" {
		t.Fatalf("spend: %v", spend)
	}

	// Paused keywords wait; removed ones go.
	cp := k["cheap proxies/desktop"]["id"].(string)
	if v := owner.must(200, "PATCH", sb+"/rankings/"+cp, map[string]any{"status": "paused", "frequency": "daily"}); v["status"] != "paused" || v["frequency"] != "daily" {
		t.Fatalf("paused: %v", v)
	}
	owner.must(400, "PATCH", sb+"/rankings/"+cp, map[string]any{"country": "GB"})
	owner.must(204, "DELETE", sb+"/rankings/"+cp, nil)
	owner.must(404, "DELETE", sb+"/rankings/"+cp, nil)

	// A budget that is spent stops checks and says so on the keyword.
	owner.must(200, "PUT", base+"/seo/"+dfs+"/settings", map[string]any{"monthly_budget": 0.05})
	if _, err := e.pool.Exec(context.Background(), `UPDATE tracked_keywords SET last_checked_at = NULL`); err != nil {
		t.Fatal(err)
	}
	owner.must(202, "POST", sb+"/rankings/check", nil)
	deadline = time.Now().Add(10 * time.Second)
	for byKey()["proxy pricing/mobile"]["last_error"] == nil {
		if time.Now().After(deadline) {
			t.Fatalf("budget: %v", byKey())
		}
		time.Sleep(50 * time.Millisecond)
	}

	// A failed check is tried again the same day, after a wait, as a new
	// job each time it fails again.
	svc := &ranks.Service{Pool: e.pool, Store: e.integrations, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	retries := func() int {
		var n int
		e.pool.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE kind = $1 AND workspace_id = $2 AND idempotency_key LIKE '%:retry:%'`, ranks.JobCheck, ws).Scan(&n)
		return n
	}
	var failures int
	e.pool.QueryRow(context.Background(), `SELECT max(failures) FROM tracked_keywords WHERE workspace_id = $1`, ws).Scan(&failures)
	if n, err := svc.QueueDue(context.Background()); err != nil || failures < 1 || retries() != 0 {
		t.Fatalf("retried before the wait: queued %d, %v, failures %d, retries %d", n, err, failures, retries())
	}
	e.pool.Exec(context.Background(), `UPDATE tracked_keywords SET last_failed_at = now() - interval '20 minutes' WHERE workspace_id = $1 AND failures = 1`, ws)
	for range 2 {
		if _, err := svc.QueueDue(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if retries() != 1 {
		t.Fatalf("retry jobs after the wait: %d", retries())
	}

	// Another workspace sees none of it.
	ows := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Other"})["id"].(string)
	owner.must(404, "GET", "/v1/workspaces/"+ows+"/seo-sites/"+site+"/rankings", nil)

	// The event follows the check that wrote the error.
	deadline = time.Now().Add(5 * time.Second)
	for {
		var checked, failed int
		for _, ev := range got() {
			if ev.Name == "rank.checked" && ev.WorkspaceID == ws {
				if ev.Outcome == track.Failure && ev.ErrorCode == "budget_reached" {
					failed++
				} else {
					checked++
				}
			}
		}
		if checked > 0 && failed > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("rank.checked events: %d checked, %d failed", checked, failed)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestRankTrackingWithGeonode(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	site := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.example.com/"})["id"].(string)
	sb := base + "/seo-sites/" + site

	// Geonode alone is the default.
	gn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "geonode", "credentials": map[string]string{"api_key": "gn-key"}})["id"].(string)
	if c := owner.must(200, "GET", sb+"/rankings", nil)["connection"].(map[string]any); c["integration_id"] != gn || c["connector"] != "geonode" {
		t.Fatalf("geonode default: %v", c)
	}
	// With DataForSEO too, DataForSEO stays the default and both are listed.
	dfs := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})["id"].(string)
	l := owner.must(200, "GET", sb+"/rankings", nil)
	conns := l["connections"].([]any)
	if l["connection"].(map[string]any)["integration_id"] != dfs || len(conns) != 2 || conns[1].(map[string]any)["integration_id"] != gn {
		t.Fatalf("both: %v", l)
	}

	owner.must(201, "POST", sb+"/rankings", map[string]any{"keywords": []string{"proxy pricing", "cut short", "nothing here"}, "integration_id": gn})
	e.runRanks(t)
	byKey := func() map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, r := range owner.must(200, "GET", sb+"/rankings", nil)["data"].([]any) {
			out[r.(map[string]any)["keyword"].(string)] = r.(map[string]any)
		}
		return out
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		k := byKey()
		if k["proxy pricing"]["last_checked_at"] != nil && k["nothing here"]["last_checked_at"] != nil && k["cut short"]["last_error"] != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not checked: %v", k)
		}
		time.Sleep(50 * time.Millisecond)
	}
	k := byKey()
	// 13th on Geonode's Google (DataForSEO would have said 3).
	if pp := k["proxy pricing"]; pp["position"].(float64) != 13 || pp["url"] != "https://www.example.com/pricing" || pp["engine"] != "google" || pp["integration_id"] != gn {
		t.Fatalf("proxy pricing: %v", pp)
	}
	if nh := k["nothing here"]; nh["position"] != nil || nh["last_error"] != nil {
		t.Fatalf("no results: %v", nh)
	}
	// Stopped early without finding the site: not "not in the top 100".
	if cs := k["cut short"]; cs["position"] != nil || cs["last_checked_at"] != nil {
		t.Fatalf("cut short: %v", cs)
	}
	// Pages returned are spend on the Geonode connection; DataForSEO was not called.
	for _, s := range owner.must(200, "GET", base+"/spend", nil)["data"].([]any) {
		if p := s.(map[string]any)["provider"]; p != "geonode" {
			t.Fatalf("spend on %v", p)
		}
	}

	// A keyword can move to the other connection.
	if v := owner.must(200, "PATCH", sb+"/rankings/"+k["proxy pricing"]["id"].(string), map[string]any{"integration_id": dfs}); v["integration_id"] != dfs {
		t.Fatalf("moved: %v", v)
	}

	deadline = time.Now().Add(5 * time.Second)
	for {
		failed := false
		for _, ev := range got() {
			if ev.Name == "rank.checked" && ev.WorkspaceID == ws && ev.Outcome == track.Error && ev.ErrorCode == "connector_unavailable" {
				failed = true
			}
		}
		if failed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no rank.checked event for the incomplete check")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
