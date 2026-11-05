-- Targeting and bidding (step 5 of the paid ads plan): Outflare can switch
-- how a campaign bids, add or take out a customer list audience, and add
-- or take out a country, as changes a person approves; and suggest a
-- target cost per conversion once a campaign has the conversions for one
-- (bidding_target).
--
-- Expand only (docs/migrations.md): each kind CHECK takes more values. The
-- previous version writes only the old ones.

ALTER TABLE ad_changes DROP CONSTRAINT ad_changes_kind_check;
ALTER TABLE ad_changes ADD CONSTRAINT ad_changes_kind_check
    CHECK (kind IN ('pause', 'resume', 'budget', 'keyword_add', 'negative_add', 'keyword_split',
                    'bidding', 'audience_add', 'audience_remove', 'country_add', 'country_remove'));

ALTER TABLE ad_suggestions DROP CONSTRAINT ad_suggestions_kind_check;
ALTER TABLE ad_suggestions ADD CONSTRAINT ad_suggestions_kind_check
    CHECK (kind IN ('budget_up', 'budget_down', 'pause', 'resume', 'keyword_add', 'negative_add', 'keyword_split', 'bidding_target'));
