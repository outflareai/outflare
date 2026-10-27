-- Moving from another CRM (Attio): one row per import. Imports run in
-- steps, one page of records each, so a large CRM never holds a worker for
-- long; progress says where the next step starts. Running an import again
-- updates the same records instead of duplicating them, because each record
-- keeps its CRM id as an identity (namespace attio.record).
CREATE TABLE crm_imports (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    integration_id  uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    connector       text NOT NULL,
    status          text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'failed')),
    -- Only these CRM objects (their slugs). Empty: every object.
    objects         text[] NOT NULL DEFAULT '{}',
    -- The plan (which record type and field each CRM object and field
    -- became) and where the next step starts.
    progress        jsonb NOT NULL DEFAULT '{}',
    -- Counts per object, catalog changes and warnings.
    summary         jsonb NOT NULL DEFAULT '{}',
    error           text,
    created_by      uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_by_type text NOT NULL DEFAULT 'human',
    created_at      timestamptz NOT NULL DEFAULT now(),
    started_at      timestamptz,
    finished_at     timestamptz
);

CREATE INDEX crm_imports_ws_idx ON crm_imports (workspace_id, created_at DESC);
-- One import at a time per connection.
CREATE UNIQUE INDEX crm_imports_active_idx ON crm_imports (integration_id) WHERE status IN ('queued', 'running');
