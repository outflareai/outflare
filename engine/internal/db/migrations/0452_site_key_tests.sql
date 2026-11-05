-- No-code A/B tests: whether the website script asks for the visitor's
-- versions on its own, so a test made in Outflare's visual editor runs
-- without anyone adding data-flags to the site's tag. On by default; off
-- leaves flags to the page's own code (data-flags or outflare.flag).
ALTER TABLE site_keys ADD COLUMN tests text NOT NULL DEFAULT 'on' CHECK (tests IN ('on', 'off'));
