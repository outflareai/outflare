package api_test

import (
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/jserrors"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Errors a brand's site sends are grouped into issues by fingerprint,
// brand by brand, and an issue someone resolved opens again when it
// happens again.
func TestErrorIssues(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	geo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "website": "https://northpeak.example"})["id"].(string)
	rp := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Summitly", "website": "https://summitly.example"})["id"].(string)
	g := "/v1/brands/" + geo

	k := owner.must(201, "POST", g+"/site-keys", map[string]any{"name": "northpeak.example", "origins": []string{"https://northpeak.example"}})
	key := k["key"].(string)
	if k["capture_errors"] != false {
		t.Fatalf("error capture on by default: %v", k)
	}
	on := owner.must(200, "PATCH", g+"/site-keys/"+k["id"].(string), map[string]any{"capture_errors": true})
	if on["capture_errors"] != true || on["capture_clicks"] != false {
		t.Fatalf("after turning errors on: %v", on)
	}
	other := owner.must(201, "POST", "/v1/brands/"+rp+"/site-keys", map[string]any{"name": "summitly.example", "origins": []string{"https://summitly.example"}})["key"].(string)

	n := 0
	send := func(origin, key, anon, typ, msg, fp, path string) map[string]any {
		t.Helper()
		n++
		props := map[string]any{"type": typ, "message": msg, "stack": typ + ": " + msg + "\n    at run (https://northpeak.example/app.js:1:1)", "handled": false}
		if fp != "" {
			props["fingerprint"] = fp
		}
		code, out := sendSite(t, e.h, origin, key, map[string]any{"type": "track", "event": "Error Caught", "anonymousId": anon, "messageId": "err-" + string(rune('a'+n)),
			"properties": props, "context": map[string]any{"page": map[string]any{"url": origin + path, "path": path}}})
		if code != 200 || out["accepted"] != 1.0 {
			t.Fatalf("send: %d %v", code, out)
		}
		return out
	}
	// The website's errors are grouped on the server, whatever fingerprint
	// the script sent: numbers in the message don't split them.
	g1 := jserrors.Group("TypeError", "plan 7 not found", "TypeError: plan 7 not found\n    at run (https://northpeak.example/app.js:1:1)")
	// The answer tells the script to capture errors.
	if c, _ := send("https://northpeak.example", key, "anon-1", "TypeError", "plan 42 not found", "aaaa1111bbbb2222", "/pricing")["capture"].(map[string]any); c["errors"] != true || c["clicks"] != false {
		t.Fatalf("capture: %v", c)
	}
	send("https://northpeak.example", key, "anon-2", "TypeError", "plan 7 not found", "aaaa1111bbbb2222", "/checkout")
	send("https://northpeak.example", key, "anon-2", "TypeError", "plan 7 not found", "aaaa1111bbbb2222", "/checkout")
	send("https://northpeak.example", key, "anon-3", "RangeError", "too deep", "", "/")
	send("https://northpeak.example", key, "anon-3", "RangeError", "too deep", "Not A Plain One!", "/")
	// Another brand's errors are its own.
	send("https://summitly.example", other, "anon-9", "TypeError", "plan 42 not found", "aaaa1111bbbb2222", "/")

	list := func(q string) (map[string]any, []any) {
		t.Helper()
		out := owner.must(200, "GET", g+"/errors"+q, nil)
		return out, out["data"].([]any)
	}
	out, issues := list("")
	// Capture began inside the period, so nothing counts as new.
	if len(issues) != 2 || out["errors"] != 5.0 || out["counts"].(map[string]any)["open"] != 2.0 || out["capture_since"] == nil {
		t.Fatalf("issues: %v", out)
	}
	top := issues[0].(map[string]any)
	if top["fingerprint"] != g1 || top["count"] != 3.0 || top["visitors"] != 2.0 || top["status"] != "open" || top["new"] != false ||
		top["type"] != "TypeError" || top["message"] != "plan 7 not found" || top["last_path"] != "/checkout" {
		t.Fatalf("top issue: %v", top)
	}
	second := issues[1].(map[string]any)
	fp2 := second["fingerprint"].(string)
	if second["count"] != 2.0 || len(fp2) != 16 {
		t.Fatalf("second issue: %v", second)
	}
	owner.must(400, "GET", g+"/errors?status=gone", nil)
	owner.must(400, "GET", g+"/errors?days=0", nil)
	if _, theirs := func() (map[string]any, []any) {
		o := owner.must(200, "GET", "/v1/brands/"+rp+"/errors", nil)
		return o, o["data"].([]any)
	}(); len(theirs) != 1 || theirs[0].(map[string]any)["count"] != 1.0 {
		t.Fatalf("the other brand's issues: %v", theirs)
	}

	// One issue: its days, pages and latest errors.
	one := owner.must(200, "GET", g+"/errors/"+g1, nil)
	occ := one["occurrences"].([]any)
	pages := one["pages"].([]any)
	if len(occ) != 3 || len(pages) != 2 || pages[0].(map[string]any)["path"] != "/checkout" || one["issue"].(map[string]any)["count"] != 3.0 {
		t.Fatalf("issue: %v", one)
	}
	latest := occ[0].(map[string]any)
	if latest["source"] != "browser" || latest["anonymous_id"] != "anon-2" || latest["handled"] != false || latest["path"] != "/checkout" || latest["stack"] == "" {
		t.Fatalf("latest error: %v", latest)
	}
	total := 0.0
	for _, d := range one["days"].([]any) {
		total += d.(map[string]any)["count"].(float64)
	}
	if total != 3 {
		t.Fatalf("days: %v", one["days"])
	}
	owner.must(404, "GET", g+"/errors/ffff0000ffff0000", nil)
	owner.must(404, "GET", g+"/errors/Not-Plain", nil)

	// Resolve it: out of the open list, and recorded.
	events := track.Capture(t)
	owner.must(400, "PATCH", g+"/errors/"+g1, map[string]any{"status": "fixed"})
	owner.must(404, "PATCH", g+"/errors/ffff0000ffff0000", map[string]any{"status": "resolved"})
	if r := owner.must(200, "PATCH", g+"/errors/"+g1, map[string]any{"status": "resolved"}); r["status"] != "resolved" {
		t.Fatalf("resolve: %v", r)
	}
	if up := named(events(), "error_issue.updated"); len(up) != 1 || up[0].Props["status"] != "resolved" || up[0].Props["brand_id"] != geo {
		t.Fatalf("error_issue.updated: %+v", up)
	}
	if out, issues := list(""); len(issues) != 1 || out["counts"].(map[string]any)["resolved"] != 1.0 {
		t.Fatalf("after resolving: %v", out)
	}
	if _, issues := list("?status=resolved"); len(issues) != 1 || issues[0].(map[string]any)["regressed"] != false {
		t.Fatalf("resolved list: %v", issues)
	}
	// The other brand's issue with the same fingerprint is still open.
	if o := owner.must(200, "GET", "/v1/brands/"+rp+"/errors", nil); len(o["data"].([]any)) != 1 {
		t.Fatalf("the other brand's issue was resolved too: %v", o)
	}
	// It happens again: open again, marked as back.
	time.Sleep(20 * time.Millisecond)
	send("https://northpeak.example", key, "anon-4", "TypeError", "plan 9 not found", "aaaa1111bbbb2222", "/pricing")
	_, issues = list("")
	if len(issues) != 2 || issues[0].(map[string]any)["regressed"] != true || issues[0].(map[string]any)["count"] != 4.0 {
		t.Fatalf("after it happened again: %v", issues)
	}
	// Ignored stays out even when it happens again.
	owner.must(200, "PATCH", g+"/errors/"+fp2, map[string]any{"status": "ignored"})
	send("https://northpeak.example", key, "anon-3", "RangeError", "too deep", "", "/")
	if out, issues := list(""); len(issues) != 1 || out["counts"].(map[string]any)["ignored"] != 1.0 {
		t.Fatalf("after ignoring: %v", out)
	}
	owner.must(200, "PATCH", g+"/errors/"+fp2, map[string]any{"status": "open"})
	if _, issues := list("?status=all"); len(issues) != 2 {
		t.Fatalf("all: %v", issues)
	}
}

