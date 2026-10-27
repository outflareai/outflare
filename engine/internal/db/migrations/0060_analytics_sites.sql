-- One analytics connection can read many sites (every GA4 property or
-- PostHog project a login sees), so the sync keeps one row per site.
-- Rows with site '' record a run that failed before any site was read.
ALTER TABLE analytics_syncs DROP CONSTRAINT analytics_syncs_pkey;
ALTER TABLE analytics_syncs ADD PRIMARY KEY (integration_id, site);
