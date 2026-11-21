-- Page checks (plans/ad-tracker.md, B8): a worker asks each lander and
-- offer in use for its page, so the brand hears when a page it pays to
-- send clicks to stops answering (the tracker.link_broken alert). Landers
-- are asked about once an hour. Offers are asked every six hours, and each
-- has a switch, because a network's offer link counts a request as a
-- click.
ALTER TABLE tracker_landers
    -- '' until first checked; ok; failing (one failed check); broken (two
    -- or more in a row); refused (the page turns automated requests away,
    -- so it can't be checked). The engine checks the names.
    ADD COLUMN check_status text NOT NULL DEFAULT '' CHECK (length(check_status) <= 20),
    -- Why the last check failed or was refused: http_404, dns, unreachable,
    -- tls, timeout, bad_url and the like; '' when it answered.
    ADD COLUMN check_code text NOT NULL DEFAULT '' CHECK (length(check_code) <= 40),
    ADD COLUMN check_failures integer NOT NULL DEFAULT 0 CHECK (check_failures >= 0),
    ADD COLUMN checked_at timestamptz,
    -- When the run of failed checks began; null while it answers.
    ADD COLUMN broken_since timestamptz;

CREATE INDEX tracker_landers_check_idx ON tracker_landers (checked_at NULLS FIRST) WHERE status = 'active';

-- The same for offers, with the switch: off, the offer is never asked.
ALTER TABLE tracker_offers
    ADD COLUMN check_enabled boolean NOT NULL DEFAULT true,
    ADD COLUMN check_status text NOT NULL DEFAULT '' CHECK (length(check_status) <= 20),
    ADD COLUMN check_code text NOT NULL DEFAULT '' CHECK (length(check_code) <= 40),
    ADD COLUMN check_failures integer NOT NULL DEFAULT 0 CHECK (check_failures >= 0),
    ADD COLUMN checked_at timestamptz,
    ADD COLUMN broken_since timestamptz;

CREATE INDEX tracker_offers_check_idx ON tracker_offers (checked_at NULLS FIRST) WHERE status = 'active' AND check_enabled;
