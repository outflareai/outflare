package emailbison

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Journeys add people to campaigns in two steps, as the API reference
// does it: the lead is found (GET /api/leads?search=) or made
// (POST /api/leads, with custom_variables as name and value pairs that must
// already exist in EmailBison), then attached with
// POST /api/campaigns/{id}/leads/attach-leads. A lead already in the
// campaign (GET /api/campaigns/{id}/leads?search=) is left as it is.
// EmailBison doesn't say whether a lead is in another campaign, so
// AllowElsewhere changes nothing here. Leads attached to an active campaign
// can take up to five minutes to start.

var _ c.LeadPusher = EmailBison{}

type foundLead struct {
	ID    id     `json:"id"`
	Email string `json:"email"`
}

func (a *api) findLead(ctx context.Context, path, email string) (string, error) {
	var p page[foundLead]
	if err := a.do(ctx, http.MethodGet, path+"?search="+url.QueryEscape(email), nil, &p); err != nil {
		return "", err
	}
	for _, l := range p.Data {
		if strings.EqualFold(l.Email, email) {
			return string(l.ID), nil
		}
	}
	return "", nil
}

func (EmailBison) PushLead(ctx context.Context, cl *c.Client, creds c.Creds, campaignID string, l c.PushedLead) (c.LeadPushed, error) {
	a, err := open(cl, creds)
	if err != nil {
		return c.LeadPushed{}, err
	}
	campaign := "/api/campaigns/" + url.PathEscape(campaignID)
	if leadID, err := a.findLead(ctx, campaign+"/leads", l.Email); err != nil || leadID != "" {
		return c.LeadPushed{Outcome: c.PushAlreadyIn, ExternalID: leadID}, err
	}
	leadID, err := a.findLead(ctx, "/api/leads", l.Email)
	if err != nil {
		return c.LeadPushed{}, err
	}
	if leadID == "" {
		body := map[string]any{"email": l.Email, "first_name": l.FirstName, "last_name": l.LastName, "company": l.Company}
		if len(l.Variables) > 0 {
			names := make([]string, 0, len(l.Variables))
			for n := range l.Variables {
				names = append(names, n)
			}
			sort.Strings(names)
			vars := make([]map[string]string, len(names))
			for i, n := range names {
				vars[i] = map[string]string{"name": n, "value": l.Variables[n]}
			}
			body["custom_variables"] = vars
		}
		var made struct {
			Data foundLead `json:"data"`
		}
		if err := a.do(ctx, http.MethodPost, "/api/leads", body, &made); err != nil {
			return c.LeadPushed{}, err
		}
		if leadID = string(made.Data.ID); leadID == "" {
			return c.LeadPushed{}, c.Errf(c.ErrUnavailable, "EmailBison made the lead but didn't say its id")
		}
	}
	// Ids are numbers, as for sender emails.
	var ids any = []string{leadID}
	if n, err := strconv.Atoi(leadID); err == nil {
		ids = []int{n}
	}
	if err := a.do(ctx, http.MethodPost, campaign+"/leads/attach-leads", map[string]any{"lead_ids": ids}, nil); err != nil {
		return c.LeadPushed{}, err
	}
	return c.LeadPushed{Outcome: c.PushAdded, ExternalID: leadID}, nil
}
