package customers

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
)

// Unsubscribes are consents too. Whatever email someone unsubscribes from,
// they withdraw consent to all marketing email in the workspace, on their
// person record, beside the suppression that blocks the address. Only the
// person opting in again clears it.

// Unsubscribe is an email address that unsubscribed, with when and how.
type Unsubscribe struct {
	Email string
	// Default now.
	At time.Time
}

// OptInSources are the consent sources that mean the person opted in
// themselves: signing up and agreeing to email, a form or checkout box, the
// preference page. Only a grant from one of these clears an unsubscribe;
// one from an import, a CRM sync, an agent or the API with no source never
// does.
var OptInSources = []string{"preference_center", "signup_form", "signup", "opt_in", "double_opt_in", "checkout", "form", "in_app"}

func optedIn(source string) bool { return slices.Contains(OptInSources, strings.ToLower(source)) }

// WithdrawEmails records a withdrawn consent to all marketing email for
// each address's person (adding a prospect record for an address nobody
// has), unless their latest decision is already a withdrawal, and links
// the address's unsubscribe suppression to them. It returns how many
// consents it recorded.
func WithdrawEmails(ctx context.Context, tx pgx.Tx, workspaceID, source, requestID string, unsubs []Unsubscribe) (int, error) {
	if len(unsubs) == 0 {
		return 0, nil
	}
	people := make([]Person, len(unsubs))
	for i, u := range unsubs {
		people[i] = Person{Email: u.Email}
	}
	owners, err := EnsurePeople(ctx, tx, workspaceID, "prospect", "unsubscribe", people)
	if err != nil {
		return 0, err
	}
	var records, emails []string
	var ats []time.Time
	seen := map[string]bool{}
	for _, u := range unsubs {
		e := strings.ToLower(strings.TrimSpace(u.Email))
		r, ok := owners[e]
		if !ok || seen[e] {
			continue
		}
		seen[e] = true
		at := u.At
		if at.IsZero() {
			at = time.Now()
		}
		records, emails, ats = append(records, r), append(emails, e), append(ats, at)
	}
	if _, err := tx.Exec(ctx, `UPDATE suppressions s SET record_id = t.r FROM unnest($2::text[], $3::uuid[]) AS t(e, r)
		WHERE s.workspace_id = $1 AND s.kind = 'email' AND s.value = t.e`, workspaceID, emails, records); err != nil {
		return 0, err
	}
	// A withdrawal is skipped when the person's latest decision is already
	// one, or a grant made after this unsubscribe (an older unsubscribe
	// recorded late never overrides it).
	rows, err := tx.Query(ctx, `
		INSERT INTO consents (workspace_id, record_id, channel, topic, purpose, status, source, evidence, occurred_at, request_id)
		SELECT DISTINCT ON (t.r) $1, t.r, 'email', '', 'marketing', 'withdrawn', $5, jsonb_build_object('email', t.e, 'via', 'unsubscribe'), t.at, $6
		FROM unnest($2::uuid[], $3::text[], $4::timestamptz[]) AS t(r, e, at)
		WHERE NOT EXISTS (
			SELECT 1 FROM (SELECT c.status, c.occurred_at FROM consents c
				WHERE c.workspace_id = $1 AND c.record_id = t.r AND c.channel = 'email' AND c.topic = '' AND c.purpose = 'marketing'
				ORDER BY c.occurred_at DESC, c.created_at DESC LIMIT 1) last
			WHERE last.status = 'withdrawn' OR last.occurred_at > t.at)
		ORDER BY t.r, t.at DESC
		RETURNING record_id::text`, workspaceID, records, emails, ats, source, requestID)
	if err != nil {
		return 0, err
	}
	var withdrawn []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		withdrawn = append(withdrawn, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// One audit event per person for an ordinary unsubscribe; one for the
	// lot when an import brings many.
	if len(withdrawn) > 20 {
		return len(withdrawn), audit.Record(ctx, tx, audit.Event{WorkspaceID: workspaceID, Action: "consent.bulk_withdrawn", TargetType: "workspace", TargetID: workspaceID,
			RequestID: requestID, ActorType: "system", Data: map[string]any{"channel": "email", "purpose": "marketing", "source": source, "count": len(withdrawn)}})
	}
	for _, id := range withdrawn {
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: workspaceID, Action: "consent.withdrawn", TargetType: "record", TargetID: id,
			RequestID: requestID, ActorType: "system", Data: map[string]any{"channel": "email", "purpose": "marketing", "topic": false, "source": source}}); err != nil {
			return 0, err
		}
	}
	return len(withdrawn), nil
}

// OptedIn clears the unsubscribe suppressions of a person who has just
// granted consent to all marketing email themselves, so cold and
// lifecycle email can reach them again. Grants from imports, CRM syncs
// and the system never do. Bounces, complaints and manual blocks stay.
// Call it after recording a grant; it reports how many it cleared.
func OptedIn(ctx context.Context, q Querier, workspaceID, recordID, channel, topic, purpose, status, source, requestID string) (int, error) {
	if status != Granted || channel != "email" || topic != "" || (purpose != "" && purpose != "marketing") || !optedIn(source) {
		return 0, nil
	}
	tx, ok := q.(pgx.Tx)
	if !ok {
		return 0, nil
	}
	rows, err := tx.Query(ctx, `DELETE FROM suppressions s USING record_identities i
		WHERE s.workspace_id = $1 AND s.kind = 'email' AND s.reason = 'unsubscribed'
		  AND i.workspace_id = $1 AND i.record_id = $2 AND i.namespace = 'email' AND i.valid_to IS NULL AND s.value = i.value
		RETURNING s.id::text, s.value`, workspaceID, recordID)
	if err != nil {
		return 0, err
	}
	type cleared struct{ id, value string }
	var list []cleared
	for rows.Next() {
		var c cleared
		if err := rows.Scan(&c.id, &c.value); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, c := range list {
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: workspaceID, Action: "suppression.removed", TargetType: "suppression", TargetID: c.id,
			RequestID: requestID, ActorType: "system", Data: map[string]any{"kind": "email", "reason": "unsubscribed", "via": "opted_in", "source": source, "record_id": recordID}}); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}

// linkUnsubscribes gives older unsubscribe suppressions their person and
// withdrawn consent, up to limit. It reports whether there may be more.
func linkUnsubscribes(ctx context.Context, tx pgx.Tx, workspaceID string, limit int) (bool, error) {
	rows, err := tx.Query(ctx, `SELECT value, created_at FROM suppressions
		WHERE workspace_id = $1 AND record_id IS NULL AND kind = 'email' AND reason = 'unsubscribed'
		ORDER BY id LIMIT $2 FOR UPDATE SKIP LOCKED`, workspaceID, limit)
	if err != nil {
		return false, err
	}
	var unsubs []Unsubscribe
	for rows.Next() {
		var u Unsubscribe
		if err := rows.Scan(&u.Email, &u.At); err != nil {
			rows.Close()
			return false, err
		}
		unsubs = append(unsubs, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	if _, err := WithdrawEmails(ctx, tx, workspaceID, "suppression", "", unsubs); err != nil {
		return false, err
	}
	return len(unsubs) == limit, nil
}
