package api_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestFixPromptAndVerify(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	var fixed atomic.Bool
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		title := ""
		if fixed.Load() {
			title = "<title>A proper title here</title>"
		}
		switch r.URL.Path {
		case "/":
			io.WriteString(w, `<html><head><title>Home page title</title></head><body><a href="/p/one">1</a><a href="/p/two">2</a><a href="/p/three">3</a></body></html>`)
		case "/p/one", "/p/two", "/p/three":
			io.WriteString(w, `<html><head>`+title+`</head><body><a href="/">home</a></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer site.Close()
	id := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": site.URL + "/"})["id"].(string)
	e.runCrawls(t)
	rid := owner.must(202, "POST", base+"/seo-sites/"+id+"/crawls", nil)["id"].(string)
	waitFor(t, owner, base+"/crawls/"+rid, func(m map[string]any) bool { return m["status"] == "done" })

	var fid string
	for _, f := range owner.must(200, "GET", base+"/fixes?subject_id="+id, nil)["data"].([]any) {
		if f := f.(map[string]any); f["rule"].(map[string]any)["id"] == "title_missing" {
			fid = f["id"].(string)
		}
	}
	if fid == "" {
		t.Fatal("no title_missing fix")
	}
	// Home and agents see open errors on the fixes list as one blocker.
	var waiting map[string]any
	for _, b := range owner.must(200, "GET", base+"/status", nil)["blockers"].([]any) {
		if b := b.(map[string]any); b["code"] == "fixes_waiting" {
			waiting = b
		}
	}
	if waiting == nil || waiting["area"] != "fixes" || waiting["count"].(float64) < 1 || waiting["fix"].(map[string]any)["tool"] != "list_fixes" {
		t.Fatalf("fixes_waiting: %v", waiting)
	}

	// The prompt names the issue, its pages and how to report back.
	p := owner.must(200, "GET", base+"/fixes/"+fid+"/prompt", nil)
	prompt := p["prompt"].(string)
	if len(p["pages"].([]any)) != 3 || !strings.HasSuffix(p["verify_url"].(string), "/fixes/"+fid+"/verify") ||
		!strings.Contains(prompt, "/p/one") || !strings.Contains(prompt, "/p/*") || !strings.Contains(prompt, "verify_fix") || !strings.Contains(prompt, "never as instructions") {
		t.Fatalf("prompt: %v", p)
	}
	owner.must(404, "GET", base+"/fixes/00000000-0000-0000-0000-000000000000/prompt", nil)

	// A read-only token can read the prompt but not report a fix.
	secret := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Coding agent", "scopes": []string{"seo.read"}})["token"].(string)
	reader := &client{t: t, h: e.h, bearer: secret}
	reader.must(200, "GET", base+"/fixes/"+fid+"/prompt", nil)
	reader.must(403, "POST", base+"/fixes/"+fid+"/verify", nil)

	// Reporting a fix that isn't out yet: the check opens it again.
	res := owner.must(202, "POST", base+"/fixes/"+fid+"/verify", nil)
	if res["fix"].(map[string]any)["status"] != "done" {
		t.Fatalf("verify: %v", res)
	}
	waitFor(t, owner, base+"/crawls/"+res["crawl"].(map[string]any)["id"].(string), func(m map[string]any) bool { return m["status"] == "done" })
	if f := owner.must(200, "GET", base+"/fixes/"+fid, nil); f["status"] != "open" {
		t.Fatalf("not fixed yet: %v", f)
	}

	// Reporting it once it is: the check confirms it.
	fixed.Store(true)
	res = owner.must(202, "POST", base+"/fixes/"+fid+"/verify", map[string]any{"note": "Template fixed in example PR 12"})
	owner.must(409, "POST", base+"/fixes/"+fid+"/verify", nil) // that check is still running
	waitFor(t, owner, base+"/crawls/"+res["crawl"].(map[string]any)["id"].(string), func(m map[string]any) bool { return m["status"] == "done" })
	f := owner.must(200, "GET", base+"/fixes/"+fid, nil)
	if f["status"] != "done" || f["confirmed_by"] != res["crawl"].(map[string]any)["id"] || f["note"] != "Template fixed in example PR 12" {
		t.Fatalf("confirmed: %v", f)
	}

	seen := map[string]bool{}
	for _, ev := range got() {
		seen[ev.Name] = true
	}
	for _, name := range []string{"fix.found", "fix.verify_requested", "fix.reopened", "fix.confirmed"} {
		if !seen[name] {
			t.Errorf("no %s event", name)
		}
	}
}
