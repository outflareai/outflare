-- A/B tests: guardrail metrics, watched so a winner that hurts something
-- else is caught; the moment new visitors stopped joining (frozen_at), while
-- those already in keep counting; and when the background check looks at a
-- running test next (to alert when it is safe to stop, its split breaks or
-- a guardrail is hurt). Expands only: the version before ignores them.
ALTER TABLE ab_tests ADD COLUMN guardrails jsonb NOT NULL DEFAULT '[]';
ALTER TABLE ab_tests ADD COLUMN frozen_at timestamptz;
ALTER TABLE ab_tests ADD COLUMN next_check_at timestamptz;

CREATE INDEX ab_tests_check_idx ON ab_tests (next_check_at) WHERE status = 'running';
