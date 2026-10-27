package calendars

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/outcomes"
)

// CalCom reads bookings from Cal.com (cloud, or a self-hosted instance
// through the base URL field).
type CalCom struct{}

const calcomAPI = "https://api.cal.com/v2"

// The bookings API version this connector speaks.
const calcomVersion = "2024-08-13"

// Like Calendly, Cal.com is re-read over a recent window every run.
const calcomRescan = 30 * 24 * time.Hour

func (CalCom) Definition() c.Definition {
	return c.Definition{
		ID: "cal_com", Name: "Cal.com", Category: c.CategoryCalendar,
		Summary:     "Count meetings leads book through your Cal.com links.",
		Description: "Reads your Cal.com bookings and their attendees. Nothing is changed in Cal.com.",
		Website:     "https://cal.com", DocsURL: "https://cal.com/docs/api-reference/v2/bookings/get-all-bookings",
		KeyURL: "https://app.cal.com/settings/developer/api-keys",
		// Signing in is for Cal.com's cloud; a self-hosted instance uses a key.
		Auth: c.AuthOAuth2, AuthMethods: c.Both,
		OAuth: &c.OAuth{
			AuthURL: "https://app.cal.com/auth/oauth2/authorize", TokenURL: "https://api.cal.com/v2/auth/oauth2/token",
			Scopes: []string{"PROFILE_READ", "BOOKING_READ"},
		},
		Fields: []c.Field{
			{Name: "api_key", Label: "API key", Secret: true, Placeholder: "cal_live_…", Help: "In Cal.com: Settings, Developer, API keys."},
			{Name: "base_url", Label: "API URL", Optional: true, Placeholder: calcomAPI, Help: "Only for self-hosted Cal.com."},
		},
		Capabilities: caps(), Pricing: "Free",
	}
}

func calcomBase(cl *c.Client, creds c.Creds) string {
	if u := creds["base_url"]; u != "" {
		if p, err := url.Parse(u); err == nil && p.Scheme == "https" && p.Host != "" {
			return cl.Base("cal_com", u)
		}
	}
	return cl.Base("cal_com", calcomAPI)
}

func calcomReq(base, path, key string) *http.Request {
	req := bearer(http.MethodGet, base+path, key, nil)
	req.Header.Set("cal-api-version", calcomVersion)
	return req
}

func (k CalCom) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var me struct {
		Data struct {
			ID       int    `json:"id"`
			Email    string `json:"email"`
			Name     string `json:"name"`
			Username string `json:"username"`
		} `json:"data"`
	}
	if _, err := doJSON(ctx, cl, "Cal.com", calcomReq(calcomBase(cl, creds), "/me", c.Token(creds)), &me); err != nil {
		return c.Account{}, err
	}
	return c.Account{ID: strconv.Itoa(me.Data.ID), Name: me.Data.Name, Email: me.Data.Email,
		Details: map[string]string{"username": me.Data.Username}}, nil
}

func (k CalCom) SyncMeetings(ctx context.Context, cl *c.Client, creds c.Creds, _ outcomes.SourceConfig, _ c.Account, cursor string) (outcomes.MeetingPage, error) {
	base := calcomBase(cl, creds)
	since := time.Now().Add(-calcomRescan)
	if cursor == "" {
		since = time.Now().Add(-lookback)
	}
	var page outcomes.MeetingPage
	for skip := 0; len(page.Meetings) < pageLimit; skip += 100 {
		q := url.Values{"afterStart": {since.UTC().Format(time.RFC3339)}, "take": {"100"}, "skip": {strconv.Itoa(skip)}, "sortStart": {"asc"}}
		var resp struct {
			Data []struct {
				UID       string `json:"uid"`
				Title     string `json:"title"`
				Status    string `json:"status"`
				Start     string `json:"start"`
				End       string `json:"end"`
				CreatedAt string `json:"createdAt"`
				Hosts     []struct {
					Email string `json:"email"`
					Name  string `json:"name"`
				} `json:"hosts"`
				Attendees []struct {
					Email string `json:"email"`
					Name  string `json:"name"`
				} `json:"attendees"`
			} `json:"data"`
		}
		if _, err := doJSON(ctx, cl, "Cal.com", calcomReq(base, "/bookings?"+q.Encode(), c.Token(creds)), &resp); err != nil {
			return page, err
		}
		for _, b := range resp.Data {
			start := parseTime(b.Start)
			if start == nil || b.Status == "rejected" || b.Status == "pending" {
				continue
			}
			m := outcomes.SyncedMeeting{ExternalID: b.UID, Title: b.Title, StartsAt: *start, EndsAt: parseTime(b.End),
				BookedAt: parseTime(b.CreatedAt), Canceled: b.Status == "cancelled"}
			for _, h := range b.Hosts {
				m.Attendees = append(m.Attendees, outcomes.Attendee{Email: h.Email, Name: h.Name})
				if m.OrganizerEmail == "" {
					m.OrganizerEmail = h.Email
				}
			}
			for _, a := range b.Attendees {
				m.Attendees = append(m.Attendees, outcomes.Attendee{Email: a.Email, Name: a.Name})
			}
			page.Meetings = append(page.Meetings, m)
		}
		if len(resp.Data) < 100 {
			break
		}
	}
	page.Cursor = time.Now().UTC().Format(time.RFC3339)
	return page, nil
}
