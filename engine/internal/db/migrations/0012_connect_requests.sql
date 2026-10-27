-- Connect requests: an agent (or a person) asks for a connector to be
-- connected and gets a one-time link. Whoever opens the link pastes the key
-- or approves the OAuth sign-in there, so secrets never pass through the
-- agent's chat. The link is the authority, so it is single use and short
-- lived, and only its hash is stored.

CREATE TABLE connect_requests (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    connector       text NOT NULL,
    -- Name for the integration it creates; empty picks the default.
    name            text NOT NULL DEFAULT '',
    -- Why the agent wants it, shown to the person on the link's page.
    reason          text NOT NULL DEFAULT '',
    -- Make the new integration the workspace's verification service.
    use_for_verification boolean NOT NULL DEFAULT false,
    token_hash      bytea NOT NULL UNIQUE,
    status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'connected', 'cancelled')),
    -- Set when connected. Not a foreign key: the integration may be
    -- disconnected later and the request kept as history.
    integration_id  uuid,
    created_by      uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_by_type text NOT NULL DEFAULT 'human',
    expires_at      timestamptz NOT NULL,
    completed_at    timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX connect_requests_ws_idx ON connect_requests (workspace_id, created_at DESC);
