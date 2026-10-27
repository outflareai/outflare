-- OAuth sign-in for Google Workspace and Microsoft 365 mailboxes.
--
-- A connection is admin-level access to a whole organisation: a Google
-- service account with domain-wide delegation, or a Microsoft Entra app
-- with application access to Exchange Online. Mailboxes added through a
-- connection need no password. Mailboxes connected one at a time through
-- the consent screen keep their own refresh token in their credentials.

CREATE TABLE oauth_connections (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    provider     text NOT NULL CHECK (provider IN ('google', 'microsoft')),
    name         text NOT NULL,
    -- Non-secret identifiers shown in the app: the service account's
    -- client_email, or the tenant and client ids.
    details      jsonb NOT NULL DEFAULT '{}',
    secret       bytea NOT NULL, -- secrets.Box ciphertext, bound to the connection id
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX oauth_connections_ws_idx ON oauth_connections (workspace_id);

ALTER TABLE mailboxes
    ADD COLUMN oauth_connection_id uuid REFERENCES oauth_connections (id) ON DELETE RESTRICT;
