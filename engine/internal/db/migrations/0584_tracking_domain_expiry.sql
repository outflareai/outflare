-- Tracking domain registration (plans/ad-tracker.md, B8): when a verified
-- tracking domain's registration runs out, read from its public
-- registration record (RDAP, no key), so its brand is told before the
-- links on it stop working (the tracking_domain.expiring alert).
ALTER TABLE tracking_domains
    -- When the registration of the domain's registrable part runs out;
    -- null until read, and when the registry gives no date.
    ADD COLUMN registration_expires_at timestamptz,
    -- When it was last read (or a failed read is next due a week on).
    ADD COLUMN registration_checked_at timestamptz;

CREATE INDEX tracking_domains_registration_check_idx ON tracking_domains (registration_checked_at NULLS FIRST) WHERE status = 'verified';
