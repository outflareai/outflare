package api_test

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/jserrors"
	"github.com/outflareai/outflare/engine/internal/track"
)

// What a brand needs to fix an error from Outflare alone: one bug is one
// issue whatever the browser or the build; noise and the brand's ignore
// rules go to Filtered; each issue says which release it began in, which
// sites it was on and what share of visitors it hit; issues merge by hand;
// every error can be paged to, with what the visitor did before it and
// who changed its status.
func TestErrorTracking(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	geo := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Northpeak", "website": "https://northpeak.example"})["id"].(string)
	g := "/v1/brands/" + geo
	key := owner.must(201, "POST", g+"/site-keys", map[string]any{"name": "northpeak.example", "origins": []string{"northpeak.example"}})["key"].(string)

	n := 0
	send := func(origin, anon string, props map[string]any, path string, ua string) {
		t.Helper()
		n++
		props["handled"] = false
		rec := sendSiteAs(t, e.h, origin, key, ua, false, map[string]any{"type": "track", "event": "Error Caught", "anonymousId": anon,
			"messageId": fmt.Sprintf("trk-%d", n), "properties": props,
			"context": map[string]any{"page": map[string]any{"url": origin + path, "path": path}, "locale": "en-GB"}})
		if rec.Code != 200 {
			t.Fatalf("send: %d %s", rec.Code, rec.Body)
		}
	}
	view := func(origin, anon, path string) {
		t.Helper()
		n++
		if code, out := sendSite(t, e.h, origin, key, map[string]any{"type": "page", "anonymousId": anon, "messageId": fmt.Sprintf("pv-%d", n),
			"context": map[string]any{"page": map[string]any{"url": origin + path, "path": path}}}); code != 200 || out["accepted"] != 1.0 {
			t.Fatalf("page view: %d %v", code, out)
		}
	}
	list := func(q string) (map[string]any, map[string]map[string]any) {
		t.Helper()
		out := owner.must(200, "GET", g+"/errors"+q, nil)
		by := map[string]map[string]any{}
		for _, i := range out["data"].([]any) {
			by[i.(map[string]any)["fingerprint"].(string)] = i.(map[string]any)
		}
		return out, by
	}

	// Ten visitors view /pricing; the same bug reaches two of them, from
	// Chrome on one build and Firefox on the next.
	for i := 0; i < 10; i++ {
		view("https://northpeak.example", fmt.Sprintf("v-%d", i), "/pricing")
	}
	msg := "Cannot read properties of undefined (reading 'plan')"
	send("https://northpeak.example", "v-0", map[string]any{"type": "TypeError", "message": msg, "fingerprint": "chromeprint0001",
		"stack": "TypeError: " + msg + "\n    at rX (https://northpeak.example/assets/app-B6vn08tg.js:1:47939)", "release": "2026.11.17",
		"viewport": "1440x900", "color_scheme": "dark", "reduced_motion": "no-preference"}, "/pricing", desktopUA)
	send("https://northpeak.example", "v-1", map[string]any{"type": "TypeError", "message": msg, "fingerprint": "firefoxprint001",
		"stack": "rX@https://northpeak.example/assets/app-mZtfGkFb.js:1:51000", "release": "2026.11.18"}, "/pricing", iphoneUA)
	bug := jserrors.Group("TypeError", msg, "")
	_, by := list("")
	if len(by) != 1 {
		t.Fatalf("one bug in two browsers and two builds is %d issues: %v", len(by), by)
	}
	for fp := range by {
		bug = fp
	}
	i := by[bug]
	if i["count"] != 2.0 || i["first_release"] != "2026.11.17" || i["last_release"] != "2026.11.18" || i["host"] != "northpeak.example" || i["hosts"] != 1.0 {
		t.Fatalf("issue: %v", i)
	}
	if share, _ := i["visitor_share"].(float64); share != 0.2 {
		t.Fatalf("visitor share: %v", i["visitor_share"])
	}
	if wp, _ := i["worst_page"].(map[string]any); wp["path"] != "/pricing" || wp["page_visitors"] != 10.0 || wp["visitors"] != 2.0 {
		t.Fatalf("worst page: %v", i["worst_page"])
	}
	if tr := i["trend"].([]any); len(tr) != 8 || tr[len(tr)-1] != 2.0 {
		t.Fatalf("trend: %v", tr)
	}

	// The context of each error.
	one := owner.must(200, "GET", g+"/errors/"+bug, nil)
	occ := one["occurrences"].([]any)
	last, first := occ[0].(map[string]any), occ[1].(map[string]any)
	if first["viewport"] != "1440x900" || first["color_scheme"] != "dark" || first["language"] != "en-GB" || first["user_agent"] != desktopUA || first["host"] != "northpeak.example" {
		t.Fatalf("context: %v", first)
	}
	if last["browser"] != "safari" || last["os"] != "ios" {
		t.Fatalf("iPhone error: %v", last)
	}
	if pages := one["pages"].([]any); pages[0].(map[string]any)["share"] != 0.2 {
		t.Fatalf("pages: %v", pages)
	}

	// What the visitor did before it.
	before := owner.must(200, "GET", g+"/errors/"+bug+"/occurrences/"+first["id"].(string)+"/before", nil)["data"].([]any)
	if len(before) != 1 || before[0].(map[string]any)["kind"] != "page" || before[0].(map[string]any)["path"] != "/pricing" {
		t.Fatalf("before: %v", before)
	}
	owner.must(404, "GET", g+"/errors/"+bug+"/occurrences/00000000-0000-0000-0000-000000000000/before", nil)

	// Every error, page by page.
	for k := 0; k < 3; k++ {
		send("https://northpeak.example", "v-2", map[string]any{"type": "TypeError", "message": msg, "stack": "TypeError: x\n    at rX (https://northpeak.example/assets/app-Q1w2e3r4.js:1:1)"}, "/pricing", desktopUA)
	}
	p1 := owner.must(200, "GET", g+"/errors/"+bug+"/occurrences?limit=2", nil)
	if len(p1["data"].([]any)) != 2 || p1["next_cursor"] == nil {
		t.Fatalf("page 1: %v", p1)
	}
	p2 := owner.must(200, "GET", g+"/errors/"+bug+"/occurrences?limit=2&cursor="+url.QueryEscape(p1["next_cursor"].(string)), nil)
	p3 := owner.must(200, "GET", g+"/errors/"+bug+"/occurrences?limit=2&cursor="+url.QueryEscape(p2["next_cursor"].(string)), nil)
	if len(p2["data"].([]any)) != 2 || len(p3["data"].([]any)) != 1 || p3["next_cursor"] != nil {
		t.Fatalf("pages 2 and 3: %v %v", p2, p3)
	}
	owner.must(400, "GET", g+"/errors/"+bug+"/occurrences?cursor=nope", nil)

	// A minified React error reads in full.
	send("https://northpeak.example", "v-3", map[string]any{"type": "Error",
		"message":         "Minified React error #418; visit https://react.dev/errors/418?args[]=text&args[]= for the full message",
		"component_stack": "\n    at Price\n    at Pricing"}, "/pricing", desktopUA)
	react := jserrors.Group("Error", "Minified React error #418", "")
	ri := owner.must(200, "GET", g+"/errors/"+react, nil)
	if ro := ri["occurrences"].([]any)[0].(map[string]any); ro["component_stack"] == "" || ri["issue"].(map[string]any)["message_full"] == "" {
		t.Fatalf("React error: %v", ri)
	}

	// Noise: an extension's error and a copy on a bare IP go to Filtered.
	send("https://northpeak.example", "v-4", map[string]any{"type": "TypeError", "message": "x is undefined",
		"stack": "TypeError: x\n    at f (chrome-extension://abcdefgh/content.js:1:2)"}, "/", desktopUA)
	send("https://northpeak.example", "v-4", map[string]any{"type": "Error", "message": "Object Not Found Matching Id:1, MethodName:update, ParamCount:4"}, "/", desktopUA)
	out, by := list("")
	if out["counts"].(map[string]any)["filtered"] != 2.0 || len(by) != 2 {
		t.Fatalf("noise in the open list: %v", out)
	}
	_, fby := list("?status=filtered")
	reasons := map[string]bool{}
	for _, i := range fby {
		reasons[i["filtered"].(string)] = true
	}
	if !reasons["extension"] || !reasons["link_scanner"] {
		t.Fatalf("filtered: %v", fby)
	}

	// An ignore rule, by pattern: applies to errors already stored, and
	// taking it back brings them back.
	events := track.Capture(t)
	owner.must(400, "POST", g+"/error-rules", map[string]any{"field": "nowhere", "pattern": "x"})
	owner.must(400, "POST", g+"/error-rules", map[string]any{"field": "message", "pattern": " "})
	rule := owner.must(201, "POST", g+"/error-rules", map[string]any{"field": "message", "pattern": "MINIFIED REACT", "note": "Hydration, known"})
	owner.must(409, "POST", g+"/error-rules", map[string]any{"field": "message", "pattern": "MINIFIED REACT"})
	if out, by := list(""); by[react] != nil || out["counts"].(map[string]any)["filtered"] != 3.0 {
		t.Fatalf("after the rule: %v", out)
	}
	rules := owner.must(200, "GET", g+"/error-rules", nil)
	if r := rules["data"].([]any); len(r) != 1 || r[0].(map[string]any)["matched"] != 1.0 || len(rules["builtin"].([]any)) != 5 {
		t.Fatalf("rules: %v", rules)
	}
	if u := owner.must(200, "PATCH", g+"/error-rules/"+rule["id"].(string), map[string]any{"note": "Known"}); u["note"] != "Known" || u["pattern"] != "MINIFIED REACT" {
		t.Fatalf("update: %v", u)
	}
	owner.must(204, "DELETE", g+"/error-rules/"+rule["id"].(string), nil)
	owner.must(404, "DELETE", g+"/error-rules/"+rule["id"].(string), nil)
	if _, by := list(""); by[react] == nil {
		t.Fatal("deleting the rule didn't bring the error back")
	}
	if len(named(events(), "error_rule.created")) != 1 || len(named(events(), "error_rule.deleted")) != 1 {
		t.Fatal("rule events")
	}

	// Sites: a host filter.
	send("https://app.northpeak.example", "v-5", map[string]any{"type": "RangeError", "message": "too deep"}, "/dashboard", desktopUA)
	out, by = list("?host=app.northpeak.example")
	if len(by) != 1 || len(out["hosts"].([]any)) != 2 {
		t.Fatalf("host filter: %v", out)
	}
	deep := ""
	for fp := range by {
		deep = fp
	}

	// Merge by hand: the errors count together, and the old link opens it.
	m := owner.must(200, "POST", g+"/errors/merge", map[string]any{"into": bug, "fingerprints": []string{deep, "ffff0000ffff0000"}})
	if m["into"] != bug || len(m["merged"].([]any)) != 1 || m["skipped"] != 1.0 {
		t.Fatalf("merge: %v", m)
	}
	if _, by := list(""); by[deep] != nil || by[bug]["count"] != 6.0 {
		t.Fatalf("after merging: %v", by)
	}
	if again := owner.must(200, "GET", g+"/errors/"+deep, nil); again["issue"].(map[string]any)["fingerprint"] != bug || len(again["merged_from"].([]any)) != 1 {
		t.Fatalf("merged link: %v", again)
	}
	owner.must(404, "POST", g+"/errors/merge", map[string]any{"into": "ffff0000ffff0000", "fingerprints": []string{bug}})
	owner.must(200, "DELETE", g+"/errors/"+bug+"/merged/"+deep, nil)
	owner.must(404, "DELETE", g+"/errors/"+bug+"/merged/"+deep, nil)
	if _, by := list(""); by[deep] == nil {
		t.Fatal("split didn't bring the issue back")
	}

	// Who changed the status.
	owner.must(200, "PATCH", g+"/errors/"+bug, map[string]any{"status": "resolved"})
	_, by = list("?status=resolved")
	if by[bug]["status_by"] == nil {
		t.Fatalf("status_by: %v", by[bug])
	}
	if h := owner.must(200, "GET", g+"/errors/"+bug, nil)["history"].([]any); len(h) != 1 || h[0].(map[string]any)["status"] != "resolved" || h[0].(map[string]any)["by"] == nil {
		t.Fatalf("history: %v", h)
	}
}

