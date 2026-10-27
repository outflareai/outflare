-- Connectors: a workspace's connected accounts at outside services (email
-- verification, sequencers, mailbox suppliers, DNS hosts, CRMs), and email
-- verification results.

CREATE TABLE integrations (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The connector's id in the engine's registry, e.g. 'zerobounce'.
    connector       text NOT NULL,
    name            text NOT NULL,
    -- error: the last call was refused for its credentials; see last_error.
    status          text NOT NULL DEFAULT 'connected' CHECK (status IN ('connected', 'error')),
    credentials     bytea NOT NULL, -- secrets.Box ciphertext of a JSON object, bound to the row id
    -- Per-integration settings the connector reads, never secret.
    config          jsonb NOT NULL DEFAULT '{}',
    -- The outside account it signed in to, from the last successful check.
    account         jsonb NOT NULL DEFAULT '{}',
    last_checked_at timestamptz,
    last_error      text,
    created_by      uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX integrations_ws_idx ON integrations (workspace_id, connector);
CREATE UNIQUE INDEX integrations_name_idx ON integrations (workspace_id, lower(name));

-- How the workspace verifies addresses. One row per workspace; no row
-- means the defaults below.
CREATE TABLE verification_settings (
    workspace_id       uuid PRIMARY KEY REFERENCES workspaces (id) ON DELETE CASCADE,
    -- Null: the free built-in checks.
    integration_id     uuid REFERENCES integrations (id) ON DELETE SET NULL,
    verify_on_import   boolean NOT NULL DEFAULT true,
    -- Hold a lead's first email until its address has a fresh result.
    verify_before_send boolean NOT NULL DEFAULT true,
    -- Undeliverable addresses are never emailed. These widen the block.
    block_risky        boolean NOT NULL DEFAULT false,
    block_catch_all    boolean NOT NULL DEFAULT false,
    block_unknown      boolean NOT NULL DEFAULT false,
    -- Results older than this are checked again before the next first email.
    max_age_days       integer NOT NULL DEFAULT 90 CHECK (max_age_days BETWEEN 1 AND 3650),
    updated_at         timestamptz NOT NULL DEFAULT now()
);

-- The latest verification of each address in a workspace. Kept apart from
-- leads so a result is reused when the same address is imported again.
CREATE TABLE email_verifications (
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    email           text NOT NULL CHECK (email = lower(email)),
    status          text NOT NULL CHECK (status IN ('deliverable', 'undeliverable', 'risky', 'unknown')),
    reason          text NOT NULL DEFAULT '',
    catch_all       boolean NOT NULL DEFAULT false,
    disposable      boolean NOT NULL DEFAULT false,
    role            boolean NOT NULL DEFAULT false,
    free            boolean NOT NULL DEFAULT false,
    suggestion      text NOT NULL DEFAULT '',
    score           smallint,
    -- Connector id that answered ('builtin' for the free checks).
    provider        text NOT NULL,
    provider_status text NOT NULL DEFAULT '',
    integration_id  uuid REFERENCES integrations (id) ON DELETE SET NULL,
    checked_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, email)
);

CREATE INDEX email_verifications_status_idx ON email_verifications (workspace_id, status);
