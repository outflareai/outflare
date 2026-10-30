package journeys

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adconv"
	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// sync_audience adds the person to a customer list audience on one of the
// workspace's ad accounts (a Meta custom audience, a Google Ads Customer
// Match list), or takes them out, through the workspace's own connection.
// The platform gets only their hashed emails and phone numbers. Adding
// follows the same consent rules as sending conversions, for ad data and
// ad personalisation; removing never needs consent.

// audienceIDRE is a platform's audience id: Meta's and Google's are
// digits.
var audienceIDRE = regexp.MustCompile(`^[0-9]{1,30}$`)

func checkAudience(st Step) string {
	switch {
	case !uuidRE.MatchString(st.AdAccountID):
		return "ad_account_id is one of the workspace's ad accounts"
	case !audienceIDRE.MatchString(st.AudienceID):
		return "audience_id is a customer list audience's id on that ad account"
	case st.Change != "" && st.Change != connectors.AudienceAdd && st.Change != connectors.AudienceRemove:
		return "change is add (the default) or remove"
	}
	return ""
}

func audienceCall(ctx context.Context, c *call) callResult {
	s, st := c.s, c.st
	if s.Store == nil || s.Store.Registry == nil {
		return failed("connector_needed", "ad platforms can't be reached from here")
	}
	change := st.Change
	if change == "" {
		change = connectors.AudienceAdd
	}
	var acct connectors.AdAccount
	var integrationID, connector string
	var meta []byte
	err := s.Pool.QueryRow(ctx, `SELECT integration_id, connector, external_id, name, currency, time_zone, meta FROM ad_accounts
		WHERE workspace_id = $1 AND id = $2 AND status = 'active'`, c.ws, st.AdAccountID).Scan(
		&integrationID, &connector, &acct.ID, &acct.Name, &acct.Currency, &acct.TimeZone, &meta)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return failed("not_found", "the ad account is gone or no longer active: pick another in the step")
	case err != nil:
		return retry("internal", "the ad account could not be read")
	}
	_ = json.Unmarshal(meta, &acct.Meta)

	m := connectors.AudienceMember{}
	rows, err := s.Pool.Query(ctx, `SELECT i.namespace, i.value FROM record_identities i JOIN records r ON r.id = i.record_id
		WHERE r.workspace_id = $1 AND r.id = $2 AND i.valid_to IS NULL AND i.namespace IN ('email', 'phone')
		ORDER BY i.verified DESC, i.created_at DESC LIMIT 6`, c.ws, c.r.recordID)
	if err != nil {
		return retry("internal", "the person could not be read")
	}
	for rows.Next() {
		var ns, v string
		if err := rows.Scan(&ns, &v); err != nil {
			rows.Close()
			return retry("internal", "the person could not be read")
		}
		if ns == "email" {
			m.Emails = append(m.Emails, connectors.HashID(v))
		} else {
			// E.164 with the plus; Meta wants digits only.
			m.Phones = append(m.Phones, connectors.HashID(v))
			m.PhoneDigits = append(m.PhoneDigits, connectors.HashID(strings.TrimPrefix(v, "+")))
		}
	}
	rows.Close()
	if rows.Err() != nil {
		return retry("internal", "the person could not be read")
	}
	if len(m.Emails) == 0 && len(m.Phones) == 0 {
		metrics.AdAudienceSynced(connector, "skipped")
		return callResult{outcome: "skipped", detail: "no email or phone number to match on"}
	}
	if change == connectors.AudienceAdd {
		mode, err := adconv.ConsentMode(ctx, s.Pool, c.ws)
		if err != nil {
			return retry("internal", "the consent setting could not be read")
		}
		add, granted, reason, err := adconv.AudienceConsent(ctx, s.Pool, c.ws, c.r.recordID, mode)
		if err != nil {
			return retry("internal", "their consent could not be read")
		}
		if !add {
			metrics.AdAudienceSynced(connector, "skipped")
			return callResult{outcome: "skipped", detail: reason}
		}
		m.ConsentGranted = granted
	}

	o, err := s.Store.Open(ctx, s.Pool, c.ws, integrationID)
	switch {
	case errors.Is(err, connectors.ErrNotFound):
		return failed("connector_needed", "the ad account's connection is gone: connect it again in Paid ads")
	case err != nil && connectors.KindOf(err) == connectors.ErrAuth:
		return failed("connector_auth", "the ad platform refused the connection: connect it again")
	case err != nil:
		return retry("connector_unavailable", shortErr(err))
	}
	syncer, ok := o.Connector.(connectors.AudienceSyncer)
	if !ok || !o.Connector.Definition().Has(connectors.CapAdsAudiences) {
		return failed("connector_needed", o.Connector.Definition().Name+" can't add people to audiences yet")
	}
	err = syncer.SyncAudience(ctx, o.Client, o.Creds, acct, st.AudienceID, change, m)
	_ = s.Store.Checked(ctx, s.Pool, c.ws, o.ID, nil, err)
	if err != nil {
		code := "connector_" + connectors.KindOf(err)
		metrics.AdAudienceSynced(connector, "error")
		if code == "connector_auth" || code == "connector_invalid" {
			return failed(code, shortErr(err))
		}
		return retry(code, shortErr(err))
	}
	name := o.Connector.Definition().Name
	outcome, detail := "added", "added to the audience in "+name
	if change == connectors.AudienceRemove {
		outcome, detail = "removed", "removed from the audience in "+name
	}
	metrics.AdAudienceSynced(connector, outcome)
	return callResult{outcome: "done", detail: detail, record: func(ctx context.Context, tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: c.ws, ActorType: "system", Action: "ads.audience_synced",
			TargetType: "record", TargetID: c.r.recordID, Data: map[string]any{"ad_account_id": st.AdAccountID, "connector": connector,
				"audience_id": st.AudienceID, "change": change, "consent_granted": m.ConsentGranted, "via": "journey", "journey_id": c.j.id, "run_id": c.r.id},
			Props: map[string]any{"connector": connector, "change": change}})
	}}
}
