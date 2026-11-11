// Package linkorders holds the Coverage-backed records for paid link
// orders: a donor catalog profile beside each publisher, the brand's
// approver settings, and the orders themselves.
package linkorders

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DefaultEquivalentDRBand is the domain-rating band width when a brand has
// not set one (a donor rated 54 is matched with 50-59).
const DefaultEquivalentDRBand = 10

// Settings are a brand's link-order approver and equivalent-donor band.
type Settings struct {
	ApproverUserID   *string    `json:"approver_user_id"`
	EquivalentDRBand int        `json:"equivalent_dr_band"`
	UpdatedAt        *time.Time `json:"updated_at"`
	UpdatedBy        *string    `json:"updated_by"`
}

// Querier is the database a settings read or write needs.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// LoadSettings returns the brand's settings, or the defaults when no row
// has been saved yet. It does not insert a row.
func LoadSettings(ctx context.Context, q Querier, workspaceID, brandID string) (Settings, error) {
	v := Settings{EquivalentDRBand: DefaultEquivalentDRBand}
	err := q.QueryRow(ctx, `SELECT approver_user_id::text, equivalent_dr_band, updated_at, updated_by::text
		FROM coverage_link_order_settings
		WHERE workspace_id = $1 AND brand_id = $2`, workspaceID, brandID).
		Scan(&v.ApproverUserID, &v.EquivalentDRBand, &v.UpdatedAt, &v.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{EquivalentDRBand: DefaultEquivalentDRBand}, nil
	}
	return v, err
}

// SaveSettings writes the brand's settings, creating the row on first save.
func SaveSettings(ctx context.Context, q Querier, workspaceID, brandID string, v Settings, actorID *string) error {
	_, err := q.Exec(ctx, `INSERT INTO coverage_link_order_settings (
			workspace_id, brand_id, approver_user_id, equivalent_dr_band, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (workspace_id, brand_id) DO UPDATE SET
			approver_user_id = EXCLUDED.approver_user_id,
			equivalent_dr_band = EXCLUDED.equivalent_dr_band,
			updated_by = EXCLUDED.updated_by,
			updated_at = now()`,
		workspaceID, brandID, v.ApproverUserID, v.EquivalentDRBand, actorID)
	return err
}
