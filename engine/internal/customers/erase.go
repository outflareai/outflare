package customers

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Erased are the Follows columns whose rows are about the person and go
// with them when their record is erased (DELETE .../records/{record}):
// what they did, what was sent to them and what they wrote. Owned rows
// (identities, links, history) go with the record itself.
var Erased = []Ref{
	{"customer_events", "record_id"}, {"consents", "record_id"},
	{"leads", "record_id"}, {"messages", "record_id"}, {"inbound_messages", "record_id"},
	{"meetings", "record_id"}, {"emails", "record_id"}, {"email_broadcast_recipients", "record_id"},
	{"journey_runs", "record_id"}, {"site_sessions", "record_id"}, {"site_visitors", "record_id"},
	{"ad_conversion_sends", "record_id"}, {"attribution_conversions", "record_id"}, {"link_prospects", "record_id"},
	{"rule_firings", "record_id"}, {"link_prospect_contacts", "record_id"}, {"link_pitches", "record_id"},
	// Lists they were put on by hand, an import or a journey.
	{"static_list_members", "record_id"},
	// The articles a journalist wrote, as their byline.
	{"news_article_authors", "record_id"},
	// Press coverage sits on the outlet's company record.
	{"coverage", "record_id"},
	// A brand's PR expert is the person; their quotes go with them.
	{"pr_experts", "record_id"},
	// The customers a partner referred, their purchases, and a partner's
	// portal sign-ins.
	{"partner_referrals", "record_id"}, {"partner_conversions", "record_id"},
	{"partner_sign_in_links", "record_id"}, {"partner_sessions", "record_id"},
}

// Kept are the Follows columns whose rows stay when a record is erased,
// unlinked from it, because they are the business's rather than the
// person's: deals and payments count as revenue, a company's events and
// visitors stay with the company, and an opt-out is the proof that they
// must not be contacted again. A test fails when a Follows column is in
// neither list.
var Kept = []Ref{
	// What partners earned and were paid: see erasedPartner.
	{"partner_memberships", "record_id"}, {"partner_rewards", "recipient_id"},
	{"customer_events", "account_id"}, {"deals", "record_id"}, {"payment_subscriptions", "record_id"},
	{"seo_competitors", "record_id"}, {"suppressions", "record_id"}, {"site_visitors", "account_id"},
}

// Erase deletes the records in ids, what Erased says is theirs, and the
// leads with any of emails (so the link job can't make the person again),
// and returns how many rows went, by table. Tables not created yet are
// skipped. ids must be records of workspaceID.
func Erase(ctx context.Context, tx pgx.Tx, workspaceID string, ids, emails []string) (map[string]int, error) {
	gone := map[string]int{}
	if err := keepPartnerLedger(ctx, tx, workspaceID, ids); err != nil {
		return nil, err
	}
	// Meaning-based search keeps a vector per record, reply and meeting.
	if ok, err := tableExists(ctx, tx, "search_embeddings"); err != nil {
		return nil, err
	} else if ok {
		tag, err := tx.Exec(ctx, `DELETE FROM search_embeddings s WHERE s.workspace_id = $1 AND (
			(s.kind = 'record' AND s.item_id = ANY($2)) OR
			(s.kind = 'message' AND s.item_id IN (SELECT id FROM inbound_messages WHERE workspace_id = $1 AND record_id = ANY($2))) OR
			(s.kind = 'meeting' AND s.item_id IN (SELECT id FROM meetings WHERE workspace_id = $1 AND record_id = ANY($2))))`, workspaceID, ids)
		if err != nil {
			return nil, err
		}
		gone["search_embeddings"] = int(tag.RowsAffected())
	}
	if len(emails) > 0 {
		tag, err := tx.Exec(ctx, `DELETE FROM leads WHERE workspace_id = $1 AND lower(email) = ANY($2)`, workspaceID, emails)
		if err != nil {
			return nil, err
		}
		gone["leads"] += int(tag.RowsAffected())
	}
	for _, r := range Erased {
		ok, err := tableExists(ctx, tx, r.Table)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		sql := `DELETE FROM ` + r.Table + ` WHERE workspace_id = $1 AND ` + r.Column + ` = ANY($2)`
		// A lifecycle email to several people stays; it is linked to a
		// record only when it went to one.
		if r.Table == "emails" {
			sql += ` AND cardinality(to_addresses) = 1`
		}
		tag, err := tx.Exec(ctx, sql, workspaceID, ids)
		if err != nil {
			return nil, err
		}
		gone[r.Table] += int(tag.RowsAffected())
	}
	tag, err := tx.Exec(ctx, `DELETE FROM records WHERE workspace_id = $1 AND id = ANY($2)`, workspaceID, ids)
	if err != nil {
		return nil, err
	}
	gone["records"] = int(tag.RowsAffected())
	return gone, nil
}

