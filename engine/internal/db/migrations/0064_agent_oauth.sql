-- Signing AI apps (Claude, ChatGPT, Cursor...) in to the hosted MCP server
-- with OAuth 2.1, so nobody copies a token. Outflare is the authorization
-- server: apps register themselves (dynamic client registration), a person
-- approves one on the consent screen, and the app gets an ordinary API
-- token (api_tokens, oft_) that lasts an hour plus a refresh token.
-- Revoking the API token disconnects the app.

-- Apps that registered themselves. Anyone can register one, so the consent
-- screen shows where it sends the person back to, not just its name.
CREATE TABLE oauth_clients (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name          text NOT NULL,
    uri           text NOT NULL DEFAULT '',
    redirect_uris text[] NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_used_at  timestamptz
);

-- Authorization codes: single use, ten minutes, bound to the PKCE
-- challenge and the redirect URI they were issued for.
CREATE TABLE oauth_codes (
    code_hash      bytea PRIMARY KEY,
    client_id      uuid NOT NULL REFERENCES oauth_clients (id) ON DELETE CASCADE,
    user_id        uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    scopes         text[] NOT NULL,
    redirect_uri   text NOT NULL,
    code_challenge text NOT NULL,
    -- The brand the app is kept to, for a person who is kept to some brands.
    brand_id       uuid REFERENCES brands (id) ON DELETE CASCADE,
    expires_at     timestamptz NOT NULL,
    used_at        timestamptz
);

CREATE INDEX oauth_codes_expires_idx ON oauth_codes (expires_at);

-- A connected app: its API token and the refresh token that renews it.
-- Refresh tokens rotate on every use; the one before stays valid for a
-- short grace period (an app retrying a refresh), and any later use of an
-- old one revokes the connection.
CREATE TABLE oauth_grants (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id          uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    client_id             uuid NOT NULL REFERENCES oauth_clients (id) ON DELETE CASCADE,
    user_id               uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    api_token_id          uuid NOT NULL UNIQUE REFERENCES api_tokens (id) ON DELETE CASCADE,
    refresh_hash          bytea NOT NULL UNIQUE,
    previous_refresh_hash bytea,
    rotated_at            timestamptz,
    created_at            timestamptz NOT NULL DEFAULT now(),
    refreshed_at          timestamptz,
    expires_at            timestamptz NOT NULL
);

CREATE INDEX oauth_grants_workspace_idx ON oauth_grants (workspace_id);
CREATE INDEX oauth_grants_previous_idx ON oauth_grants (previous_refresh_hash) WHERE previous_refresh_hash IS NOT NULL;
