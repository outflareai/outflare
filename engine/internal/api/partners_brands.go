package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// Partner routes serve brand-limited people and tokens. A program belongs
// to a brand or is shared by every brand, and its partners, rewards and
// payouts follow it. A brand-limited caller works in its brands' programs,
// reads shared ones (like every shared record), and gets a 404 for a
// program of another brand.

// partnerBrandAccess checks a brand-limited caller against a program's
// brand: errNotFound when it can't see the program, errDenied when the
// request would change a program it may only read.
func partnerBrandAccess(r *http.Request, a *access, brand *string) error {
	if !a.sees(brand) {
		return errNotFound
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && !a.writes(brand) {
		return errDenied(brandLimitedMessage)
	}
	return nil
}

// partnerProgramBrand is a program's brand, nil when it is shared.
func partnerProgramBrand(ctx context.Context, q pgx.Tx, ws, program string) (*string, error) {
	var brand *string
	err := q.QueryRow(ctx, `SELECT brand_id::text FROM partner_programs WHERE workspace_id = $1 AND id::text = $2`, ws, program).Scan(&brand)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound
	}
	return brand, err
}

// checkPartnerBrandWrite refuses to put a program in a brand the caller
// can't change, or to share it with every brand when it is brand-limited.
func checkPartnerBrandWrite(a *access, brand *string) error {
	if !a.writes(brand) {
		return errDenied(brandLimitedMessage)
	}
	return nil
}
