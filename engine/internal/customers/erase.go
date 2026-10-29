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
	// A partner's memberships, referrals, conversions, reward ledger and
	// portal sign-ins: their foreign keys cascade, so they go with the
	// record whichever list they are in.
	{"partner_memberships", "record_id"}, {"partner_referrals", "record_id"}, {"partner_conversions", "record_id"},
	{"partner_rewards", "recipient_id"}, {"partner_sign_in_links", "record_id"}, {"partner_sessions", "record_id"},
}

// Kept are the Follows columns whose rows stay when a record is erased,
// unlinked from it, because they are the business's rather than the
// person's: deals and payments count as revenue, a company's events and
// visitors stay with the company, and an opt-out is the proof that they
// must not be contacted again. A test fails when a Follows column is in
// neither list.
var Kept = []Ref{
	{"customer_events", "account_id"}, {"deals", "record_id"}, {"payment_subscriptions", "record_id"},
	{"seo_competitors", "record_id"}, {"suppressions", "record_id"}, {"site_visitors", "account_id"},
}

// Erase deletes the records in ids, what Erased says is theirs, and the
// leads with any of emails (so the link job can't make the person again),
// and returns how many rows went, by table. Tables not created yet are
// skipped. ids must be records of workspaceID.
func Erase(ctx context.Context, tx pgx.Tx, workspaceID string, ids, emails []string) (map[string]int, error) {
	gone := map[string]int{}
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
