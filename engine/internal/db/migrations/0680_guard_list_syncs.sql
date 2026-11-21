-- The copy the opt-out list keeps of each contact guard's whole list (a
-- product's account feed, a blocklist's exports), read on a schedule by a
-- job, so companies are checked against it without a call. One row per
-- guard connection: its settings, where a feed's next read starts, and
-- how its syncs went.
CREATE TABLE guard_list_syncs (
    integration_id   uuid PRIMARY KEY REFERENCES integrations (id) ON DELETE CASCADE,
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    enabled          boolean NOT NULL DEFAULT true,
    -- How often it is read; null is the connector's default.
    interval_seconds integer CHECK (interval_seconds BETWEEN 60 AND 86400),
    -- A feed's cursor: the next sync reads what came after it.
    cursor           text,
    -- When the whole list had been read once; until then the copy is not
    -- used in place of the guard.
    backfilled_at    timestamptz,
    -- One sync at a time: a run holds it until then.
    running_until    timestamptz,
    attempted_at     timestamptz,
    succeeded_at     timestamptz,
    -- What the last sync did, and in all.
    last_added       integer NOT NULL DEFAULT 0,
    last_removed     integer NOT NULL DEFAULT 0,
    last_duration_ms integer NOT NULL DEFAULT 0,
    added_count      bigint NOT NULL DEFAULT 0,
    removed_count    bigint NOT NULL DEFAULT 0,
    -- How many opt-out list entries came from it, after the last sync.
    entries          integer NOT NULL DEFAULT 0,
    last_error       text,
    error_code       text,
    -- After a failure, no sync before this.
    retry_at         timestamptz,
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX guard_list_syncs_workspace_idx ON guard_list_syncs (workspace_id);

-- A connection's own entries, to check a company against one guard's copy
-- and to remove what left a snapshot.
CREATE INDEX suppressions_integration_idx ON suppressions (integration_id, kind, value) WHERE integration_id IS NOT NULL;
