package emailbison_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/emailbison"
)

// Before people are added, the campaign is read as the earlier pipeline
// read it: its status, sequence steps, schedule and connected senders with
// their daily limits. A campaign that isn't ready says why; one that is
// says how many new people a day its senders can start.
func TestCampaignReady(t *testing.T) {
	campaign := map[string]any{"status": "active", "max_new_leads_per_day": 80, "max_emails_per_day": 500}
	steps := []map[string]any{{"email_subject": "Hi {FIRST_NAME}", "email_body": "<p>Hello {FIRST_NAME} at {COMPANY}</p>"},
		{"email_subject": "", "email_body": "Following up", "thread_reply": true}}
	schedule := map[string]any{"monday": true, "tuesday": true, "wednesday": true, "thursday": true, "friday": true, "saturday": false, "sunday": false,
		"start_time": "09:00", "end_time": "17:00", "timezone": "Europe/Vilnius"}
	senders := [][]map[string]any{
		{{"id": 1, "email": "a@send.test", "status": "Connected", "daily_limit": 30}, {"id": 2, "email": "b@send.test", "status": "Disconnected", "daily_limit": 30}},
		{{"id": 3, "email": "c@send.test", "status": "Connected", "daily_limit": 25}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enc := json.NewEncoder(w)
		switch r.URL.Path {
		case "/api/campaigns/10":
			enc.Encode(map[string]any{"data": campaign})
		case "/api/campaigns/v1.1/10/sequence-steps":
			enc.Encode(map[string]any{"data": map[string]any{"sequence_steps": steps}})
		case "/api/campaigns/10/schedule":
			enc.Encode(map[string]any{"data": schedule})
		case "/api/campaigns/10/sender-emails":
			p := 1
			if r.URL.Query().Get("page") == "2" {
				p = 2
			}
			enc.Encode(map[string]any{"data": senders[p-1], "meta": map[string]any{"current_page": p, "last_page": 2, "total": 3}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	creds := c.Creds{"base_url": srv.URL, "api_key": "key"}
	read := func() c.Readiness {
		t.Helper()
		r, err := emailbison.EmailBison{}.CampaignReady(context.Background(), &c.Client{}, creds, "10")
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := read(); !r.Ready() || r.DailyCapacity != 55 || r.Senders != 2 {
		t.Fatalf("ready: %+v", r)
	}
	campaign["max_new_leads_per_day"] = 40
	if r := read(); r.DailyCapacity != 40 {
		t.Fatalf("within the campaign's own limit: %+v", r)
	}
	campaign["status"] = "paused"
	steps[0]["email_body"] = "Hello {FIRST_NAME}, about {HOOK_BODY_1}"
	schedule["start_time"] = "18:00"
	senders[0][0]["status"], senders[1][0]["status"] = "Disconnected", "Disconnected"
	r := read()
	all := strings.Join(r.Reasons, " | ")
	for _, want := range []string{"paused", "no connected sender", "{HOOK_BODY_1}", "sending window"} {
		if !strings.Contains(all, want) {
			t.Errorf("reasons %q lack %q", all, want)
		}
	}
	if r.Ready() || r.DailyCapacity != 0 {
		t.Fatalf("not ready: %+v", r)
	}
	steps = nil
	if r := read(); !strings.Contains(strings.Join(r.Reasons, " "), "no email steps") {
		t.Fatalf("no steps: %+v", r)
	}
}
