package smartlead

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Journeys add people to campaigns with POST /campaigns/{id}/leads. Its
// answer counts what happened to the lead list: uploaded, already in the
// campaign, in another campaign (duplicate), unsubscribed, blocked or a bad
// address. Leads in other campaigns are left out unless
// ignore_duplicate_leads_in_other_campaign is set; the global block list
// and unsubscribes always apply.

var _ c.LeadPusher = Smartlead{}

func (Smartlead) PushLead(ctx context.Context, cl *c.Client, cr c.Creds, campaignID string, l c.PushedLead) (c.LeadPushed, error) {
	lead := map[string]any{"email": l.Email, "first_name": l.FirstName, "last_name": l.LastName, "company_name": l.Company, "website": l.Website}
	if len(l.Variables) > 0 {
		lead["custom_fields"] = l.Variables
	}
	b, _ := json.Marshal(map[string]any{"lead_list": []any{lead}, "settings": map[string]any{
		"ignore_global_block_list": false, "ignore_unsubscribe_list": false,
		"ignore_duplicate_leads_in_other_campaign": l.AllowElsewhere}})
	q := url.Values{"api_key": {cr["api_key"]}}
	req, _ := http.NewRequest(http.MethodPost, cl.Base(ID, "https://server.smartlead.ai")+"/api/v1/campaigns/"+url.PathEscape(campaignID)+"/leads?"+q.Encode(), bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	var out struct {
		Uploaded     int             `json:"upload_count"`
		AlreadyAdded int             `json:"already_added_to_campaign"`
		Duplicate    int             `json:"duplicate_count"`
		Invalid      int             `json:"invalid_email_count"`
		Unsubscribed json.RawMessage `json:"unsubscribed_leads"` // a count, or a list of them
		Blocked      int             `json:"block_count"`
	}
	if _, err := c.DoJSON(ctx, cl, "Smartlead", req, &out); err != nil {
		return c.LeadPushed{}, err
	}
	switch {
	case out.Uploaded > 0:
		return c.LeadPushed{Outcome: c.PushAdded}, nil
	case out.AlreadyAdded > 0:
		return c.LeadPushed{Outcome: c.PushAlreadyIn}, nil
	case out.Duplicate > 0:
		return c.LeadPushed{Outcome: c.PushRefused, Reason: "in another Smartlead campaign"}, nil
	case out.Invalid > 0:
		return c.LeadPushed{Outcome: c.PushRefused, Reason: "Smartlead won't take the address"}, nil
	case counted(out.Unsubscribed):
		return c.LeadPushed{Outcome: c.PushRefused, Reason: "unsubscribed in Smartlead"}, nil
	}
	return c.LeadPushed{Outcome: c.PushRefused, Reason: "on Smartlead's block list"}, nil
}

// counted reads a count that may come as a number or a list.
func counted(raw json.RawMessage) bool {
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return n > 0
	}
	var list []any
	return json.Unmarshal(raw, &list) == nil && len(list) > 0
}
