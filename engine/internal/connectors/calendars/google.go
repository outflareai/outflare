package calendars

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/outcomes"
)

// Google reads a Google Calendar with incremental sync tokens.
type Google struct{}

const googleAPI = "https://www.googleapis.com/calendar/v3"

func (Google) Definition() c.Definition {
	return c.Definition{
		ID: "google_calendar", Name: "Google Calendar", Category: c.CategoryCalendar,
		Summary:     "Count meetings booked with leads, from any Google Workspace or Gmail calendar.",
		Description: "Reads events from one calendar (the primary one unless calendar_id is set) and keeps only those with a lead or a lead's colleague among the guests. Nothing is written to the calendar.",
		Website:     "https://calendar.google.com", DocsURL: "https://developers.google.com/calendar/api/v3/reference/events/list",
		Auth: c.AuthOAuth2,
		OAuth: &c.OAuth{
			AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
			Scopes:   []string{"https://www.googleapis.com/auth/calendar.events.readonly", "openid", "email"},
			Params:   map[string]string{"access_type": "offline", "prompt": "consent"},
		},
		Fields: []c.Field{}, Capabilities: caps(), Pricing: "Free",
	}
}

func (g Google) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var cal struct {
		ID       string `json:"id"`
		Summary  string `json:"summary"`
		TimeZone string `json:"timeZone"`
	}
	base := cl.Base("google_calendar", googleAPI)
	if _, err := doJSON(ctx, cl, "Google Calendar", bearer(http.MethodGet, base+"/calendars/primary", creds["access_token"], nil), &cal); err != nil {
		return c.Account{}, err
	}
	return c.Account{ID: cal.ID, Name: cal.Summary, Email: cal.ID, Details: map[string]string{"time_zone": cal.TimeZone}}, nil
}

type googleCursor struct {
	SyncToken string `json:"sync_token,omitempty"`
	PageToken string `json:"page_token,omitempty"`
	TimeMin   string `json:"time_min,omitempty"`
}

type googleEvent struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Summary   string `json:"summary"`
	Created   string `json:"created"`
	Organizer struct {
		Email string `json:"email"`
	} `json:"organizer"`
	Start, End struct {
		DateTime string `json:"dateTime"`
		Date     string `json:"date"`
	}
	Attendees []struct {
		Email          string `json:"email"`
		DisplayName    string `json:"displayName"`
		Resource       bool   `json:"resource"`
		ResponseStatus string `json:"responseStatus"`
	} `json:"attendees"`
}

func (g Google) SyncMeetings(ctx context.Context, cl *c.Client, creds c.Creds, cfg outcomes.SourceConfig, _ c.Account, cursor string) (outcomes.MeetingPage, error) {
	var cur googleCursor
	_ = json.Unmarshal([]byte(cursor), &cur)
	cal := cfg.CalendarID
	if cal == "" {
		cal = "primary"
	}
	base := cl.Base("google_calendar", googleAPI) + "/calendars/" + url.PathEscape(cal) + "/events"
	if cur.SyncToken == "" && cur.TimeMin == "" {
		cur.TimeMin = time.Now().Add(-lookback).UTC().Format(time.RFC3339)
	}
	var page outcomes.MeetingPage
	for len(page.Meetings) < pageLimit {
		q := url.Values{"singleEvents": {"true"}, "showDeleted": {"true"}, "maxResults": {"250"}}
		if cur.SyncToken != "" {
			q.Set("syncToken", cur.SyncToken)
		} else {
			q.Set("timeMin", cur.TimeMin)
		}
		if cur.PageToken != "" {
			q.Set("pageToken", cur.PageToken)
		}
		var resp struct {
			Items         []googleEvent `json:"items"`
			NextPageToken string        `json:"nextPageToken"`
			NextSyncToken string        `json:"nextSyncToken"`
		}
		status, err := doJSON(ctx, cl, "Google Calendar", bearer(http.MethodGet, base+"?"+q.Encode(), creds["access_token"], nil), &resp)
		if status == http.StatusGone {
			// The sync token expired: read the calendar again from the start.
			return outcomes.MeetingPage{Reset: true}, nil
		}
		if err != nil {
			return page, err
		}
		for _, e := range resp.Items {
			if m, ok := googleMeeting(e); ok {
				page.Meetings = append(page.Meetings, m)
			}
		}
		if resp.NextPageToken != "" {
			cur.PageToken = resp.NextPageToken
			continue
		}
		cur = googleCursor{SyncToken: resp.NextSyncToken}
		b, _ := json.Marshal(cur)
		page.Cursor = string(b)
		return page, nil
	}
	b, _ := json.Marshal(cur)
	page.Cursor, page.More = string(b), true
	return page, nil
}

func googleMeeting(e googleEvent) (outcomes.SyncedMeeting, bool) {
	start := parseTime(e.Start.DateTime)
	if start == nil {
		// All-day events are not meetings; a deleted event comes without
		// times and is still worth passing on so a stored one is canceled.
		if e.Status != "cancelled" {
			return outcomes.SyncedMeeting{}, false
		}
		start = &time.Time{}
	}
	m := outcomes.SyncedMeeting{
		ExternalID: e.ID, Title: e.Summary, StartsAt: *start, EndsAt: parseTime(e.End.DateTime), BookedAt: parseTime(e.Created),
		Canceled: e.Status == "cancelled", OrganizerEmail: e.Organizer.Email,
	}
	for _, a := range e.Attendees {
		if a.Resource {
			continue
		}
		m.Attendees = append(m.Attendees, outcomes.Attendee{Email: a.Email, Name: a.DisplayName})
	}
	return m, true
}
