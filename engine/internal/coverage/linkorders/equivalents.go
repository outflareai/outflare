package linkorders

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Equivalent is a catalog donor that can stand in for the source order's
// donor: same niche intersection, same domain-rating band, and the other
// filters in ListEquivalents.
type Equivalent struct {
	Donor       Donor `json:"donor"`
	SameDonor   bool  `json:"same_donor"`
	DRBandStart int   `json:"dr_band_start"`
	DRBandEnd   int   `json:"dr_band_end"`
}

// DRBand is the inclusive range floor(dr/band)*band .. +band-1.
func DRBand(dr, band int) (start, end int) {
	if band < 1 {
		band = DefaultEquivalentDRBand
	}
	start = (dr / band) * band
	end = start + band - 1
	if end > 100 {
		end = 100
	}
	return start, end
}

// ListEquivalents returns catalog rows that can replace the order's donor.
// The current donor is first when it is still eligible. A donor with an
// open (not lost) order on the same target is excluded, except the
// source order itself so the same donor can be suggested after a loss.
func ListEquivalents(ctx context.Context, q Querier, workspaceID, brandID, orderID uuid.UUID) ([]Equivalent, error) {
	o, err := GetOrder(ctx, q, workspaceID, brandID, orderID)
	if err != nil {
		return nil, err
	}
	src, err := GetDonor(ctx, q, workspaceID, brandID, o.DonorProfileID)
	if err != nil {
		return nil, err
	}
	set, err := LoadSettings(ctx, q, workspaceID.String(), brandID.String())
	if err != nil {
		return nil, err
	}
	band := set.EquivalentDRBand
	if band < 1 {
		band = DefaultEquivalentDRBand
	}

	var bandStart, bandEnd *int
	if src.DomainRating != nil {
		s, e := DRBand(*src.DomainRating, band)
		bandStart, bandEnd = &s, &e
	}

	currencies, err := publishedCurrencies(ctx, q, workspaceID, brandID, src.PublisherID)
	if err != nil {
		return nil, err
	}
	niches := src.Niches
	if niches == nil {
		niches = []string{}
	}

	rows, err := q.Query(ctx, `
		SELECT p.id
		FROM coverage_donor_profiles p
		JOIN coverage_publishers pub ON pub.id = p.publisher_id
		WHERE p.workspace_id = $1 AND p.brand_id = $2 AND p.archived_at IS NULL
		  AND ($3::int IS NULL OR (p.domain_rating IS NOT NULL AND p.domain_rating BETWEEN $3 AND $4))
		  AND (
		      CARDINALITY($5::text[]) = 0
		      OR EXISTS (
		          SELECT 1 FROM unnest(p.niches) n
		          JOIN unnest($5::text[]) s ON lower(n) = lower(s)
		      )
		  )
		  AND (
		      CARDINALITY($6::text[]) = 0
		      OR EXISTS (
		          SELECT 1 FROM coverage_rates r
		          WHERE r.workspace_id = p.workspace_id AND r.brand_id = p.brand_id
		            AND r.publisher_id = p.publisher_id AND r.status = 'published'
		            AND r.currency = ANY($6)
		      )
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM coverage_link_orders o
		      WHERE o.workspace_id = p.workspace_id AND o.brand_id = p.brand_id
		        AND o.donor_profile_id = p.id AND o.status <> $7
		        AND o.target_url = $8 AND o.id <> $9
		  )
		ORDER BY
		  (p.id = $10) DESC,
		  CASE WHEN $11 THEN CASE WHEN p.pbn THEN 1 ELSE 0 END ELSE 0 END,
		  abs(coalesce(p.domain_rating, 0) - coalesce($12, 0)),
		  pub.domain`,
		workspaceID, brandID, bandStart, bandEnd, niches, currencies,
		StatusLost, o.TargetURL, o.ID, src.ID, !src.PBN, src.DomainRating)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, err
	}
	out := []Equivalent{}
	start, end := 0, 0
	if bandStart != nil {
		start, end = *bandStart, *bandEnd
	}
	for _, id := range ids {
		d, err := GetDonor(ctx, q, workspaceID, brandID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, Equivalent{
			Donor:       *d,
			SameDonor:   id == src.ID,
			DRBandStart: start,
			DRBandEnd:   end,
		})
	}
	return out, nil
}

func publishedCurrencies(ctx context.Context, q Querier, workspaceID, brandID, publisherID uuid.UUID) ([]string, error) {
	rows, err := q.Query(ctx, `
		SELECT DISTINCT currency FROM coverage_rates
		WHERE workspace_id = $1 AND brand_id = $2 AND publisher_id = $3 AND status = 'published'`,
		workspaceID, brandID, publisherID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []string{}
	}
	return list, nil
}
