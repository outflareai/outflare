-- Missions: a goal an agent works toward inside a project, such as "book 10
-- meetings with fintech CFOs in Australia by 30 November". The mission holds
-- what the agent may do (mode and limits), what it plans, every call it
-- made, the writes waiting for a person, and the updates between them.
--
-- An agent joins a mission by sending the header Outflare-Mission: <id> on
-- its ordinary API calls. The engine then logs the call against the
-- mission and, depending on the mission's state and mode, runs the write,
-- holds it for approval, or records it as a dry run without running it.

CREATE TABLE missions (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The motion the mission works in; its brief is what the agent reads.
    project_id      uuid REFERENCES projects (id) ON DELETE SET NULL,
    name            text NOT NULL,
    -- The goal in words, and optionally as a number to track.
    goal            text NOT NULL DEFAULT '',
    goal_metric     text CHECK (goal_metric IN ('meetings', 'replies', 'sends', 'leads')),
    goal_target     integer CHECK (goal_target BETWEEN 1 AND 1000000),
    CHECK ((goal_metric IS NULL) = (goal_target IS NULL)),
    -- Extra guidance on top of the project brief, in Markdown.
    instructions    text NOT NULL DEFAULT '',
    -- The agent's plan, in Markdown, which a person approves.
    plan            text NOT NULL DEFAULT '',
    plan_updated_at timestamptz,
    -- dry_run: writes are recorded, never run. approve_writes: each write
    -- waits for a person. autonomous: writes run within the limits.
    mode            text NOT NULL DEFAULT 'dry_run' CHECK (mode IN ('dry_run', 'approve_writes', 'autonomous')),
    -- draft: being set up; writes are dry runs whatever the mode.
    -- pending_approval: the plan waits for a person; writes are dry runs.
    -- running: writes follow the mode. paused: reads only.
    -- completed, stopped, failed: over; calls in the mission are refused.
    status          text NOT NULL DEFAULT 'draft'
                    CHECK (status IN ('draft', 'pending_approval', 'running', 'paused', 'completed', 'stopped', 'failed')),
    status_reason   text NOT NULL DEFAULT '',
    -- The only token that may act in the mission. Null: any member, or
    -- any token with missions.run.
    token_id        uuid REFERENCES api_tokens (id) ON DELETE SET NULL,
    -- Limits on top of the token's own. Null: no mission limit.
    max_sends       integer CHECK (max_sends BETWEEN 1 AND 10000000),
    max_spend_cents bigint CHECK (max_spend_cents BETWEEN 0 AND 100000000000),
    -- The mission ends itself at this time.
    ends_at         timestamptz,
    -- The project brief version the mission was approved against, so the
    -- agent can tell the brief changed under it.
    brief_version   integer,
    approved_by     uuid REFERENCES actors (id),
    approved_at     timestamptz,
    started_at      timestamptz,
    ended_at        timestamptz,
    report          text NOT NULL DEFAULT '',
    created_by      uuid REFERENCES actors (id),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX missions_ws_idx ON missions (workspace_id, created_at DESC);
CREATE INDEX missions_project_idx ON missions (project_id) WHERE project_id IS NOT NULL;
CREATE INDEX missions_live_idx ON missions (ends_at) WHERE status IN ('draft', 'pending_approval', 'running', 'paused') AND ends_at IS NOT NULL;

-- Every call made in a mission, reads included. Details of what a write
-- changed are in audit_events with the same request_id.
CREATE TABLE mission_calls (
    id           bigserial PRIMARY KEY,
    mission_id   uuid NOT NULL REFERENCES missions (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    actor_id     uuid REFERENCES actors (id),
    method       text NOT NULL,
    path         text NOT NULL,
    query        text NOT NULL DEFAULT '',
    -- executed: ran. simulated: dry run, not run. queued: waits for
    -- approval. refused: the mission did not allow it.
    outcome      text NOT NULL CHECK (outcome IN ('executed', 'simulated', 'queued', 'refused')),
    status_code  integer NOT NULL,
    duration_ms  integer NOT NULL DEFAULT 0,
    -- Why the agent made the call, from the Outflare-Reason header.
    reason       text NOT NULL DEFAULT '',
    action_id    uuid,
    request_id   text,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX mission_calls_mission_idx ON mission_calls (mission_id, id DESC);

-- Writes the engine did not run straight away: dry runs, and writes
-- waiting for a person. Approving one runs the stored request as the
-- agent that made it.
CREATE TABLE mission_actions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    mission_id   uuid NOT NULL REFERENCES missions (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    actor_id     uuid REFERENCES actors (id),
    token_id     uuid REFERENCES api_tokens (id) ON DELETE SET NULL,
    method       text NOT NULL,
    path         text NOT NULL,
    query        text NOT NULL DEFAULT '',
    content_type text NOT NULL DEFAULT '',
    body         text NOT NULL DEFAULT '',
    reason       text NOT NULL DEFAULT '',
    -- simulated: a dry run, kept for review. pending: waits for a person.
    -- executed, failed: approved and run. rejected: a person said no.
    -- canceled: the mission ended first.
    status       text NOT NULL CHECK (status IN ('simulated', 'pending', 'executed', 'failed', 'rejected', 'canceled')),
    decided_by   uuid REFERENCES actors (id),
    decided_at   timestamptz,
    decision_note text NOT NULL DEFAULT '',
    result_status integer,
    result       jsonb,
    request_id   text,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX mission_actions_mission_idx ON mission_actions (mission_id, created_at DESC);
CREATE INDEX mission_actions_pending_idx ON mission_actions (workspace_id) WHERE status = 'pending';

-- Notes between the agent and people: progress, questions, answers,
-- instructions, and requests to connect a tool. Agents read the latest
-- when they check in.
--
-- connect_request: the agent needs a connector it cannot set up alone
-- (a person pastes the API key or signs in with OAuth). data holds
-- {"connector": "<id>", "why": "..."}; resolved_at is set once a person
-- connects it or declines.
CREATE TABLE mission_updates (
    id           bigserial PRIMARY KEY,
    mission_id   uuid NOT NULL REFERENCES missions (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    actor_id     uuid REFERENCES actors (id),
    -- agent: written with a token. person: written by a signed-in member.
    author       text NOT NULL CHECK (author IN ('agent', 'person', 'system')),
    kind         text NOT NULL DEFAULT 'note'
                 CHECK (kind IN ('note', 'progress', 'question', 'instruction', 'report', 'connect_request')),
    body         text NOT NULL,
    data         jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(data) = 'object'),
    resolved_at  timestamptz,
    resolved_by  uuid REFERENCES actors (id),
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX mission_updates_mission_idx ON mission_updates (mission_id, id DESC);

-- Campaigns started inside a mission count against its send limit and its
-- results count toward its goal.
ALTER TABLE sequences ADD COLUMN mission_id uuid REFERENCES missions (id) ON DELETE SET NULL;
CREATE INDEX sequences_mission_idx ON sequences (mission_id) WHERE mission_id IS NOT NULL;

-- Outcomes and spend recorded in a mission (0014 left these without a key).
ALTER TABLE spend_entries ADD CONSTRAINT spend_entries_mission_fk FOREIGN KEY (mission_id) REFERENCES missions (id) ON DELETE SET NULL;
ALTER TABLE meetings ADD CONSTRAINT meetings_mission_fk FOREIGN KEY (mission_id) REFERENCES missions (id) ON DELETE SET NULL;
ALTER TABLE deals ADD CONSTRAINT deals_mission_fk FOREIGN KEY (mission_id) REFERENCES missions (id) ON DELETE SET NULL;
