-- Two more campaign changes: a campaign's places set together (the places
-- it shows in and the places it is kept out of, in one request the
-- platform takes whole or not at all), and a campaign renamed. Each is an
-- ad_changes row like the others, carried out by a job.
--
-- Expand only (docs/migrations.md): the CHECK takes two more values and
-- the previous version keeps writing the old ones.
ALTER TABLE ad_changes DROP CONSTRAINT ad_changes_kind_check;
ALTER TABLE ad_changes ADD CONSTRAINT ad_changes_kind_check
    CHECK (kind IN ('pause', 'resume', 'budget', 'keyword_add', 'negative_add', 'keyword_split',
        'bidding', 'audience_add', 'audience_remove', 'country_add', 'country_remove',
        'keyword_pause', 'keyword_resume', 'ad_pause', 'ad_resume', 'negative_remove', 'target',
        'exclusion_add', 'exclusion_remove', 'campaign_remove', 'ad_remove', 'keyword_remove', 'bid',
        'ad_group_pause', 'ad_group_resume', 'ad_group_budget', 'locations', 'rename'));
