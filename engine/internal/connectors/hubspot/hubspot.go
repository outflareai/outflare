// Package hubspot connects HubSpot as the CRM: it reads deals and meetings
// so cost per meeting and per deal can be reported, and can write meetings
// booked from outreach back onto the contact. HubSpot stays the source of
// truth for deals; Outflare never edits them.
//
// Two ways to connect, one connector: OAuth (one click, needs a HubSpot app
// configured on the server) or a private app access token (pasted, works
// on any server).
package hubspot

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/outcomes"
)

const api = "https://api.hubapi.com"

// Scopes for reading deals and meetings and writing contacts and meetings.
var scopes = []string{
	"oauth",
	"crm.objects.contacts.read", "crm.objects.contacts.write",
	"crm.objects.deals.read",
	"crm.schemas.deals.read",
}

// All returns HubSpot.
func All() []c.Connector { return []c.Connector{HubSpot{}} }

// HubSpot connects through its consent screen (needs a HubSpot app on the
// server) or with a private app's access token.
type HubSpot struct{ conn }

func (HubSpot) Definition() c.Definition {
	return c.Definition{
		ID: "hubspot", Name: "HubSpot", Category: c.CategoryCRM,
		Summary: "Count meetings and deals from outreach, and log booked meetings on the contact.",
		Description: "Reads deals (stage, amount, close date) and meetings, and ties each to the lead and campaign that started it. " +
			"Optionally writes meetings booked from outreach onto the HubSpot contact (off by default, since HubSpot may already log them from its own calendar sync). Deals are never changed.",
		Website: "https://www.hubspot.com", DocsURL: "https://developers.hubspot.com/docs/api/crm/deals",
		KeyURL: "https://knowledge.hubspot.com/integrations/how-do-i-get-my-hubspot-api-key",
		Auth:   c.AuthOAuth2, AuthMethods: c.Both,
		OAuth: &c.OAuth{
			AuthURL: "https://app.hubspot.com/oauth/authorize", TokenURL: api + "/oauth/v1/token",
			Scopes: scopes,
		},
		Fields: []c.Field{{Name: "api_key", Label: "Private app access token", Secret: true, Placeholder: "pat-…",
			Help: "In HubSpot: Settings, Integrations, Private apps, Create a private app. Give it the scopes " + strings.Join(scopes[1:], ", ") + "."}},
		Capabilities: []string{outcomes.CapDeals, outcomes.CapMeetings, outcomes.CapPushCRM},
		Pricing:      "Works on every HubSpot plan, including free",
		Replaces:     []string{"hubspot_private_app"},
	}
}

// conn is the shared implementation; the token is access_token for OAuth
// and api_key for a private app.
type conn struct{}

func (conn) call(ctx context.Context, cl *c.Client, creds c.Creds, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(ctx, method, cl.Base("hubspot", api)+path, r)
	req.Header.Set("Authorization", "Bearer "+c.Token(creds))
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := cl.Do(req)
	if err != nil {
		return c.Errf(c.ErrUnavailable, "could not reach HubSpot: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusForbidden && strings.Contains(string(data), "scope") {
		return c.Errf(c.ErrAuth, "HubSpot says this connection is missing a permission. Connect again and allow every requested scope.")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return c.HTTPError("HubSpot", resp.StatusCode, string(data))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return c.Errf(c.ErrUnavailable, "HubSpot returned something that is not JSON")
		}
	}
	return nil
}

func (h conn) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var acct struct {
		PortalID    int64  `json:"portalId"`
		UIDomain    string `json:"uiDomain"`
		CompanyCurr string `json:"companyCurrency"`
		AccountType string `json:"accountType"`
		TimeZone    string `json:"timeZone"`
	}
	if err := h.call(ctx, cl, creds, http.MethodGet, "/account-info/v3/details", nil, &acct); err != nil {
		return c.Account{}, err
	}
	id := strconv.FormatInt(acct.PortalID, 10)
	return c.Account{ID: id, Name: "HubSpot " + id, Details: map[string]string{
		"currency": acct.CompanyCurr, "account_type": acct.AccountType, "time_zone": acct.TimeZone, "ui_domain": acct.UIDomain}}, nil
}

