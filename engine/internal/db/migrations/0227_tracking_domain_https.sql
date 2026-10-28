-- HTTPS for tracking domains, when the server has a certificate provider
-- (Outflare Cloud asks Cloudflare for one per domain). Null when there is
-- none: the operator gives the domain its certificate on their own proxy.
-- Until a provider's certificate is active, links keep the server's own
-- address.
ALTER TABLE tracking_domains ADD COLUMN https_status text CHECK (https_status IN ('pending', 'active', 'failed'));
ALTER TABLE tracking_domains ADD COLUMN https_error text NOT NULL DEFAULT '';
