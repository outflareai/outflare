-- A site that forwards tracking through its own domain can give its proxy
-- a secret, sent with each forwarded request, so the visitor's address the
-- proxy passes on is trusted. Only the hash is kept; the secret is shown
-- once.
ALTER TABLE site_keys ADD COLUMN proxy_secret_hash bytea;
ALTER TABLE site_keys ADD COLUMN proxy_secret_at timestamptz;
