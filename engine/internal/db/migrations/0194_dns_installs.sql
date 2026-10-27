-- The last one-click run that added a site's proof or a tracking domain's
-- CNAME through the workspace's own DNS host (Cloudflare).
ALTER TABLE seo_sites ADD COLUMN IF NOT EXISTS dns_install jsonb;
ALTER TABLE tracking_domains ADD COLUMN IF NOT EXISTS dns_install jsonb;
