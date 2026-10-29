-- A/B tests' learnings: what a finished test taught, written by the
-- workspace's own AI model when it ends ({headline, learning, tags}), for
-- the library of past tests; and two more kinds of AI run, writing a
-- learning and answering a question over past tests. Only widens the check,
-- so the version before still works.
ALTER TABLE ab_tests ADD COLUMN learning jsonb;
ALTER TABLE ab_tests ADD COLUMN learning_at timestamptz;

ALTER TABLE ab_test_ai_runs DROP CONSTRAINT IF EXISTS ab_test_ai_runs_kind_check;
ALTER TABLE ab_test_ai_runs ADD CONSTRAINT ab_test_ai_runs_kind_check
    CHECK (kind IN ('suggest', 'versions', 'summary', 'learning', 'ask'));
