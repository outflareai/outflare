-- When each domain was first registered, from the public registration
-- record (RDAP): the brand's own SEO sites and its competitors, for the
-- domain age on the Competition overview. Read when a competitor is looked
-- at (crawl.Glance), at most twice a year. Only adds, so the version
-- before still runs.
ALTER TABLE seo_competitors ADD COLUMN registered_on date, ADD COLUMN registered_checked_at timestamptz;
ALTER TABLE seo_sites ADD COLUMN registered_on date, ADD COLUMN registered_checked_at timestamptz;
