-- Destinations: "Send a test event". A person asks for one made-up event
-- to be sent, and the destination keeps how the last test went, apart
-- from how its real events are doing.

ALTER TABLE event_destinations
    -- When a test was last asked for; a result older than this is still
    -- on its way.
    ADD COLUMN test_requested_at timestamptz,
    ADD COLUMN last_test_at      timestamptz,
    ADD COLUMN last_test_ok      boolean,
    -- Why it failed, or what a passing test did; never the tool's words.
    ADD COLUMN last_test_reason  text NOT NULL DEFAULT '' CHECK (length(last_test_reason) <= 500);