// cursor is the last modified time seen, per object, as epoch milliseconds.
type cursor struct {
	Since int64  `json:"since"`
	After string `json:"after,omitempty"`
}

type searchResp struct {
	Results []struct {
		ID         string            `json:"id"`
		Properties map[string]string `json:"properties"`
		UpdatedAt  string            `json:"updatedAt"`
	} `json:"results"`
	Paging struct {
		Next struct {
			After string `json:"after"`
		} `json:"next"`
	} `json:"paging"`
}

// search reads objects modified at or after the cursor, oldest first.
func (h conn) search(ctx context.Context, cl *c.Client, creds c.Creds, object string, props []string, cur cursor) (searchResp, error) {
	since := cur.Since
	if since == 0 {
		since = time.Now().Add(-180 * 24 * time.Hour).UnixMilli()
	}
	body := map[string]any{
		"filterGroups": []any{map[string]any{"filters": []any{map[string]any{
			"propertyName": "hs_lastmodifieddate", "operator": "GTE", "value": strconv.FormatInt(since, 10)}}}},
		"sorts":      []any{map[string]any{"propertyName": "hs_lastmodifieddate", "direction": "ASCENDING"}},
		"properties": append(props, "hs_lastmodifieddate"),
		"limit":      100,
	}
	if cur.After != "" {
		body["after"] = cur.After
	}
	var out searchResp
	err := h.call(ctx, cl, creds, http.MethodPost, "/crm/v3/objects/"+object+"/search", body, &out)
	return out, err
}

// next advances the cursor past a page. The search API stops at 10,000
// results, so the page token is dropped in favour of the newest modified
// time once it gets deep.
func next(cur cursor, page searchResp) (cursor, bool) {
	newest := cur.Since
	for _, r := range page.Results {
		if t, err := time.Parse(time.RFC3339Nano, r.Properties["hs_lastmodifieddate"]); err == nil && t.UnixMilli() > newest {
			newest = t.UnixMilli()
		}
	}
	if page.Paging.Next.After != "" {
		if n, _ := strconv.Atoi(page.Paging.Next.After); n < 9000 {
			return cursor{Since: cur.Since, After: page.Paging.Next.After}, true
		}
		return cursor{Since: newest}, true
	}
	return cursor{Since: newest}, false
}

// contactEmails resolves an object's associated contacts to emails.
func (h conn) contactEmails(ctx context.Context, cl *c.Client, creds c.Creds, object string, ids []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(ids) == 0 {
		return out, nil
	}
	inputs := make([]any, len(ids))
	for i, id := range ids {
		inputs[i] = map[string]string{"id": id}
	}
	var assoc struct {
		Results []struct {
			From struct {
				ID string `json:"id"`
			} `json:"from"`
			To []struct {
				ToObjectID int64 `json:"toObjectId"`
			} `json:"to"`
		} `json:"results"`
	}
	if err := h.call(ctx, cl, creds, http.MethodPost, "/crm/v4/associations/"+object+"/contacts/batch/read", map[string]any{"inputs": inputs}, &assoc); err != nil {
		return nil, err
	}
	byContact := map[string][]string{}
	var contactIDs []any
	for _, r := range assoc.Results {
		for _, t := range r.To {
			cid := strconv.FormatInt(t.ToObjectID, 10)
			if _, seen := byContact[cid]; !seen {
				contactIDs = append(contactIDs, map[string]string{"id": cid})
			}
			byContact[cid] = append(byContact[cid], r.From.ID)
		}
	}
	for start := 0; start < len(contactIDs); start += 100 {
		end := min(start+100, len(contactIDs))
		var contacts struct {
			Results []struct {
				ID         string            `json:"id"`
				Properties map[string]string `json:"properties"`
			} `json:"results"`
		}
		if err := h.call(ctx, cl, creds, http.MethodPost, "/crm/v3/objects/contacts/batch/read",
			map[string]any{"properties": []string{"email"}, "inputs": contactIDs[start:end]}, &contacts); err != nil {
			return nil, err
		}
		for _, ct := range contacts.Results {
			if e := ct.Properties["email"]; e != "" {
				for _, obj := range byContact[ct.ID] {
					out[obj] = append(out[obj], e)
				}
			}
		}
	}
	return out, nil
}

