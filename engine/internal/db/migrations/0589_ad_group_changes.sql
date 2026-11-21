-- Ad group changes (plans/ad-tracker.md, B8): an ad group (Meta's ad set)
-- is paused or resumed, and its own daily budget set, on the workspace's
-- own ad account as an ad_changes row like the others, carried out by a
-- job. The row stays on the ad group's campaign, with the ad group named
-- in its after.
--
-- Expand only (docs/migrations.md): the CHECK takes three more values and
-- the previous version keeps writing the old ones.
ALTER TABLE ad_changes DROP CONSTRAINT ad_changes_kind_check;
ALTER TABLE ad_changes ADD CONSTRAINT ad_changes_kind_check
    CHECK (kind IN ('pause', 'resume', 'budget', 'keyword_add', 'negative_add', 'keyword_split',
        'bidding', 'audience_add', 'audience_remove', 'country_add', 'country_remove',
        'keyword_pause', 'keyword_resume', 'ad_pause', 'ad_resume', 'negative_remove', 'target',
        'exclusion_add', 'exclusion_remove', 'campaign_remove', 'ad_remove', 'keyword_remove', 'bid',
        'ad_group_pause', 'ad_group_resume', 'ad_group_budget'));
