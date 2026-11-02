-- The action layer (ADR 13): every change an agent token makes goes
-- through one gate that runs it, holds it for a person, or refuses it,
-- by the grants a person set for that token. It replaces the mission
-- gate (ADR 7); missions keep working beside it until they are retired.
--
-- Expand only (docs/migrations.md): a new column with a default and new
-- tables. The previous version ignores all of it.

-- A token whose changes go through the action layer. Off for every
-- existing token, so nothing changes until a person turns it on.
ALTER TABLE api_tokens ADD COLUMN action_gate boolean NOT NULL DEFAULT false;

-- What an agent token may do. The most specific grant that matches a
-- change decides it: one naming the method beats one that doesn't, and a
-- longer path pattern beats a shorter one. No match means hold.
CREATE TABLE action_grants (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    -- Null: changes to any brand's records, and to shared ones.
    brand_id     uuid REFERENCES brands(id) ON DELETE CASCADE,
    token_id     uuid NOT NULL REFERENCES api_tokens(id) ON DELETE CASCADE,
    -- Null: any method that changes something.
    method       text CHECK (method IN ('POST', 'PUT', 'PATCH', 'DELETE')),
    -- The path after /v1/workspaces/{ws}/, where * matches one segment
    -- and a trailing ** matches the rest ("sequences/*/enrollments", "**").
    path         text NOT NULL CHECK (length(path) BETWEEN 1 AND 300),
    mode         text NOT NULL CHECK (mode IN ('run', 'hold', 'refuse')),
    -- Most changes this grant runs in a UTC day; the rest are held.
    daily_limit  int CHECK (daily_limit > 0),
    note         text NOT NULL DEFAULT '' CHECK (length(note) <= 500),
    created_by   uuid REFERENCES actors(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    revoked_at   timestamptz,
    revoked_by   uuid REFERENCES actors(id) ON DELETE SET NULL
);
CREATE INDEX action_grants_token_idx ON action_grants (token_id) WHERE revoked_at IS NULL;
CREATE INDEX action_grants_workspace_idx ON action_grants (workspace_id, created_at DESC);

-- Every change an agent token made through the gate: run, held, refused,
-- and what a person decided.
CREATE TABLE actions (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    -- The token's brand when it is kept to one; null otherwise.
    brand_id       uuid REFERENCES brands(id) ON DELETE CASCADE,
    actor_id       uuid REFERENCES actors(id) ON DELETE SET NULL,
    token_id       uuid REFERENCES api_tokens(id) ON DELETE SET NULL,
    grant_id       uuid REFERENCES action_grants(id) ON DELETE SET NULL,
    method         text NOT NULL,
    path           text NOT NULL,
    query          text NOT NULL DEFAULT '',
    content_type   text NOT NULL DEFAULT '',
    body           text NOT NULL DEFAULT '',
    -- sha256 of method, path, query and body: what a person approved.
    body_hash      text NOT NULL,
    reason         text NOT NULL DEFAULT '',
    -- held: waits for a person. running: claimed and running now. executed
    -- and failed: it ran. refused: a
    -- grant refused it. rejected, expired and cancelled: it never ran.
    status         text NOT NULL CHECK (status IN ('held', 'running', 'executed', 'failed', 'refused', 'rejected', 'expired', 'cancelled')),
    -- Why it was held or refused: no_grant, grant, daily_limit, always_person.
    decided_by_rule text NOT NULL DEFAULT '' CHECK (decided_by_rule IN ('', 'grant', 'no_grant', 'daily_limit', 'always_person')),
    hold_reason    text NOT NULL DEFAULT '',
    decided_by     uuid REFERENCES actors(id) ON DELETE SET NULL,
    decided_at     timestamptz,
    decision_note  text NOT NULL DEFAULT '',
    result_status  int,
    result         jsonb,
    request_id     text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    -- A held change nobody decides by then counts as expired.
    expires_at     timestamptz
);
CREATE INDEX actions_workspace_idx ON actions (workspace_id, created_at DESC);
CREATE INDEX actions_held_idx ON actions (workspace_id, created_at) WHERE status = 'held';
CREATE INDEX actions_grant_day_idx ON actions (grant_id, created_at) WHERE status IN ('running', 'executed', 'failed');
