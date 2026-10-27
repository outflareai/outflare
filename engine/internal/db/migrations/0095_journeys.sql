-- Journeys: if-this-then-that flows for people on customer records. An
-- event on someone's timeline (or a manual add) starts a run, which goes
-- through steps: send an email, wait, branch on a condition, exit.
CREATE TABLE journeys (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid REFERENCES brands (id) ON DELETE SET NULL,
    name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    description   text NOT NULL DEFAULT '',
    status        text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'paused', 'archived')),
    -- event: a customer event with this name starts a run. manual: only
    -- the API or the app adds people.
    trigger_kind  text NOT NULL DEFAULT 'event' CHECK (trigger_kind IN ('event', 'manual')),
    trigger_event text,
    -- Conditions the trigger event and person must meet to start.
    trigger_filter jsonb NOT NULL DEFAULT '[]',
    -- once: a person goes through it once ever. again: again after they
    -- finish (one run at a time).
    entry         text NOT NULL DEFAULT 'once' CHECK (entry IN ('once', 'again')),
    -- Doing this event ends their run as reached its goal.
    goal_event    text,
    -- [{id, kind: email|wait|branch|exit, ...}]; the first step starts.
    steps         jsonb NOT NULL DEFAULT '[]',
    version       integer NOT NULL DEFAULT 1,
    activated_at  timestamptz,
    created_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX journeys_trigger ON journeys (workspace_id, trigger_event) WHERE status = 'active';
CREATE INDEX journeys_goal ON journeys (workspace_id, goal_event) WHERE status = 'active' AND goal_event IS NOT NULL;

-- One person's way through a journey.
CREATE TABLE journey_runs (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    journey_id    uuid NOT NULL REFERENCES journeys (id) ON DELETE CASCADE,
    record_id     uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'completed', 'exited', 'failed')),
    -- The step it is on, and when it next moves.
    step          text,
    next_at       timestamptz,
    -- What started it: a customer event, or a person's or API call's add.
    trigger_event_id uuid,
    context       jsonb NOT NULL DEFAULT '{}',
    exit_reason   text,
    steps_taken   integer NOT NULL DEFAULT 0,
    entered_at    timestamptz NOT NULL DEFAULT now(),
    finished_at   timestamptz,
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX journey_runs_one_active ON journey_runs (journey_id, record_id) WHERE status = 'active';
CREATE INDEX journey_runs_journey ON journey_runs (journey_id, entered_at DESC);
CREATE INDEX journey_runs_record ON journey_runs (workspace_id, record_id);

-- What each step did for a run: the email it made, which way a branch
-- went, how long a wait was.
CREATE TABLE journey_run_steps (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    run_id       uuid NOT NULL REFERENCES journey_runs (id) ON DELETE CASCADE,
    journey_id   uuid NOT NULL,
    step         text NOT NULL,
    kind         text NOT NULL,
    outcome      text NOT NULL,
    email_id     uuid REFERENCES emails (id) ON DELETE SET NULL,
    detail       text,
    at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX journey_run_steps_run ON journey_run_steps (run_id, id);
CREATE INDEX journey_run_steps_journey ON journey_run_steps (journey_id, step);