// Errors stored before server-side grouping are regrouped by a job, and
// what a person decided about an old issue holds for the new one; links
// to the old issue open the new one.
func TestErrorRegroup(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	geo := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Northpeak", "website": "https://northpeak.example"})["id"].(string)
	g := "/v1/brands/" + geo
	ctx := context.Background()
	// Two errors as the first version stored them: the browser's own
	// fingerprints, which split one bug by build.
	msg := "plan is undefined"
	for k, fp := range []string{"oldprint0000aaaa", "oldprint0000bbbb"} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, brand_id, kind, name, raw_name, anonymous_id, properties, context, source, message_id, occurred_at)
			VALUES ($1, $2, 'track', 'error_caught', 'Error Caught', 'anon-1', $3, $4, 'browser', $5, $6)`, ws, geo,
			map[string]any{"type": "TypeError", "message": msg, "fingerprint": fp, "stack": fmt.Sprintf("TypeError: x\n    at f (https://northpeak.example/assets/app-%dxY7kq9Lm.js:1:2)", k)},
			map[string]any{"page": map[string]any{"url": "https://northpeak.example/", "path": "/"}}, "old-"+fp, time.Now().Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, by := func() (map[string]any, []any) {
		o := owner.must(200, "GET", g+"/errors", nil)
		return o, o["data"].([]any)
	}(); len(by) != 2 {
		t.Fatalf("before the job: %v", by)
	}
	owner.must(200, "PATCH", g+"/errors/oldprint0000aaaa", map[string]any{"status": "ignored"})
	if _, err := e.pool.Exec(ctx, `INSERT INTO jobs (workspace_id, kind, payload) VALUES ($1, 'errors.regroup', '{}')`, ws); err != nil {
		t.Fatal(err)
	}
	e.runServerJobs(t)
	var out map[string]any
	for start := time.Now(); time.Since(start) < 10*time.Second; time.Sleep(50 * time.Millisecond) {
		out = owner.must(200, "GET", g+"/errors?status=all", nil)
		if len(out["data"].([]any)) == 1 {
			break
		}
	}
	data := out["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("after the job: %v", out)
	}
	i := data[0].(map[string]any)
	if i["count"] != 2.0 || i["status"] != "ignored" {
		t.Fatalf("regrouped issue: %v", i)
	}
	if old := owner.must(200, "GET", g+"/errors/oldprint0000bbbb", nil); old["issue"].(map[string]any)["fingerprint"] != i["fingerprint"] {
		t.Fatalf("old link: %v", old["issue"])
	}
}
