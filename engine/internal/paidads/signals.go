package paidads

import (
	"context"
	"time"
)

// Signal is one campaign's first-party numbers for one day, for the rule
// engine (adsuggest): the sign-ups and first-time payers its clicks
// brought, and the revenue the ledger ties to it. Money is in minor units
// of the brand's currency; payments in another currency are left out.
type Signal struct {
	Signups          int   `json:"signups"`
	NewPayers        int   `json:"new_payers"`
	NewRevenue       int64 `json:"new_revenue"`
	ReturningRevenue int64 `json:"returning_revenue"`
	LifetimeRevenue  int64 `json:"lifetime_revenue"`
}

// Daily reads the signals of the campaigns in entityIDs (ad_entities ids)
// for every day from from to to (dates, inclusive), as
// map[entity id]map["2026-01-31"]Signal. Days with nothing are absent.
func Daily(ctx context.Context, q Querier, brandID string, entityIDs []string, from, to time.Time) (map[string]map[string]Signal, error) {
	out := map[string]map[string]Signal{}
	if len(entityIDs) == 0 {
		return out, nil
	}
	var currency string
	if err := q.QueryRow(ctx, `SELECT COALESCE((SELECT currency FROM ad_brand_economics WHERE brand_id = $1), (SELECT currency FROM brands WHERE id = $1), 'USD')`, brandID).Scan(&currency); err != nil {
		return nil, err
	}
	f, t := from.UTC().Format(time.DateOnly), to.UTC().Format(time.DateOnly)
	rows, err := q.Query(ctx, `
		SELECT x.entity_id::text, to_char(x.day, 'YYYY-MM-DD'), sum(x.signups)::int, sum(x.payers)::int, sum(x.new_rev)::bigint, sum(x.ret_rev)::bigint, sum(x.life_rev)::bigint FROM (
			SELECT a.campaign_entity_id AS entity_id, (a.signed_up_at AT TIME ZONE 'UTC')::date AS day, 1 AS signups, 0 AS payers, 0 AS new_rev, 0 AS ret_rev, 0 AS life_rev
			FROM ad_acquisitions a WHERE a.brand_id = $1 AND a.campaign_entity_id = ANY($2::uuid[]) AND a.signed_up_at >= $3::date AND a.signed_up_at < $4::date + 1
			UNION ALL
			SELECT a.campaign_entity_id, (a.first_paid_at AT TIME ZONE 'UTC')::date, 0, 1, 0, 0, 0
			FROM ad_acquisitions a WHERE a.brand_id = $1 AND a.campaign_entity_id = ANY($2::uuid[]) AND a.first_paid_at >= $3::date AND a.first_paid_at < $4::date + 1
			UNION ALL
			SELECT a.campaign_entity_id, (r.occurred_at AT TIME ZONE 'UTC')::date, 0, 0, CASE WHEN r.bucket = 'new' THEN r.value_minor ELSE 0 END, 0, r.value_minor
			FROM ad_revenue r JOIN ad_acquisitions a ON a.id = r.acquisition_id
			WHERE a.brand_id = $1 AND a.campaign_entity_id = ANY($2::uuid[]) AND r.currency = $5 AND r.occurred_at >= $3::date AND r.occurred_at < $4::date + 1
			UNION ALL
			SELECT r.touch_campaign_entity_id, (r.occurred_at AT TIME ZONE 'UTC')::date, 0, 0, 0, r.value_minor, 0
			FROM ad_revenue r WHERE r.brand_id = $1 AND r.bucket = 'returning' AND r.touch_campaign_entity_id = ANY($2::uuid[]) AND r.currency = $5
			  AND r.occurred_at >= $3::date AND r.occurred_at < $4::date + 1
		) x GROUP BY 1, 2`, brandID, entityIDs, f, t, currency)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, day string
		var s Signal
		if err := rows.Scan(&id, &day, &s.Signups, &s.NewPayers, &s.NewRevenue, &s.ReturningRevenue, &s.LifetimeRevenue); err != nil {
			return nil, err
		}
		if out[id] == nil {
			out[id] = map[string]Signal{}
		}
		out[id][day] = s
	}
	return out, rows.Err()
}
