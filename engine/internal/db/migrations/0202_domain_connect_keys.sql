-- Outflare's key for signing Domain Connect apply links (the one-off
-- "apply these records?" screen at a DNS provider). The private half is
-- sealed with OUTFLARE_SECRET_KEY; the public half is published in DNS at
-- <id>.outflare.ai. The server makes one on first use.
CREATE TABLE IF NOT EXISTS domain_connect_keys (
    id          text PRIMARY KEY,
    private_key bytea NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
