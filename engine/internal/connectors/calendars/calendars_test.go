package calendars

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/outcomes"
)

func fake(t *testing.T, fn http.HandlerFunc) *c.Client {
	t.Helper()
	srv := httptest.NewServer(fn)
	t.Cleanup(srv.Close)
	return &c.Client{BaseURLs: map[string]string{"google_calendar": srv.URL, "outlook_calendar": srv.URL, "calendly": srv.URL, "cal_com": srv.URL}}
}

func TestGooglePagesThenKeepsSyncToken(t *testing.T) {
	cl := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		q := r.URL.Query()
		switch {
		case q.Get("syncToken") == "expired":
			w.WriteHeader(http.StatusGone)
		case q.Get("pageToken") == "":
			json.NewEncoder(w).Encode(map[string]any{"nextPageToken": "p2", "items": []any{
				map[string]any{"id": "a", "summary": "Intro", "start": map[string]string{"dateTime": "2026-10-20T10:00:00Z"},
					"attendees": []any{map[string]any{"email": "sam@acme.test"}, map[string]any{"email": "room@x", "resource": true}}},
				map[string]any{"id": "allday", "start": map[string]string{"date": "2026-10-20"}},
			}})
		default:
			json.NewEncoder(w).Encode(map[string]any{"nextSyncToken": "s1", "items": []any{
				map[string]any{"id": "b", "status": "cancelled"},
			}})
		}
	})
	creds := c.Creds{"access_token": "tok"}
	page, err := Google{}.SyncMeetings(context.Background(), cl, creds, outcomes.SourceConfig{}, c.Account{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Meetings) != 2 || len(page.Meetings[0].Attendees) != 1 || !page.Meetings[1].Canceled || page.Cursor != `{"sync_token":"s1"}` {
		t.Fatalf("page: %+v", page)
	}
	page, err = Google{}.SyncMeetings(context.Background(), cl, creds, outcomes.SourceConfig{}, c.Account{}, `{"sync_token":"expired"}`)
	if err != nil || !page.Reset {
		t.Fatalf("expired token: %+v %v", page, err)
	}
	if _, err := (Google{}).Test(context.Background(), cl, c.Creds{"access_token": "bad"}); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("bad token: %v", err)
	}
}

func TestCalComSkipsUnconfirmedBookings(t *testing.T) {
	cl := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("cal-api-version") == "" {
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []any{
			map[string]any{"uid": "1", "status": "accepted", "start": "2026-10-20T10:00:00.000Z", "attendees": []any{map[string]string{"email": "sam@acme.test"}}},
			map[string]any{"uid": "2", "status": "pending", "start": "2026-10-20T11:00:00.000Z"},
			map[string]any{"uid": "3", "status": "cancelled", "start": "2026-10-20T12:00:00.000Z"},
		}})
	})
	page, err := CalCom{}.SyncMeetings(context.Background(), cl, c.Creds{"api_key": "k"}, outcomes.SourceConfig{}, c.Account{}, "")
	if err != nil || len(page.Meetings) != 2 || !page.Meetings[1].Canceled {
		t.Fatalf("page: %+v %v", page, err)
	}
}
