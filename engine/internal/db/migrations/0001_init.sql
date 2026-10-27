-- Foundation schema: workspaces, actors, audit log and the job queue.
-- Everything a workspace owns carries workspace_id so isolation is structural.

CREATE TABLE workspaces (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL,
    slug       text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- An actor is anything that can perform an action: a person, an API token,
-- a service account, an agent or the system itself. Every audited action
-- points at one.
CREATE TABLE actors (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid REFERENCES workspaces (id) ON DELETE CASCADE,
    type         text NOT NULL CHECK (type IN ('human', 'api_token', 'service_account', 'agent', 'system')),
    display_name text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX actors_workspace_idx ON actors (workspace_id);

-- Append-only record of every significant action.
CREATE TABLE audit_events (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id uuid REFERENCES workspaces (id) ON DELETE CASCADE,
    actor_id     uuid REFERENCES actors (id) ON DELETE SET NULL,
    actor_type   text NOT NULL,
    action       text NOT NULL,
    target_type  text,
    target_id    text,
    request_id   text,
    data         jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_events_workspace_created_idx ON audit_events (workspace_id, created_at DESC);

-- Durable job queue. Workers claim rows with FOR UPDATE SKIP LOCKED, so many
-- workers can run against one database without double-processing.
CREATE TABLE jobs (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id    uuid REFERENCES workspaces (id) ON DELETE CASCADE,
    kind            text NOT NULL,
    payload         jsonb NOT NULL DEFAULT '{}'::jsonb,
    status          text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'succeeded', 'dead')),
    attempts        integer NOT NULL DEFAULT 0,
    max_attempts    integer NOT NULL DEFAULT 10,
    run_at          timestamptz NOT NULL DEFAULT now(),
    locked_by       text,
    locked_at       timestamptz,
    last_error      text,
    idempotency_key text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX jobs_idempotency_idx ON jobs (kind, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX jobs_ready_idx ON jobs (run_at) WHERE status = 'queued';
CREATE INDEX jobs_running_idx ON jobs (locked_at) WHERE status = 'running';
