-- Two more ways to prove a site, found without adding anything: a Search
-- Console connection that owns the site, or the workspace's Outflare
-- tracking tag on its start page. The check only widens.
ALTER TABLE seo_sites DROP CONSTRAINT IF EXISTS seo_sites_verify_method_check;
ALTER TABLE seo_sites ADD CONSTRAINT seo_sites_verify_method_check
    CHECK (verify_method IN ('', 'dns', 'meta', 'file', 'test', 'search_console', 'tracking'));
