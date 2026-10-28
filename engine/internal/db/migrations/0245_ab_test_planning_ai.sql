-- A/B tests (0223), part two. Each goal's expected control rate is recorded
-- when a test starts, so its sequential test keeps one tuning for its whole
-- life. And AI runs: a request to the workspace's own AI model to suggest
-- tests, write versions of a flag from a prompt, or summarise a test's
-- results. Runs are jobs; this keeps what was asked and what came back,
-- for a person to read and act on.

-- [0.031, 0.004, ...], one per goal in order; null when the test started
-- before this or the site had too little history (the live control rate
-- is used then).
ALTER TABLE ab_tests ADD COLUMN baselines jsonb;

CREATE TABLE ab_test_ai_runs (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid REFERENCES brands (id) ON DELETE SET NULL,
    -- suggest: test ideas for the site; versions: copy for a flag's
    -- versions from a prompt; summary: a test's results in words.
    kind         text NOT NULL CHECK (kind IN ('suggest', 'versions', 'summary')),
    test_id      uuid REFERENCES ab_tests (id) ON DELETE CASCADE,
    flag_id      uuid REFERENCES flags (id) ON DELETE CASCADE,
    status       text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'ready', 'failed')),
    -- What the person asked for (a prompt, a page, how many), and the
    -- answer once ready.
    input        jsonb NOT NULL DEFAULT '{}',
    output       jsonb,
    error        text NOT NULL DEFAULT '',
    model        text NOT NULL DEFAULT '',
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz
);
CREATE INDEX ab_test_ai_runs_ws_idx ON ab_test_ai_runs (workspace_id, created_at DESC);
CREATE INDEX ab_test_ai_runs_test_idx ON ab_test_ai_runs (test_id) WHERE test_id IS NOT NULL;
