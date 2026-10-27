package calendars

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/outcomes"
)

// Outlook reads a Microsoft 365 or Outlook.com calendar with delta queries.
type Outlook struct{}

const graphAPI = "https://graph.microsoft.com/v1.0"

func (Outlook) Definition() c.Definition {
	return c.Definition{
		ID: "outlook_calendar", Name: "Outlook Calendar", Category: c.CategoryCalendar,
		Summary:     "Count meetings booked with leads, from a Microsoft 365 or Outlook.com calendar.",
		Description: "Reads the signed-in person's calendar and keeps only meetings with a lead or a lead's colleague among the attendees. Nothing is written to the calendar.",
		Website:     "https://outlook.office.com/calendar", DocsURL: "https://learn.microsoft.com/graph/api/event-delta",
		Auth: c.AuthOAuth2,
		OAuth: &c.OAuth{
			AuthURL:  "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
			TokenURL: "https://login.microsoftonline.com/common/oauth2/v2.0/token",
			Scopes:   []string{"offline_access", "User.Read", "Calendars.Read"},
		},
		Fields: []c.Field{}, Capabilities: caps(), Pricing: "Free",
	}
}

func (o Outlook) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var me struct {
		ID                string `json:"id"`
		DisplayName       string `json:"displayName"`
		Mail              string `json:"mail"`
		UserPrincipalName string `json:"userPrincipalName"`
	}
	base := cl.Base("outlook_calendar", graphAPI)
	if _, err := doJSON(ctx, cl, "Microsoft", bearer(http.MethodGet, base+"/me", creds["access_token"], nil), &me); err != nil {
		return c.Account{}, err
	}
	email := me.Mail
	if email == "" {
		email = me.UserPrincipalName
	}
	return c.Account{ID: me.ID, Name: me.DisplayName, Email: email}, nil
}

type graphEvent struct {
	ID              string `json:"id"`
	Subject         string `json:"subject"`
	IsCancelled     bool   `json:"isCancelled"`
	IsAllDay        bool   `json:"isAllDay"`
	CreatedDateTime string `json:"createdDateTime"`
	Start, End      struct {
		DateTime string `json:"dateTime"`
	}
	Organizer struct {
		EmailAddress struct{ Address string } `json:"emailAddress"`
	} `json:"organizer"`
	Attendees []struct {
		Type         string `json:"type"`
		EmailAddress struct {
			Address string `json:"address"`
			Name    string `json:"name"`
		} `json:"emailAddress"`
	} `json:"attendees"`
	Removed *struct{} `json:"@removed"`
}

// SyncMeetings follows the delta query: the cursor is the next page or
// the delta link Microsoft hands back, always on the Graph host.
func (o Outlook) SyncMeetings(ctx context.Context, cl *c.Client, creds c.Creds, _ outcomes.SourceConfig, _ c.Account, cursor string) (outcomes.MeetingPage, error) {
	base := cl.Base("outlook_calendar", graphAPI)
	next := cursor
	if next == "" || !strings.HasPrefix(next, base+"/") {
		// The delta window: Microsoft tracks changes inside it from now on.
		q := url.Values{
			"startDateTime": {time.Now().Add(-lookback).UTC().Format(time.RFC3339)},
			"endDateTime":   {time.Now().Add(365 * 24 * time.Hour).UTC().Format(time.RFC3339)},
		}
		next = base + "/me/calendarView/delta?" + q.Encode()
	}
	var page outcomes.MeetingPage
	for len(page.Meetings) < pageLimit {
		req := bearer(http.MethodGet, next, creds["access_token"], nil)
		req.Header.Set("Prefer", `outlook.timezone="UTC", odata.maxpagesize=100`)
		var resp struct {
			Value     []graphEvent `json:"value"`
			NextLink  string       `json:"@odata.nextLink"`
			DeltaLink string       `json:"@odata.deltaLink"`
		}
		status, err := doJSON(ctx, cl, "Microsoft", req, &resp)
		if status == http.StatusGone || (status == http.StatusBadRequest && cursor != "") {
			// The delta token expired or no longer matches: start over.
			return outcomes.MeetingPage{Reset: true}, nil
		}
		if err != nil {
			return page, err
		}
		for _, e := range resp.Value {
			if m, ok := graphMeeting(e); ok {
				page.Meetings = append(page.Meetings, m)
			}
		}
		if resp.NextLink != "" && strings.HasPrefix(resp.NextLink, base+"/") {
			next = resp.NextLink
			continue
		}
		if resp.DeltaLink != "" && strings.HasPrefix(resp.DeltaLink, base+"/") {
			page.Cursor = resp.DeltaLink
		}
		return page, nil
	}
	page.Cursor, page.More = next, true
	return page, nil
}

func graphMeeting(e graphEvent) (outcomes.SyncedMeeting, bool) {
	if e.Removed != nil {
		return outcomes.SyncedMeeting{ExternalID: e.ID, Canceled: true}, true
	}
	start := parseTime(e.Start.DateTime)
	if start == nil || e.IsAllDay {
		return outcomes.SyncedMeeting{}, false
	}
	m := outcomes.SyncedMeeting{
		ExternalID: e.ID, Title: e.Subject, StartsAt: start.UTC(), EndsAt: parseTime(e.End.DateTime), BookedAt: parseTime(e.CreatedDateTime),
		Canceled: e.IsCancelled, OrganizerEmail: e.Organizer.EmailAddress.Address,
	}
	for _, a := range e.Attendees {
		if a.Type == "resource" {
			continue
		}
		m.Attendees = append(m.Attendees, outcomes.Attendee{Email: a.EmailAddress.Address, Name: a.EmailAddress.Name})
	}
	if org := e.Organizer.EmailAddress.Address; org != "" {
		m.Attendees = append(m.Attendees, outcomes.Attendee{Email: org})
	}
	return m, true
}
