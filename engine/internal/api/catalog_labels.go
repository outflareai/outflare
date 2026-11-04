package api

import (
	"context"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Field and event labels (plans/customer-data-platform, blueprint 24.4;
// the owner's decisions of 2026-11-03, migration 0408): besides its
// sensitivity, each field and event says who owns it, how long its data is
// meant to be kept and where it may be sent. They are labels only: nothing
// reads them to block, hide or delete anything.

// labelDestinations are the places a field or event's data may be sent.
// Stored null means every one, so one added later is allowed for fields
// nobody restricted.
var labelDestinations = []string{"webhooks", "exports", "ai", "published_queries", "email"}

// maxRetentionDays is the longest retention a label can name (100 years).
const maxRetentionDays = 36500

// labelInput is the labels a create or update may set. Each null resets
// it: no owner, kept forever, every destination.
type labelInput struct {
	OwnerID       optional[string]   `json:"owner_id"`
	RetentionDays optional[int]      `json:"retention_days"`
	Destinations  optional[[]string] `json:"destinations"`
}

func (l *labelInput) set() bool { return l.OwnerID.Set || l.RetentionDays.Set || l.Destinations.Set }

func (l *labelInput) validate() error {
	if v := l.OwnerID.Value; v != nil && !validUUID(*v) {
		return errInvalid("owner_id is a team member's user id (GET /v1/workspaces/{ws}/members), or null for nobody")
	}
	if v := l.RetentionDays.Value; v != nil && (*v < 1 || *v > maxRetentionDays) {
		return errInvalid("retention_days is 1 to 36500 days, or null for forever")
	}
	if v := l.Destinations.Value; v != nil {
		seen := []string{}
		for _, d := range *v {
			if !slices.Contains(labelDestinations, d) || slices.Contains(seen, d) {
				return errInvalid("destinations lists each of " + strings.Join(labelDestinations, ", ") + " at most once, or is null for all of them")
			}
			seen = append(seen, d)
		}
	}
	return nil
}

// checkOwner fails unless owner (if set) is a member of the workspace.
func (l *labelInput) checkOwner(ctx context.Context, tx pgx.Tx, ws string) error {
	if l.OwnerID.Value == nil {
		return nil
	}
	var member bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM memberships WHERE workspace_id = $1 AND user_id = $2)`, ws, *l.OwnerID.Value).Scan(&member); err != nil {
		return err
	}
	if !member {
		return errInvalid("owner_id is not a member of this workspace")
	}
	return nil
}

// orEvery is a stored destinations list as shown: every destination when
// none was stored.
func orEvery(d []string) []string {
	if d == nil {
		return slices.Clone(labelDestinations)
	}
	return d
}

// saveFieldLabels writes a field's labels, and a built-in field's
// sensitivity, keeping what l leaves out.
func saveFieldLabels(ctx context.Context, tx pgx.Tx, ws, brand, recordType, key string, sensitivity *string, l labelInput) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO field_labels (workspace_id, brand_id, record_type, key, sensitivity, owner_id, retention_days, destinations)
		VALUES ($1, $2, $3, $4, $5, $7, $9, $11)
		ON CONFLICT (brand_id, record_type, key) DO UPDATE SET
			sensitivity = COALESCE($5, field_labels.sensitivity),
			owner_id = CASE WHEN $6 THEN $7::uuid ELSE field_labels.owner_id END,
			retention_days = CASE WHEN $8 THEN $9::int ELSE field_labels.retention_days END,
			destinations = CASE WHEN $10 THEN $11::text[] ELSE field_labels.destinations END,
			updated_at = now()`,
		ws, brand, recordType, key, sensitivity, l.OwnerID.Set, l.OwnerID.Value, l.RetentionDays.Set, l.RetentionDays.Value,
		l.Destinations.Set, l.Destinations.Value)
	return err
}

// applyFieldLabels fills in every field's labels from field_labels.
func applyFieldLabels(ctx context.Context, q pgx.Tx, ws string, c *catalog) error {
	for i := range c.Types {
		for j := range c.Types[i].Fields {
			c.Types[i].Fields[j].Destinations = orEvery(nil)
		}
	}
	if c.Brand == "" {
		return nil
	}
	rows, err := q.Query(ctx, `SELECT record_type, key, sensitivity, owner_id::text, retention_days, destinations FROM field_labels
		WHERE workspace_id = $1 AND brand_id = $2`, ws, c.Brand)
	if err != nil {
		return err
	}
	type label struct {
		recordType, key string
		sensitivity     *string
		owner           *string
		retention       *int
		destinations    []string
	}
	labels, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (label, error) {
		var l label
		return l, row.Scan(&l.recordType, &l.key, &l.sensitivity, &l.owner, &l.retention, &l.destinations)
	})
	if err != nil {
		return err
	}
	for _, l := range labels {
		t := c.recordType(l.recordType)
		if t == nil {
			continue
		}
		f := t.field(l.key)
		if f == nil {
			continue
		}
		if f.Builtin && l.sensitivity != nil {
			f.Sensitivity = *l.sensitivity
		}
		f.OwnerID, f.RetentionDays, f.Destinations = l.owner, l.retention, orEvery(l.destinations)
	}
	return nil
}
