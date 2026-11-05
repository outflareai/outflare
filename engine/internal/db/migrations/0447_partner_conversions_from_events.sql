-- Partner programs read the events a workspace already sends: a sign-up
-- or a program's own conversion event credits the partner without a call
-- to the partner conversions API. Those conversions have source events.
ALTER TABLE partner_conversions DROP CONSTRAINT partner_conversions_source_check,
    ADD CONSTRAINT partner_conversions_source_check CHECK (source IN ('api', 'postback', 'billing', 'manual', 'import', 'browser', 'events'));
