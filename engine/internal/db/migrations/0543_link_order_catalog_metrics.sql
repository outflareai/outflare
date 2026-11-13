-- Link orders catalog: first-class fields for who sells a site and what it
-- is about, how old the site is, and the full set of metrics a broker's
-- export carries.
--
-- intermediary is the broker or contact a site is bought through (searched
-- on the Catalog page). theme is the site's own theme, kept apart from the
-- niches it is bought for. site_since is the date the site has existed
-- since; an import of "2012" or "12 years" stores the first day of that
-- year. metrics holds the intermediary's metrics that are only shown, not
-- filtered on (referring domains, backlinks, keywords, outgoing links and
-- so on) as one JSON object; DR and traffic keep their own columns.
--
-- Every column has a default, so the previous release keeps writing rows
-- without them. Only adds, so the version before still works.
ALTER TABLE coverage_donor_profiles
    ADD COLUMN intermediary text NOT NULL DEFAULT '' CHECK (length(intermediary) <= 500),
    ADD COLUMN theme text NOT NULL DEFAULT '' CHECK (length(theme) <= 200),
    ADD COLUMN site_since date,
    ADD COLUMN metrics jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metrics) = 'object');

CREATE INDEX coverage_donor_profiles_intermediary_idx
    ON coverage_donor_profiles (workspace_id, brand_id, lower(intermediary))
    WHERE intermediary <> '';
