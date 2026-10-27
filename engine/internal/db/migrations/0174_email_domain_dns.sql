-- One-click DNS for sending domains, and checks that speed up again when
-- someone has just added the records.
ALTER TABLE email_domains
    -- The last "Add in Cloudflare" run: status (queued, done, failed),
    -- integration_id, added, existing, zone, error, at.
    ADD COLUMN dns_install jsonb,
    -- When the every-minute checks last started again (Check now, a
    -- one-click install). The schedule counts from the later of this and
    -- created_at.
    ADD COLUMN checks_from timestamptz NOT NULL DEFAULT '-infinity';
