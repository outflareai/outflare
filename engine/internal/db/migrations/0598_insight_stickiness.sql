-- Saved insights can be stickiness questions: of the people who did
-- something in the dates, how many did it on one day, two days, three
-- and so on. Widens the kind check; nothing else changes, so the
-- previous version keeps running beside this one.
ALTER TABLE insights DROP CONSTRAINT insights_kind_check;
ALTER TABLE insights ADD CONSTRAINT insights_kind_check CHECK (kind IN ('funnel', 'trend', 'retention', 'paths', 'lifecycle', 'stickiness'));
