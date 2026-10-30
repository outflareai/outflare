-- Multivariate A/B tests: other flags a test adds to its own, each with its
-- control ([{"flag_id", "flag_key", "control"}]), so every combination of
-- their versions is measured. [] for a test of one flag. Expands only: the
-- version before ignores it.
ALTER TABLE ab_tests ADD COLUMN factors jsonb NOT NULL DEFAULT '[]';
