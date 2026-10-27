-- The opt-out list kept in step with connected sequencers (EmailBison,
-- Instantly, Smartlead): their blocklists are read into it, and what it
-- holds is written to theirs, so a send from either side honours both.
CREATE TABLE suppression_syncs (
    integration_id uuid PRIMARY KEY REFERENCES integrations (id) ON DELETE CASCADE,
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- Read their blocklist into ours, and write ours to theirs.
    pull           boolean NOT NULL DEFAULT true,
    push           boolean NOT NULL DEFAULT true,
    pulled_at      timestamptz,
    pushed_at      timestamptz,
    -- Entries up to this one (by created_at, then id) have been written
    -- to theirs.
    pushed_until   timestamptz,
    pushed_id      uuid,
    pulled_count   int NOT NULL DEFAULT 0,
    pushed_count   int NOT NULL DEFAULT 0,
    last_error     text,
    error_code     text,
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX suppression_syncs_workspace_idx ON suppression_syncs (workspace_id);

CREATE INDEX suppressions_created_idx ON suppressions (workspace_id, created_at, id);
