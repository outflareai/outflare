package instantly

import (
	"context"
	"net/http"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Journeys add people to campaigns with POST /leads, campaign set. A lead
// already in the campaign is found first with POST /leads/list (search), so
// a retry never adds them twice; skip_if_in_workspace leaves out people in
// the account's other campaigns and lists unless the step allows it. A lead
// Instantly skips comes back without an id.

var _ c.LeadPusher = Instantly{}

func (Instantly) PushLead(ctx context.Context, cl *c.Client, cr c.Creds, campaignID string, l c.PushedLead) (c.LeadPushed, error) {
	var found struct {
		Items []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"items"`
	}
	if err := call(ctx, cl, cr, http.MethodPost, "/leads/list", nil, map[string]any{"campaign": campaignID, "search": l.Email, "limit": 10}, &found); err != nil {
		return c.LeadPushed{}, err
	}
	for _, it := range found.Items {
		if strings.EqualFold(it.Email, l.Email) {
			return c.LeadPushed{Outcome: c.PushAlreadyIn, ExternalID: it.ID}, nil
		}
	}
	body := map[string]any{"campaign": campaignID, "email": l.Email, "first_name": l.FirstName, "last_name": l.LastName,
		"company_name": l.Company, "website": l.Website, "skip_if_in_campaign": true, "skip_if_in_workspace": !l.AllowElsewhere}
	if len(l.Variables) > 0 {
		body["custom_variables"] = l.Variables
	}
	var made struct {
		ID string `json:"id"`
	}
	if err := call(ctx, cl, cr, http.MethodPost, "/leads", nil, body, &made); err != nil {
		return c.LeadPushed{}, err
	}
	if made.ID == "" {
		why := "Instantly skipped them"
		if !l.AllowElsewhere {
			why = "in another Instantly campaign or list"
		}
		return c.LeadPushed{Outcome: c.PushRefused, Reason: why}, nil
	}
	return c.LeadPushed{Outcome: c.PushAdded, ExternalID: made.ID}, nil
}
