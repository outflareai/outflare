package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// watchPage serves one page whose body and status the test changes.
type watchPage struct {
	body   atomic.Value
	status atomic.Int32
}

func (f *watchPage) Fetch(ctx context.Context, u string) (*crawl.Response, error) {
	if strings.HasSuffix(u, "/robots.txt") {
		return &crawl.Response{URL: u, Status: 200, Body: []byte("User-agent: *\nAllow: /"), Header: http.Header{"Content-Type": []string{"text/plain"}}}, nil
	}
	return &crawl.Response{URL: u, Status: int(f.status.Load()), Header: http.Header{"Content-Type": []string{"text/html"}}, Body: []byte(f.body.Load().(string))}, nil
}

// Presence watches each placement: with no target page or expected wording
// set, it still asks whether the page links to any of the brand's domains
// and names the brand, keeps what two checks agree on, and raises work when
// the link or the mention is removed or the page goes offline.
func TestPresenceWatchesLinkMentionAndOffline(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	root := "/v1/brands/" + brand + "/coverage"
	rec := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Example publisher", "brand_id": brand})
	pub := owner.must(201, "POST", root+"/publishers", map[string]any{"record_id": rec["id"], "domain": "publisher.example"})
	id := owner.must(201, "POST", root+"/placements", map[string]any{"publisher_id": pub["id"], "title": "Roundup", "url": "https://publisher.example/best-tools"})["id"].(string)

	with := `<html><body>Northpeak is our pick. <a href="https://www.northpeak.example/pricing">See pricing</a></body></html>`
	without := `<html><body>Acme is our pick.</body></html>`
	fake := &watchPage{}
	fake.body.Store(with)
	fake.status.Store(200)
	e.crawl.Fetcher = fake
	e.crawl.Wait = func(context.Context, time.Duration) error { return nil }
	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 10 * time.Millisecond
	e.srv.RegisterJobs(q)
	run, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = q.Run(run, 1); close(done) }()
	defer func() { cancel(); <-done }()
	check := func() map[string]any {
		t.Helper()
		job, err := jobs.Enqueue(ctx, e.pool, jobs.NewJob{Kind: "coverage.check", WorkspaceID: &ws, Payload: map[string]string{"placement_id": id, "brand_id": brand}, MaxAttempts: 1})
		if err != nil {
			t.Fatal(err)
		}
		for until := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			var status string
			if err = e.pool.QueryRow(ctx, "SELECT status FROM jobs WHERE id=$1", job).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status == "succeeded" {
				break
			}
			if status == "dead" || time.Now().After(until) {
				t.Fatalf("check ended %s", status)
			}
		}
		return owner.must(200, "GET", root+"/placements/"+id, nil)
	}
	tasks := func(reason, status string) float64 {
		t.Helper()
		v := url.QueryEscape(`{"mode":"all","rules":[{"field":"reason","op":"eq","value":"` + reason + `"},{"field":"status","op":"eq","value":"` + status + `"}]}`)
		return owner.must(200, "GET", root+"/tasks?filters="+v, nil)["total"].(float64)
	}

	// The brand's name and a link to its site are seen, without verifying a
	// placement that expected neither.
	p := check()
	if p["link_status"] != "present" || p["mention_status"] != "present" || p["first_verified_at"] != nil {
		t.Fatalf("first check: %v", p)
	}
	fake.body.Store(without)
	if p = check(); p["link_status"] != "present" || p["mention_status"] != "present" {
		t.Fatalf("one check without them removed them: %v", p)
	}
	p = check()
	if p["link_status"] != "removed" || p["mention_status"] != "removed" || p["link_removed_at"] == nil || p["mention_removed_at"] == nil {
		t.Fatalf("two checks without them: %v", p)
	}
	if tasks("mention_missing", "open") != 1 || tasks("link_missing", "open") != 1 {
		t.Fatal("a removed mention and link raise work")
	}
	check()
	if tasks("mention_missing", "open") != 1 {
		t.Fatal("a removed mention raised work twice")
	}

	fake.body.Store(with)
	if p = check(); p["link_status"] != "present" || p["mention_status"] != "present" || p["link_removed_at"] != nil {
		t.Fatalf("restored: %v", p)
	}
	if tasks("mention_missing", "resolved") != 1 || tasks("link_missing", "resolved") != 1 {
		t.Fatal("restored mention and link resolve their work")
	}

	fake.status.Store(404)
	if p = check(); p["offline_since"] != nil {
		t.Fatalf("one missing page is not offline: %v", p)
	}
	if p = check(); p["offline_since"] == nil || p["health"] != "gone" {
		t.Fatalf("two missing pages: %v", p)
	}
	fake.status.Store(200)
	if p = check(); p["offline_since"] != nil || p["link_status"] != "present" {
		t.Fatalf("back online: %v", p)
	}

	// Rules can switch the new work off like any other.
	owner.must(201, "POST", root+"/rules", map[string]any{"title": "No mention alerts", "kind": "mention_missing", "enabled": false})
}

// The brand facts on a watched page are checked again soon after the
// brand's published Knowledge changes, at most once a day.
func TestPresenceRechecksFactsWhenKnowledgeChanges(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	root := "/v1/brands/" + brand + "/coverage"
	rec := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Example publisher", "brand_id": brand})
	pub := owner.must(201, "POST", root+"/publishers", map[string]any{"record_id": rec["id"], "domain": "publisher.example"})
	id := owner.must(201, "POST", root+"/placements", map[string]any{"publisher_id": pub["id"], "title": "Review", "url": "https://publisher.example/review", "monitor_accuracy": true})["id"].(string)
	if _, err := e.pool.Exec(ctx, `INSERT INTO coverage_observations(workspace_id,brand_id,placement_id,health,source,evidence) VALUES($1,$2,$3,'up','page_read',jsonb_build_object('url','https://publisher.example/review'))`, ws, brand, id); err != nil {
		t.Fatal(err)
	}
	queued := func() int {
		t.Helper()
		if _, err := e.srv.QueueDueCoverage(ctx); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE workspace_id=$1 AND kind='coverage.review'`, ws).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := queued(); n != 1 {
		t.Fatalf("first scheduled check: %d", n)
	}
	// Nothing changed: the next check waits for its schedule.
	if n := queued(); n != 1 {
		t.Fatalf("checked again without a change: %d", n)
	}
	// Knowledge changed a day after the last check: checked again now.
	if _, err := e.pool.Exec(ctx, `UPDATE coverage_placements SET accuracy_queued_at=now()-interval '2 days' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	owner.must(201, "POST", base+"/knowledge/items", map[string]any{"title": "Pricing", "body": "Northpeak costs $10 a month.", "brand_id": brand, "visibility": "customers_ai"})
	if n := queued(); n != 2 {
		t.Fatalf("knowledge change did not queue a check: %d", n)
	}
	if n := queued(); n != 2 {
		t.Fatalf("checked twice in a day: %d", n)
	}
}
