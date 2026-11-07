package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// citedPage serves a review page that links to one competitor, names
// another and names the brand without a link.
type citedPage struct{}

func (citedPage) Fetch(ctx context.Context, u string) (*crawl.Response, error) {
	if strings.HasSuffix(u, "/robots.txt") {
		return &crawl.Response{URL: u, Status: 200, Body: []byte("User-agent: *\nAllow: /"), Header: http.Header{"Content-Type": []string{"text/plain"}}}, nil
	}
	body := `<html><head><title>Best proxy tools</title></head><body><p>We tried Northpeak and Small Co.</p>
		<p><a href="https://www.rival.example/pricing" rel="nofollow">Rival pricing</a></p></body></html>`
	return &crawl.Response{URL: u, Status: 200, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: []byte(body)}, nil
}

// A page AI answers cite, in full: when it was first cited, the questions,
// the site's numbers, and what reading it showed about who it links to.
// ChatGPT's own image links are not pages.
func TestAIVisibilityPage(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	var site, rival, small string
	if err := e.pool.QueryRow(ctx, `INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, 'northpeak.example', 'https://northpeak.example/', 'tok') RETURNING id`, ws, brand).Scan(&site); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		host, name string
		id         *string
	}{{"rival.example", "Rival", &rival}, {"small.example", "Small Co", &small}} {
		if err := e.pool.QueryRow(ctx, `INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, status) VALUES ($1, $2, $3, $4, $5, 'tracked') RETURNING id`, ws, site, brand, c.host, c.name).Scan(c.id); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO coverage_competitor_placements (workspace_id,brand_id,competitor_id,url,host,title,domain_rating,url_rating,domain_traffic) VALUES
		($1,$2,$3,'https://reviews.example/best','reviews.example','Best',72,30,50000)`, ws, brand, rival)
	var cohort, sample string
	if err := e.pool.QueryRow(ctx, `INSERT INTO ai_cohorts(workspace_id,brand_id,name) VALUES($1,$2,'Buyers') RETURNING id`, ws, brand).Scan(&cohort); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `WITH p AS (INSERT INTO ai_prompts(workspace_id,cohort_id,text) VALUES($1,$2,'best proxy tools') RETURNING id),
		r AS (INSERT INTO ai_runs(workspace_id,cohort_id,trigger) VALUES($1,$2,'manual') RETURNING id)
		INSERT INTO ai_samples(workspace_id,run_id,cohort_id,prompt_id,brand_id,provider,model,status,answer) SELECT $1,r.id,$2,p.id,$3,'openai','test','ok','Rival' FROM p,r RETURNING id`, ws, cohort, brand).Scan(&sample); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO ai_mentions(sample_id,workspace_id,cohort_id,entity,name,position,count,sampled_at) VALUES ($1,$2,$3,'competitor','Rival',1,1,now())`, sample, ws, cohort)
	exec(`INSERT INTO ai_citations(sample_id,workspace_id,cohort_id,url,host,title,position,owner,sampled_at) VALUES
		($1,$2,$3,'https://reviews.example/best','reviews.example','Best',1,'other',now() - interval '3 days'),
		($1,$2,$3,'https://images.openai.com/static-rsc-4/abc','images.openai.com','',2,'other',now())`, sample, ws, cohort)

	// The placements leave out the assistant's own image.
	places := owner.must(200, "GET", "/v1/brands/"+brand+"/ai-visibility/placements", nil)["data"].([]any)
	if len(places) != 1 || places[0].(map[string]any)["url"] != "https://reviews.example/best" || places[0].(map[string]any)["first_seen_at"] == nil {
		t.Fatalf("placements: %v", places)
	}
	page := "/v1/brands/" + brand + "/ai-visibility/page?page=" + url.QueryEscape("https://reviews.example/best")
	got := owner.must(200, "GET", page, nil)
	check := got["check"].(map[string]any)
	if got["answers"] != 1.0 || got["domain_rating"] != 72.0 || got["url_rating"] != 30.0 || got["links"] != nil || check["status"] != "none" ||
		len(got["questions"].([]any)) != 1 || got["questions"].([]any)[0].(map[string]any)["text"] != "best proxy tools" ||
		got["competitors"].([]any)[0].(map[string]any)["name"] != "Rival" {
		t.Fatalf("page: %v", got)
	}
	owner.must(404, "GET", "/v1/brands/"+brand+"/ai-visibility/page?page="+url.QueryEscape("https://nowhere.example/x"), nil)
	owner.must(400, "GET", "/v1/brands/"+brand+"/ai-visibility/page?page=nope", nil)

	// Reading it shows the brand named without a link, a nofollow link to
	// Rival and Small Co named.
	e.crawl.Fetcher = citedPage{}
	e.crawl.Wait = func(context.Context, time.Duration) error { return nil }
	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 10 * time.Millisecond
	e.srv.RegisterJobs(q)
	run, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = q.Run(run, 1); close(done) }()
	defer func() { cancel(); <-done }()
	if res := owner.must(202, "POST", "/v1/brands/"+brand+"/ai-visibility/page/check", map[string]any{"url": "https://reviews.example/best"}); res["queued"] != true {
		t.Fatalf("check: %v", res)
	}
	deadline := time.Now().Add(10 * time.Second)
	for check["status"] != "done" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		check = owner.must(200, "GET", page, nil)["check"].(map[string]any)
	}
	found := map[string]map[string]any{}
	for _, f := range check["found"].([]any) {
		found[f.(map[string]any)["name"].(string)] = f.(map[string]any)
	}
	if check["status"] != "done" || len(found) != 3 || found["Northpeak"]["how"] != "mention" || found["Northpeak"]["competitor_id"] != nil ||
		found["Rival"]["how"] != "link" || found["Rival"]["nofollow"] != true || found["Rival"]["anchor"] != "Rival pricing" || found["Rival"]["competitor_id"] != rival ||
		found["Small Co"]["how"] != "mention" {
		t.Fatalf("check: %v", check)
	}
	owner.must(404, "POST", "/v1/brands/"+brand+"/ai-visibility/page/check", map[string]any{"url": "https://nowhere.example/x"})
	seen := map[string]bool{}
	for _, ev := range events() {
		seen[ev.Name] = true
	}
	if !seen["ai_visibility.page_check_requested"] || !seen["ai_visibility.pages_checked"] {
		t.Fatalf("events: %v", seen)
	}
}
