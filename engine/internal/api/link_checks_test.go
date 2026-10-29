package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/emailfind"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/linkbuild"
	"github.com/outflareai/outflare/engine/internal/track"
)

// readLinkPage is the fake AI page reader's verdict.
func readLinkPage(w http.ResponseWriter) {
	verdict, _ := json.Marshal(map[string]any{"page_kind": "resource_list", "topic": "hiking gear", "author": "", "fit": 80,
		"fit_reason": "A list of hiking resources.", "best_page_url": "", "best_page_reason": ""})
	json.NewEncoder(w).Encode(map[string]any{
		"id": "msg_l", "type": "message", "role": "assistant", "model": "claude-haiku-5-5",
		"content":     []any{map[string]any{"type": "text", "text": string(verdict)}},
		"stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 1500, "output_tokens": 120},
	})
}

// runLinkChecks runs the link building jobs: page checks, pitch openers
// and reading replies.
func (e *env) runLinkChecks(t *testing.T) *linkbuild.Service {
	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 20 * time.Millisecond
	q.Only = []string{linkbuild.JobCheck, linkbuild.JobWriteOpener, linkbuild.JobReadReply}
	svc := &linkbuild.Service{Pool: e.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Crawl: e.crawl, Models: e.srv.AIModels(),
		Emails: &emailfind.Service{Pool: e.pool, Resolver: e.dns}}
	svc.Register(q)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { q.Run(ctx, 2); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })
	return svc
}

func TestLinkProspectPageChecks(t *testing.T) {
	e := freshEnv(t)
	owner, _ := setupOwner(t, e.h)
	events := track.Capture(t)
	// A test workspace: OutflareBot reads the simulated site, never a real one.
	ws := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Sandbox", "test": true})["id"].(string)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})
	siteID := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.example.com/"})["id"].(string)
	e.runLinkChecks(t)

	res := owner.must(200, "POST", base+"/link-prospects", map[string]any{"site_id": siteID, "prospects": []any{
		map[string]any{"page_url": "https://blog.example.org/resources", "method": "resource_page", "domain_rating": 40},
		map[string]any{"page_url": "https://blog.example.org/links", "method": "broken_link", "domain_rating": 40},
		map[string]any{"page_url": "https://gone.example.org/missing", "method": "resource_page"},
	}})["data"].([]any)
	if q := owner.must(202, "POST", base+"/link-prospects/check", map[string]any{"site_id": siteID}); q["count"].(float64) != 3 {
		t.Fatalf("queued: %v", q)
	}
	get := func(i int) map[string]any {
		return owner.must(200, "GET", base+"/link-prospects/"+res[i].(map[string]any)["id"].(string), nil)
	}
	checked := func(i int) map[string]any {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			p := get(i)
			if c, ok := p["check"].(map[string]any); ok && c["status"] != nil {
				return p
			}
			if time.Now().After(deadline) {
				t.Fatalf("not checked: %v", p)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	// A page that already links to the site is kept out of outreach; its
	// broken link has an archive snapshot; addresses on the page and its
	// contact page are contacts, and another site's address isn't.
	p := checked(0)
	c := p["check"].(map[string]any)
	if c["status"] != "up" || c["links_to_us"] != true || c["title"] != "Hiking gear resources" || c["author"] != "Jo Rivers" ||
		c["page_kind"] != "resource_list" || c["fit"].(float64) != 80 || c["outbound"].(float64) != 1 {
		t.Fatalf("check: %v", c)
	}
	broken := c["broken"].([]any)
	if len(broken) != 1 || broken[0].(map[string]any)["url"] != "https://tools.example/old-tool" || broken[0].(map[string]any)["archive_url"] == "" {
		t.Fatalf("broken: %v", broken)
	}
	if p["stage"] != "excluded" || p["excluded_reason"] != "links_to_us" {
		t.Fatalf("excluded: %v", p)
	}
	contacts := p["contacts"].([]any)
	if len(contacts) != 2 || contacts[0].(map[string]any)["email"] != "jo@blog.example.org" || contacts[0].(map[string]any)["source"] != "page" ||
		contacts[1].(map[string]any)["email"] != "editor@blog.example.org" || contacts[1].(map[string]any)["source"] != "contact_page" {
		t.Fatalf("contacts: %v", contacts)
	}
	// The relevance the reader found counts in the score: 40/2 + 10 + 9.
	if p["score"].(float64) != 39 {
		t.Fatalf("score: %v %v", p["score"], p["score_factors"])
	}

	// A page that doesn't link stays to pitch; a gone page is kept out.
	if p := checked(1); p["stage"] != "prospect" || p["check"].(map[string]any)["links_to_us"] != false || len(p["contacts"].([]any)) != 1 {
		t.Fatalf("to pitch: %v", p)
	}
	if p := checked(2); p["stage"] != "excluded" || p["excluded_reason"] != "page_gone" || p["check"].(map[string]any)["status"] != "gone" {
		t.Fatalf("gone: %v", p)
	}
	// The contacts are person records on the website's company. (Two
	// checks at once can each add the same address's person; merge
	// suggestions catch those, as for any import.)
	var people, linked int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(DISTINCT k.record_id), count(DISTINCT r.from_id) FROM link_prospect_contacts k
		LEFT JOIN record_relationships r ON r.from_id = k.record_id AND r.to_id = $2 AND r.kind = 'companies' WHERE k.workspace_id = $1`,
		ws, res[0].(map[string]any)["record_id"]).Scan(&people, &linked); err != nil || people < 2 || linked != people {
		t.Fatalf("people: %d linked %d %v", people, linked, err)
	}
	// Checking again within the hour queues nothing new.
	owner.must(202, "POST", base+"/link-prospects/"+res[1].(map[string]any)["id"].(string)+"/check", nil)
	if q := owner.must(202, "POST", base+"/link-prospects/check", map[string]any{"site_id": siteID, "again": true}); q["count"].(float64) != 0 {
		t.Fatalf("again: %v", q)
	}

	got := map[string]bool{}
	for _, ev := range events() {
		got[ev.Name] = true
	}
	for _, name := range []string{"link_prospect.checked", "link_prospect.check_requested", "link_prospect.excluded"} {
		if !got[name] {
			t.Errorf("no %s event", name)
		}
	}
}

// answerJSON is a fake Claude answer whose text is v as JSON.
func answerJSON(w http.ResponseWriter, v any) {
	text, _ := json.Marshal(v)
	json.NewEncoder(w).Encode(map[string]any{
		"id": "msg_j", "type": "message", "role": "assistant", "model": "claude-haiku-5-5",
		"content":     []any{map[string]any{"type": "text", "text": string(text)}},
		"stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 800, "output_tokens": 80},
	})
}
