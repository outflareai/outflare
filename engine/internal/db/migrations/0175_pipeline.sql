-- The pipeline runtime: work that moves through stages one item at a
-- time, such as a lead finder run (search, read, qualify, review, ...)
-- or, later, a lead list run. A run is many items; stage workers claim
-- items in small batches, so a slow or failing item never holds a whole
-- run, and the backlog of every stage can be counted and watched
-- (docs/pipeline.md).
--
-- A feature keeps its own data in its own tables, keyed by the item's
-- ref; these tables only hold where each item is and why.

CREATE TABLE pipeline_runs (
    id           uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The feature it belongs to, such as 'lead_finder'.
    pipeline     text NOT NULL CHECK (pipeline ~ '^[a-z][a-z0-9_.]{0,63}$'),
    state        text NOT NULL DEFAULT 'running' CHECK (state IN ('running', 'paused', 'cancelled', 'finished')),
    -- Why it paused, as an error code (connector_needed, ai_budget_reached).
    pause_code   text,
    pause_reason text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz
);

CREATE INDEX pipeline_runs_workspace_idx ON pipeline_runs (workspace_id, created_at DESC);
CREATE INDEX pipeline_runs_live_idx ON pipeline_runs (pipeline) WHERE state IN ('running', 'paused');

CREATE TABLE pipeline_items (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    run_id           uuid NOT NULL REFERENCES pipeline_runs (id) ON DELETE CASCADE,
    pipeline         text NOT NULL,
    -- The feature's own key for the item (a candidate id, a domain).
    ref              text NOT NULL CHECK (length(ref) BETWEEN 1 AND 300),
    stage            text NOT NULL CHECK (stage ~ '^[a-z][a-z0-9_]{0,63}$'),
    -- waiting: due at next_at; working: claimed until lease_until;
    -- done: went through every stage; skipped: left on purpose (a reject,
    -- a duplicate, an opt-out); held: needs a person; failed: gave up.
    state            text NOT NULL DEFAULT 'waiting' CHECK (state IN ('waiting', 'working', 'done', 'skipped', 'held', 'failed')),
    next_at          timestamptz NOT NULL DEFAULT now(),
    lease_until      timestamptz,
    -- Attempts in the current stage; a move to the next stage resets it.
    attempts         integer NOT NULL DEFAULT 0,
    -- The last error or reason, as a code, and a short note for staff.
    error_code       text,
    reason           text CHECK (length(reason) <= 500),
    stage_entered_at timestamptz NOT NULL DEFAULT now(),
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (run_id, ref)
);

-- What claims read: a live run's due items in one stage.
CREATE INDEX pipeline_items_due_idx ON pipeline_items (run_id, stage, next_at) WHERE state = 'waiting';
-- Leases to reap and per-workspace work in progress.
CREATE INDEX pipeline_items_working_idx ON pipeline_items (pipeline, stage, workspace_id) WHERE state = 'working';
-- Unfinished items, which the metrics count per stage.
CREATE INDEX pipeline_items_live_idx ON pipeline_items (pipeline, stage) WHERE state IN ('waiting', 'working', 'held');
CREATE INDEX pipeline_items_run_idx ON pipeline_items (run_id, stage, state);
CREATE INDEX pipeline_items_workspace_idx ON pipeline_items (workspace_id);

-- A small summary per run and stage, kept in the same transaction as the
-- items it counts, so "why was this run slow" stays answerable after the
-- items are gone.
CREATE TABLE pipeline_stage_stats (
    run_id       uuid NOT NULL REFERENCES pipeline_runs (id) ON DELETE CASCADE,
    stage        text NOT NULL,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    entered      integer NOT NULL DEFAULT 0,
    advanced     integer NOT NULL DEFAULT 0,
    done         integer NOT NULL DEFAULT 0,
    skipped      integer NOT NULL DEFAULT 0,
    held         integer NOT NULL DEFAULT 0,
    failed       integer NOT NULL DEFAULT 0,
    retried      integer NOT NULL DEFAULT 0,
    waited       integer NOT NULL DEFAULT 0,
    batches      integer NOT NULL DEFAULT 0,
    -- Time spent working on this stage's batches, in milliseconds.
    busy_ms      bigint NOT NULL DEFAULT 0,
    -- {"<error code>": count}
    errors       jsonb NOT NULL DEFAULT '{}',
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, stage)
);

CREATE INDEX pipeline_stage_stats_workspace_idx ON pipeline_stage_stats (workspace_id);

-- Rate limits and circuit breakers per outside account (a connection, an
-- AI key), shared by every worker: a token bucket refilled per minute,
-- slowed for a while after a 429, and a breaker that pauses calls after
-- repeated failures. Keys are "integration:<id>", "ai:<workspace id>".
CREATE TABLE pipeline_limits (
    key           text PRIMARY KEY CHECK (length(key) BETWEEN 1 AND 200),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    tokens        double precision NOT NULL,
    refilled_at   timestamptz NOT NULL DEFAULT now(),
    -- Below 1 while slowed down after a rate limit answer.
    slow_factor   double precision NOT NULL DEFAULT 1 CHECK (slow_factor > 0 AND slow_factor <= 1),
    slow_until    timestamptz,
    breaker       text NOT NULL DEFAULT 'closed' CHECK (breaker IN ('closed', 'open', 'half_open')),
    failures      integer NOT NULL DEFAULT 0,
    -- How long the breaker stays open next time; doubles up to a cap.
    open_seconds  integer NOT NULL DEFAULT 60,
    reopen_at     timestamptz,
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX pipeline_limits_workspace_idx ON pipeline_limits (workspace_id);
