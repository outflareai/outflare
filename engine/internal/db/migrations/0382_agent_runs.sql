-- Agent runs (ADR 13): a goal an agent works toward in turns, on the
-- action layer. Outflare's own agent runs a playbook as an agent token
-- whose grants come from the mode a person chose; every change it makes
-- runs, waits for a person or is refused by the gate, and is kept as an
-- action. Runs replace missions for playbooks; missions keep working
-- until they are retired. Expand only: new tables.

CREATE TABLE agent_runs (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The brand the run works for; null for a run about the whole company.
    brand_id         uuid REFERENCES brands (id) ON DELETE SET NULL,
    -- The agent token the run acts as (action_gate on, no secret kept).
    token_id         uuid REFERENCES api_tokens (id) ON DELETE SET NULL,
    name             text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    goal             text NOT NULL DEFAULT '',
    instructions     text NOT NULL DEFAULT '',
    plan             text NOT NULL DEFAULT '',
    inputs           jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(inputs) = 'object'),
    playbook_id      text,
    playbook_version integer,
    automation_id    uuid REFERENCES playbook_automations (id) ON DELETE SET NULL,
    -- copilot: every change waits for a person. autonomous: changes run
    -- within the spend limit. dry_run: every change is refused and kept,
    -- so people see what it would have done.
    mode             text NOT NULL DEFAULT 'copilot' CHECK (mode IN ('copilot', 'autonomous', 'dry_run')),
    max_spend_cents  bigint CHECK (max_spend_cents BETWEEN 0 AND 100000000000),
    -- running: takes turns. paused: a person paused it, or it failed too
    -- often. completed, stopped, failed: over.
    status           text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'paused', 'completed', 'stopped', 'failed')),
    status_reason    text NOT NULL DEFAULT '',
    report           text NOT NULL DEFAULT '',
    turns            integer NOT NULL DEFAULT 0,
    next_turn_at     timestamptz,
    turn_started_at  timestamptz,
    last_turn_at     timestamptz,
    turn_summary     text NOT NULL DEFAULT '',
    turn_error       text NOT NULL DEFAULT '',
    turn_failures    integer NOT NULL DEFAULT 0,
    created_by       uuid REFERENCES actors (id),
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    ended_at         timestamptz
);
CREATE INDEX agent_runs_ws_idx ON agent_runs (workspace_id, created_at DESC);
CREATE INDEX agent_runs_due_idx ON agent_runs (next_turn_at) WHERE status = 'running';
CREATE INDEX agent_runs_automation_idx ON agent_runs (automation_id, created_at DESC) WHERE automation_id IS NOT NULL;

-- What the agent and people said to each other in a run.
CREATE TABLE agent_run_updates (
    id           bigserial PRIMARY KEY,
    run_id       uuid NOT NULL REFERENCES agent_runs (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The run's brand, so a brand's updates are its own.
    brand_id     uuid REFERENCES brands (id) ON DELETE SET NULL,
    actor_id     uuid REFERENCES actors (id),
    author       text NOT NULL CHECK (author IN ('agent', 'person', 'system')),
    kind         text NOT NULL DEFAULT 'note' CHECK (kind IN ('note', 'progress', 'question', 'instruction', 'report')),
    body         text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX agent_run_updates_run_idx ON agent_run_updates (run_id, id DESC);

-- A grant can cap what its changes spend in all, for good: a budget for
-- one job, such as an autonomous run's.
ALTER TABLE action_grants ADD COLUMN total_spend_cents bigint CHECK (total_spend_cents > 0);

-- The last agent run an automation started (missions: last_mission_id).
ALTER TABLE playbook_automations ADD COLUMN last_run_id uuid REFERENCES agent_runs (id) ON DELETE SET NULL;
