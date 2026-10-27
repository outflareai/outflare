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
