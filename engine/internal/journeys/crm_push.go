package journeys

import (
	"context"
	"errors"
	"regexp"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// push_to_crm creates the person in the workspace's own CRM (HubSpot), or
// updates the contact with their email: their name, company and website,
// plus CRM properties filled from {{person.x}}, {{event.x}} and
// {{vars.x}}. It only writes the contact; deals stay where they are.

// crmPropertyRE is a CRM property's internal name.
var crmPropertyRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,99}$`)

func checkCRMPush(st Step) string {
	switch {
	case st.ConnectionID != "" && !uuidRE.MatchString(st.ConnectionID):
		return "connection_id is one of the workspace's CRM connections"
	case len(st.Properties) > 30:
		return "at most 30 properties"
	case st.SaveAs != "" && !saveRE.MatchString(st.SaveAs):
		return "save_as is lowercase letters, digits and _, starting with a letter"
	}
	for name, v := range st.Properties {
		if !crmPropertyRE.MatchString(name) {
			return "properties are named by the CRM's internal names: lowercase letters, digits and _"
		}
		if len(v) > 1000 {
			return "a property's value is at most 1,000 characters"
		}
		if err := emails.CheckFill(v); err != nil {
			return "properties." + name + ": " + err.Error()
		}
	}
	return ""
}

func crmPushCall(ctx context.Context, c *call) callResult {
	s, st := c.s, c.st
	if s.Store == nil || s.Store.Registry == nil {
		return failed("connector_needed", "CRMs can't be reached from here")
	}
	who, err := pushPersonOf(ctx, s.Pool, c.ws, c.r.recordID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return callResult{outcome: "skipped", detail: "the person no longer exists"}
	case err != nil:
		return retry("internal", "the person could not be read")
	case who.email == "":
		// A CRM contact is matched by email, so there is nothing to match.
		return callResult{outcome: "skipped", detail: "no verified email address"}
	}
	props := map[string]string{}
	for name, v := range st.Properties {
		if props[name], err = emails.Fill(v, c.data, nil); err != nil {
			return failed("invalid_request", "properties."+name+": "+err.Error())
		}
	}

	var o *connectors.Opened
	if st.ConnectionID != "" {
		o, err = s.Store.Open(ctx, s.Pool, c.ws, st.ConnectionID)
	} else {
		brand := ""
		if c.j.brandID != nil {
			brand = *c.j.brandID
		}
		o, err = s.Store.OpenCapable(ctx, s.Pool, c.ws, brand, connectors.CapPushContacts)
	}
	switch {
	case errors.Is(err, connectors.ErrNotFound), errors.Is(err, connectors.ErrNoCapable):
		return failed("connector_needed", "connect HubSpot in Settings, Connections")
	case err != nil && connectors.KindOf(err) == connectors.ErrAuth:
		return failed("connector_auth", "the CRM refused the connection: connect it again")
	case err != nil:
		return retry("connector_unavailable", shortErr(err))
	}
	p, ok := o.Connector.(connectors.ContactPusher)
	if !ok || !o.Connector.Definition().Has(connectors.CapPushContacts) {
		return failed("connector_needed", "that connection can't create contacts")
	}
	connector := o.Integration.Connector
	res, err := p.PushContact(ctx, o.Client, o.Creds, connectors.PushedContact{Email: who.email, FirstName: who.first, LastName: who.last,
		Company: who.company, Website: who.domain, Properties: props})
	_ = s.Store.Checked(ctx, s.Pool, c.ws, o.ID, nil, err)
	if err != nil {
		code := "connector_" + connectors.KindOf(err)
		metrics.CRMContactPushed(connector, "error")
		if code == "connector_auth" || code == "connector_invalid" {
			return failed(code, shortErr(err))
		}
		return retry(code, shortErr(err))
	}
	name := o.Connector.Definition().Name
	outcome, detail := "updated", "updated in "+name
	if res.Created {
		outcome, detail = "created", "created in "+name
	}
	metrics.CRMContactPushed(connector, outcome)
	r := callResult{outcome: "done", detail: detail, record: func(ctx context.Context, tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: c.ws, ActorType: "system", Action: "crm.contact_pushed",
			TargetType: "record", TargetID: c.r.recordID, Data: map[string]any{"integration_id": o.ID, "connector": connector,
				"external_id": res.ExternalID, "created": res.Created, "via": "journey", "journey_id": c.j.id, "run_id": c.r.id},
			Props: map[string]any{"connector": connector, "created": res.Created}})
	}}
	if st.SaveAs != "" {
		r.saved = map[string]any{"id": res.ExternalID, "created": res.Created}
	}
	return r
}
