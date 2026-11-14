package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A Workspace-read member's calendar: meetings with a CRM contact go on
// that contact, the rest of the calendar is never copied.
func TestCRMCalendarThroughWorkspace(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana", "identities": []map[string]any{{"namespace": "email", "value": "ana@customer.example"}}})
	brand, anaID := ana["brand_id"].(string), ana["id"].(string)
	root := "/v1/brands/" + brand + "/crm/mail"
	ownerEmail := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["email"].(string)
	inv := owner.must(201, "POST", base+"/invites", map[string]any{"email": "employee@northpeak.example", "role": "operator"})
	employee := &client{t: t, h: e.h}
	employee.must(200, "POST", "/v1/invites/accept", map[string]any{"token": inv["token"], "name": "Employee", "password": "not-real-test-password"})
	employeeID := employee.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)

	start := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(49 * time.Hour).UTC().Format(time.RFC3339)
	event := func(id, title string, guests ...string) map[string]any {
		var att []map[string]any
		for _, g := range guests {
			att = append(att, map[string]any{"email": g})
		}
		return map[string]any{"id": id, "status": "confirmed", "summary": title, "organizer": map[string]any{"email": "employee@northpeak.example"},
			"start": map[string]any{"dateTime": start}, "end": map[string]any{"dateTime": end}, "attendees": att}
	}
	private := func(e map[string]any) map[string]any { e["visibility"] = "private"; return e }
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/sa":
			_ = r.ParseForm()
			parts := strings.Split(r.PostForm.Get("assertion"), ".")
			raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
			var claims struct{ Sub, Scope string }
			_ = json.Unmarshal(raw, &claims)
			reply(map[string]any{"access_token": "test-workspace-token:" + claims.Scope + ":" + claims.Sub, "expires_in": 3600})
		case "/gmail/users/me/profile":
			reply(map[string]any{"emailAddress": ownerEmail})
		case "/gmail/users/me/messages":
			reply(map[string]any{"messages": []any{}})
		case "/calendar/calendars/primary/events":
			if !strings.Contains(r.Header.Get("Authorization"), "calendar.readonly:employee@northpeak.example") {
				t.Errorf("calendar read as %q", r.Header.Get("Authorization"))
			}
			reply(map[string]any{"nextSyncToken": "sync-1", "items": []any{
				event("e1", "Fictional renewal call", "employee@northpeak.example", "ana@customer.example"),
				event("e2", "Team standup", "employee@northpeak.example", "colleague@northpeak.example"),
				event("e3", "Dentist", "employee@northpeak.example", "front-desk@clinic.example"),
				private(event("e4", "Fictional private chat", "employee@northpeak.example", "ana@customer.example")),
			}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer fake.Close()
	e.integrations.Client.BaseURLs["google.token"] = fake.URL + "/sa"
	e.integrations.Client.BaseURLs["gmail"] = fake.URL + "/gmail"
	e.integrations.Client.BaseURLs["google_calendar"] = fake.URL + "/calendar"
	e.oauth.Endpoints.GoogleMail = fake.URL + "/gmail"

	conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "google_workspace", "credentials": map[string]string{"service_account_json": newServiceAccount(t), "admin_email": ownerEmail}})["id"].(string)
	owner.must(200, "PUT", root+"/policy", map[string]any{"internal_domains": []string{"northpeak.example"}, "blocked_addresses": []string{}, "blocked_domains": []string{}, "version": 0})
	owner.must(200, "PUT", root+"/rollout", map[string]any{"enabled": true, "allowed_user_ids": []string{employeeID}, "excluded_user_ids": []string{}, "workspace_integration_id": conn, "version": 0})
	var mailbox string
	if err := e.pool.QueryRow(ctx, `SELECT id::text FROM crm_google_connections WHERE workspace_id=$1 AND owner_id=$2`, ws, employeeID).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	var queued int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind='crm_calendar.workspace_sync' AND payload->>'connection_id'=$1`, mailbox).Scan(&queued); err != nil || queued == 0 {
		t.Fatalf("calendar not queued: %d %v", queued, err)
	}
	if err := e.srv.RunWorkspaceCalendar(ctx, ws, mailbox); err != nil {
		t.Fatal(err)
	}
	// Nothing goes into the team-visible meetings.
	var shared int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM meetings WHERE workspace_id=$1`, ws).Scan(&shared); err != nil || shared != 0 {
		t.Fatalf("team-visible meetings: %d %v", shared, err)
	}
	meetings := root + "/records/" + anaID + "/meetings"
	mine := employee.must(200, "GET", meetings, nil)["data"].([]any)
	if len(mine) != 1 || mine[0].(map[string]any)["title"] != "Fictional renewal call" || mine[0].(map[string]any)["record_id"] != anaID {
		t.Fatalf("kept %v; only the work meeting with a CRM contact should be", mine)
	}
	// Private to its owner, like their email, until they share the contact.
	if got := owner.must(200, "GET", meetings, nil)["data"].([]any); len(got) != 0 {
		t.Fatalf("a colleague saw a private meeting: %v", got)
	}
	// Under team sharing, a colleague sees it only once the AI check finds it is about work.
	owner.must(200, "PUT", root+"/policy", map[string]any{"internal_domains": []string{"northpeak.example"}, "blocked_addresses": []string{}, "blocked_domains": []string{}, "team_sharing": true, "version": 1})
	if _, err := e.pool.Exec(ctx, `UPDATE crm_mail_policy SET config = jsonb_set(config, '{team_sharing_from}', to_jsonb(now() - interval '1 hour')) WHERE brand_id=$1`, brand); err != nil {
		t.Fatal(err)
	}
	if got := owner.must(200, "GET", meetings, nil)["data"].([]any); len(got) != 0 {
		t.Fatalf("shown before the AI check: %v", got)
	}
	connectFakePrivacyModel(t, e, owner, ws)
	if err := e.srv.RunMailPrivacyCheck(ctx, ws, brand); err != nil {
		t.Fatal(err)
	}
	if got := owner.must(200, "GET", meetings, nil)["data"].([]any); len(got) != 1 || got[0].(map[string]any)["title"] != "Fictional renewal call" {
		t.Fatalf("after the AI check: %v", got)
	}
	// The owner's own choice wins: who and when only.
	employee.must(200, "PUT", root+"/records/"+anaID+"/sharing", map[string]any{"visibility": "metadata", "audience": "brand", "viewer_ids": []string{}, "version": 0})
	got := owner.must(200, "GET", meetings, nil)["data"].([]any)
	if len(got) != 1 || got[0].(map[string]any)["title"] != nil || got[0].(map[string]any)["visibility"] != "metadata" {
		t.Fatalf("who and when only: %v", got)
	}
	var cursor string
	if err := e.pool.QueryRow(ctx, `SELECT calendar_cursor FROM crm_google_connections WHERE id=$1`, mailbox).Scan(&cursor); err != nil || !strings.Contains(cursor, "sync-1") {
		t.Fatalf("cursor %q %v", cursor, err)
	}
	// Reading again keeps one meeting.
	if err := e.srv.RunWorkspaceCalendar(ctx, ws, mailbox); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM crm_calendar_events WHERE workspace_id=$1`, ws).Scan(&n); err != nil || n != 1 {
		t.Fatalf("meetings after a second read: %d %v", n, err)
	}
}
