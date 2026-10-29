package hubspot

import (
	"context"
	"net/http"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Journeys' push_to_crm steps create or update a contact by email with
// POST /crm/v3/objects/contacts/batch/upsert (idProperty email), which
// answers whether the contact is new. Properties are HubSpot's internal
// property names; one HubSpot doesn't have fails the push with HubSpot's
// own message.

var _ c.ContactPusher = HubSpot{}

func (h conn) PushContact(ctx context.Context, cl *c.Client, creds c.Creds, p c.PushedContact) (c.ContactPushed, error) {
	props := map[string]string{"email": p.Email}
	for name, v := range map[string]string{"firstname": p.FirstName, "lastname": p.LastName, "company": p.Company, "website": p.Website} {
		if v != "" {
			props[name] = v
		}
	}
	for name, v := range p.Properties {
		if name != "email" {
			props[name] = v
		}
	}
	var up struct {
		Results []struct {
			ID  string `json:"id"`
			New bool   `json:"new"`
		} `json:"results"`
	}
	if err := h.call(ctx, cl, creds, http.MethodPost, "/crm/v3/objects/contacts/batch/upsert", map[string]any{
		"inputs": []any{map[string]any{"id": p.Email, "idProperty": "email", "properties": props}},
	}, &up); err != nil {
		return c.ContactPushed{}, err
	}
	if len(up.Results) == 0 || up.Results[0].ID == "" {
		return c.ContactPushed{}, c.Errf(c.ErrUnavailable, "HubSpot did not return the contact")
	}
	return c.ContactPushed{Created: up.Results[0].New, ExternalID: up.Results[0].ID}, nil
}
