package api

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/inbox"
)

// Sales conversations and the CRM describe the same people, so what happens
// in a Sales conversation shows on the person: whoever takes or is handed a
// conversation becomes the person's Owner, and confirming a customer wins
// their deal. Both are written to the record's own history, as the Sales
// teammate who did it.

// salesDeal is where a confirmed customer's deal goes: the brand's own deal
// type, moved to Won.
var salesDeal = inbox.DealRule{Type: "deal", Field: "stage", Stage: "won", Closed: []string{"won", "lost"}}

// salesSetOwner makes owner the Owner of the Sales conversation's person.
// It does nothing when they already are, when the brand's people have no
// owner field, or when the person was merged or deleted meanwhile.
func salesSetOwner(ctx context.Context, tx pgx.Tx, a *access, recordID, owner string) error {
	return salesWriteOwner(ctx, tx, a, recordID, owner, "")
}

// salesClearOwner empties the person's Owner, but only when it is still
// the given teammate: someone else's ownership is not undone by a release.
func salesClearOwner(ctx context.Context, tx pgx.Tx, a *access, recordID, only string) error {
	return salesWriteOwner(ctx, tx, a, recordID, "", only)
}

// salesWriteOwner sets the Owner, or clears it when owner is empty. With only
// set, it acts just when the current Owner is that teammate.
func salesWriteOwner(ctx context.Context, tx pgx.Tx, a *access, recordID, owner, only string) error {
	rec, err := lockRecord(ctx, tx, a, recordID)
	var conflict recordConflict
	if errors.Is(err, errNotFound) || errors.Is(err, errSharedRecord) || errors.As(err, &conflict) {
		return nil
	}
	if err != nil {
		return err
	}
	cur, _ := rec.Attributes["owner"].(string)
	if cur == owner || only != "" && cur != only {
		return nil
	}
	c, err := loadCatalog(ctx, tx, a.WorkspaceID, deref(rec.BrandID), false)
	if err != nil {
		return err
	}
	t := c.recordType(rec.Type)
	if t == nil || t.field("owner") == nil {
		return nil
	}
	if rec.Attributes == nil {
		rec.Attributes = map[string]any{}
	}
	if rec.AttributeSources == nil {
		rec.AttributeSources = map[string]string{}
	}
	value := json.RawMessage("null")
	if owner != "" {
		value, _ = json.Marshal(owner)
	}
	changed, err := applyAttributes(ctx, tx, c, t, a, &rec, map[string]json.RawMessage{"owner": value}, "manual")
	if err != nil || !changed {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE records SET attributes = $2, attribute_sources = $3, version = version + 1, updated_at = now() WHERE id = $1`,
		rec.ID, rec.Attributes, rec.AttributeSources); err != nil {
		return err
	}
	return record(ctx, tx, &a.principal, a.WorkspaceID, "record.updated", "record", rec.ID, map[string]any{"type": rec.Type, "via": "sales"})
}

// salesWinDeal moves the person's open deal to Won, or opens one there when
// they have none. A brand whose deals no longer have that shape is left
// alone: confirming the customer still succeeds.
func salesWinDeal(ctx context.Context, tx pgx.Tx, a *access, recordID string) error {
	c, err := loadCatalog(ctx, tx, a.WorkspaceID, a.Brand, false)
	if err != nil {
		return err
	}
	shape, err := checkDealRule(c, salesDeal)
	if err != nil {
		return nil
	}
	_, err = moveReplyDeal(ctx, tx, c, shape, a, salesDeal, recordID, "sales")
	return err
}
