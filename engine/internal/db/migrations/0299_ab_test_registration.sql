-- A/B tests: what a test set out to show, kept as it was when it started
-- (hypothesis, control, goals, guardrails, smallest change, the plan, and
-- the flag's versions, weights and rollout). Null for drafts and for tests
-- started before this. Expands only: the version before ignores it.
ALTER TABLE ab_tests ADD COLUMN registration jsonb;
