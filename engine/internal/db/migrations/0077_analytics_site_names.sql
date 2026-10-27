-- The site's name in its tool (a GA4 property's display name), so the
-- overview can tell apart sites that have no domain yet.
ALTER TABLE analytics_syncs ADD COLUMN site_name text NOT NULL DEFAULT '';
