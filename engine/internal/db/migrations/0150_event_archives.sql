-- Product events past their retention (OUTFLARE_EVENT_RETENTION_DAYS) are
-- written to object storage before Postgres drops them: one row per file.
-- The workspace column is from_workspace_id, with no foreign key, on
-- purpose: when a workspace is deleted, its rows stay until the archiver
-- has removed the files from the bucket too.
CREATE TABLE event_archives (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    from_workspace_id uuid NOT NULL,
    month        date NOT NULL,
    object_key   text NOT NULL UNIQUE,
    events       integer NOT NULL,
    bytes        bigint NOT NULL,
    first_at     timestamptz NOT NULL,
    last_at      timestamptz NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX event_archives_workspace_idx ON event_archives (from_workspace_id, month);