func tableExists(ctx context.Context, tx pgx.Tx, table string) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&ok)
	return ok, err
}

// erasedPartner is the name of the record a partner's ledger moves to when
// they are erased.
const erasedPartner = "Erased partner"

// keepPartnerLedger moves what a record earned and was paid as a partner
// (its program memberships, which hold its payouts, and its rewards) to a
// new archived record of its own with no name, fields or identities, so
// commission totals and payout history still add up once the person is
// gone, as deals and payments do. Their foreign keys cascade, so leaving
// them on the record would delete them with it. The memberships are
// suspended and lose their payout details, notes and sign-up answers, but
// keep their codes so the partner's links still lead to the site. Their
// payouts lose their notes and references too, unless the workspace's
// erase_partner_payout_notes is off.
func keepPartnerLedger(ctx context.Context, tx pgx.Tx, workspaceID string, ids []string) error {
	for _, table := range []string{"partner_memberships", "partner_rewards", "partner_payouts"} {
		if ok, err := tableExists(ctx, tx, table); err != nil || !ok {
			return err
		}
	}
	rows, err := tx.Query(ctx, `SELECT r.id::text, r.brand_id FROM records r
		WHERE r.workspace_id = $1 AND r.id = ANY($2) AND (
			EXISTS (SELECT 1 FROM partner_memberships m WHERE m.workspace_id = $1 AND m.record_id = r.id) OR
			EXISTS (SELECT 1 FROM partner_rewards w WHERE w.workspace_id = $1 AND w.recipient_id = r.id))`, workspaceID, ids)
	if err != nil {
		return err
	}
	type partner struct {
		id    string
		brand *string
	}
	partners, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (partner, error) {
		var p partner
		return p, r.Scan(&p.id, &p.brand)
	})
	if err != nil {
		return err
	}
	// One stand-in per erased record, so two of them never share a
	// program membership.
	for _, p := range partners {
		var standIn string
		if err := tx.QueryRow(ctx, `INSERT INTO records (workspace_id, record_type, brand_id, name, origin, archived_at)
			VALUES ($1, 'person', $2, $3, 'manual', now()) RETURNING id::text`, workspaceID, p.brand, erasedPartner).Scan(&standIn); err != nil {
			return err
		}
		for _, q := range []string{
			// What they wrote and where they're paid go; they earn no more.
			`UPDATE partner_memberships SET record_id = $3, payout_method = NULL, note = '', application = '{}', postback_url = '',
				status = 'suspended', updated_at = now() WHERE workspace_id = $1 AND record_id = $2`,
			`UPDATE partner_rewards SET recipient_id = $3 WHERE workspace_id = $1 AND recipient_id = $2`,
		} {
			if _, err := tx.Exec(ctx, q, workspaceID, p.id, standIn); err != nil {
				return err
			}
		}
		// A payout's note and reference can name them; the workspace says
		// whether they go. The amounts, dates and status stay.
		if _, err := tx.Exec(ctx, `UPDATE partner_payouts y SET note = '', reference = '', updated_at = now()
			FROM partner_memberships m, workspaces w
			WHERE y.workspace_id = $1 AND y.membership_id = m.id AND m.workspace_id = $1 AND m.record_id = $2
				AND w.id = $1 AND w.erase_partner_payout_notes`, workspaceID, standIn); err != nil {
			return err
		}
	}
	return nil
}
