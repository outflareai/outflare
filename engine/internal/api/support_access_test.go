package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestSupportViewIsReadOnlyAndHidesMail(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ownerID := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)

	// A staff member signs in like anyone else, in a workspace of their own.
	acc := &api.Accounts{Pool: e.pool, PublicURL: "http://localhost:8080"}
	rec := httptest.NewRecorder()
	staffID, _, err := acc.CreateAccount(ctx, nil, rec, httptest.NewRequest("POST", "/", nil),
		api.NewAccount{Email: "staff@example.com", Name: "Sam", Password: "staff long password", WorkspaceName: "Staff"})
	if err != nil {
		t.Fatal(err)
	}
	staff := &client{t: t, h: e.h, cookie: cookieNamed(rec, "outflare_session")}

	start := func() *http.Cookie {
		t.Helper()
		rec := httptest.NewRecorder()
		if _, err := acc.StartSupportView(ctx, rec, staffID, ownerID); err != nil {
			t.Fatal(err)
		}
		return cookieNamed(rec, "outflare_view")
	}
	if _, err := acc.StartSupportView(ctx, httptest.NewRecorder(), staffID, staffID); err == nil {
		t.Fatal("staff viewed their own account")
	}
	view := start()
	staff.extra = []*http.Cookie{view}

	// The staff member now sees the person's account, with no way to
	// change anything and no inbox.
	me := staff.must(200, "GET", "/v1/me", nil)
	if me["user"].(map[string]any)["id"] != ownerID || me["support_view"] == nil || len(me["menu_links"].([]any)) != 0 {
		t.Fatalf("me in a view: %v", me)
	}
	for _, sc := range me["workspaces"].([]any)[0].(map[string]any)["scopes"].([]any) {
		if s := sc.(string); !strings.HasSuffix(s, ".read") || s == "inbox.read" {
			t.Fatalf("a view may %s", s)
		}
	}
	staff.must(200, "GET", base, nil)
	staff.must(200, "GET", base+"/members", nil)
	for _, path := range []string{base + "/inbox", base + "/messages"} {
		if out := staff.must(403, "GET", path, nil); errCode(out) != "support_view_hidden" {
			t.Fatalf("%s: %v", path, out)
		}
	}
	// Nor do queries or search reach email content, sent or received.
	for _, sql := range []string{"select subject from messages", "select subject from replies"} {
		if code, out := staff.do("POST", base+"/query", map[string]any{"sql": sql}); code != 403 {
			t.Fatalf("%s in a view: %d %v", sql, code, out)
		}
	}
	staff.must(200, "POST", base+"/query", map[string]any{"sql": "select count(*) from people"})
	if out := staff.must(403, "GET", base+"/search?q=hello&kinds=message", nil); errCode(out) != "support_view_hidden" {
		t.Fatalf("search for messages in a view: %v", out)
	}
	staff.must(200, "GET", base+"/search?q=hello", nil)
	for _, c := range []struct{ method, path string }{
		{"POST", base + "/invites"},
		{"PATCH", base},
		{"PATCH", "/v1/me/support-access"},
		{"POST", "/v1/workspaces"},
	} {
		if out := staff.must(403, c.method, c.path, map[string]any{}); errCode(out) != "support_view_read_only" {
			t.Fatalf("%s %s: %v", c.method, c.path, out)
		}
	}

	// The view's cookie is nothing on its own, or beside someone else's
	// session.
	(&client{t: t, h: e.h, extra: []*http.Cookie{view}}).must(401, "GET", "/v1/me", nil)
	other := &client{t: t, h: e.h, cookie: owner.cookie, extra: []*http.Cookie{view}}
	if me := other.must(200, "GET", "/v1/me", nil); me["support_view"] != nil {
		t.Fatalf("the person's own session took the view: %v", me)
	}

	// The person sees the view and can turn support access off, which
	// ends it straight away and stops new ones.
	sa := owner.must(200, "GET", "/v1/me/support-access", nil)
	if sa["enabled"] != true || len(sa["views"].([]any)) != 1 {
		t.Fatalf("support access: %v", sa)
	}
	owner.must(200, "PATCH", "/v1/me/support-access", map[string]any{"enabled": false})
	if me := staff.must(200, "GET", "/v1/me", nil); me["user"].(map[string]any)["id"] != staffID || me["support_view"] != nil {
		t.Fatalf("after turning it off: %v", me)
	}
	if _, err := acc.StartSupportView(ctx, httptest.NewRecorder(), staffID, ownerID); !errors.Is(err, api.ErrSupportAccessOff) {
		t.Fatalf("started a view with support access off: %v", err)
	}
	owner.must(400, "PATCH", "/v1/me/support-access", map[string]any{})

	// Turned back on, staff can view again, and end the view themselves.
	owner.must(200, "PATCH", "/v1/me/support-access", map[string]any{"enabled": true})
	staff.extra = []*http.Cookie{start()}
	staff.must(200, "GET", base, nil)
	staff.must(200, "DELETE", "/v1/me/support-view", nil)
	if me := staff.must(200, "GET", "/v1/me", nil); me["user"].(map[string]any)["id"] != staffID {
		t.Fatalf("after ending the view: %v", me)
	}
	staff.must(404, "DELETE", "/v1/me/support-view", nil)
	staff.must(404, "GET", base, nil)

	// Ending a staff member's views, as when they leave, stops them too.
	staff.extra = []*http.Cookie{start()}
	if err := acc.EndSupportViews(ctx, e.pool, staffID); err != nil {
		t.Fatal(err)
	}
	staff.must(404, "GET", base, nil)

	ev := got()
	if len(named(ev, "support_access.turned_off")) != 1 || len(named(ev, "support_access.turned_on")) != 1 {
		t.Fatalf("events: %+v", ev)
	}
	// What staff did in a view counts as theirs, not the person's.
	viewed := 0
	for _, e := range ev {
		if e.Props["support_view"] == true {
			viewed++
			if e.UserID != staffID {
				t.Fatalf("a view's event credited to %s: %+v", e.UserID, e)
			}
		}
	}
	if viewed == 0 || len(named(ev, "query.ran")) == 0 {
		t.Fatalf("no events from the view: %+v", ev)
	}
	if v := owner.must(200, "GET", "/v1/me/support-access", nil)["views"].([]any); len(v) != 3 ||
		!slices.ContainsFunc(v, func(x any) bool { return x.(map[string]any)["ended_at"] != nil }) {
		t.Fatalf("views: %v", v)
	}
}

func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}
