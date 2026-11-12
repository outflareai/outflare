-- Paid ads intelligence layer (plans/paid-ads/plan.md, verdicts): the
-- findings queue applies and reverts more kinds of change than the first
-- campaign calls did. Each is an ad_changes row like the others, carried
-- out by a job and read back: pausing or resuming a keyword or an ad,
-- taking a negative keyword out again, setting a campaign's target (tCPA
-- or tROAS), and adding or removing an exclusion (a device, a place, a
-- placement, a network, a schedule window, a brand).
--
-- Expand only (docs/migrations.md): the CHECK takes more values and the
-- previous version keeps writing the old ones.
ALTER TABLE ad_changes DROP CONSTRAINT ad_changes_kind_check;
ALTER TABLE ad_changes ADD CONSTRAINT ad_changes_kind_check
    CHECK (kind IN ('pause', 'resume', 'budget', 'keyword_add', 'negative_add', 'keyword_split',
        'bidding', 'audience_add', 'audience_remove', 'country_add', 'country_remove',
        'keyword_pause', 'keyword_resume', 'ad_pause', 'ad_resume', 'negative_remove', 'target',
        'exclusion_add', 'exclusion_remove'));
