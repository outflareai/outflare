-- Paid ads targets (the owner, 2026-11-03): the cost per conversion a
-- brand wants from a campaign, so the page and agents can say whether it is
-- over or under, beside how the platform itself bids.

-- Ours: set on the campaign, or on its account for every campaign without
-- one of its own. In micros of the account's currency.
ALTER TABLE ad_entities ADD COLUMN target_cost_per_conversion_micros bigint
    CHECK (target_cost_per_conversion_micros > 0);
ALTER TABLE ad_accounts ADD COLUMN target_cost_per_conversion_micros bigint
    CHECK (target_cost_per_conversion_micros > 0);

-- The platform's: how it bids (Google: target_cpa, maximize_conversions,
-- manual_cpc, ...) and the cost per conversion or return it bids to.
ALTER TABLE ad_entities ADD COLUMN bidding_strategy text NOT NULL DEFAULT '';
ALTER TABLE ad_entities ADD COLUMN platform_target_cpa_micros bigint;
ALTER TABLE ad_entities ADD COLUMN platform_target_roas double precision;
