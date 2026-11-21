-- Bid changes (plans/ad-tracker.md, B8): a campaign's bid, or the bid of
-- one of its placements (a Zeropark target), is set on the workspace's own
-- ad account as an ad_changes row like the others, carried out by a job.
--
-- Expand only (docs/migrations.md): the CHECK takes one more value and the
-- previous version keeps writing the old ones.
ALTER TABLE ad_changes DROP CONSTRAINT ad_changes_kind_check;
ALTER TABLE ad_changes ADD CONSTRAINT ad_changes_kind_check
    CHECK (kind IN ('pause', 'resume', 'budget', 'keyword_add', 'negative_add', 'keyword_split',
        'bidding', 'audience_add', 'audience_remove', 'country_add', 'country_remove',
        'keyword_pause', 'keyword_resume', 'ad_pause', 'ad_resume', 'negative_remove', 'target',
        'exclusion_add', 'exclusion_remove', 'campaign_remove', 'ad_remove', 'keyword_remove', 'bid'));
