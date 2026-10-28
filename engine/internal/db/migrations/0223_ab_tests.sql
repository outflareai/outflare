-- A/B tests: a question asked of a feature flag (0222). The flag splits
-- visitors between versions; the test says which version is the control,
-- what counts as success (goals), and when it measured. Results come from
-- product events: a visitor joins the test at their first Experiment Viewed
-- event for the flag (customer_events name experiment_viewed, properties
-- flag and variant), and converts when a goal event follows it.
--
-- These are separate from SEO experiments (0108, table experiments), which
-- compare groups of pages rather than visitors.

CREATE TABLE ab_tests (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid REFERENCES brands (id) ON DELETE SET NULL,
    flag_id      uuid NOT NULL REFERENCES flags (id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    -- What the team expects and why, in their words.
    hypothesis   text NOT NULL DEFAULT '',
    -- draft: being set up; running: measuring; done: ended, result kept.
    status       text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'running', 'done')),
    -- The flag variant the others are compared with.
    control      text NOT NULL,
    -- [{"kind": "event", "name": "signup_started"} or {"kind": "page",
    -- "path": "/thanks"}]; the first is the primary goal, the rest are
    -- watched alongside it.
    goals        jsonb NOT NULL DEFAULT '[]',
    -- The smallest relative change worth detecting (0.25 is +25%): the
    -- always-valid p-value is tuned to find changes of about this size.
    min_effect   double precision NOT NULL DEFAULT 0.25 CHECK (min_effect BETWEEN 0.01 AND 1),
    started_at   timestamptz,
    ended_at     timestamptz,
    -- The variant the team chose when it ended ('' for none), and the
    -- results as they stood then, so a finished test reads the same later.
    decision     text NOT NULL DEFAULT '',
    result       jsonb,
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
-- A flag answers one running test at a time.
CREATE UNIQUE INDEX ab_tests_running_idx ON ab_tests (flag_id) WHERE status = 'running';
CREATE INDEX ab_tests_ws_idx ON ab_tests (workspace_id, status, created_at DESC);
