-- Exclusions: who a journey, a broadcast or a sequence leaves out
-- (customers.Exclusion): people on any of its lists (smart or static), and
-- people with any of its tags or at a company with one. {"lists": [],
-- "tags": []}; empty leaves nobody out.
ALTER TABLE journeys ADD COLUMN exclude jsonb NOT NULL DEFAULT '{"lists": [], "tags": []}';
ALTER TABLE journey_versions ADD COLUMN exclude jsonb NOT NULL DEFAULT '{"lists": [], "tags": []}';
ALTER TABLE email_broadcasts ADD COLUMN exclude jsonb NOT NULL DEFAULT '{"lists": [], "tags": []}';
ALTER TABLE sequences ADD COLUMN exclude jsonb NOT NULL DEFAULT '{"lists": [], "tags": []}';

-- Versions kept before this carry their journey's (empty) exclusion.
UPDATE journey_versions v SET exclude = j.exclude FROM journeys j WHERE j.id = v.journey_id;
