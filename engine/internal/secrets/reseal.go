package secrets

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is what ResealStored needs from a pool.
type Querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

// sealedColumn is a column of Box ciphertexts, each bound to its row's key
// column. Add new sealed columns here, so rotating the key reaches them.
type sealedColumn struct{ table, key, column string }

var sealedColumns = []sealedColumn{
	{"mailboxes", "id", "credentials"},
	{"oauth_connections", "id", "secret"},
	{"integrations", "id", "credentials"},
	{"vendor_syncs", "integration_id", "webhook_secret"},
	{"vendor_syncs", "integration_id", "push_token_sealed"},
	{"webhook_endpoints", "id", "secret"},
	{"webhook_endpoints", "id", "previous_secret"},
	{"email_providers", "id", "secrets"},
	{"connection_hooks", "integration_id", "token"},
	{"connection_hooks", "integration_id", "signing_secrets"},
	{"connection_hook_events", "id", "payload"},
	{"domain_connect_keys", "id", "private_key"},
	{"partner_memberships", "id", "payout_method"},
	{"journey_signing_secrets", "workspace_id", "secret"},
	{"journey_signing_secrets", "workspace_id", "previous_secret"},
	{"journey_step_secrets", "id", "secret"},
}

// ResealResult counts what ResealStored did.
type ResealResult struct {
	Resealed int // re-encrypted with the current key
	Current  int // already under the current key
	// Unreadable values open with none of the keys: they were sealed with
	// a key that is no longer listed.
	Unreadable int
}

// ResealStored re-encrypts every stored secret still sealed with an
// earlier key, so the earlier key can be dropped afterwards. It is safe to
// run while the server works: a row changed meanwhile is left alone.
func (b *Box) ResealStored(ctx context.Context, q Querier) (ResealResult, error) {
	var res ResealResult
	for _, c := range sealedColumns {
		// Empty values hold no secret: mailboxes sending through another
		// platform have no login, so their credentials are empty.
		rows, err := q.Query(ctx, fmt.Sprintf(`SELECT %s::text, %s FROM %s WHERE length(%s) > 0`, c.key, c.column, c.table, c.column))
		if err != nil {
			return res, fmt.Errorf("%s.%s: %w", c.table, c.column, err)
		}
		type row struct {
			key    string
			sealed []byte
		}
		all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
			var x row
			return x, r.Scan(&x.key, &x.sealed)
		})
		if err != nil {
			return res, fmt.Errorf("%s.%s: %w", c.table, c.column, err)
		}
		for _, x := range all {
			fresh, changed, err := b.Reseal(x.sealed, []byte(x.key))
			switch {
			case err != nil:
				res.Unreadable++
				continue
			case !changed:
				res.Current++
				continue
			}
			if _, err := q.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s = $2 WHERE %s::text = $1 AND %s = $3`, c.table, c.column, c.key, c.column),
				x.key, fresh, x.sealed); err != nil {
				return res, fmt.Errorf("%s.%s: %w", c.table, c.column, err)
			}
			res.Resealed++
		}
	}
	return res, nil
}
