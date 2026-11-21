-- The last Ahrefs check of a catalog site: when it ran and how it ended.
-- The check reads only what judges a site to buy a link on (rating,
-- worldwide traffic, referring and linked domains, the three pages with
-- the most traffic) into the row's own columns and metrics; this keeps
-- its outcome for the catalog and for picks.
ALTER TABLE coverage_donor_profiles
    ADD COLUMN ahrefs_checked_at timestamptz,
    ADD COLUMN ahrefs_check_status text NOT NULL DEFAULT ''
        CHECK (ahrefs_check_status IN ('', 'checked', 'no_connection', 'budget', 'failed'));
