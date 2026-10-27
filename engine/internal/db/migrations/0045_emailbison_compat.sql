-- EmailBison-compatible API: tools that push mailboxes into EmailBison
-- (Cheap Inboxes and other suppliers) can push them into Outflare instead.

-- EmailBison numbers its sender emails; clients store those numbers.
CREATE TABLE emailbison_compat_ids (
    id           bigserial PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    mailbox_id   uuid NOT NULL UNIQUE REFERENCES mailboxes (id) ON DELETE CASCADE
);
CREATE INDEX emailbison_compat_ids_ws_idx ON emailbison_compat_ids (workspace_id);

-- Every call a client made, without values, so we can see which parts of
-- the EmailBison API it relies on.
CREATE TABLE emailbison_compat_calls (
    id           bigserial PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    method       text NOT NULL,
    path         text NOT NULL,
    query_keys   text[] NOT NULL DEFAULT '{}',
    body_keys    text[] NOT NULL DEFAULT '{}',
    status       integer NOT NULL,
    supported    boolean NOT NULL,
    user_agent   text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX emailbison_compat_calls_ws_idx ON emailbison_compat_calls (workspace_id, id DESC);
