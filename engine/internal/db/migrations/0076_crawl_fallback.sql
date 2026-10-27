-- Pages of a proved site that its firewall blocked, read through a
-- scraping connector (Geonode) instead: which one, and how many per crawl.
ALTER TABLE crawl_pages
    ADD COLUMN fetched_via text NOT NULL DEFAULT '';

ALTER TABLE crawl_runs
    ADD COLUMN pages_fallback integer NOT NULL DEFAULT 0;

-- Pages a firewall blocked, read through a connector or not, and what
-- would let the next crawl read them: '' (nothing to do), 'verify_site'
-- or 'connect_geonode'. Outflare never reads them with keys of its own.
ALTER TABLE crawl_runs
    ADD COLUMN pages_firewalled integer NOT NULL DEFAULT 0,
    ADD COLUMN fallback_hint text NOT NULL DEFAULT '';
