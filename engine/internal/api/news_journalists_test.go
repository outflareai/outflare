package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/emailfind"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/news"
	"github.com/outflareai/outflare/engine/internal/smartlists"
	"github.com/outflareai/outflare/engine/internal/track"
)

// runArticleReads runs the news article read jobs.
func (e *env) runArticleReads(t *testing.T) {
	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 20 * time.Millisecond
	q.Only = []string{news.JobRead}
	(&news.Reader{Pool: e.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Crawl: e.crawl,
		Emails: &emailfind.Service{Pool: e.pool, Resolver: e.dns}}).Register(q)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { q.Run(ctx, 2); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })
}

func TestNewsWatchFindsJournalists(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, _ := setupOwner(t, e.h)
	ctx := context.Background()
	// A test workspace: OutflareBot reads the simulated site, never a real one.
	ws := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Sandbox", "test": true})["id"].(string)
	base := "/v1/workspaces/" + ws
	fake := &fakeNewsSearch{results: []map[string]any{
		{"title": "Northpeak raises a seed round", "url": "https://news.example.org/news/northpeak-seed", "snippet": "Northpeak, the proxy firm"},
		{"title": "Hiking pricing links", "url": "https://blog.example.org/links", "snippet": "Northpeak and more"},
		{"title": "A page that's gone", "url": "https://old.example.net/missing", "snippet": "Northpeak"},
	}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	e.integrations.Client.BaseURLs["geonode"] = srv.URL
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "geonode", "credentials": map[string]string{"api_key": "gn-key"}})
	// The workspace once verified the reporter's address.
	if _, err := e.pool.Exec(ctx, `INSERT INTO email_verifications (workspace_id, email, status, provider) VALUES ($1, 'sam.okafor@news.example.org', 'deliverable', 'builtin')`, ws); err != nil {
		t.Fatal(err)
	}
	search := owner.must(201, "POST", base+"/news-searches", map[string]any{"name": "Proxy news", "query": "proxies"})["id"].(string)
	if err := e.srv.RunNewsSearch(ctx, ws, search); err != nil {
		t.Fatal(err)
	}
	var queued int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE workspace_id = $1 AND kind = $2`, ws, news.JobRead).Scan(&queued); err != nil || queued != 3 {
		t.Fatalf("reads queued: %d %v", queued, err)
	}
	e.runArticleReads(t)

	// Each article is read once: the byline and date from its JSON-LD, no
	// byline, and a page that's gone.
	articles := map[string]map[string]any{}
	deadline := time.Now().Add(15 * time.Second)
	for {
		list := owner.must(200, "GET", base+"/news-articles", nil)["data"].([]any)
		read := 0
		for _, x := range list {
			x := x.(map[string]any)
			articles[x["url"].(string)] = x
			if x["read_status"] != nil {
				read++
			}
		}
		if read == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not read: %v", list)
		}
		time.Sleep(50 * time.Millisecond)
	}
	seed := articles["https://news.example.org/news/northpeak-seed"]
	if seed["read_status"] != "up" || seed["author"] != "Sam Okafor" || seed["published_at"] != "2026-10-27T09:00:00Z" || len(seed["journalist_ids"].([]any)) != 1 {
		t.Fatalf("seed article: %v", seed)
	}
	if a := articles["https://blog.example.org/links"]; a["read_status"] != "up" || a["author"] != "" || len(a["journalist_ids"].([]any)) != 0 {
		t.Fatalf("no byline: %v", a)
	}
	if a := articles["https://old.example.net/missing"]; a["read_status"] != "gone" {
		t.Fatalf("gone: %v", a)
	}

	// The reporter is a journalist: a person record on the outlet's company,
	// with what they wrote and the search it came up in.
	list := owner.must(200, "GET", base+"/journalists", nil)
	if list["total"] != 1.0 {
		t.Fatalf("journalists: %v", list)
	}
	j := list["data"].([]any)[0].(map[string]any)
	if j["name"] != "Sam Okafor" || j["email"] != "sam.okafor@news.example.org" || j["articles"] != 1.0 || j["confidence"] != 95.0 ||
		len(j["outlets"].([]any)) != 1 || j["outlets"].([]any)[0] != "news.example.org" ||
		j["latest_article"].(map[string]any)["title"] != "Northpeak raises a seed round" ||
		len(j["topics"].([]any)) != 1 || j["topics"].([]any)[0].(map[string]any)["name"] != "Proxy news" {
		t.Fatalf("journalist: %v", j)
	}
	var onOutlet bool
	if err := e.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM record_relationships rr JOIN records c ON c.id = rr.to_id
		WHERE rr.from_id = $1 AND rr.kind = 'companies' AND c.record_type = 'account')`, j["record_id"]).Scan(&onOutlet); err != nil || !onOutlet {
		t.Fatalf("on the outlet's company: %v %v", onOutlet, err)
	}
	if n := owner.must(200, "GET", base+"/news-articles?journalist_id="+j["record_id"].(string), nil)["total"]; n != 1.0 {
		t.Fatalf("their articles: %v", n)
	}
	other := owner.must(201, "POST", base+"/news-searches", map[string]any{"query": "other"})["id"].(string)
	if n := owner.must(200, "GET", base+"/journalists?search_id="+other, nil)["total"]; n != 0.0 {
		t.Fatalf("another search's journalists: %v", n)
	}
	owner.must(400, "GET", base+"/journalists?since=yesterday", nil)

	// A media list is a smart list of the journalists who wrote about a
	// topic.
	media := owner.must(201, "POST", base+"/smart-lists", map[string]any{"name": "Proxy reporters",
		"rules": []any{map[string]any{"news_search": search, "op": "exists", "within_days": 3650}}})["id"].(string)
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		_, err := smartlists.Refresh(ctx, tx, ws, media)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if m := owner.must(200, "GET", base+"/smart-lists/"+media+"/members", nil)["data"].([]any); len(m) != 1 ||
		m[0].(map[string]any)["email"] != "sam.okafor@news.example.org" {
		t.Fatalf("media list: %v", m)
	}
	owner.must(400, "POST", base+"/smart-lists", map[string]any{"name": "Bad", "rules": []any{map[string]any{"news_search": "00000000-0000-0000-0000-000000000000", "op": "exists"}}})
	owner.must(400, "POST", base+"/smart-lists", map[string]any{"name": "Bad", "rules": []any{map[string]any{"news_search": search, "op": "eq", "value": "x"}}})

	// Running the search again reads nothing new.
	if err := e.srv.RunNewsSearch(ctx, ws, search); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE workspace_id = $1 AND kind = $2`, ws, news.JobRead).Scan(&queued); err != nil || queued != 3 {
		t.Fatalf("reads after a second run: %d %v", queued, err)
	}

	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
	}
	if names["news_article.read"] != 3 || names["journalist.found"] != 1 {
		t.Fatalf("events: %v", names)
	}
}