// Archiving takes an issue out of the way until it happens again, when it
// opens again without counting as back; the bulk action changes or deletes
// many at once, and deleting removes the issue's errors, brand by brand.
func TestErrorIssueActions(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	geo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "website": "https://northpeak.example"})["id"].(string)
	rp := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Summitly", "website": "https://summitly.example"})["id"].(string)
	g := "/v1/brands/" + geo
	key := owner.must(201, "POST", g+"/site-keys", map[string]any{"name": "northpeak.example", "origins": []string{"https://northpeak.example"}})["key"].(string)
	other := owner.must(201, "POST", "/v1/brands/"+rp+"/site-keys", map[string]any{"name": "summitly.example", "origins": []string{"https://summitly.example"}})["key"].(string)
	n := 0
	send := func(origin, key, fp, release string) {
		t.Helper()
		n++
		props := map[string]any{"type": "TypeError", "message": "broke " + fp, "stack": "TypeError: broke\n    at run (https://northpeak.example/app.js:1:1)", "fingerprint": fp, "release": release}
		code, out := sendSite(t, e.h, origin, key, map[string]any{"type": "track", "event": "Error Caught", "anonymousId": "anon-1", "messageId": "act-" + string(rune('a'+n)),
			"properties": props, "context": map[string]any{"page": map[string]any{"url": origin + "/p", "path": "/p"}}})
		if code != 200 || out["accepted"] != 1.0 {
			t.Fatalf("send: %d %v", code, out)
		}
	}
	// The issue each one lands in.
	is := map[string]string{}
	for _, fp := range []string{"aaaa", "bbbb", "cccc", "dddd"} {
		is[fp] = jserrors.Group("TypeError", "broke "+fp, "TypeError: broke\n    at run (https://northpeak.example/app.js:1:1)")
	}
	for _, fp := range []string{"aaaa", "bbbb", "cccc", "dddd"} {
		send("https://northpeak.example", key, fp, "v1.2.3")
	}
	send("https://summitly.example", other, "cccc", "")

	list := func(q string) (map[string]any, map[string]map[string]any) {
		t.Helper()
		out := owner.must(200, "GET", g+"/errors"+q, nil)
		by := map[string]map[string]any{}
		for _, i := range out["data"].([]any) {
			by[i.(map[string]any)["fingerprint"].(string)] = i.(map[string]any)
		}
		return out, by
	}
	_, all := list("")
	if a := all[is["aaaa"]]; a["source"] != "browser" || a["handled"] != false || a["pages"] != 1.0 || a["status_at"] != nil {
		t.Fatalf("list fields: %v", a)
	}
	occ := owner.must(200, "GET", g+"/errors/"+is["aaaa"], nil)["occurrences"].([]any)[0].(map[string]any)
	if occ["release"] != "v1.2.3" {
		t.Fatalf("release: %v", occ)
	}

	// Archive: out of the open list; back as open, not as back again.
	owner.must(200, "PATCH", g+"/errors/"+is["aaaa"], map[string]any{"status": "archived"})
	if out, by := list(""); by[is["aaaa"]] != nil || out["counts"].(map[string]any)["archived"] != 1.0 {
		t.Fatalf("after archiving: %v", out)
	}
	if _, by := list("?status=archived"); by[is["aaaa"]] == nil || by[is["aaaa"]]["status_at"] == nil {
		t.Fatalf("archived list: %v", by)
	}
	time.Sleep(20 * time.Millisecond)
	send("https://northpeak.example", key, "aaaa", "")
	if _, by := list(""); by[is["aaaa"]] == nil || by[is["aaaa"]]["regressed"] != false {
		t.Fatalf("archived issue that happened again: %v", by[is["aaaa"]])
	}

	// Bulk: bad input refused; unknown fingerprints skipped.
	owner.must(400, "POST", g+"/errors/bulk", map[string]any{"fingerprints": []string{is["aaaa"]}, "action": "fixed"})
	owner.must(400, "POST", g+"/errors/bulk", map[string]any{"fingerprints": []string{}, "action": "ignored"})
	owner.must(400, "POST", g+"/errors/bulk", map[string]any{"fingerprints": []string{"Not-Plain"}, "action": "ignored"})
	events := track.Capture(t)
	r := owner.must(200, "POST", g+"/errors/bulk", map[string]any{"fingerprints": []string{is["aaaa"], is["bbbb"], is["bbbb"], "ffff"}, "action": "ignored"})
	if r["changed"] != 2.0 || r["skipped"] != 1.0 {
		t.Fatalf("bulk ignore: %v", r)
	}
	if up := named(events(), "error_issue.updated"); len(up) != 2 || up[0].Props["bulk"] != true {
		t.Fatalf("bulk events: %+v", up)
	}
	if out, _ := list(""); out["counts"].(map[string]any)["ignored"] != 2.0 {
		t.Fatalf("after bulk ignore: %v", out)
	}

	// Delete in bulk: the issues and their errors go, this brand only.
	r = owner.must(200, "POST", g+"/errors/bulk", map[string]any{"fingerprints": []string{is["bbbb"], is["cccc"]}, "action": "delete"})
	if r["changed"] != 2.0 || r["errors_deleted"] != 2.0 {
		t.Fatalf("bulk delete: %v", r)
	}
	owner.must(404, "GET", g+"/errors/"+is["cccc"], nil)
	if o := owner.must(200, "GET", "/v1/brands/"+rp+"/errors", nil); len(o["data"].([]any)) != 1 {
		t.Fatalf("the other brand's issue was deleted too: %v", o)
	}
	if out, by := list("?status=all"); by[is["bbbb"]] != nil || out["counts"].(map[string]any)["ignored"] != 1.0 {
		t.Fatalf("after bulk delete: %v", out)
	}
	// Delete one; it happens again: a new, open issue.
	if d := owner.must(200, "DELETE", g+"/errors/"+is["dddd"], nil); d["errors_deleted"] != 1.0 {
		t.Fatalf("delete: %v", d)
	}
	if del := named(events(), "error_issue.deleted"); len(del) != 3 {
		t.Fatalf("error_issue.deleted: %+v", del)
	}
	owner.must(404, "DELETE", g+"/errors/"+is["dddd"], nil)
	send("https://northpeak.example", key, "dddd", "")
	if _, by := list(""); by[is["dddd"]] == nil || by[is["dddd"]]["new"] != false || by[is["dddd"]]["count"] != 1.0 {
		t.Fatalf("deleted issue that happened again: %v", by[is["dddd"]])
	}
}
