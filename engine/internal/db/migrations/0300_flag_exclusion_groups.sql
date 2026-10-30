-- Feature flags in an exclusion group split the group's visitors: each owns
-- the buckets group_from to group_to (of 10,000) of a hash of the group's
-- name and the visitor's id, and is off for everyone else, so no visitor is
-- in two of the group's A/B tests. '' is no group. Expands only: the
-- version before ignores the columns.
ALTER TABLE flags ADD COLUMN exclusion_group text NOT NULL DEFAULT '';
ALTER TABLE flags ADD COLUMN group_from int NOT NULL DEFAULT 0;
ALTER TABLE flags ADD COLUMN group_to int NOT NULL DEFAULT 0;
CREATE INDEX flags_exclusion_group ON flags (workspace_id, exclusion_group) WHERE exclusion_group <> '';
