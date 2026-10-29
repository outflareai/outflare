package journeys

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/leads"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// push_to_sequencer adds the person to the platform campaign a sequence
// is linked to (Smartlead, Instantly, EmailBison), through the workspace's
// own connection, leaving out people the sequence excludes. The sequencer
// then sends; Outflare's own enrollment is
// for sequences that send through Outflare (enroll_in_sequence).

// variableRE is a campaign variable's name on the platform.
var variableRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_ \-]{0,63}$`)

func checkPush(st Step) string {
	switch {
	case !uuidRE.MatchString(st.SequenceID):
		return "sequence_id is a sequence linked to a campaign in a connected sequencer"
	case len(st.Variables) > 20:
		return "at most 20 variables"
	}
	for name, v := range st.Variables {
		if !variableRE.MatchString(name) {
			return "variables are named as in the campaign: letters, digits, _, - and spaces, up to 64"
		}
		if len(v) > 500 {
			return "a variable's value is at most 500 characters"
		}
		if err := emails.CheckFill(v); err != nil {
			return "variables." + name + ": " + err.Error()
		}
	}
	return ""
}

// campaignLink is the platform campaign a push goes to.
type campaignLink struct {
	integrationID, connector, campaignID, name string
}

func pushCall(ctx context.Context, c *call) callResult {
	s, st := c.s, c.st
	if s.Store == nil || s.Store.Registry == nil {
		return failed("connector_needed", "sequencers can't be reached from here")
	}
	var status string
	var exclude customers.Exclusion
	err := s.Pool.QueryRow(ctx, `SELECT status, exclude FROM sequences WHERE workspace_id = $1 AND id = $2`, c.ws, st.SequenceID).Scan(&status, &exclude)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return failed("not_found", "the sequence no longer exists")
	case err != nil:
		return retry("internal", "the sequence could not be read")
	case status == "archived":
		return failed("invalid_request", "the sequence is archived")
	}
	rows, err := s.Pool.Query(ctx, `SELECT l.integration_id::text, i.connector, l.external_id, l.external_name FROM campaign_links l
		JOIN integrations i ON i.id = l.integration_id AND i.workspace_id = l.workspace_id
		WHERE l.workspace_id = $1 AND l.sequence_id = $2 ORDER BY l.created_at, l.id`, c.ws, st.SequenceID)
	if err != nil {
		return retry("internal", "the sequence's campaigns could not be read")
	}
	links, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (campaignLink, error) {
		var l campaignLink
		return l, row.Scan(&l.integrationID, &l.connector, &l.campaignID, &l.name)
	})
	if err != nil {
		return retry("internal", "the sequence's campaigns could not be read")
	}
	var link *campaignLink
	for i := range links {
		if d, ok := s.Store.Registry.Definition(links[i].connector); ok && d.Has(connectors.CapPushLeads) {
			link = &links[i]
			break
		}
	}
	switch {
	case len(links) == 0:
		return failed("invalid_request", "the sequence sends through Outflare, not a sequencer; use an enroll_in_sequence step")
	case link == nil:
		return failed("connector_needed", "the sequence's campaign is in a sequencer Outflare can't add people to")
	}

	who, err := pushPersonOf(ctx, s.Pool, c.ws, c.r.recordID)
	email, first, last, company, domain, suppressed := who.email, who.first, who.last, who.company, who.domain, who.suppressed
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return callResult{outcome: "skipped", detail: "the person no longer exists"}
	case err != nil:
		return retry("internal", "the person could not be read")
	case email == "":
		return callResult{outcome: "skipped", detail: "no verified email address"}
	case suppressed:
		// The opt-out list holds for every sequencer too.
		return callResult{outcome: "skipped", detail: "suppressed"}
	}
	// The sequence's exclusion holds wherever it sends, as it does for
	// enrolling.
	why, err := customers.Excluded(ctx, s.Pool, c.ws, c.r.recordID, exclude)
	if err != nil {
		return retry("internal", "the sequence's exclusion could not be checked")
	}
	if why != "" {
		metrics.Excluded("sequence")
		return callResult{outcome: "skipped", detail: why}
	}
	vars := map[string]string{}
	for name, v := range st.Variables {
		if vars[name], err = emails.Fill(v, c.data, nil); err != nil {
			return failed("invalid_request", "variables."+name+": "+err.Error())
		}
	}

	o, err := s.Store.Open(ctx, s.Pool, c.ws, link.integrationID)
	switch {
	case errors.Is(err, connectors.ErrNotFound):
		return failed("connector_needed", "the sequencer connection is gone: connect it again in Settings, Connections")
	case err != nil && connectors.KindOf(err) == connectors.ErrAuth:
		return failed("connector_auth", "the sequencer refused the connection: connect it again")
	case err != nil:
		return retry("connector_unavailable", shortErr(err))
	}
	p, ok := o.Connector.(connectors.LeadPusher)
	if !ok {
		return failed("connector_needed", "that connection can't add people to campaigns")
	}
	res, err := p.PushLead(ctx, o.Client, o.Creds, link.campaignID, connectors.PushedLead{Email: email, FirstName: first, LastName: last,
		Company: company, Website: domain, Variables: vars, AllowElsewhere: st.AllowElsewhere})
	_ = s.Store.Checked(ctx, s.Pool, c.ws, o.ID, nil, err)
	if err != nil {
		code := "connector_" + connectors.KindOf(err)
		metrics.SequencerPushed(link.connector, "error")
		if code == "connector_auth" || code == "connector_invalid" {
			return failed(code, shortErr(err))
		}
		return retry(code, shortErr(err))
	}
	metrics.SequencerPushed(link.connector, res.Outcome)
	where := link.name
	if where == "" {
		where = link.campaignID
	}
	switch res.Outcome {
	case connectors.PushAlreadyIn:
		return callResult{outcome: "skipped", detail: "already in " + where}
	case connectors.PushRefused:
		return callResult{outcome: "skipped", detail: strings.TrimSpace(res.Reason)}
	}
	return callResult{outcome: "done", detail: "added to " + where, record: func(ctx context.Context, tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: c.ws, ActorType: "system", Action: "sequence.lead_pushed",
			TargetType: "sequence", TargetID: st.SequenceID, Data: map[string]any{"record_id": c.r.recordID, "integration_id": link.integrationID,
				"connector": link.connector, "campaign_id": link.campaignID, "external_id": res.ExternalID,
				"via": "journey", "journey_id": c.j.id, "run_id": c.r.id},
			Props: map[string]any{"connector": link.connector}})
	}}
}

// pushPerson is what a push to another tool sends about the person: their
// verified email, name and the company they work at, and whether the
// address is on the opt-out list.
type pushPerson struct {
	email, first, last, company, domain string
	suppressed                          bool
}

// pushPersonOf reads the person for a push; pgx.ErrNoRows when they are
// gone.
func pushPersonOf(ctx context.Context, q jobs.Querier, ws, recordID string) (pushPerson, error) {
	var p pushPerson
	err := q.QueryRow(ctx, `SELECT COALESCE(e.value, ''), COALESCE(r.attributes->>'first_name', ''), COALESCE(r.attributes->>'last_name', ''),
			COALESCE(a.name, ''), COALESCE(a.domain, ''), e.value IS NOT NULL AND EXISTS (`+leads.SuppressedSQL("r.workspace_id", "e.value")+`)
		FROM records r
		LEFT JOIN LATERAL (SELECT value FROM record_identities i WHERE i.record_id = r.id AND i.namespace = 'email' AND i.verified
			AND i.valid_to IS NULL ORDER BY i.valid_from, i.id LIMIT 1) e ON true
		LEFT JOIN LATERAL (SELECT co.name, (SELECT value FROM record_identities d WHERE d.record_id = co.id AND d.namespace = 'domain'
				AND d.valid_to IS NULL ORDER BY d.valid_from, d.id LIMIT 1) AS domain
			FROM record_relationships rr JOIN records co ON co.id = rr.to_id AND co.archived_at IS NULL AND co.merged_into IS NULL
			WHERE rr.from_id = r.id AND rr.kind = 'companies' AND rr.valid_to IS NULL ORDER BY rr.valid_from, rr.to_id LIMIT 1) a ON true
		WHERE r.workspace_id = $1 AND r.id = $2`, ws, recordID).Scan(&p.email, &p.first, &p.last, &p.company, &p.domain, &p.suppressed)
	return p, err
}
