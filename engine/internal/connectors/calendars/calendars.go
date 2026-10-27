// Package calendars reads meetings from calendars and booking tools:
// Google Calendar, Microsoft Outlook, Calendly and Cal.com. Every one only
// reads. Events without a lead among the attendees are dropped by the
// outcomes package before anything is stored.
//
// The request shapes follow each provider's public API reference. Base URLs
// can be pointed at a fake in tests through connectors.Client.BaseURLs.
package calendars

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/outcomes"
)

// All returns every available calendar connector.
func All() []c.Connector {
	return []c.Connector{Google{}, Outlook{}, Calendly{}, CalCom{}}
}

// Planned lists calendar and booking tools we intend to add.
func Planned() []c.Definition {
	p := func(id, name, site, summary string) c.Definition {
		return c.Definition{ID: id, Name: name, Category: c.CategoryCalendar, Website: site, Summary: summary,
			Auth: c.AuthAPIKey, Capabilities: []string{outcomes.CapMeetings}}
	}
	// both: the vendor has a sign-in for apps as well as a key.
	both := func(d c.Definition) c.Definition { d.Auth, d.AuthMethods = c.AuthOAuth2, c.Both; return d }
	return []c.Definition{
		p("chili_piper", "Chili Piper", "https://www.chilipiper.com", "Meetings booked through Chili Piper routers."),
		both(p("savvycal", "SavvyCal", "https://savvycal.com", "Meetings booked through SavvyCal links.")),
		both(p("zoom_scheduler", "Zoom Scheduler", "https://www.zoom.com/en/products/scheduler/", "Meetings booked through Zoom Scheduler.")),
	}
}

// How far back the first sync of a calendar reaches.
const lookback = 90 * 24 * time.Hour

// Most events read in one run; the rest come in the next run straight away.
const pageLimit = 500

func caps() []string { return []string{outcomes.CapMeetings} }

// doJSON sends req and decodes a JSON body into v. A non-2xx status is
// turned into a connectors.Error.
func doJSON(ctx context.Context, cl *c.Client, name string, req *http.Request, v any) (int, error) {
	req = req.WithContext(ctx)
	req.Header.Set("Accept", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		return 0, c.Errf(c.ErrUnavailable, "could not reach %s: %v", name, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp.StatusCode, c.Errf(c.ErrUnavailable, "reading the %s response: %v", name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, c.HTTPError(name, resp.StatusCode, string(body))
	}
	if v != nil && len(body) > 0 {
		if err := json.Unmarshal(body, v); err != nil {
			return resp.StatusCode, c.Errf(c.ErrUnavailable, "%s returned something that is not JSON", name)
		}
	}
	return resp.StatusCode, nil
}

func bearer(method, url, token string, body any) *http.Request {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, r)
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func parseTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.9999999", "2006-01-02T15:04:05", time.DateOnly} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}