func (h conn) SyncDeals(ctx context.Context, cl *c.Client, creds c.Creds, cur string) (outcomes.DealPage, error) {
	var cr cursor
	_ = json.Unmarshal([]byte(cur), &cr)
	stages, err := h.stages(ctx, cl, creds)
	if err != nil {
		return outcomes.DealPage{}, err
	}
	page, err := h.search(ctx, cl, creds, "deals", []string{"dealname", "dealstage", "pipeline", "amount", "deal_currency_code",
		"createdate", "closedate", "hs_is_closed", "hs_is_closed_won"}, cr)
	if err != nil {
		return outcomes.DealPage{}, err
	}
	ids := make([]string, len(page.Results))
	for i, r := range page.Results {
		ids[i] = r.ID
	}
	emails, err := h.contactEmails(ctx, cl, creds, "deals", ids)
	if err != nil {
		return outcomes.DealPage{}, err
	}
	var out outcomes.DealPage
	for _, r := range page.Results {
		p := r.Properties
		d := outcomes.SyncedDeal{ExternalID: r.ID, Name: p["dealname"], Stage: stages[p["dealstage"]], Currency: p["deal_currency_code"],
			ContactEmails: emails[r.ID], State: outcomes.DealOpen}
		if d.Stage == "" {
			d.Stage = p["dealstage"]
		}
		if p["hs_is_closed_won"] == "true" {
			d.State = outcomes.DealWon
		} else if p["hs_is_closed"] == "true" {
			d.State = outcomes.DealLost
		}
		if a, err := strconv.ParseFloat(p["amount"], 64); err == nil && a >= 0 {
			cents := int64(a*100 + 0.5)
			d.AmountCents = &cents
		}
		if t := parse(p["createdate"]); t != nil {
			d.OpenedAt = *t
		} else {
			d.OpenedAt = time.Now()
		}
		if d.State != outcomes.DealOpen {
			d.ClosedAt = parse(p["closedate"])
		}
		out.Deals = append(out.Deals, d)
	}
	nc, more := next(cr, page)
	b, _ := json.Marshal(nc)
	out.Cursor, out.More = string(b), more
	return out, nil
}

// stages maps deal stage ids to labels.
func (h conn) stages(ctx context.Context, cl *c.Client, creds c.Creds) (map[string]string, error) {
	var resp struct {
		Results []struct {
			Label  string `json:"label"`
			Stages []struct {
				ID    string `json:"id"`
				Label string `json:"label"`
			} `json:"stages"`
		} `json:"results"`
	}
	if err := h.call(ctx, cl, creds, http.MethodGet, "/crm/v3/pipelines/deals", nil, &resp); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, p := range resp.Results {
		for _, s := range p.Stages {
			out[s.ID] = s.Label
		}
	}
	return out, nil
}

