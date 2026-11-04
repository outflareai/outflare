package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestProductEventSink(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var user string
	if err := e.pool.QueryRow(ctx, `SELECT id::text FROM users`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.ProductEventSink(ctx, "00000000-0000-0000-0000-000000000000"); err == nil {
		t.Fatal("a sink for a missing workspace was made")
	}
	sink, err := e.srv.ProductEventSink(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	before := len(got())
	at := time.Now().UTC().Add(-time.Minute)
	sink.Send(ctx, []track.Event{
		{Name: "mailbox.connected", UserID: user, WorkspaceID: "w-1", Surface: "web", Outcome: track.Success, At: at, Props: map[string]any{"provider": "google"}},
		{Name: "api.called", UserID: user, Surface: "mcp", Outcome: track.Failure, ErrorCode: "forbidden", At: at},
		{Name: "job.dead", WorkspaceID: "w-1", Outcome: track.Error, ErrorCode: "internal", At: at},
	})
	if n := len(got()); n != before {
		t.Fatalf("storing product events made %d events of its own", n-before)
	}

	base := "/v1/workspaces/" + ws
	list := owner.must(200, "GET", base+"/customer-events?user_id="+user, nil)["data"].([]any)
	if len(list) != 3 {
		t.Fatalf("events for the person: %v", list)
	}
	var called map[string]any
	for _, v := range list {
		if m := v.(map[string]any); m["name"] == "api.called" {
			called = m
		}
	}
	p := called["properties"].(map[string]any)
	if called["source"] != "outflare" || p["outcome"] != "failure" || p["error_code"] != "forbidden" || p["surface"] != "mcp" || called["record_id"] == nil {
		t.Fatalf("api.called: %v", called)
	}
	// The person is a record with their account's email and name.
	rec := owner.must(200, "GET", base+"/records/"+called["record_id"].(string), nil)
	if rec["name"] != "Jack" {
		t.Fatalf("record: %v", rec)
	}
	ids := owner.must(200, "GET", base+"/records/"+called["record_id"].(string)+"/identities", nil)
	if !hasIdentity(ids, "email", "jack@example.com") {
		t.Fatalf("identities: %v", ids)
	}
	// System work waits unmatched, and Outflare's own names need no approval.
	if l := owner.must(200, "GET", base+"/customer-events?anonymous_id=workspace:w-1", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("system event: %v", l)
	}
	for _, v := range owner.must(200, "GET", base+"/catalog/events", nil)["data"].([]any) {
		if d := v.(map[string]any); d["status"] != "approved" {
			t.Fatalf("catalog: %v", d)
		}
	}

	// A person is identified once per process.
	sink.Send(ctx, []track.Event{{Name: "api.called", UserID: user, At: at}})
	if l := owner.must(200, "GET", base+"/customer-events?kind=identify", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("identify calls: %d", len(l))
	}
}

func hasIdentity(body map[string]any, namespace, value string) bool {
	for _, v := range body["data"].([]any) {
		if m := v.(map[string]any); m["namespace"] == namespace && m["value"] == value {
			return true
		}
	}
	return false
}

// A person who signs in from a browser that visited the operator's own
// website (site.js's of_aid cookie) is joined to those visits in the
// events workspace, with the ad click ids on them, so a Google click days
// before the sign-up is credited and sent back to Google.
func TestProductEventSinkJoinsVisits(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	site := "https://www.northpeak.example"
	key := owner.must(201, "POST", "/v1/workspaces/"+ws+"/site-keys", map[string]any{"name": "Northpeak", "origins": []string{site}})["key"].(string)
	if rec := sendSiteAs(t, e.h, site, key, iphoneUA, true, map[string]any{"type": "page", "anonymousId": "visitor-from-ad-1",
		"properties": map[string]any{"click_ids": map[string]any{"gclid": "Cj0-test-click"}, "ad": map[string]any{"campaign": "1900001"}},
		"context":    map[string]any{"page": map[string]any{"url": site + "/pricing", "path": "/pricing"}}}); rec.Code != 200 {
		t.Fatalf("visit: %d %s", rec.Code, rec.Body)
	}

	// Signing in from that browser: the cookie rides on the event.
	before := len(got())
	login := func(cookie string) {
		t.Helper()
		req := httptest.NewRequest("POST", "/v1/auth/login", strings.NewReader(`{"email":"jack@example.com","password":"long enough password"}`))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "of_aid", Value: cookie})
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("login: %d %s", rec.Code, rec.Body)
		}
	}
	login("not a valid id!")
	login("visitor-from-ad-1")
	var signedIn []track.Event
	for _, ev := range got()[before:] {
		if ev.Name == "user.signed_in" {
			signedIn = append(signedIn, ev)
		}
	}
	if len(signedIn) != 2 || signedIn[0].AnonymousID != "" || signedIn[1].AnonymousID != "visitor-from-ad-1" {
		t.Fatalf("signed in events: %+v", signedIn)
	}

	sink, err := e.srv.ProductEventSink(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	sink.Send(ctx, signedIn)
	var rec *string
	var clicks map[string]string
	if err := e.pool.QueryRow(ctx, `SELECT record_id::text, click_ids FROM site_sessions WHERE workspace_id = $1 AND anonymous_id = 'visitor-from-ad-1'`, ws).Scan(&rec, &clicks); err != nil {
		t.Fatal(err)
	}
	var person string
	if err := e.pool.QueryRow(ctx, `SELECT record_id::text FROM record_identities WHERE workspace_id = $1 AND namespace = 'user_id' AND valid_to IS NULL`, ws).Scan(&person); err != nil {
		t.Fatal(err)
	}
	if rec == nil || *rec != person || clicks["gclid"] != "Cj0-test-click" {
		t.Fatalf("visit record %v, person %s, clicks %v", rec, person, clicks)
	}
}
