-- Verified browser identify. A site key can have an identify secret: the
-- site's own server signs a user id with it (HMAC-SHA256), the page hands
-- the id and its signature to outflare.identify, and Outflare joins the
-- browser to that person only when the signature is right. The secret is
-- sealed with the server's key (not hashed: Outflare must compute the
-- signature itself), bound to the key's id.
ALTER TABLE site_keys ADD COLUMN identify_secret bytea;
ALTER TABLE site_keys ADD COLUMN identify_secret_at timestamptz;
