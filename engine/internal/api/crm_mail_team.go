package api

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/alerts"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/crmmail"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// Team mail sharing: an owner or admin can make a brand's captured work
// mail readable by everyone who works on that brand, so the history with a
// customer, partner or supplier isn't stuck in one person's inbox. It
// starts crmmail.TeamSharingNotice after it is switched on, and every
// member is alerted at once, so nobody's mail is shared before they know.
// A person can still keep any contact's mail private (its sharing set to
// private), private Gmail labels and payroll, HR and banking services are
// never captured, and every read of a colleague's mail is recorded
// (crm_mail.viewed).

// AlertTeamMailSharing tells a member their brand's work mail will be
// shared with the brand.
const AlertTeamMailSharing = "crm_mail.team_sharing"

func init() {
	alerts.Register(alerts.Kind{ID: AlertTeamMailSharing, Title: "Team email sharing",
		Description: "An owner or admin switched team email sharing on or off for a brand. It goes to every member of the brand, so they know before their work email with CRM contacts is shared.",
		Scope:       auth.ScopeCustomersRead, Describe: describeTeamMailSharing})
}

func describeTeamMailSharing(ctx context.Context, q jobs.Querier, a alerts.Row) (alerts.View, error) {
	v := alerts.View{Path: "crm"}
	if on, _ := a.Data["on"].(bool); !on {
		v.Title, v.Text = "Team email sharing is off", "Your work email with CRM contacts is private to you again."
		return v, nil
	}
	starts, _ := a.Data["starts_at"].(string)
	when := starts
	if t, err := time.Parse(time.RFC3339, starts); err == nil {
		when = t.UTC().Format("2 January 2006 at 15:04 UTC")
	}
	v.Title = "Your team will be able to read your work email from " + when
	v.Text = "Email you've saved with customers, partners and suppliers in the CRM becomes readable by everyone on this brand. Mark a contact private to keep it to yourself; mail labelled Personal, HR, Legal, Finance or Medical, and payroll, HR and banking mail, is never saved."
	return v, nil
}

// crmTeamSharingChanged records the switch and alerts the brand's members,
// in the policy write's transaction.
func (s *Server) crmTeamSharingChanged(ctx context.Context, tx pgx.Tx, a *access, p crmmail.Policy) error {
	data := map[string]any{"brand_id": a.Brand, "on": p.TeamSharing}
	if p.TeamSharingFrom != nil {
		data["starts_at"] = p.TeamSharingFrom.Format(time.RFC3339)
	}
	if err := record(ctx, tx, &a.principal, a.WorkspaceID, "crm_mail.team_sharing_changed", "brand", a.Brand, data); err != nil {
		return err
	}
	// The mail already saved is checked during the notice period.
	if p.TeamSharing {
		if err := enqueueMailPrivacy(ctx, tx, a.WorkspaceID, a.Brand, "on:"+data["starts_at"].(string)); err != nil {
			return err
		}
	}
	rows, err := tx.Query(ctx, `SELECT m.user_id::text FROM memberships m JOIN users u ON u.id = m.user_id AND u.disabled_at IS NULL
		WHERE m.workspace_id = $1 AND (m.brand_ids IS NULL OR cardinality(m.brand_ids) = 0 OR $2::uuid = ANY(m.brand_ids))`, a.WorkspaceID, a.Brand)
	if err != nil {
		return err
	}
	members, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	key := "off:" + time.Now().UTC().Format(time.RFC3339Nano)
	if p.TeamSharingFrom != nil {
		key = "on:" + p.TeamSharingFrom.Format(time.RFC3339)
	}
	brand := a.Brand
	for _, m := range members {
		owner := m
		if _, err := alerts.Raise(ctx, tx, alerts.Alert{WorkspaceID: a.WorkspaceID, BrandID: &brand, Kind: AlertTeamMailSharing,
			DedupeKey: a.Brand + ":" + m + ":" + key, TargetType: "brand", TargetID: a.Brand, OwnerID: &owner, Data: data, RequestID: RequestID(ctx)}); err != nil {
			return err
		}
	}
	return nil
}

// mailLabels checks a list of private Gmail labels: nil keeps the
// defaults, and each is 1 to 100 characters.
func mailLabels(in []string) ([]string, error) {
	if in == nil {
		return nil, nil
	}
	if len(in) > 50 {
		return nil, errInvalid("private_labels holds at most 50 labels")
	}
	out := []string{}
	for _, l := range in {
		l = strings.TrimSpace(l)
		if l == "" || len(l) > 100 || strings.ContainsAny(l, "\"{}()") {
			return nil, errInvalid("each private label is 1 to 100 characters, without quotes or brackets")
		}
		out = append(out, l)
	}
	return out, nil
}
