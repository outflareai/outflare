package api

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/crmmail"
)

// Capturing every contact: with the brand's capture_all_contacts on, each
// enabled person's Gmail sync saves their mail with any of the brand's CRM
// contacts and companies, not only the ones they picked. Every filter
// still applies (internal-only and mixed threads, blocked and private
// senders, private labels), and a message is saved only when all of its
// outside participants belong to one record: the contact whose address it
// is, or else the company whose domain it is. Mail that fits two contacts,
// or two companies, isn't saved.

// mailMatchLimit caps the records one message is checked against.
const mailMatchLimit = 20

// crmCaptureAllChanged records an owner switching capture of every contact
// and syncs every connected mailbox of the brand again from the start.
func (s *Server) crmCaptureAllChanged(ctx context.Context, tx pgx.Tx, a *access, on bool) error {
	rows, err := tx.Query(ctx, `UPDATE crm_google_connections SET version=version+1,last_synced_at=NULL,updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND status='connected' RETURNING id::text`, a.WorkspaceID, a.Brand)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.enqueueCRMGoogle(ctx, tx, a.WorkspaceID, id, time.Now(), "capture_all:"+newID()); err != nil {
			return err
		}
	}
	return record(ctx, tx, &a.principal, a.WorkspaceID, "crm_mail.capture_all_changed", "crm_mail_policy", a.Brand,
		map[string]any{"brand_id": a.Brand, "on": on, "mailboxes": len(ids)})
}

// matchMailRecord is the one contact or company of the brand a message
// belongs to, or "" when none or more than one fits. base carries the
// policies and members; its record fields are ignored.
func matchMailRecord(ctx context.Context, q mailRowsQuery, ws, brand string, base mailContext, m crmmail.Message) (string, error) {
	external := crmmail.External(m, base.brand, base.members)
	if len(external) == 0 {
		return "", nil
	}
	var domains []string
	for _, p := range external {
		domains = append(domains, crmmail.CaptureDomains(p)...)
	}
	rows, err := q.Query(ctx, `SELECT DISTINCT r.id::text, r.record_type FROM record_identities i
		JOIN records r ON r.id=i.record_id AND r.workspace_id=i.workspace_id
		WHERE i.workspace_id=$1 AND r.brand_id=$2 AND r.record_type IN ('person','account')
		AND r.deleted_at IS NULL AND r.merged_into IS NULL AND r.archived_at IS NULL
		AND (i.valid_to IS NULL OR i.valid_to>now())
		AND ((i.namespace='email' AND lower(i.value)=ANY($3)) OR (i.namespace='domain' AND lower(i.value)=ANY($4)))
		ORDER BY 1 LIMIT $5`, ws, brand, external, domains, mailMatchLimit)
	if err != nil {
		return "", err
	}
	type candidate struct{ id, kind string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (candidate, error) {
		var c candidate
		return c, r.Scan(&c.id, &c.kind)
	})
	if err != nil {
		return "", err
	}
	fits := map[string][]string{}
	for _, c := range list {
		rc, err := base.forRecord(ctx, q, ws, c.id)
		if err != nil {
			return "", err
		}
		if rc.eligible(m) {
			fits[c.kind] = append(fits[c.kind], c.id)
		}
	}
	// The contact whose address it is wins over their company.
	for _, kind := range []string{"person", "account"} {
		switch len(fits[kind]) {
		case 0:
			continue
		case 1:
			return fits[kind][0], nil
		default:
			return "", nil
		}
	}
	return "", nil
}
