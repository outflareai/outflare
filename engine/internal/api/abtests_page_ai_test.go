package api_test

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

// For a no-code test of a page, the AI writes each version as changes to
// the page's numbered parts; Outflare turns them into checked selectors,
// drops what isn't allowed, and a person keeps them on the version.
func TestABTestAIPageVersions(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	e.runServerJobs(t)
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})
	ab := owner.must(201, "POST", base+"/ab-tests", map[string]any{"name": "Proxy list header", "goals": []map[string]any{{"kind": "event", "name": "signup_started"}},
		"page": map[string]any{"url": "https://northpeak.example/free-proxy-list", "versions": 1}})
	id := ab["id"].(string)
	outline := []map[string]any{
		{"selector": "#hero > h1:nth-of-type(1)", "tag": "h1", "text": "Free Proxy List"},
		{"selector": "#hero > p:nth-of-type(1)", "tag": "p", "text": "Updated every 10 minutes."},
		{"selector": "#hero > a:nth-of-type(1)", "tag": "a", "text": "Download", "href": "/download"},
		// Not a selector the site script would take: left out.
		{"selector": "h1{color:red}", "tag": "h1", "text": "x"},
	}
	owner.must(400, "POST", base+"/ab-tests/versions", map[string]any{"test": id, "prompt": "x", "version": "control", "outline": outline})
	owner.must(400, "POST", base+"/ab-tests/versions", map[string]any{"test": id, "prompt": ""})
	owner.must(404, "POST", base+"/ab-tests/versions", map[string]any{"test": "00000000-0000-0000-0000-000000000000", "prompt": "x"})
	r := owner.must(202, "POST", base+"/ab-tests/versions", map[string]any{"test": id, "prompt": "Under the header, add a line about premium proxies and a Try Premium button to /sign-up", "outline": outline})
	if r["test_id"] != id {
		t.Fatalf("run: %v", r)
	}
	run := waitFor(t, owner, base+"/ab-test-ai-runs/"+r["id"].(string), func(m map[string]any) bool { return m["status"] == "ready" || m["status"] == "failed" })
	out, _ := run["output"].(map[string]any)
	if run["status"] != "ready" || out["mode"] != "page" {
		t.Fatalf("page versions: %v", run)
	}
	versions := out["versions"].([]any)
	if len(versions) != 1 {
		t.Fatalf("versions: %v", versions)
	}
	v := versions[0].(map[string]any)
	cs := v["changes"].([]any)
	if v["key"] != "premium" || len(cs) != 3 {
		t.Fatalf("changes: %v", v)
	}
	first, button, style := cs[0].(map[string]any), cs[1].(map[string]any), cs[2].(map[string]any)
	if first["selector"] != "#hero > h1:nth-of-type(1)" || first["rich"] != "Free proxies, and **premium** ones" {
		t.Fatalf("text change: %v", first)
	}
	if ins := button["insert"].(map[string]any); ins["kind"] != "button" || ins["href"] != "/sign-up" || ins["where"] != "after" {
		t.Fatalf("button: %v", button)
	}
	if style["selector"] != "#hero > a:nth-of-type(1)" || style["style"].(map[string]any)["background-color"] != "#0f766e" {
		t.Fatalf("style: %v", style)
	}
	abAIPrompts.Lock()
	sent := abAIPrompts.last["page_versions"]
	abAIPrompts.Unlock()
	for _, want := range []string{"e1 <h1> Free Proxy List", "e3 <a> Download → /download", "premium proxies", "b: no changes", "Never invent facts"} {
		// The body is JSON, so < and > arrive escaped.
		quoted, _ := json.Marshal(want)
		if !strings.Contains(sent, strings.Trim(string(quoted), `"`)) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(sent, "color:red") {
		t.Error("a bad selector reached the AI")
	}

	// The person keeps it on version b, and the test can start.
	owner.must(409, "POST", base+"/ab-tests/"+id+"/start", nil)
	owner.must(200, "PUT", base+"/ab-tests/"+id+"/versions/b", map[string]any{"changes": cs})
	// More changes for one version: its key is kept.
	r = owner.must(202, "POST", base+"/ab-tests/versions", map[string]any{"test": id, "version": "b", "prompt": "Make the button green", "outline": outline})
	run = waitFor(t, owner, base+"/ab-test-ai-runs/"+r["id"].(string), func(m map[string]any) bool { return m["status"] == "ready" || m["status"] == "failed" })
	if k := run["output"].(map[string]any)["versions"].([]any)[0].(map[string]any)["key"]; k != "b" {
		t.Fatalf("version key: %v", k)
	}
	abAIPrompts.Lock()
	sent = abAIPrompts.last["page_versions"]
	abAIPrompts.Unlock()
	if !strings.Contains(sent, "b:\n- e1 text: Free proxies") && !strings.Contains(sent, `b:\n- e1 text: Free proxies`) {
		t.Errorf("the version's own changes weren't in the prompt: %s", sent)
	}
	owner.must(200, "POST", base+"/ab-tests/"+id+"/start", nil)

	// A test has a readable link.
	link := owner.must(200, "GET", base+"/links/ab_test/"+id, nil)
	if link["slug"] != "proxy-list-header" || link["name"] != "Proxy list header" {
		t.Fatalf("link: %v", link)
	}
	if again := owner.must(200, "GET", base+"/links/ab_test/proxy-list-header", nil); again["id"] != id {
		t.Fatalf("by slug: %v", again)
	}
}

