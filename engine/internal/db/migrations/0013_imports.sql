-- Imports from other sequencers (Instantly, Smartlead, ...): one run per
-- request, and what each outside campaign or mailbox became here, so
-- running an import again updates instead of duplicating.

CREATE TABLE import_runs (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    integration_id uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    connector      text NOT NULL,
    options        jsonb NOT NULL DEFAULT '{}',
    status         text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'failed')),
    -- Counts, mailboxes still to connect, and warnings about converted steps.
    summary        jsonb NOT NULL DEFAULT '{}',
    error          text,
    created_by     uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_by_type text NOT NULL DEFAULT 'human',
    created_at     timestamptz NOT NULL DEFAULT now(),
    started_at     timestamptz,
    finished_at    timestamptz
);

CREATE INDEX import_runs_ws_idx ON import_runs (workspace_id, created_at DESC);

CREATE TABLE import_items (
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    integration_id uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    kind           text NOT NULL CHECK (kind IN ('campaign')),
    external_id    text NOT NULL,
    -- The sequence it became.
    local_id       uuid NOT NULL,
    name           text NOT NULL DEFAULT '',
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (integration_id, kind, external_id)
);
