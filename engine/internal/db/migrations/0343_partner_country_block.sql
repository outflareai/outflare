-- The countries a partner program doesn't credit (ISO codes, empty is
-- none), and where a visit's country came from: 'proxy' (the site's own
-- proxy said it), 'ip' (the engine's IP database) or 'timezone' (the
-- browser's time zone; '' on visits from before this). Only proxy and ip
-- countries are trusted to block. The inbox keeps a batch's country until a
-- worker stores it; the address itself is never stored.
ALTER TABLE partner_programs
    ADD COLUMN blocked_countries text[] NOT NULL DEFAULT '{}';

ALTER TABLE site_sessions
    ADD COLUMN country_source text NOT NULL DEFAULT ''
        CHECK (country_source IN ('', 'timezone', 'ip', 'proxy'));

ALTER TABLE site_event_inbox
    ADD COLUMN country        text NOT NULL DEFAULT '' CHECK (country ~ '^([A-Z]{2})?$'),
    ADD COLUMN country_source text NOT NULL DEFAULT ''
        CHECK (country_source IN ('', 'ip', 'proxy'));