// A run can be stopped while it waits: it fails as "Stopped." and stays so.
func TestABTestAIRunStop(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})
	r := owner.must(202, "POST", base+"/ab-tests/suggestions", nil)
	brand := r["brand_id"].(string)
	stop := "/v1/brands/" + brand + "/ab-test-ai-runs/" + r["id"].(string) + "/stop"
	got := owner.must(200, "POST", stop, nil)
	if got["status"] != "failed" || got["error"] != "Stopped." {
		t.Fatalf("stopped: %v", got)
	}
	e.runServerJobs(t)
	if again := owner.must(200, "POST", stop, nil); again["status"] != "failed" || again["error"] != "Stopped." {
		t.Fatalf("after the job: %v", again)
	}
	owner.must(404, "POST", "/v1/brands/"+brand+"/ab-test-ai-runs/00000000-0000-0000-0000-000000000000/stop", nil)
}

// In the visual editor, the AI changes the version open there, from the
// page as the editor sees it, and the part picked.
func TestEditorAI(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	e.runServerJobs(t)
	ab := owner.must(201, "POST", base+"/ab-tests", map[string]any{"name": "Hero", "goals": []map[string]any{{"kind": "event", "name": "signup_started"}},
		"page": map[string]any{"url": "https://northpeak.example/", "versions": 2}})
	flag := ab["flag_key"].(string)
	link := owner.must(200, "POST", base+"/flags/"+flag+"/edit-link", map[string]any{"variant": "b", "url": "https://northpeak.example/"})
	u, _ := url.Parse(link["url"].(string))
	tok := u.Query().Get("of_edit")
	site := &client{t: t, h: e.h}

	// The editor learns the test's versions, and can switch to another.
	info := site.must(200, "GET", "/v1/site/flags/edits?token="+url.QueryEscape(tok)+"&variant=c", nil)
	if info["variant"] != "c" || info["control"] != "control" || info["ai"] != true || len(info["versions"].([]any)) != 3 || info["test_name"] != "Hero" {
		t.Fatalf("info: %v", info)
	}
	saved := site.must(200, "POST", "/v1/site/flags/edits", map[string]any{"token": tok, "variant": "c", "changes": []map[string]any{{"selector": "h1", "hide": true}}})
	if saved["variant"] != "c" || len(saved["changes"].([]any)) != 1 {
		t.Fatalf("saved c: %v", saved)
	}

	outline := []map[string]any{
		{"selector": "#hero > h1:nth-of-type(1)", "tag": "h1", "text": "Free Proxy List"},
		{"selector": "#hero > p:nth-of-type(1)", "tag": "p", "text": "Updated every 10 minutes."},
		{"selector": "#hero > a:nth-of-type(1)", "tag": "a", "text": "Download", "href": "/download"},
	}
	ask := map[string]any{"token": tok, "prompt": "Make the headline about premium proxies", "outline": outline, "focus": "#hero > h1:nth-of-type(1)"}
	site.must(409, "POST", "/v1/site/flags/edits/ai", ask) // no AI model yet
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})
	site.must(400, "POST", "/v1/site/flags/edits/ai", map[string]any{"token": tok, "variant": "control", "prompt": "x", "outline": outline})
	site.must(400, "POST", "/v1/site/flags/edits/ai", map[string]any{"token": tok, "prompt": "x"})
	site.must(401, "POST", "/v1/site/flags/edits/ai", map[string]any{"token": "nope", "prompt": "x", "outline": outline})
	run := site.must(202, "POST", "/v1/site/flags/edits/ai", ask)
	id := run["id"].(string)
	var got map[string]any
	for i := 0; i < 100; i++ {
		got = site.must(200, "GET", "/v1/site/flags/edits/ai?token="+url.QueryEscape(tok)+"&run="+id, nil)
		if got["status"] == "ready" || got["status"] == "failed" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	out, _ := got["output"].(map[string]any)
	if got["status"] != "ready" || out["version"] != "b" {
		t.Fatalf("run: %v", got)
	}
	abAIPrompts.Lock()
	sent := abAIPrompts.last["page_versions"]
	abAIPrompts.Unlock()
	if !strings.Contains(sent, "The part the person picked") || !strings.Contains(sent, "premium proxies") {
		t.Errorf("prompt: %s", sent)
	}
	// Stopping a finished run leaves it as it is; other runs aren't found.
	if s := site.must(200, "POST", "/v1/site/flags/edits/ai/stop", map[string]any{"token": tok, "run": id}); s["status"] != "ready" {
		t.Fatalf("stop: %v", s)
	}
	site.must(404, "POST", "/v1/site/flags/edits/ai/stop", map[string]any{"token": tok, "run": "00000000-0000-0000-0000-000000000000"})
	site.must(404, "GET", "/v1/site/flags/edits/ai?token="+url.QueryEscape(tok)+"&run=00000000-0000-0000-0000-000000000000", nil)

	// A flag with no test of a page gets no AI here.
	owner.must(201, "POST", base+"/flags", map[string]any{"key": "plain", "name": "Plain", "variants": []map[string]any{{"key": "control", "weight": 50}, {"key": "b", "weight": 50}}})
	plink := owner.must(200, "POST", base+"/flags/plain/edit-link", map[string]any{"variant": "b", "url": "https://northpeak.example/"})
	pu, _ := url.Parse(plink["url"].(string))
	ptok := pu.Query().Get("of_edit")
	if info := site.must(200, "GET", "/v1/site/flags/edits?token="+url.QueryEscape(ptok), nil); info["ai"] != false || info["control"] != "" {
		t.Fatalf("plain info: %v", info)
	}
	site.must(409, "POST", "/v1/site/flags/edits/ai", map[string]any{"token": ptok, "prompt": "x", "outline": outline})
}
