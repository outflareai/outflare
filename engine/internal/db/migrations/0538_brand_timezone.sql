-- The time zone a brand reports in (plans/ad-tracker.md, B4a): reports
-- bucket days and hours in it, and tracker caps reset at its midnight.
-- Stored times stay in UTC; this only changes how they are read. An IANA
-- zone name, checked by the server; UTC by default, as before.
ALTER TABLE brands ADD COLUMN timezone text NOT NULL DEFAULT 'UTC' CHECK (length(timezone) BETWEEN 1 AND 64);
