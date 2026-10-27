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

// Calendly reads scheduled events and their invitees.
type Calendly struct{}

const calendlyAPI = "https://api.calendly.com"

// Calendly has no "changed since" filter, so every run reads events that
// start in this window and upserts them; cancellations inside it are seen.
const calendlyRescan = 30 * 24 * time.Hour

func (Calendly) Definition() c.Definition {
	return c.Definition{
		ID: "calendly", Name: "Calendly", Category: c.CategoryCalendar,
		Summary:     "Count meetings leads book through your Calendly links.",
		Description: "Reads scheduled events for your Calendly organization and their invitees. Nothing is changed in Calendly.",
		Website:     "https://calendly.com", DocsURL: "https://developer.calendly.com/api-docs",
		KeyURL: "https://calendly.com/integrations/api_webhooks",
		Auth:   c.AuthOAuth2, AuthMethods: c.Both,
		OAuth: &c.OAuth{
			AuthURL: "https://auth.calendly.com/oauth/authorize", TokenURL: "https://auth.calendly.com/oauth/token",
			Scopes: []string{"users:read", "scheduled_events:read"},
		},
		Fields: []c.Field{{Name: "api_key", Label: "Personal access token", Secret: true,
			Help: "In Calendly: Integrations, API and webhooks, Generate new token, with the users:read and scheduled_events:read scopes."}},
		Capabilities: caps(), Pricing: "Included with paid Calendly plans",
	}
}

func (k Calendly) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var me struct {
		Resource struct {
			URI                 string `json:"uri"`
			Name                string `json:"name"`
			Email               string `json:"email"`
			CurrentOrganization string `json:"current_organization"`
		} `json:"resource"`
	}
	base := cl.Base("calendly", calendlyAPI)
	if _, err := doJSON(ctx, cl, "Calendly", bearer(http.MethodGet, base+"/users/me", c.Token(creds), nil), &me); err != nil {
		return c.Account{}, err
	}
	return c.Account{ID: me.Resource.URI, Name: me.Resource.Name, Email: me.Resource.Email,
		Details: map[string]string{"organization": me.Resource.CurrentOrganization}}, nil
}

func (k Calendly) SyncMeetings(ctx context.Context, cl *c.Client, creds c.Creds, _ outcomes.SourceConfig, acct c.Account, cursor string) (outcomes.MeetingPage, error) {
	base := cl.Base("calendly", calendlyAPI)
	org := acct.Details["organization"]
	if org == "" {
		a, err := k.Test(ctx, cl, creds)
		if err != nil {
			return outcomes.MeetingPage{}, err
		}
		org = a.Details["organization"]
	}
	since := time.Now().Add(-calendlyRescan)
	if cursor == "" {
		since = time.Now().Add(-lookback)
	}
	q := url.Values{"organization": {org}, "min_start_time": {since.UTC().Format(time.RFC3339)}, "count": {"100"}, "sort": {"start_time:asc"}}
	next := base + "/scheduled_events?" + q.Encode()
	var page outcomes.MeetingPage
	for next != "" && len(page.Meetings) < pageLimit {
		var resp struct {
			Collection []struct {
				URI       string `json:"uri"`
				Name      string `json:"name"`
				Status    string `json:"status"`
				StartTime string `json:"start_time"`
				EndTime   string `json:"end_time"`
				CreatedAt string `json:"created_at"`
				Members   []struct {
					UserEmail string `json:"user_email"`
				} `json:"event_memberships"`
			} `json:"collection"`
			Pagination struct {
				NextPage string `json:"next_page"`
			} `json:"pagination"`
		}
		if _, err := doJSON(ctx, cl, "Calendly", bearer(http.MethodGet, next, c.Token(creds), nil), &resp); err != nil {
			return page, err
		}
		for _, e := range resp.Collection {
			start := parseTime(e.StartTime)
			if start == nil || !strings.HasPrefix(e.URI, base+"/") {
				continue
			}
			m := outcomes.SyncedMeeting{ExternalID: e.URI, Title: e.Name, StartsAt: *start, EndsAt: parseTime(e.EndTime),
				BookedAt: parseTime(e.CreatedAt), Canceled: e.Status == "canceled"}
			for _, mem := range e.Members {
				m.Attendees = append(m.Attendees, outcomes.Attendee{Email: mem.UserEmail})
				if m.OrganizerEmail == "" {
					m.OrganizerEmail = mem.UserEmail
				}
			}
			var inv struct {
				Collection []struct {
					Email  string `json:"email"`
					Name   string `json:"name"`
					Status string `json:"status"`
				} `json:"collection"`
			}
			if _, err := doJSON(ctx, cl, "Calendly", bearer(http.MethodGet, e.URI+"/invitees?count=100", c.Token(creds), nil), &inv); err != nil {
				return page, err
			}
			for _, i := range inv.Collection {
				m.Attendees = append(m.Attendees, outcomes.Attendee{Email: i.Email, Name: i.Name})
			}
			page.Meetings = append(page.Meetings, m)
		}
		next = resp.Pagination.NextPage
		if next != "" && !strings.HasPrefix(next, base+"/") {
			next = ""
		}
	}
	page.Cursor = time.Now().UTC().Format(time.RFC3339)
	return page, nil
}
