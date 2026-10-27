-- People, sessions, membership, invites and API tokens.

-- Human actors are global (a person can belong to several workspaces), so
-- actors.workspace_id stays NULL for them. API tokens and service accounts are
-- workspace-bound actors.
CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_id      uuid NOT NULL UNIQUE REFERENCES actors (id),
    email         text NOT NULL,
    name          text NOT NULL,
    password_hash text NOT NULL,
    disabled_at   timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX users_email_idx ON users (lower(email));

CREATE TABLE sessions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash   bytea NOT NULL UNIQUE,
    user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    user_agent   text,
    ip           text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL
);

CREATE INDEX sessions_user_idx ON sessions (user_id);

CREATE TABLE memberships (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role         text NOT NULL CHECK (role IN ('owner', 'admin', 'operator', 'viewer')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, user_id)
);

CREATE INDEX memberships_user_idx ON memberships (user_id);

CREATE TABLE invites (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    email        text NOT NULL,
    role         text NOT NULL CHECK (role IN ('admin', 'operator', 'viewer')),
    token_hash   bytea NOT NULL UNIQUE,
    invited_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    accepted_at  timestamptz,
    revoked_at   timestamptz
);

CREATE INDEX invites_workspace_idx ON invites (workspace_id);

CREATE TABLE api_tokens (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    actor_id     uuid NOT NULL UNIQUE REFERENCES actors (id),
    name         text NOT NULL,
    prefix       text NOT NULL,
    token_hash   bytea NOT NULL UNIQUE,
    scopes       text[] NOT NULL,
    created_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz,
    last_used_at timestamptz,
    revoked_at   timestamptz
);

CREATE INDEX api_tokens_workspace_idx ON api_tokens (workspace_id);
