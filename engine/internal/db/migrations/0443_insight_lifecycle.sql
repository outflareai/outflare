-- Saved insights can be lifecycle questions: in each period, how many
-- active people were new, returning or resurrected, and how many went
-- dormant. Widens the kind check; nothing else changes, so the previous
-- version keeps running beside this one.
ALTER TABLE insights DROP CONSTRAINT insights_kind_check;
ALTER TABLE insights ADD CONSTRAINT insights_kind_check CHECK (kind IN ('funnel', 'trend', 'retention', 'paths', 'lifecycle'));
