-- Google Ads Required Minimum Functionality (docs/paid-media.md): what the
-- reports must show beside what the sync already stores, and the changes
-- that remove a campaign, an ad or a keyword.
--
-- All conversions (R.10, R.20): every conversion the platform counted,
-- secondary actions included, beside the primary ones in conversions.
-- Keyword bid estimates (R.50): what Google estimates a keyword must bid
-- to show on the first page, at the top of the page and in the first
-- position, and its quality score (1 to 10), as of the last sync. Search
-- term match type (R.70): how the search matched the keyword (exact, near
-- exact, phrase, near phrase, broad), the one with the most impressions
-- when Google splits a search between several.
--
-- Removing (M.110, M.130, M.140) cannot be undone on the platform, so a
-- person asks for it on their own session; it is an ad_changes row like a
-- pause.
--
-- Expand only (docs/migrations.md): new columns are nullable or have a
-- default, and the CHECK takes more values; the previous version keeps
-- writing what it wrote.
ALTER TABLE ad_metrics_daily
    ADD COLUMN all_conversions             numeric(18, 4),
    ADD COLUMN all_conversion_value_micros bigint;

ALTER TABLE ad_keywords
    ADD COLUMN first_page_cpc_micros     bigint CHECK (first_page_cpc_micros >= 0),
    ADD COLUMN top_of_page_cpc_micros    bigint CHECK (top_of_page_cpc_micros >= 0),
    ADD COLUMN first_position_cpc_micros bigint CHECK (first_position_cpc_micros >= 0),
    ADD COLUMN quality_score             smallint CHECK (quality_score BETWEEN 1 AND 10);

ALTER TABLE ad_search_terms_daily
    ADD COLUMN search_term_match_type text NOT NULL DEFAULT '' CHECK (length(search_term_match_type) <= 30);

ALTER TABLE ad_changes DROP CONSTRAINT ad_changes_kind_check;
ALTER TABLE ad_changes ADD CONSTRAINT ad_changes_kind_check
    CHECK (kind IN ('pause', 'resume', 'budget', 'keyword_add', 'negative_add', 'keyword_split',
        'bidding', 'audience_add', 'audience_remove', 'country_add', 'country_remove',
        'keyword_pause', 'keyword_resume', 'ad_pause', 'ad_resume', 'negative_remove', 'target',
        'exclusion_add', 'exclusion_remove', 'campaign_remove', 'ad_remove', 'keyword_remove'));
