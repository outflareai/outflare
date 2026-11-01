package aiaccounts

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"slices"
	"strings"
)

var ErrInvalidBrand = errors.New("every assigned brand must belong to this workspace")

func validateBrands(ids *[]string) error {
	if ids == nil {
		return nil
	}
	if len(*ids) > 50 {
		return errors.New("at most 50 brands per account")
	}
	seen := []string{}
	for _, raw := range *ids {
		id := strings.ToLower(strings.TrimSpace(raw))
		var uuid pgtype.UUID
		if err := uuid.Scan(id); err != nil || !uuid.Valid || slices.Contains(seen, id) {
			return ErrInvalidBrand
		}
		seen = append(seen, id)
	}
	slices.Sort(seen)
	*ids = seen
	return nil
}

func setBrands(ctx context.Context, tx pgx.Tx, ws, account string, ids []string) error {
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM brands WHERE workspace_id=$1 AND id::text=ANY($2::text[])`, ws, ids).Scan(&count); err != nil {
		return err
	}
	if count != len(ids) {
		return ErrInvalidBrand
	}
	if _, err := tx.Exec(ctx, `DELETE FROM ai_account_brands WHERE workspace_id=$1 AND account_id::text=$2`, ws, account); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO ai_account_brands(workspace_id,account_id,brand_id)
 SELECT $1,a.id,b.id FROM ai_accounts a CROSS JOIN brands b
 WHERE a.workspace_id=$1 AND a.id::text=$2 AND b.workspace_id=$1 AND b.id::text=ANY($3::text[])`, ws, account, ids)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != int64(len(ids)) {
		return ErrInvalidBrand
	}
	return nil
}
