-- Scheduled syncs that fail are tried again soon (engine/internal/retry:
-- 15 minutes, then 1, 2 and every 4 hours) rather than after their whole
-- interval: analytics, Search Console and page conversions (every 6 or
-- 24 hours), ads (every 6 hours) and SEO domain snapshots (weekly).
-- failures counts the runs in a row that failed; last_error_code says why
-- in a word (connector_auth, connector_quota, connector_unavailable,
-- internal ...).
ALTER TABLE analytics_syncs
    ADD COLUMN failures        int NOT NULL DEFAULT 0,
    ADD COLUMN last_error_code text;

ALTER TABLE search_syncs
    ADD COLUMN failures        int NOT NULL DEFAULT 0,
    ADD COLUMN last_error_code text;

ALTER TABLE page_conversion_syncs
    ADD COLUMN failures        int NOT NULL DEFAULT 0,
    ADD COLUMN last_error_code text;

ALTER TABLE ad_syncs
    ADD COLUMN failures        int NOT NULL DEFAULT 0,
    ADD COLUMN last_error_code text;

ALTER TABLE seo_syncs
    ADD COLUMN failures        int NOT NULL DEFAULT 0,
    ADD COLUMN last_error_code text;
