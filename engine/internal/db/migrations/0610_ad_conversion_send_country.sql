-- Each conversion send remembers the country its consent decision was made
-- on and where that country came from, so the send log can show why a
-- conversion was sent or held: visit (the person's latest website visit),
-- event (the event's own context), earlier_event (their most recent event
-- that carried a country or an IP address), record (the country on their
-- customer record), or '' when nothing said.
-- Expand only (docs/migrations.md): new columns with defaults, so the
-- version before this one still runs on this schema.
ALTER TABLE ad_conversion_sends ADD COLUMN country text NOT NULL DEFAULT '';
ALTER TABLE ad_conversion_sends ADD COLUMN country_source text NOT NULL DEFAULT '' CHECK (country_source IN ('', 'visit', 'event', 'earlier_event', 'record'));

-- A third consent mode, collected: the brand says its own site already
-- collects consent for ad data from everyone, so conversions go for
-- everyone. Expand only (docs/migrations.md): the CHECK takes one more
-- value, and the previous version reads it as opt_out.
ALTER TABLE ad_settings DROP CONSTRAINT ad_settings_consent_mode_check;
ALTER TABLE ad_settings ADD CONSTRAINT ad_settings_consent_mode_check
    CHECK (consent_mode IN ('opt_out', 'opt_in', 'collected'));
