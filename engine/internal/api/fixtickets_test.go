package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// ticketPrompts keeps what the brief writer was sent, for the test to read.
var ticketPrompts struct {
	sync.Mutex
	last string
}

// writeTicket answers fakeClaude's brief requests with a brief.
func writeTicket(w http.ResponseWriter, body string) {
	ticketPrompts.Lock()
	ticketPrompts.last = body
	ticketPrompts.Unlock()
	brief, _ := json.Marshal(map[string]any{
		"kind": "code", "title": "Add titles to 3 product pages",
		"summary": "Three pages under /p/ have no title, so search results make one up.",
		"why":     "Titles are the headline in search results.", "change": "Set the title in the product page template from the product name.",
		"done_when": []string{"Every page under /p/ has one unique title of 30 to 60 characters."}, "dont": []string{"Don't touch the home page."},
	})
	json.NewEncoder(w).Encode(map[string]any{
		"id": "msg_t", "type": "message", "role": "assistant", "model": "claude-sonnet-5-5",
		"content":     []any{map[string]any{"type": "text", "text": string(brief)}},
		"stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 2000, "output_tokens": 400},
	})
}

func TestFixTickets(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/":
			io.WriteString(w, `<html><head><title>Home page title</title></head><body><a href="/p/one">1</a><a href="/p/two">2</a><a href="/p/three">3</a><a href="/blog/a">a</a></body></html>`)
		case "/p/one", "/p/two", "/p/three", "/blog/a":
			io.WriteString(w, `<html><head></head><body><a href="/">home</a></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer site.Close()
	id := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": site.URL + "/"})["id"].(string)
	e.runCrawls(t)
	rid := owner.must(202, "POST", base+"/seo-sites/"+id+"/crawls", nil)["id"].(string)
	waitFor(t, owner, base+"/crawls/"+rid, func(m map[string]any) bool { return m["status"] == "done" })
	e.runServerJobs(t)

	// One ticket per problem, its parts one per URL template.
	var ticket map[string]any
	for _, x := range owner.must(200, "GET", base+"/fix-tickets?subject_id="+id, nil)["data"].([]any) {
		if x := x.(map[string]any); x["rule"].(map[string]any)["id"] == "title_missing" {
			ticket = x
		}
	}
	if ticket == nil || ticket["count"] != 4.0 || ticket["parts"] != 2.0 || ticket["status"] != "open" || ticket["title"] != "Missing title" ||
		ticket["brief_status"] != "none" || ticket["brief"] != nil {
		t.Fatalf("ticket: %v", ticket)
	}
	tid := ticket["id"].(string)
	d := owner.must(200, "GET", base+"/fix-tickets/"+tid, nil)
	if len(d["fixes"].([]any)) != 2 || len(d["pages"].([]any)) != 4 || len(d["related"].([]any)) == 0 {
		t.Fatalf("detail: %v", d)
	}
	owner.must(404, "GET", base+"/fix-tickets/00000000-0000-0000-0000-000000000000", nil)
	owner.must(400, "GET", base+"/fix-tickets?status=later", nil)
	// The plain prompt works before any AI is connected.
	if p := owner.must(200, "GET", base+"/fix-tickets/"+tid+"/prompt", nil)["prompt"].(string); !strings.Contains(p, "/p/*:") || !strings.Contains(p, "/verify") {
		t.Fatalf("plain prompt: %s", p)
	}

	// Writing needs the workspace's own AI model.
	if c := owner.must(409, "POST", base+"/fix-tickets/"+tid+"/write", nil)["error"].(map[string]any)["code"]; c != "connector_needed" {
		t.Fatalf("no AI: %v", c)
	}
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})

	// What people decided goes into every brief: a rejected ticket's reason
	// and a saved guideline.
	var other string
	for _, x := range d["related"].([]any) {
		if x := x.(map[string]any); x["status"] == "open" {
			other = x["id"].(string)
			break
		}
	}
	owner.must(400, "POST", base+"/fix-tickets/"+other+"/review", map[string]any{"decision": "reject"})
	rej := owner.must(200, "POST", base+"/fix-tickets/"+other+"/review", map[string]any{"decision": "reject", "note": "Our CMS can't change this", "guideline": "site"})
	if rej["review"] != "rejected" || rej["status"] != "ignored" {
		t.Fatalf("rejected: %v", rej)
	}
	owner.must(201, "POST", base+"/fix-guidelines", map[string]any{"text": "Use sentence case in titles"})
	if g := owner.must(200, "GET", base+"/fix-guidelines", nil)["data"].([]any); len(g) != 2 {
		t.Fatalf("guidelines: %v", g)
	}

	owner.must(202, "POST", base+"/fix-tickets/"+tid+"/write", map[string]any{"feedback": "Mention the product name"})
	owner.must(409, "POST", base+"/fix-tickets/"+tid+"/write", nil)
	w := waitFor(t, owner, base+"/fix-tickets/"+tid, func(m map[string]any) bool { return m["brief_status"] == "ready" || m["brief_status"] == "failed" })
	if w["brief_status"] != "ready" || w["title"] != "Add titles to 3 product pages" || w["brief"].(map[string]any)["kind"] != "code" {
		t.Fatalf("written: %v", w)
	}
	ticketPrompts.Lock()
	sent := ticketPrompts.last
	ticketPrompts.Unlock()
	for _, want := range []string{"Our CMS can't change this", "Use sentence case in titles", "Mention the product name", "/p/one", "Missing title"} {
		if !strings.Contains(sent, want) {
			t.Errorf("brief context lacks %q", want)
		}
	}
	p := owner.must(200, "GET", base+"/fix-tickets/"+tid+"/prompt", nil)["prompt"].(string)
	if !strings.Contains(p, "# Add titles to 3 product pages") || !strings.Contains(p, "- [ ] Every page under /p/") || !strings.Contains(p, "Use sentence case") {
		t.Fatalf("prompt: %s", p)
	}

	// Approving keeps the work open; reopening a rejected ticket brings its parts back.
	if a := owner.must(200, "POST", base+"/fix-tickets/"+tid+"/review", map[string]any{"decision": "approve"}); a["review"] != "approved" || a["status"] != "open" {
		t.Fatalf("approved: %v", a)
	}
	if o := owner.must(200, "POST", base+"/fix-tickets/"+other+"/review", map[string]any{"decision": "reopen"}); o["review"] != "" || o["status"] != "open" {
		t.Fatalf("reopened: %v", o)
	}
	gid := owner.must(200, "GET", base+"/fix-guidelines", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	owner.must(204, "DELETE", base+"/fix-guidelines/"+gid, nil)
	owner.must(404, "DELETE", base+"/fix-guidelines/"+gid, nil)

	// Deleting the site takes its tickets with it.
	owner.must(204, "DELETE", base+"/seo-sites/"+id, nil)
	if l := owner.must(200, "GET", base+"/fix-tickets?status=all", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("after delete: %v", l)
	}
	seen := map[string]bool{}
	for _, ev := range got() {
		seen[ev.Name] = true
	}
	for _, name := range []string{"fix_ticket.write_requested", "fix_ticket.written", "fix_ticket.rejected", "fix_ticket.approved", "fix_guideline.created"} {
		if !seen[name] {
			t.Errorf("no %s event", name)
		}
	}
}
