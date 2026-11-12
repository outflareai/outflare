package api

import (
	"context"
	"math"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// Paid ads blockers on the workspace status (area ads): what stops a brand
// from being judged in cost per new paying customer, and a brand whose
// ads cost more than it allows.
//
// Over target counts the last 7 days and fires only with enough to judge
// by: at least adsOverTargetPayers new payers, or spend worth
// adsOverTargetSpendTargets targets with fewer.
const (
	adsOverTargetDays         = 7
	adsOverTargetPayers       = 5
	adsOverTargetSpendTargets = 3
)

func (s *Server) adsBlockers(ctx context.Context, ws string, brandArg *string, page func(string) string, add func(statusBlocker)) error {
	// Brands with an active ad account and no target.
	missing, nMissing, err := s.statusItems(ctx, `
		SELECT b.id::text, b.name, '', count(*) OVER ()::int FROM brands b
		WHERE b.workspace_id = $1 AND b.status = 'active' AND ($2::uuid IS NULL OR b.id = $2::uuid)
		  AND EXISTS (SELECT 1 FROM ad_accounts a WHERE a.workspace_id = $1 AND a.brand_id = b.id AND a.status = 'active')
		  AND NOT EXISTS (SELECT 1 FROM ad_brand_economics x WHERE x.brand_id = b.id AND x.target_cost_per_payer_micros IS NOT NULL)
		ORDER BY b.created_at LIMIT 5`, ws, brandArg)
	if err != nil {
		return err
	}
	if nMissing > 0 {
		add(statusBlocker{Code: "ads_economics_missing", Severity: "info", Area: "ads", Title: countOf(nMissing, "brand with ads has", "brands with ads have") + " no target cost per new customer",
			Detail: "Until a person sets what a new paying customer may cost, ads are judged on the networks' own conversions. set_ad_economics sets the target, the window and payback.",
			Count:  nMissing, Items: missing, Fix: statusFix{Tool: "set_ad_economics", Page: page("ads"), Who: "person"}})
	}
	// Brands over target: spend across the brand's campaigns over the last
	// days against the new payers the ledger ties to them.
	rows, err := s.pool.Query(ctx, `
		SELECT b.id::text, b.name, x.target_cost_per_payer_micros,
			COALESCE((SELECT sum(m.spend_micros) FROM ad_metrics_daily m JOIN ad_entities e ON e.id = m.ad_entity_id JOIN ad_accounts a ON a.id = e.ad_account_id
				WHERE m.workspace_id = $1 AND e.level = 'campaign' AND COALESCE(e.brand_id, a.brand_id) = b.id AND m.day >= current_date - $3::int), 0)::bigint,
			(SELECT count(*) FROM ad_acquisitions q WHERE q.brand_id = b.id AND q.first_paid_at >= now() - make_interval(days => $3))::int
		FROM brands b JOIN ad_brand_economics x ON x.brand_id = b.id
		WHERE b.workspace_id = $1 AND b.status = 'active' AND ($2::uuid IS NULL OR b.id = $2::uuid) AND x.target_cost_per_payer_micros IS NOT NULL
		ORDER BY b.created_at`, ws, brandArg, adsOverTargetDays)
	if err != nil {
		return err
	}
	type brandCost struct {
		id, name string
		target   int64
		spend    int64
		payers   int
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (brandCost, error) {
		var c brandCost
		return c, row.Scan(&c.id, &c.name, &c.target, &c.spend, &c.payers)
	})
	if err != nil {
		return err
	}
	var over []statusItem
	for _, c := range list {
		enough := c.payers >= adsOverTargetPayers || c.spend >= c.target*adsOverTargetSpendTargets
		if !enough || c.spend == 0 {
			continue
		}
		if c.payers > 0 && float64(c.spend)/float64(c.payers) <= float64(c.target)*(1+targetBand) {
			continue
		}
		reason := "spent " + ratio(float64(c.spend)/float64(c.target)) + " targets' worth in the last 7 days with no new customer"
		if c.payers > 0 {
			reason = "a new customer cost " + ratio(float64(c.spend)/float64(c.payers)/float64(c.target)) + " times the target over the last 7 days"
		}
		over = append(over, statusItem{ID: c.id, Name: c.name, Reason: reason})
	}
	if n := len(over); n > 0 {
		add(statusBlocker{Code: "ads_over_target", Severity: "warning", Area: "ads", Title: countOf(n, "brand's ads cost", "brands' ads cost") + " more than the target per new customer",
			Detail: "Over the last 7 days a new paying customer cost more than the brand allows. get_ads_performance shows which campaigns; the findings queue has what Outflare would change.",
			Count:  n, Items: over[:min(n, 5)], Fix: statusFix{Tool: "get_ads_performance", Page: page("ads"), Who: "person"}})
	}
	return nil
}

// ratio writes a multiple with one decimal, such as 1.4.
func ratio(v float64) string {
	return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64)
}
