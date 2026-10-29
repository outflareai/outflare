-- Journey webhooks and Slack (customer data platform, actions step 2):
-- run variables, the workspace's signing secret, and the keys webhook
-- steps send.

-- What a run's steps saved for later steps, such as a webhook's answer
-- under vars.<save_as>. Branches, templates and field values read it.
ALTER TABLE journey_runs ADD COLUMN vars jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(vars) = 'object');

-- The secret that signs every webhook step's call (Standard Webhooks), one
-- per workspace, sealed. After a rotation the previous one signs too until
-- previous_expires_at, so receivers can switch over.
CREATE TABLE journey_signing_secrets (
    workspace_id        uuid PRIMARY KEY REFERENCES workspaces (id) ON DELETE CASCADE,
    secret              bytea NOT NULL,
    previous_secret     bytea,
    previous_expires_at timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    rotated_at          timestamptz
);

-- The key a webhook step sends (a header's value, a password or a URL
-- parameter's value), sealed and never shown again. It is set on its own,
-- never through the journey's steps, so it never appears in them.
CREATE TABLE journey_step_secrets (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    journey_id   uuid NOT NULL REFERENCES journeys (id) ON DELETE CASCADE,
    step         text NOT NULL,
    secret       bytea NOT NULL,
    created_by   uuid,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (journey_id, step)
);
CREATE INDEX journey_step_secrets_workspace_idx ON journey_step_secrets (workspace_id);
