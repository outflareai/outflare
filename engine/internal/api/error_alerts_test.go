package api_test

import (
	"fmt"
	"testing"

	"github.com/outflareai/outflare/engine/internal/jserrors"
)

// A new issue alerts once, an ignored one never, a resolved one when it
// comes back, and a broken release stops at ten an hour.
func TestErrorAlerts(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	geo := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Northpeak", "website": "https://northpeak.example"})["id"].(string)
	g := "/v1/brands/" + geo
	k := owner.must(201, "POST", g+"/site-keys", map[string]any{"name": "northpeak.example", "origins": []string{"https://northpeak.example"}})
	key := k["key"].(string)
	owner.must(200, "PATCH", g+"/site-keys/"+k["id"].(string), map[string]any{"capture_errors": true})

	n := 0
	send := func(typ, msg, fp string) {
		t.Helper()
		n++
		code, out := sendSite(t, e.h, "https://northpeak.example", key, map[string]any{"type": "track", "event": "Error Caught", "anonymousId": "anon-1",
			"messageId": fmt.Sprintf("alert-err-%d", n), "properties": map[string]any{"type": typ, "message": msg, "fingerprint": fp, "handled": false}})
		if code != 200 || out["accepted"] != 1.0 {
			t.Fatalf("send: %d %v", code, out)
		}
	}
	raised := func() []map[string]any {
		t.Helper()
		var out []map[string]any
		for _, a := range owner.must(200, "GET", g+"/alerts", nil)["data"].([]any) {
			if a := a.(map[string]any); a["kind"] == "error.new_issue" {
				out = append(out, a)
			}
		}
		return out
	}

	// The website's errors are grouped on the server: numbers don't split them.
	g1 := jserrors.Group("TypeError", "plan 0 not found", "")
	send("TypeError", "plan 42 not found", "aaaa1111bbbb2222")
	send("TypeError", "plan 7 not found", "aaaa1111bbbb2222")
	list := raised()
	if len(list) != 1 {
		t.Fatalf("one alert for one issue: %v", list)
	}
	if a := list[0]; a["title"] != "A new error: TypeError" || a["text"] != "plan 42 not found" || a["path"] != "errors/"+g1 || a["brand_id"] != geo {
		t.Fatalf("the alert: %v", a)
	}

	// Nobody is emailed about errors until the brand switches it on: the
	// kind says so, and nothing was queued.
	emails := func() (n int) {
		t.Helper()
		if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM alert_deliveries d JOIN alerts a ON a.id = d.alert_id
			WHERE a.workspace_id = (SELECT workspace_id FROM brands WHERE id = $1) AND a.kind = 'error.new_issue' AND d.channel = 'email'`, geo).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if _, err := e.pool.Exec(t.Context(), `UPDATE users SET email_verified_at = now()`); err != nil {
		t.Fatal(err)
	}
	off := false
	for _, k := range owner.must(200, "GET", g+"/alerts/settings", nil)["kinds"].([]any) {
		k := k.(map[string]any)
		off = off || (k["id"] == "error.new_issue" && k["default_email"] == "off")
		if k["id"] == "reply.hot" && k["default_email"] != "owner" {
			t.Fatalf("other kinds keep their default: %v", k)
		}
	}
	if !off || emails() != 0 {
		t.Fatalf("error emails are off by default: kind off %v, emails %d", off, emails())
	}
	// Switched on, the next new issue is emailed; switched off, no more.
	owner.must(200, "PUT", g+"/alerts/settings", map[string]any{"kind": "error.new_issue", "email": "admins"})
	send("RangeError", "emails are on", "eeee1111ffff2222")
	if emails() != 1 {
		t.Fatalf("switched on: %d emails", emails())
	}
	owner.must(200, "PUT", g+"/alerts/settings", map[string]any{"kind": "error.new_issue", "email": "off"})
	send("RangeError", "emails are off again", "eeee3333ffff4444")
	if emails() != 1 {
		t.Fatalf("switched off: %d emails", emails())
	}
	if list = raised(); len(list) != 3 {
		t.Fatalf("both still raised in the app: %v", list)
	}

	// Resolved, then back: one more. Ignored: nothing, also for an issue never seen.
	owner.must(200, "PATCH", g+"/errors/"+g1, map[string]any{"status": "resolved"})
	send("TypeError", "plan 9 not found", "aaaa1111bbbb2222")
	send("TypeError", "plan 9 not found", "aaaa1111bbbb2222")
	list = raised()
	if len(list) != 4 {
		t.Fatalf("back after resolving: %v", list)
	}
	back := false
	for _, a := range list {
		back = back || a["title"] == "An error you resolved is back: TypeError"
	}
	if !back {
		t.Fatalf("no alert says it is back: %v", list)
	}
	owner.must(200, "PATCH", g+"/errors/"+g1, map[string]any{"status": "ignored"})
	send("TypeError", "plan 11 not found", "aaaa1111bbbb2222")
	if list = raised(); len(list) != 4 {
		t.Fatalf("an ignored issue alerted: %v", list)
	}

	// A broken release: ten alerts an hour for a brand, the rest only on the page.
	for i := 0; i < 12; i++ {
		send("RangeError", "too deep in "+string(rune('a'+i)), fmt.Sprintf("storm%04d", i))
	}
	if list = raised(); len(list) != 10 {
		t.Fatalf("alerts in a storm: %d", len(list))
	}
	if issues := owner.must(200, "GET", g+"/errors?status=all", nil)["data"].([]any); len(issues) != 15 {
		t.Fatalf("every issue is still on the page: %d", len(issues))
	}
}