func (h conn) SyncMeetings(ctx context.Context, cl *c.Client, creds c.Creds, _ outcomes.SourceConfig, _ c.Account, cur string) (outcomes.MeetingPage, error) {
	var cr cursor
	_ = json.Unmarshal([]byte(cur), &cr)
	page, err := h.search(ctx, cl, creds, "meetings", []string{"hs_meeting_title", "hs_meeting_start_time", "hs_meeting_end_time",
		"hs_meeting_outcome", "hs_createdate", "hs_activity_type"}, cr)
	if err != nil {
		return outcomes.MeetingPage{}, err
	}
	ids := make([]string, len(page.Results))
	for i, r := range page.Results {
		ids[i] = r.ID
	}
	emails, err := h.contactEmails(ctx, cl, creds, "meetings", ids)
	if err != nil {
		return outcomes.MeetingPage{}, err
	}
	var out outcomes.MeetingPage
	for _, r := range page.Results {
		p := r.Properties
		start := parse(p["hs_meeting_start_time"])
		if start == nil {
			continue
		}
		m := outcomes.SyncedMeeting{ExternalID: r.ID, Title: p["hs_meeting_title"], StartsAt: *start, EndsAt: parse(p["hs_meeting_end_time"]),
			BookedAt: parse(p["hs_createdate"])}
		switch p["hs_meeting_outcome"] {
		case "COMPLETED":
			m.Status = outcomes.MeetingHeld
		case "NO_SHOW":
			m.Status = outcomes.MeetingNoShow
		case "CANCELED":
			m.Canceled = true
		}
		for _, e := range emails[r.ID] {
			m.Attendees = append(m.Attendees, outcomes.Attendee{Email: e})
		}
		out.Meetings = append(out.Meetings, m)
	}
	nc, more := next(cr, page)
	b, _ := json.Marshal(nc)
	out.Cursor, out.More = string(b), more
	return out, nil
}

// PushMeeting upserts the lead as a contact (by email) and logs the
// meeting on it.
func (h conn) PushMeeting(ctx context.Context, cl *c.Client, creds c.Creds, m outcomes.PushMeeting) (string, error) {
	email := m.AttendeeEmail
	if m.LeadEmail != nil {
		email = *m.LeadEmail
	}
	if email == "" {
		return "", c.Errf(c.ErrInvalid, "meeting %s has no attendee email to find the HubSpot contact by", m.ID)
	}
	props := map[string]string{"email": email}
	if m.LeadFirstName != "" {
		props["firstname"] = m.LeadFirstName
	}
	if m.LeadLastName != "" {
		props["lastname"] = m.LeadLastName
	}
	if m.LeadCompany != "" {
		props["company"] = m.LeadCompany
	}
	var up struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	}
	if err := h.call(ctx, cl, creds, http.MethodPost, "/crm/v3/objects/contacts/batch/upsert", map[string]any{
		"inputs": []any{map[string]any{"id": email, "idProperty": "email", "properties": props}},
	}, &up); err != nil {
		return "", err
	}
	if len(up.Results) == 0 {
		return "", c.Errf(c.ErrUnavailable, "HubSpot did not return the contact for %s", email)
	}
	title := m.Title
	if title == "" {
		title = "Meeting with " + email
	}
	body := "Booked from outreach in Outflare."
	if m.SequenceName != nil {
		body = "Booked from the Outflare sequence \"" + *m.SequenceName + "\"."
	}
	props = map[string]string{
		"hs_timestamp":          m.StartsAt.UTC().Format(time.RFC3339),
		"hs_meeting_title":      title,
		"hs_meeting_body":       body,
		"hs_meeting_start_time": m.StartsAt.UTC().Format(time.RFC3339),
		"hs_meeting_outcome":    map[string]string{outcomes.MeetingHeld: "COMPLETED", outcomes.MeetingNoShow: "NO_SHOW"}[m.Status],
	}
	if props["hs_meeting_outcome"] == "" {
		props["hs_meeting_outcome"] = "SCHEDULED"
	}
	if m.EndsAt != nil {
		props["hs_meeting_end_time"] = m.EndsAt.UTC().Format(time.RFC3339)
	}
	var created struct {
		ID string `json:"id"`
	}
	// 200 is HubSpot's own meeting-to-contact association.
	err := h.call(ctx, cl, creds, http.MethodPost, "/crm/v3/objects/meetings", map[string]any{
		"properties": props,
		"associations": []any{map[string]any{
			"to":    map[string]string{"id": up.Results[0].ID},
			"types": []any{map[string]any{"associationCategory": "HUBSPOT_DEFINED", "associationTypeId": 200}},
		}},
	}, &created)
	return created.ID, err
}

func parse(s string) *time.Time {
	if s == "" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return &t
	}
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
		t := time.UnixMilli(ms)
		return &t
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return &t
	}
	return nil
}
